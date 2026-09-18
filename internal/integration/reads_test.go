//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http/middleware"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// newInternalRuntime builds the HTTP boundary over the real service with an
// internal client carrying every internal scope and no provider identity.
func newInternalRuntime(t *testing.T, h *harness) *httpRuntime {
	t.Helper()
	return newHTTPRuntimeWith(t, h, internalAuthenticator(), nil)
}

// newProviderRuntime builds the HTTP boundary with a provider-a identity.
func newProviderRuntime(t *testing.T, h *harness) *httpRuntime {
	t.Helper()
	return newHTTPRuntimeWith(t, h, allScopes("provider-a"), nil)
}

type ledgerEntryJSON struct {
	ID            string    `json:"id"`
	WalletID      string    `json:"walletId"`
	TransactionID string    `json:"transactionId"`
	Direction     string    `json:"direction"`
	Money         moneyJSON `json:"money"`
	BalanceBefore moneyJSON `json:"balanceBefore"`
	BalanceAfter  moneyJSON `json:"balanceAfter"`
	CreatedAt     string    `json:"createdAt"`
}

type ledgerPageJSON struct {
	Items      []ledgerEntryJSON `json:"items"`
	NextCursor string            `json:"nextCursor"`
}

type transactionJSON struct {
	TransactionID                  string     `json:"transactionId"`
	Origin                         string     `json:"origin"`
	ProviderID                     string     `json:"providerId"`
	ExternalTransactionID          string     `json:"externalTransactionId"`
	WalletID                       string     `json:"walletId"`
	PlayerID                       string     `json:"playerId"`
	RoundID                        string     `json:"roundId"`
	GameID                         string     `json:"gameId"`
	Kind                           string     `json:"kind"`
	Money                          *moneyJSON `json:"money"`
	Status                         string     `json:"status"`
	Balance                        *moneyJSON `json:"balance"`
	FailureCode                    string     `json:"failureCode"`
	ReferenceExternalTransactionID string     `json:"referenceExternalTransactionId"`
	ReferenceDeadline              *string    `json:"referenceDeadline"`
	CreatedAt                      *string    `json:"createdAt"`
	UpdatedAt                      *string    `json:"updatedAt"`
}

func (h *harness) openingTransactionID(walletID financial.WalletID) financial.TransactionID {
	h.t.Helper()
	var raw string
	if err := adminPool.QueryRow(h.ctx(),
		`SELECT "id" FROM wager_transactions WHERE "walletId" = $1 AND "kind" = 'OPENING'`,
		walletID.String(),
	).Scan(&raw); err != nil {
		h.t.Fatalf("reading the opening transaction: %v", err)
	}
	id, err := financial.ParseTransactionID(raw)
	if err != nil {
		h.t.Fatalf("parsing the opening transaction id: %v", err)
	}
	return id
}

// C112 - The internal client gets an existing wallet with id, playerId,
// balance, version, createdAt and updatedAt.
func TestWalletGetResponse(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	runtime := newInternalRuntime(t, h)

	status, body := runtime.request(http.MethodGet, "/wallets/"+view.ID.String(), nil, nil)
	requireStatus(t, status, 200, body)
	wallet := decodeJSONBody[walletJSON](t, body)
	if wallet.ID != view.ID.String() {
		t.Errorf("id = %s, want %s", wallet.ID, view.ID)
	}
	if wallet.PlayerID != view.PlayerID.String() {
		t.Errorf("playerId = %s, want %s", wallet.PlayerID, view.PlayerID)
	}
	if wallet.Balance.Amount != "1000.00" || wallet.Balance.Currency != "BRL" {
		t.Errorf("balance = %+v, want 1000.00 BRL", wallet.Balance)
	}
	if wallet.Version != 1 {
		t.Errorf("version = %d, want 1", wallet.Version)
	}
	if wallet.CreatedAt == "" || wallet.UpdatedAt == "" {
		t.Errorf("timestamps = %q/%q, want both present", wallet.CreatedAt, wallet.UpdatedAt)
	}

	status, body = runtime.request(http.MethodGet, "/wallets/"+financial.NewWalletID().String(), nil, nil)
	requireStatus(t, status, 404, body)
}

// C113 - A ledger list without limit returns at most 50 entries ordered by
// (createdAt,id) ascending with an opaque nextCursor.
func TestLedgerPagination(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	for i := 0; i < 54; i++ {
		h.mustSubmit(h.command(view, financial.KindWin, "1.00"))
	}
	runtime := newInternalRuntime(t, h)

	status, body := runtime.request(http.MethodGet, "/wallets/"+view.ID.String()+"/ledger", nil, nil)
	requireStatus(t, status, 200, body)
	first := decodeJSONBody[ledgerPageJSON](t, body)
	if len(first.Items) != 50 {
		t.Fatalf("first page items = %d, want 50", len(first.Items))
	}
	if first.NextCursor == "" {
		t.Fatal("first page carries no nextCursor")
	}
	if strings.Contains(first.NextCursor, " ") || strings.Contains(first.NextCursor, "=") {
		t.Errorf("nextCursor %q is not an opaque base64url token", first.NextCursor)
	}
	assertAscendingLedger(t, first.Items)

	status, body = runtime.request(http.MethodGet, "/wallets/"+view.ID.String()+"/ledger?cursor="+first.NextCursor, nil, nil)
	requireStatus(t, status, 200, body)
	second := decodeJSONBody[ledgerPageJSON](t, body)
	if len(second.Items) != 5 {
		t.Fatalf("second page items = %d, want 5", len(second.Items))
	}
	if second.NextCursor != "" {
		t.Errorf("second page nextCursor = %q, want empty", second.NextCursor)
	}
	assertAscendingLedger(t, second.Items)

	seen := map[string]bool{}
	for _, item := range append(first.Items, second.Items...) {
		if seen[item.ID] {
			t.Fatalf("entry %s appeared on two pages", item.ID)
		}
		seen[item.ID] = true
	}
	if len(seen) != 55 {
		t.Errorf("paged entries = %d, want 55", len(seen))
	}
}

func assertAscendingLedger(t *testing.T, items []ledgerEntryJSON) {
	t.Helper()
	var previous ledgerEntryJSON
	for index, item := range items {
		if index > 0 {
			previousTime, err := time.Parse(time.RFC3339Nano, previous.CreatedAt)
			if err != nil {
				t.Fatalf("parsing createdAt %q: %v", previous.CreatedAt, err)
			}
			currentTime, err := time.Parse(time.RFC3339Nano, item.CreatedAt)
			if err != nil {
				t.Fatalf("parsing createdAt %q: %v", item.CreatedAt, err)
			}
			if currentTime.Before(previousTime) || (currentTime.Equal(previousTime) && item.ID < previous.ID) {
				t.Fatalf("entries are not ascending at %d: %s (%s) after %s (%s)", index, item.ID, item.CreatedAt, previous.ID, previous.CreatedAt)
			}
		}
		previous = item
	}
}

// C114 - A ledger page with no entries returns 200 with items:[] and no
// nextCursor.
func TestLedgerEmptyPage(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("0.00")
	runtime := newInternalRuntime(t, h)

	status, body := runtime.request(http.MethodGet, "/wallets/"+view.ID.String()+"/ledger", nil, nil)
	requireStatus(t, status, 200, body)
	if !strings.Contains(string(body), `"items":[]`) {
		t.Errorf("empty page body = %s, want items:[]", string(body))
	}
	if strings.Contains(string(body), "nextCursor") {
		t.Errorf("empty page body = %s, want no nextCursor", string(body))
	}
	page := decodeJSONBody[ledgerPageJSON](t, body)
	if len(page.Items) != 0 || page.NextCursor != "" {
		t.Errorf("empty page = %+v, want no items and no cursor", page)
	}
}

// C115 - An invalid cursor or a limit outside 1..100 returns 400 with
// INVALID_CURSOR or INVALID_LIMIT.
func TestLedgerInvalidCursor(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	runtime := newInternalRuntime(t, h)
	base := "/wallets/" + view.ID.String() + "/ledger"

	cases := []struct {
		name string
		path string
		code string
	}{
		{name: "invalid cursor", path: base + "?cursor=not-a-cursor", code: "INVALID_CURSOR"},
		{name: "cursor with wrong version", path: base + "?cursor=eyJ2Ijo5LCJ0IjoiMjAyNi0wMS0wMVQwMDowMDowMFoifQ", code: "INVALID_CURSOR"},
		{name: "zero limit", path: base + "?limit=0", code: "INVALID_LIMIT"},
		{name: "limit above maximum", path: base + "?limit=101", code: "INVALID_LIMIT"},
		{name: "non numeric limit", path: base + "?limit=abc", code: "INVALID_LIMIT"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, body := runtime.request(http.MethodGet, testCase.path, nil, nil)
			requireStatus(t, status, 400, body)
			envelope := decodeError(t, body)
			if envelope.Error.Code != testCase.code {
				t.Errorf("code = %s, want %s", envelope.Error.Code, testCase.code)
			}
		})
	}

	status, body := runtime.request(http.MethodGet, base+"?limit=100", nil, nil)
	requireStatus(t, status, 200, body)
}

// C116 - An authorized caller gets a transaction by internal ID with identity,
// provider, kind, money, reference, status, failure code, balance and
// timestamps.
func TestTransactionGetResponse(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	command := h.command(view, financial.KindBet, "25.00")
	result := h.mustSubmit(command)
	runtime := newInternalRuntime(t, h)

	status, body := runtime.request(http.MethodGet, "/wagering/transactions/"+result.TransactionID.String(), nil, nil)
	requireStatus(t, status, 200, body)
	transaction := decodeJSONBody[transactionJSON](t, body)
	if transaction.TransactionID != result.TransactionID.String() {
		t.Errorf("transactionId = %s, want %s", transaction.TransactionID, result.TransactionID)
	}
	if transaction.Origin != "EXTERNAL" {
		t.Errorf("origin = %s, want EXTERNAL", transaction.Origin)
	}
	if transaction.ProviderID != "provider-a" {
		t.Errorf("providerId = %s, want provider-a", transaction.ProviderID)
	}
	if transaction.ExternalTransactionID != command.ExternalTransactionID.String() {
		t.Errorf("externalTransactionId = %s, want %s", transaction.ExternalTransactionID, command.ExternalTransactionID)
	}
	if transaction.WalletID != view.ID.String() || transaction.PlayerID != view.PlayerID.String() {
		t.Errorf("wallet/player = %s/%s, want %s/%s", transaction.WalletID, transaction.PlayerID, view.ID, view.PlayerID)
	}
	if transaction.RoundID != command.RoundID.String() || transaction.GameID != command.GameID.String() {
		t.Errorf("round/game = %s/%s, want %s/%s", transaction.RoundID, transaction.GameID, command.RoundID, command.GameID)
	}
	if transaction.Kind != "BET" {
		t.Errorf("kind = %s, want BET", transaction.Kind)
	}
	if transaction.Money == nil || transaction.Money.Amount != "25.00" || transaction.Money.Currency != "BRL" {
		t.Errorf("money = %+v, want 25.00 BRL", transaction.Money)
	}
	if transaction.Status != "PROCESSED" {
		t.Errorf("status = %s, want PROCESSED", transaction.Status)
	}
	if transaction.Balance == nil || transaction.Balance.Amount != "975.00" {
		t.Errorf("balance = %+v, want 975.00 BRL", transaction.Balance)
	}
	if transaction.CreatedAt == nil || transaction.UpdatedAt == nil {
		t.Errorf("timestamps = %v/%v, want both present", transaction.CreatedAt, transaction.UpdatedAt)
	}

	// The internal OPENING transaction is readable by the internal client and
	// exposes no provider identity.
	openingID := h.openingTransactionID(view.ID)
	status, body = runtime.request(http.MethodGet, "/wagering/transactions/"+openingID.String(), nil, nil)
	requireStatus(t, status, 200, body)
	opening := decodeJSONBody[transactionJSON](t, body)
	if opening.Origin != "INTERNAL" || opening.ProviderID != "" || opening.Kind != "OPENING" {
		t.Errorf("opening = %s/%s/%s, want INTERNAL/empty/OPENING", opening.Origin, opening.ProviderID, opening.Kind)
	}
	if opening.Balance == nil || opening.Balance.Amount != "1000.00" {
		t.Errorf("opening balance = %+v, want 1000.00", opening.Balance)
	}
}

// C117 - A provider gets its external transaction by provider and external
// identity with the same view.
func TestProviderTransactionGet(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	command := h.command(view, financial.KindBet, "25.00")
	result := h.mustSubmit(command)
	runtime := newProviderRuntime(t, h)

	status, body := runtime.request(http.MethodGet,
		"/providers/provider-a/wagering/transactions/"+command.ExternalTransactionID.String(), nil, nil)
	requireStatus(t, status, 200, body)
	transaction := decodeJSONBody[transactionJSON](t, body)
	if transaction.TransactionID != result.TransactionID.String() {
		t.Errorf("transactionId = %s, want %s", transaction.TransactionID, result.TransactionID)
	}
	if transaction.ProviderID != "provider-a" || transaction.ExternalTransactionID != command.ExternalTransactionID.String() {
		t.Errorf("provider/external = %s/%s, want provider-a/%s", transaction.ProviderID, transaction.ExternalTransactionID, command.ExternalTransactionID)
	}

	status, body = runtime.request(http.MethodGet,
		"/providers/provider-a/wagering/transactions/"+newExternalID("missing").String(), nil, nil)
	requireStatus(t, status, 404, body)
}

// C118 - A queried transaction pending, rejected or failed exposes its exact
// persisted status and applicable deadline or failure code.
func TestTransactionStatusExposure(t *testing.T) {
	t.Run("pending reference", func(t *testing.T) {
		h := newHarness(t)
		view := h.openWallet("1000.00")
		win := h.command(view, financial.KindWin, "10.00")
		win.ReferenceExternalID = newExternalID("bet")
		pending := h.mustSubmit(win)
		runtime := newInternalRuntime(t, h)

		status, body := runtime.request(http.MethodGet, "/wagering/transactions/"+pending.TransactionID.String(), nil, nil)
		requireStatus(t, status, 200, body)
		transaction := decodeJSONBody[transactionJSON](t, body)
		if transaction.Status != "PENDING_REFERENCE" {
			t.Errorf("status = %s, want PENDING_REFERENCE", transaction.Status)
		}
		if transaction.ReferenceDeadline == nil {
			t.Error("pending transaction exposes no referenceDeadline")
		}
		if transaction.Balance != nil {
			t.Errorf("pending transaction balance = %+v, want none", transaction.Balance)
		}
		if transaction.ReferenceExternalTransactionID != win.ReferenceExternalID.String() {
			t.Errorf("reference = %s, want %s", transaction.ReferenceExternalTransactionID, win.ReferenceExternalID)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		h := newHarness(t)
		view := h.openWallet("10.00")
		bet := h.command(view, financial.KindBet, "25.00")
		rejected := h.mustSubmit(bet)
		runtime := newInternalRuntime(t, h)

		status, body := runtime.request(http.MethodGet, "/wagering/transactions/"+rejected.TransactionID.String(), nil, nil)
		requireStatus(t, status, 200, body)
		transaction := decodeJSONBody[transactionJSON](t, body)
		if transaction.Status != "REJECTED" || transaction.FailureCode != "INSUFFICIENT_FUNDS" {
			t.Errorf("status/failureCode = %s/%s, want REJECTED/INSUFFICIENT_FUNDS", transaction.Status, transaction.FailureCode)
		}
		if transaction.Balance == nil || transaction.Balance.Amount != "10.00" {
			t.Errorf("rejected balance = %+v, want 10.00", transaction.Balance)
		}
	})

	t.Run("failed", func(t *testing.T) {
		h := newHarness(t)
		view := h.openWallet("1000.00")
		// Seed one durably accepted operation that ended FAILED with its
		// permanent failure code; the write path is proven by C61.
		failedID := financial.NewTransactionID()
		external := "failed-" + newCorrelation()
		if _, err := adminPool.Exec(h.ctx(),
			`INSERT INTO wager_transactions
				("id", "origin", "kind", "state", "providerId", "externalTransactionId", "idempotencyKey", "digestVersion", "digest",
				 "walletId", "playerId", "roundId", "gameId", "amount", "currency", "referenceExternalTransactionId",
				 "failureCode", "observedBalance", "createdAt", "updatedAt")
			VALUES ($1, 'EXTERNAL', 'REFUND', 'FAILED', 'provider-a', $2, $2, 'sha256-jcs-v1', $3,
				$4, $5, 'round-failed', 'game-failed', 100, 'BRL', 'bet-failed',
				'PERMANENT_INFRASTRUCTURE_FAILURE', 100000, now(), now())`,
			failedID.String(), external, strings.Repeat("0", 64), view.ID.String(), view.PlayerID.String()); err != nil {
			t.Fatalf("seeding a failed transaction: %v", err)
		}
		runtime := newInternalRuntime(t, h)

		status, body := runtime.request(http.MethodGet, "/wagering/transactions/"+failedID.String(), nil, nil)
		requireStatus(t, status, 200, body)
		transaction := decodeJSONBody[transactionJSON](t, body)
		if transaction.Status != "FAILED" || transaction.FailureCode != "PERMANENT_INFRASTRUCTURE_FAILURE" {
			t.Errorf("status/failureCode = %s/%s, want FAILED/PERMANENT_INFRASTRUCTURE_FAILURE", transaction.Status, transaction.FailureCode)
		}
		if transaction.Balance == nil || transaction.Balance.Amount != "1000.00" {
			t.Errorf("failed balance = %+v, want 1000.00", transaction.Balance)
		}
	})
}

// C120 - The reconciliation difference equals stored balance minus ledger
// credits plus ledger debits in the wallet currency.
func TestReconciliationCalculation(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	h.mustSubmit(h.command(view, financial.KindWin, "10.00"))
	h.mustSubmit(h.command(view, financial.KindBet, "25.00"))
	runtime := newInternalRuntime(t, h)

	status, body := runtime.reconcile(view.ID.String())
	requireStatus(t, status, 200, body)
	report := decodeJSONBody[reconciliationJSON](t, body)
	if report.StoredBalance.Amount != "985.00" {
		t.Errorf("storedBalance = %+v, want 985.00", report.StoredBalance)
	}
	if report.CalculatedBalance.Amount != "985.00" {
		t.Errorf("calculatedBalance = %+v, want 985.00 (1000.00 + 10.00 - 25.00)", report.CalculatedBalance)
	}
	if report.Difference.Amount != "0.00" || !report.Consistent {
		t.Errorf("difference/consistent = %+v/%v, want 0.00/true", report.Difference, report.Consistent)
	}
	if report.CheckedEntries != 3 {
		t.Errorf("checkedEntries = %d, want 3", report.CheckedEntries)
	}
}

// C121 - Reconciliation sees opening 1000.00 BRL and bet 25.00 BRL and reports
// stored 975.00, calculated 975.00, difference 0.00, consistent true and two
// checked entries.
func TestReconciliationConsistent(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	h.mustSubmit(h.command(view, financial.KindBet, "25.00"))
	runtime := newInternalRuntime(t, h)

	status, body := runtime.reconcile(view.ID.String())
	requireStatus(t, status, 200, body)
	report := decodeJSONBody[reconciliationJSON](t, body)
	if report.StoredBalance.Amount != "975.00" || report.CalculatedBalance.Amount != "975.00" {
		t.Errorf("stored/calculated = %+v/%+v, want 975.00/975.00", report.StoredBalance, report.CalculatedBalance)
	}
	if report.Difference.Amount != "0.00" || !report.Consistent {
		t.Errorf("difference/consistent = %+v/%v, want 0.00/true", report.Difference, report.Consistent)
	}
	if report.CheckedEntries != 2 {
		t.Errorf("checkedEntries = %d, want 2", report.CheckedEntries)
	}
}

// C122 - Reconciliation detecting a nonzero difference returns that exact
// difference with consistent:false.
func TestReconciliationDivergence(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	h.mustSubmit(h.command(view, financial.KindBet, "25.00"))
	if _, err := adminPool.Exec(h.ctx(),
		`UPDATE wallets SET "balance" = "balance" + 10000 WHERE "id" = $1`, view.ID.String()); err != nil {
		t.Fatalf("introducing a controlled divergence: %v", err)
	}
	runtime := newInternalRuntime(t, h)

	status, body := runtime.reconcile(view.ID.String())
	requireStatus(t, status, 200, body)
	report := decodeJSONBody[reconciliationJSON](t, body)
	if report.StoredBalance.Amount != "1075.00" {
		t.Errorf("storedBalance = %+v, want 1075.00", report.StoredBalance)
	}
	if report.CalculatedBalance.Amount != "975.00" {
		t.Errorf("calculatedBalance = %+v, want 975.00", report.CalculatedBalance)
	}
	if report.Difference.Amount != "100.00" || report.Consistent {
		t.Errorf("difference/consistent = %+v/%v, want 100.00/false", report.Difference, report.Consistent)
	}
}

// C123 - Reconciliation performs no wallet, transaction or ledger write.
func TestReconciliationNoWrite(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	h.mustSubmit(h.command(view, financial.KindBet, "25.00"))
	runtime := newInternalRuntime(t, h)

	counts := func() (int, int, int, int) {
		return h.countRows(`SELECT count(*) FROM wallets`),
			h.countRows(`SELECT count(*) FROM wager_transactions`),
			h.countRows(`SELECT count(*) FROM wallet_ledger_entries`),
			h.countRows(`SELECT count(*) FROM outbox_events`)
	}
	walletsBefore, transactionsBefore, ledgerBefore, outboxBefore := counts()
	var versionBefore int64
	var updatedBefore time.Time
	if err := adminPool.QueryRow(h.ctx(),
		`SELECT "version", "updatedAt" FROM wallets WHERE "id" = $1`, view.ID.String(),
	).Scan(&versionBefore, &updatedBefore); err != nil {
		t.Fatalf("reading the wallet snapshot: %v", err)
	}

	status, body := runtime.reconcile(view.ID.String())
	requireStatus(t, status, 200, body)

	walletsAfter, transactionsAfter, ledgerAfter, outboxAfter := counts()
	if walletsAfter != walletsBefore || transactionsAfter != transactionsBefore ||
		ledgerAfter != ledgerBefore || outboxAfter != outboxBefore {
		t.Errorf("row counts changed: wallets %d->%d transactions %d->%d ledger %d->%d outbox %d->%d",
			walletsBefore, walletsAfter, transactionsBefore, transactionsAfter,
			ledgerBefore, ledgerAfter, outboxBefore, outboxAfter)
	}
	var versionAfter int64
	var updatedAfter time.Time
	if err := adminPool.QueryRow(h.ctx(),
		`SELECT "version", "updatedAt" FROM wallets WHERE "id" = $1`, view.ID.String(),
	).Scan(&versionAfter, &updatedAfter); err != nil {
		t.Fatalf("reading the wallet snapshot after reconciliation: %v", err)
	}
	if versionAfter != versionBefore || !updatedAfter.Equal(updatedBefore) {
		t.Errorf("wallet changed: version %d->%d updatedAt %s->%s", versionBefore, versionAfter, updatedBefore, updatedAfter)
	}
}

type reconciliationJSON struct {
	WalletID          string    `json:"walletId"`
	StoredBalance     moneyJSON `json:"storedBalance"`
	CalculatedBalance moneyJSON `json:"calculatedBalance"`
	Difference        moneyJSON `json:"difference"`
	Consistent        bool      `json:"consistent"`
	CheckedEntries    int       `json:"checkedEntries"`
}

// C124 - A transport or validation error returns the common error envelope
// with a stable code and a correlation id.
func TestErrorEnvelope(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	internal := newInternalRuntime(t, h)
	provider := newProviderRuntime(t, h)
	bet := h.command(view, financial.KindBet, "10.00")

	cases := []struct {
		name     string
		method   string
		path     string
		body     any
		headers  map[string]string
		status   int
		code     string
		useCases *httpRuntime
	}{
		{name: "idempotency key required", method: http.MethodPost, path: "/wagering/transactions",
			body: wagerJSONOf(bet), status: 400, code: "IDEMPOTENCY_KEY_REQUIRED", useCases: provider},
		{name: "invalid money", method: http.MethodPost, path: "/wagering/transactions",
			body: wagerJSON{
				ExternalTransactionID: newExternalID("ext").String(), PlayerID: view.PlayerID.String(),
				WalletID: view.ID.String(), RoundID: newExternalID("round").String(), GameID: newExternalID("game").String(),
				Kind: "BET", Money: moneyJSON{Amount: "1.0", Currency: "BRL"},
			}, headers: map[string]string{"Idempotency-Key": newCorrelation()}, status: 422, code: "INVALID_MONEY", useCases: provider},
		{name: "unknown wallet", method: http.MethodGet, path: "/wallets/" + financial.NewWalletID().String(),
			status: 404, code: "NOT_FOUND", useCases: internal},
		{name: "duplicate wallet", method: http.MethodPost, path: "/wallets",
			body:   openWalletJSON{PlayerID: view.PlayerID.String(), InitialBalance: moneyJSON{Amount: "0.00", Currency: "BRL"}},
			status: 409, code: "WALLET_ALREADY_EXISTS", useCases: internal},
		{name: "invalid cursor", method: http.MethodGet, path: "/wallets/" + view.ID.String() + "/ledger?cursor=%25%25",
			status: 400, code: "INVALID_CURSOR", useCases: internal},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, body := testCase.useCases.request(testCase.method, testCase.path, testCase.body, testCase.headers)
			requireStatus(t, status, testCase.status, body)
			envelope := decodeError(t, body)
			if envelope.Error.Code != testCase.code {
				t.Errorf("code = %s, want %s", envelope.Error.Code, testCase.code)
			}
			if envelope.Error.Message == "" {
				t.Error("envelope carries no message")
			}
			if envelope.Error.CorrelationID == "" {
				t.Error("envelope carries no correlationId")
			}
			if correlation := testCase.useCases.correlation(); correlation != envelope.Error.CorrelationID {
				t.Errorf("correlationId = %s, want the response correlation %s", envelope.Error.CorrelationID, correlation)
			}
		})
	}
}

// unavailableUseCases always reports a transient dependency failure.
type unavailableUseCases struct{}

func (unavailableUseCases) OpenWallet(context.Context, application.OpenWalletCommand) (application.WalletView, error) {
	return application.WalletView{}, &application.Error{Code: application.CodeServiceUnavailable, Message: "service temporarily unavailable"}
}

func (unavailableUseCases) SubmitWagerTransaction(context.Context, application.SubmitWagerCommand) (application.WagerResult, error) {
	return application.WagerResult{}, &application.Error{Code: application.CodeServiceUnavailable, Message: "service temporarily unavailable"}
}

func (unavailableUseCases) WalletByID(context.Context, financial.WalletID) (application.WalletView, error) {
	return application.WalletView{}, &application.Error{Code: application.CodeServiceUnavailable, Message: "service temporarily unavailable"}
}

func (unavailableUseCases) LedgerPage(context.Context, financial.WalletID, string, int) (application.LedgerPage, error) {
	return application.LedgerPage{}, &application.Error{Code: application.CodeServiceUnavailable, Message: "service temporarily unavailable"}
}

func (unavailableUseCases) TransactionByID(context.Context, financial.TransactionID) (application.WagerResult, error) {
	return application.WagerResult{}, &application.Error{Code: application.CodeServiceUnavailable, Message: "service temporarily unavailable"}
}

func (unavailableUseCases) TransactionByProviderAndExternalID(context.Context, financial.ProviderID, financial.ExternalID) (application.WagerResult, error) {
	return application.WagerResult{}, &application.Error{Code: application.CodeServiceUnavailable, Message: "service temporarily unavailable"}
}

func (unavailableUseCases) ReconcileWallet(context.Context, financial.WalletID) (application.ReconciliationReport, error) {
	return application.ReconciliationReport{}, &application.Error{Code: application.CodeServiceUnavailable, Message: "service temporarily unavailable"}
}

// C125 - The HTTP contract distinguishes invalid input 400|422, 401, 403,
// 404, 409, 413|415, 202, 200|201 and 503.
func TestHttpResponseCodes(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	internal := newInternalRuntime(t, h)
	provider := newProviderRuntime(t, h)

	// 201: wallet created.
	playerID := financial.NewPlayerID()
	status, body := internal.request(http.MethodPost, "/wallets",
		openWalletJSON{PlayerID: playerID.String(), InitialBalance: moneyJSON{Amount: "0.00", Currency: "BRL"}}, nil)
	requireStatus(t, status, 201, body)

	// 400: missing idempotency key.
	bet := h.command(view, financial.KindBet, "10.00")
	status, body = provider.postWager(wagerJSONOf(bet), "")
	requireStatus(t, status, 400, body)

	// 422: semantically invalid command.
	status, body = provider.postWager(wagerJSONOf(bet), bet.IdempotencyKey.String())
	requireStatus(t, status, 200, body) // control: the same command is otherwise valid
	invalid := wagerJSONOf(h.command(view, financial.KindLoss, "1.00"))
	status, body = provider.postWager(invalid, newCorrelation())
	requireStatus(t, status, 422, body)

	// 401: no credential.
	noCredential := newHTTPRuntimeWith(t, h, unauthenticated(), nil)
	status, body = noCredential.request(http.MethodGet, "/wallets/"+view.ID.String(), nil, nil)
	requireStatus(t, status, 401, body)

	// 403: authenticated caller without the route scope.
	readOnly := newHTTPRuntimeWith(t, h, internalClient(middleware.ScopeWalletsRead), nil)
	status, body = readOnly.request(http.MethodPost, "/wallets",
		openWalletJSON{PlayerID: financial.NewPlayerID().String(), InitialBalance: moneyJSON{Amount: "0.00", Currency: "BRL"}}, nil)
	requireStatus(t, status, 403, body)

	// 404: absent resource.
	status, body = internal.request(http.MethodGet, "/wallets/"+financial.NewWalletID().String(), nil, nil)
	requireStatus(t, status, 404, body)

	// 409: duplicate wallet.
	status, body = internal.request(http.MethodPost, "/wallets",
		openWalletJSON{PlayerID: playerID.String(), InitialBalance: moneyJSON{Amount: "0.00", Currency: "BRL"}}, nil)
	requireStatus(t, status, 409, body)

	// 413: body above 1 MiB.
	oversized := `{"playerId":"` + strings.Repeat("a", 1<<20) + `"}`
	status, body = internal.request(http.MethodPost, "/wallets", oversized, nil)
	requireStatus(t, status, 413, body)

	// 415: unsupported media type.
	status, body = internal.request(http.MethodPost, "/wallets",
		openWalletJSON{PlayerID: financial.NewPlayerID().String(), InitialBalance: moneyJSON{Amount: "0.00", Currency: "BRL"}},
		map[string]string{"Content-Type": "text/plain"})
	requireStatus(t, status, 415, body)

	// 202: accepted pending reference.
	pending := h.command(view, financial.KindWin, "10.00")
	pending.ReferenceExternalID = newExternalID("bet")
	status, body = provider.postWager(wagerJSONOf(pending), pending.IdempotencyKey.String())
	requireStatus(t, status, 202, body)

	// 200: durable processed outcome.
	processed := h.command(view, financial.KindBet, "10.00")
	status, body = provider.postWager(wagerJSONOf(processed), processed.IdempotencyKey.String())
	requireStatus(t, status, 200, body)

	// 503: transient dependency failure.
	unavailable := newHandlerRuntime(t, h, unavailableUseCases{}, internalClient(middleware.ScopeWalletsWrite), 16)
	status, body = unavailable.request(http.MethodPost, "/wallets",
		openWalletJSON{PlayerID: financial.NewPlayerID().String(), InitialBalance: moneyJSON{Amount: "0.00", Currency: "BRL"}}, nil)
	requireStatus(t, status, 503, body)
}
