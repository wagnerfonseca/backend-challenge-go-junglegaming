//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http"
	deadapter "github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/postgres"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/sqs/consumer"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/event"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// C44 - OPENING and unknown kinds are rejected as invalid external input and
// persist nothing.
func TestInvalidKindRejection(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	for _, kind := range []financial.Kind{financial.KindOpening, financial.Kind("TRANSFER")} {
		cmd := h.command(view, kind, "25.00")
		h.mustFailWith(cmd, application.CodeInvalidRequest)
		if h.transactionExists(cmd.ProviderID.String(), cmd.ExternalTransactionID) {
			t.Errorf("kind %q persisted a transaction", kind)
		}
	}
	if got := h.transactionsForWallet(view.ID); got != 1 {
		t.Errorf("transactions = %d, want 1 (only the opening)", got)
	}
}

// C45 - A command without an idempotency key is rejected with
// IDEMPOTENCY_KEY_REQUIRED and persists nothing. The HTTP 400 status is
// deferred to the HTTP adapter (batch C).
func TestMissingIdempotencyKey(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	cmd := h.command(view, financial.KindBet, "25.00")
	cmd.IdempotencyKey = ""
	h.mustFailWith(cmd, application.CodeIdempotencyKeyRequired)
	if h.transactionExists(cmd.ProviderID.String(), cmd.ExternalTransactionID) {
		t.Error("a command without an idempotency key persisted a transaction")
	}

	// C45 transport boundary: POST /wagering/transactions returns 400
	// IDEMPOTENCY_KEY_REQUIRED and persists nothing.
	runtime := newHTTPRuntime(t)
	httpView := runtime.harness.openWallet("100.00")
	httpCommand := runtime.harness.command(httpView, financial.KindBet, "25.00")
	status, data := runtime.postWager(wagerJSONOf(httpCommand), "")
	requireStatus(t, status, 400, data)
	if envelope := decodeError(t, data); envelope.Error.Code != "IDEMPOTENCY_KEY_REQUIRED" {
		t.Errorf("code = %s, want IDEMPOTENCY_KEY_REQUIRED", envelope.Error.Code)
	}
	if runtime.harness.transactionExists(httpCommand.ProviderID.String(), httpCommand.ExternalTransactionID) {
		t.Error("HTTP request without Idempotency-Key persisted a transaction")
	}
}

// C46 - A valid independent operation returns the transaction identity, a
// terminal status, the persisted balance and idempotentReplay:false. The HTTP
// 200 status is deferred to the HTTP adapter (batch C).
func TestValidOperationResponse(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	result := h.mustSubmit(h.command(view, financial.KindBet, "25.00"))
	if result.TransactionID.IsZero() {
		t.Error("transactionId is empty")
	}
	if result.State != financial.StateProcessed {
		t.Errorf("state = %s, want PROCESSED", result.State)
	}
	if result.ObservedBalance.MinorUnits() != 97500 {
		t.Errorf("balance = %d, want 97500", result.ObservedBalance.MinorUnits())
	}
	if result.IdempotentReplay {
		t.Error("idempotentReplay = true, want false")
	}

	// C46 transport boundary: POST /wagering/transactions returns 200 with
	// transactionId, terminal status, persisted balance and idempotentReplay:false.
	runtime := newHTTPRuntime(t)
	httpView := runtime.harness.openWallet("1000.00")
	httpCommand := runtime.harness.command(httpView, financial.KindBet, "25.00")
	status, data := runtime.postWager(wagerJSONOf(httpCommand), httpCommand.IdempotencyKey.String())
	requireStatus(t, status, 200, data)
	httpResult := decodeJSONBody[wagerResultJSON](t, data)
	if httpResult.TransactionID == "" {
		t.Error("HTTP transactionId is empty")
	}
	if httpResult.Status != "PROCESSED" {
		t.Errorf("HTTP status = %s, want PROCESSED", httpResult.Status)
	}
	if httpResult.Balance == nil || httpResult.Balance.Amount != "975.00" || httpResult.Balance.Currency != "BRL" {
		t.Errorf("HTTP balance = %+v, want 975.00 BRL", httpResult.Balance)
	}
	if httpResult.IdempotentReplay {
		t.Error("HTTP idempotentReplay = true, want false")
	}
}

// C47 - The persisted digest is SHA-256 over the canonical sha256-jcs-v1
// business projection, independently recomputed here.
func TestIdempotencyDigest(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	cmd := h.command(view, financial.KindBet, "25.00")
	result := h.mustSubmit(cmd)

	record := h.transactionByID(result.TransactionID)
	if record.DigestVersion == nil || *record.DigestVersion != "sha256-jcs-v1" {
		t.Fatalf("digestVersion = %v, want sha256-jcs-v1", record.DigestVersion)
	}
	canonical := fmt.Sprintf(
		`{"amount":"25.00","currency":"BRL","externalTransactionId":%q,"gameId":%q,"kind":"BET","playerId":%q,"providerId":%q,"roundId":%q,"walletId":%q}`,
		cmd.ExternalTransactionID.String(), cmd.GameID.String(), cmd.PlayerID.String(),
		cmd.ProviderID.String(), cmd.RoundID.String(), cmd.WalletID.String(),
	)
	sum := sha256.Sum256([]byte(canonical))
	want := hex.EncodeToString(sum[:])
	if record.Digest == nil || *record.Digest != want {
		t.Errorf("digest = %v, want %s (canonical %s)", record.Digest, want, canonical)
	}
}

// C48 - The same key with an equivalent business projection replays the
// persisted result and adds no movement, ledger row or outbox row.
func TestIdempotentReplay(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	cmd := h.command(view, financial.KindBet, "25.00")
	first := h.mustSubmit(cmd)
	transactions := h.transactionsForWallet(view.ID)
	ledger := h.ledgerForWallet(view.ID)
	outbox := h.outboxForWallet(view.ID)

	second := h.mustSubmit(cmd)
	if !second.IdempotentReplay {
		t.Error("idempotentReplay = false, want true")
	}
	if second.TransactionID != first.TransactionID {
		t.Errorf("transactionId = %s, want the persisted %s", second.TransactionID, first.TransactionID)
	}
	if second.State != first.State || second.ObservedBalance.MinorUnits() != first.ObservedBalance.MinorUnits() {
		t.Errorf("replay = %s %d, want %s %d", second.State, second.ObservedBalance.MinorUnits(), first.State, first.ObservedBalance.MinorUnits())
	}
	if got := h.transactionsForWallet(view.ID); got != transactions {
		t.Errorf("transactions = %d, want %d", got, transactions)
	}
	if got := h.ledgerForWallet(view.ID); got != ledger {
		t.Errorf("ledger entries = %d, want %d", got, ledger)
	}
	if got := h.outboxForWallet(view.ID); got != outbox {
		t.Errorf("outbox events = %d, want %d", got, outbox)
	}
}

// C49 - The same provider key with a different business projection returns
// IDEMPOTENCY_CONFLICT and preserves the first result.
func TestIdempotencyConflict(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	first := h.command(view, financial.KindBet, "25.00")
	h.mustSubmit(first)

	conflicting := h.command(view, financial.KindBet, "25.01")
	conflicting.IdempotencyKey = first.IdempotencyKey
	_, err := h.submit(conflicting)
	if !errors.Is(err, application.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if code, _ := application.ErrorCodeOf(err); code != application.CodeIdempotencyConflict {
		t.Fatalf("code = %s, want IDEMPOTENCY_CONFLICT", code)
	}
	preserved := h.transactionByExternal(first.ProviderID.String(), first.ExternalTransactionID)
	if preserved.State != "PROCESSED" || preserved.Amount != 2500 {
		t.Errorf("first result changed: state=%s amount=%d", preserved.State, preserved.Amount)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 97500 {
		t.Errorf("balance = %d, want 97500", got)
	}

	// C49 transport boundary: POST returns 409 IDEMPOTENCY_CONFLICT.
	runtime := newHTTPRuntime(t)
	httpView := runtime.harness.openWallet("1000.00")
	firstHTTP := runtime.harness.command(httpView, financial.KindBet, "25.00")
	status, data := runtime.postWager(wagerJSONOf(firstHTTP), firstHTTP.IdempotencyKey.String())
	requireStatus(t, status, 200, data)
	conflict := runtime.harness.command(httpView, financial.KindBet, "25.01")
	conflict.IdempotencyKey = firstHTTP.IdempotencyKey
	status, data = runtime.postWager(wagerJSONOf(conflict), conflict.IdempotencyKey.String())
	requireStatus(t, status, 409, data)
	if envelope := decodeError(t, data); envelope.Error.Code != "IDEMPOTENCY_CONFLICT" {
		t.Errorf("code = %s, want IDEMPOTENCY_CONFLICT", envelope.Error.Code)
	}
}

// C50 - The same (providerId, externalTransactionId) under a different key
// returns EXTERNAL_TRANSACTION_CONFLICT and preserves the first result.
func TestExternalTransactionConflict(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	first := h.command(view, financial.KindBet, "25.00")
	h.mustSubmit(first)

	conflicting := first
	conflicting.IdempotencyKey = financial.IdempotencyKey("key-" + newCorrelation())
	_, err := h.submit(conflicting)
	if !errors.Is(err, application.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if code, _ := application.ErrorCodeOf(err); code != application.CodeExternalTransactionConflict {
		t.Fatalf("code = %s, want EXTERNAL_TRANSACTION_CONFLICT", code)
	}
	if got := h.transactionsForWallet(view.ID); got != 2 {
		t.Errorf("transactions = %d, want 2 (opening and one bet)", got)
	}
	if got := h.ledgerForWallet(view.ID); got != 2 {
		t.Errorf("ledger entries = %d, want 2", got)
	}

	// C50 transport boundary: POST returns 409 EXTERNAL_TRANSACTION_CONFLICT.
	runtime := newHTTPRuntime(t)
	httpView := runtime.harness.openWallet("1000.00")
	firstHTTP := runtime.harness.command(httpView, financial.KindBet, "25.00")
	status, data := runtime.postWager(wagerJSONOf(firstHTTP), firstHTTP.IdempotencyKey.String())
	requireStatus(t, status, 200, data)
	secondHTTP := firstHTTP
	secondHTTP.IdempotencyKey = financial.IdempotencyKey("key-" + newCorrelation())
	status, data = runtime.postWager(wagerJSONOf(secondHTTP), secondHTTP.IdempotencyKey.String())
	requireStatus(t, status, 409, data)
	if envelope := decodeError(t, data); envelope.Error.Code != "EXTERNAL_TRANSACTION_CONFLICT" {
		t.Errorf("code = %s, want EXTERNAL_TRANSACTION_CONFLICT", envelope.Error.Code)
	}
}

// C51 - Equivalent commands submitted for the two ingresses converge on one
// WagerTransaction and at most one financial movement, because both adapters
// invoke the same use case.
func TestHTTPSQSIddempotency(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	overHTTP := h.command(view, financial.KindBet, "25.00")
	overSQS := overHTTP
	first := h.mustSubmit(overHTTP)
	second := h.mustSubmit(overSQS)
	if second.TransactionID != first.TransactionID || !second.IdempotentReplay {
		t.Errorf("second ingress produced %s replay=%v, want %s replay=true", second.TransactionID, second.IdempotentReplay, first.TransactionID)
	}
	if got := h.transactionsForWallet(view.ID); got != 2 {
		t.Errorf("transactions = %d, want 2", got)
	}
	if got := h.ledgerForWallet(view.ID); got != 2 {
		t.Errorf("ledger entries = %d, want 2", got)
	}
	if got := h.outboxCountOfType(view.ID, string(event.TypeWagerTransactionProcessed)); got != 2 {
		t.Errorf("processed events = %d, want 2 (opening and one bet)", got)
	}

	// C51 transport boundary: the same command over HTTP and over the real
	// consumer produces one transaction and one debit.
	runtime := newHTTPRuntime(t)
	httpView := runtime.harness.openWallet("1000.00")
	command := runtime.harness.command(httpView, financial.KindBet, "25.00")
	status, data := runtime.postWager(wagerJSONOf(command), command.IdempotencyKey.String())
	requireStatus(t, status, 200, data)
	broker := &sqsFake{}
	messageID := "message-" + newCorrelation()
	broker.enqueue(sqsMessage(messageID, "sender-a", 1, envelopeJSON(t, envelopeFor(t, command, messageID))))
	if err := newConsumer(runtime.harness, broker).PollOnce(context.Background()); err != nil {
		t.Fatalf("processing SQS delivery: %v", err)
	}
	if got := runtime.harness.transactionsForWallet(httpView.ID); got != 2 {
		t.Errorf("transactions after HTTP+SQS = %d, want 2 (opening and one bet)", got)
	}
	if got := runtime.harness.ledgerForWallet(httpView.ID); got != 2 {
		t.Errorf("ledger entries after HTTP+SQS = %d, want 2", got)
	}
	if handles := broker.deletedHandles(); len(handles) != 1 || handles[0] != "receipt-"+messageID {
		t.Errorf("deleted handles = %v, want the duplicate message deleted", handles)
	}
}

// C52 - A restart preserves every idempotency decision in PostgreSQL.
func TestRestartIdempotencyPreserved(t *testing.T) {
	first := newHarness(t)
	view := first.openWallet("1000.00")
	cmd := first.command(view, financial.KindBet, "25.00")
	original := first.mustSubmit(cmd)

	restarted := newHarness(t)
	replayed, err := restarted.service.SubmitWagerTransaction(context.Background(), cmd)
	if err != nil {
		t.Fatalf("replaying after restart: %v", err)
	}
	if !replayed.IdempotentReplay || replayed.TransactionID != original.TransactionID {
		t.Errorf("replay = %s replay=%v, want %s replay=true", replayed.TransactionID, replayed.IdempotentReplay, original.TransactionID)
	}
}

// C53 - Replaying a terminal operation after later wallet movements returns
// the balance captured by the original operation.
func TestTerminalReplayBalance(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	bet := h.command(view, financial.KindBet, "25.00")
	original := h.mustSubmit(bet)
	if original.ObservedBalance.MinorUnits() != 97500 {
		t.Fatalf("original balance = %d, want 97500", original.ObservedBalance.MinorUnits())
	}
	h.mustSubmit(h.command(view, financial.KindWin, "100.00"))
	if got := h.walletBalance(view.ID).MinorUnits(); got != 107500 {
		t.Fatalf("current balance = %d, want 107500", got)
	}
	replayed := h.mustSubmit(bet)
	if replayed.ObservedBalance.MinorUnits() != 97500 {
		t.Errorf("replay balance = %d, want the captured 97500", replayed.ObservedBalance.MinorUnits())
	}
}

// C54 - An accepted external transaction persists every required field,
// including the resolved reference identity for a dependent operation.
func TestTransactionPersistence(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	bet := h.command(view, financial.KindBet, "25.00")
	betResult := h.mustSubmit(bet)

	record := h.transactionByExternal(bet.ProviderID.String(), bet.ExternalTransactionID)
	if record.ID != betResult.TransactionID.String() {
		t.Errorf("id = %s, want %s", record.ID, betResult.TransactionID)
	}
	if record.Origin != "EXTERNAL" || record.Kind != "BET" || record.State != "PROCESSED" {
		t.Errorf("origin/kind/state = %s/%s/%s, want EXTERNAL/BET/PROCESSED", record.Origin, record.Kind, record.State)
	}
	if deref(record.ProviderID) != bet.ProviderID.String() || deref(record.ExternalTransactionID) != bet.ExternalTransactionID.String() {
		t.Error("provider or external identity was not persisted")
	}
	if deref(record.IdempotencyKey) != bet.IdempotencyKey.String() {
		t.Errorf("idempotencyKey = %v, want the exact supplied key", record.IdempotencyKey)
	}
	if deref(record.DigestVersion) != "sha256-jcs-v1" || len(deref(record.Digest)) != 64 {
		t.Errorf("digest version/value = %v/%v", record.DigestVersion, record.Digest)
	}
	if record.WalletID != view.ID.String() || record.PlayerID != view.PlayerID.String() {
		t.Error("wallet or player identity was not persisted")
	}
	if deref(record.RoundID) != bet.RoundID.String() || deref(record.GameID) != bet.GameID.String() {
		t.Error("round or game identity was not persisted")
	}
	if record.Amount != 2500 || record.Currency != "BRL" {
		t.Errorf("money = %d %s, want 2500 BRL", record.Amount, record.Currency)
	}
	if record.ReferenceExternalID != nil || record.ReferenceTransactionID != nil {
		t.Error("an independent BET persisted a reference")
	}
	if record.FailureCode != nil {
		t.Errorf("failureCode = %v, want NULL", record.FailureCode)
	}
	if record.ObservedBalance == nil || *record.ObservedBalance != 97500 {
		t.Errorf("observedBalance = %v, want 97500", record.ObservedBalance)
	}
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
		t.Error("timestamps were not persisted")
	}

	refund := h.command(view, financial.KindRefund, "25.00")
	refund.ReferenceExternalID = bet.ExternalTransactionID
	refund.RoundID = bet.RoundID
	refundResult := h.mustSubmit(refund)
	refundRecord := h.transactionByID(refundResult.TransactionID)
	if deref(refundRecord.ReferenceExternalID) != bet.ExternalTransactionID.String() {
		t.Error("external reference was not persisted")
	}
	if deref(refundRecord.ReferenceTransactionID) != betResult.TransactionID.String() {
		t.Errorf("referenceTransactionId = %v, want %s", refundRecord.ReferenceTransactionID, betResult.TransactionID)
	}
}

// C55 - An operation with no unresolved dependency is terminal when its first
// transaction commits: no PENDING row ever survives.
func TestImmediateTerminalTransition(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	for _, kind := range []financial.Kind{financial.KindBet, financial.KindLoss, financial.KindWin} {
		amount := "10.00"
		if kind == financial.KindLoss {
			amount = "0.00"
		}
		result := h.mustSubmit(h.command(view, kind, amount))
		if !result.State.IsTerminal() {
			t.Errorf("%s state = %s, want terminal", kind, result.State)
		}
	}
	if got := h.countRows(`SELECT count(*) FROM wager_transactions WHERE "walletId" = $1 AND "state" IN ('PENDING', 'PENDING_REFERENCE')`, view.ID.String()); got != 0 {
		t.Errorf("waiting transactions = %d, want 0", got)
	}
}

// C56 - An operation with an unresolved reference commits as
// PENDING_REFERENCE with durable retry scheduling.
func TestPendingReferenceCommit(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	refund := h.command(view, financial.KindRefund, "25.00")
	refund.ReferenceExternalID = newExternalID("bet")
	result := h.mustSubmit(refund)
	if result.State != financial.StatePendingReference {
		t.Fatalf("state = %s, want PENDING_REFERENCE", result.State)
	}
	if result.ObservedBalance.IsInitialized() {
		t.Error("pending result reported a terminal balance")
	}
	record := h.transactionByID(result.TransactionID)
	if record.State != "PENDING_REFERENCE" {
		t.Fatalf("persisted state = %s, want PENDING_REFERENCE", record.State)
	}
	if record.ReferenceDeadline == nil {
		t.Fatal("referenceDeadline was not persisted")
	}
	created := record.CreatedAt
	deadline := record.ReferenceDeadline
	if deadline.Sub(created) != 24*time.Hour {
		t.Errorf("deadline = %s after creation, want 24h", deadline.Sub(created))
	}
	if record.NextAttemptAt == nil {
		t.Fatal("nextAttemptAt was not persisted")
	}
	delay := record.NextAttemptAt.Sub(created)
	if delay < time.Second || delay > time.Second+100*time.Millisecond {
		t.Errorf("first retry delay = %s, want within [1s, 1.1s]", delay)
	}
	if record.ReferenceAttempts != 0 {
		t.Errorf("referenceAttempts = %d, want 0", record.ReferenceAttempts)
	}
}

// C59 - A PENDING_REFERENCE transaction exposes its status and reference
// deadline without a terminal balance. The HTTP body is deferred to the HTTP
// adapter (batch C).
func TestPendingReferenceQuery(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	refund := h.command(view, financial.KindRefund, "25.00")
	refund.ReferenceExternalID = newExternalID("bet")
	submitted := h.mustSubmit(refund)

	result, err := h.service.TransactionByID(h.ctx(), submitted.TransactionID)
	if err != nil {
		t.Fatalf("querying pending transaction: %v", err)
	}
	if result.State != financial.StatePendingReference {
		t.Errorf("state = %s, want PENDING_REFERENCE", result.State)
	}
	if result.ReferenceDeadline.IsZero() {
		t.Error("reference deadline is missing")
	}
	if result.ObservedBalance.IsInitialized() {
		t.Error("pending transaction reported a terminal balance")
	}

	// C59 transport boundary: GET /wagering/transactions/{id} returns 200 with
	// the exact status and deadline and no terminal balance.
	runtime := newHTTPRuntime(t)
	httpView := runtime.harness.openWallet("1000.00")
	httpRefund := runtime.harness.command(httpView, financial.KindRefund, "25.00")
	httpRefund.ReferenceExternalID = newExternalID("bet")
	httpPending := runtime.harness.mustSubmit(httpRefund)
	status, data := runtime.getTransaction(httpPending.TransactionID.String())
	requireStatus(t, status, 200, data)
	body := decodeJSONBody[wagerResultJSON](t, data)
	if body.Status != "PENDING_REFERENCE" {
		t.Errorf("HTTP status = %s, want PENDING_REFERENCE", body.Status)
	}
	if body.ReferenceDeadline == nil {
		t.Error("HTTP reference deadline is missing")
	}
	if body.Balance != nil {
		t.Errorf("HTTP pending response reported balance %+v, want none", body.Balance)
	}
}

// C60 - A rejected transaction persists a stable failureCode and the balance
// observed when the rejection was decided.
func TestRejectedPersistsFailureCode(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	bet := h.command(view, financial.KindBet, "150.00")
	result := h.mustSubmit(bet)
	if result.State != financial.StateRejected {
		t.Fatalf("state = %s, want REJECTED", result.State)
	}
	record := h.transactionByID(result.TransactionID)
	if deref(record.FailureCode) != "INSUFFICIENT_FUNDS" {
		t.Errorf("failureCode = %v, want INSUFFICIENT_FUNDS", record.FailureCode)
	}
	if record.ObservedBalance == nil || *record.ObservedBalance != 10000 {
		t.Errorf("observedBalance = %v, want 10000", record.ObservedBalance)
	}
	if record.State != "REJECTED" {
		t.Errorf("persisted state = %s, want REJECTED", record.State)
	}
}

// C61 - Accepted asynchronous work whose persisted reference violates an
// infrastructure invariant ends FAILED with PERMANENT_INFRASTRUCTURE_FAILURE
// and no wallet movement.
func TestPermanentFailureNoMovement(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	refund := h.command(view, financial.KindRefund, "100.00")
	referenceExternal := newExternalID("bet")
	refund.ReferenceExternalID = referenceExternal
	pending := h.mustSubmit(refund)
	if pending.State != financial.StatePendingReference {
		t.Fatalf("state = %s, want PENDING_REFERENCE", pending.State)
	}
	bet := h.command(view, financial.KindBet, "100.00")
	bet.ExternalTransactionID = referenceExternal
	h.mustSubmit(bet)
	ledgerBefore := h.ledgerForWallet(view.ID)

	if _, err := adminPool.Exec(h.ctx(),
		`UPDATE wager_transactions SET "idempotencyKey" = '' WHERE "providerId" = $1 AND "externalTransactionId" = $2`,
		bet.ProviderID.String(), referenceExternal.String()); err != nil {
		t.Fatalf("corrupting the reference row: %v", err)
	}

	resolved, err := h.service.ResolvePendingReference(h.ctx(), application.ResolvePendingReferenceCommand{
		TransactionID: pending.TransactionID,
		CorrelationID: newCorrelation(),
	})
	if err != nil {
		t.Fatalf("resolving corrupt reference: %v", err)
	}
	if resolved.State != financial.StateFailed {
		t.Fatalf("state = %s, want FAILED", resolved.State)
	}
	if resolved.FailureCode != financial.FailurePermanentInfrastructure {
		t.Errorf("failureCode = %s, want PERMANENT_INFRASTRUCTURE_FAILURE", resolved.FailureCode)
	}
	record := h.transactionByID(pending.TransactionID)
	if record.State != "FAILED" || deref(record.FailureCode) != "PERMANENT_INFRASTRUCTURE_FAILURE" {
		t.Errorf("persisted = %s/%v, want FAILED/PERMANENT_INFRASTRUCTURE_FAILURE", record.State, record.FailureCode)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 90000 {
		t.Errorf("balance = %d, want 90000 (no movement on failure)", got)
	}
	if got := h.ledgerForWallet(view.ID); got != ledgerBefore {
		t.Errorf("ledger entries = %d, want %d (no movement on failure)", got, ledgerBefore)
	}
}

// C62 - PostgreSQL unavailability before a durable commit is a transient
// error and permits a safe retry; the HTTP 503 and SQS retry are deferred to
// batches B and C.
func TestDatabaseUnavailableHandling(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")

	config, err := pgxpool.ParseConfig(testDSN)
	if err != nil {
		t.Fatalf("parsing dsn: %v", err)
	}
	config.ConnConfig.User = "wager_app"
	config.ConnConfig.Password = "wager_app_local"
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("opening disposable pool: %v", err)
	}
	offline := application.NewWagerService(deadapter.NewStore(pool), &testClock{now: h.clock.Now()})
	pool.Close()

	command := h.command(view, financial.KindBet, "25.00")
	_, err = offline.SubmitWagerTransaction(context.Background(), command)
	if err == nil {
		t.Fatal("submission with a closed pool succeeded, want transient failure")
	}
	if !application.IsTransient(err) {
		t.Fatalf("error = %v, want a transient infrastructure failure", err)
	}
	if h.transactionExists(command.ProviderID.String(), command.ExternalTransactionID) {
		t.Error("a failed transaction persisted a partial row")
	}

	retried := h.mustSubmit(command)
	if retried.State != financial.StateProcessed {
		t.Errorf("retry state = %s, want PROCESSED", retried.State)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 97500 {
		t.Errorf("balance = %d, want 97500", got)
	}

	// C62 transport boundary: HTTP returns 503 before a durable commit and the
	// SQS consumer leaves the message unacknowledged for retry.
	runtime := newHTTPRuntime(t)
	offlineConfig, err := pgxpool.ParseConfig(testDSN)
	if err != nil {
		t.Fatalf("parsing dsn: %v", err)
	}
	offlineConfig.ConnConfig.User = "wager_app"
	offlineConfig.ConnConfig.Password = "wager_app_local"
	offlinePool, err := pgxpool.NewWithConfig(context.Background(), offlineConfig)
	if err != nil {
		t.Fatalf("opening offline pool: %v", err)
	}
	offlineService := application.NewWagerService(deadapter.NewStore(offlinePool), &testClock{now: h.clock.Now()})
	offlinePool.Close()
	httpView := runtime.harness.openWallet("1000.00")
	httpCommand := runtime.harness.command(httpView, financial.KindBet, "25.00")
	offlineHandler := httpadapter.NewHandler(httpadapter.Config{
		UseCases:      offlineService,
		Authenticator: allScopes("provider-a"),
	})
	offlineServer := httptest.NewServer(offlineHandler.Routes())
	defer offlineServer.Close()
	encoded, _ := json.Marshal(wagerJSONOf(httpCommand))
	request, _ := http.NewRequest(http.MethodPost, offlineServer.URL+"/wagering/transactions", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", httpCommand.IdempotencyKey.String())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("offline HTTP request: %v", err)
	}
	offlineBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	requireStatus(t, response.StatusCode, http.StatusServiceUnavailable, offlineBody)

	broker := &sqsFake{}
	messageID := "message-" + newCorrelation()
	broker.enqueue(sqsMessage(messageID, "sender-a", 1, envelopeJSON(t, envelopeFor(t, httpCommand, messageID))))
	offlineConsumer := consumer.New(consumer.Config{
		QueueURL:     "http://fake/wager-transactions.fifo",
		ConsumerName: "wager-transactions",
		ProviderForSender: func(string) (string, bool) {
			return "provider-a", true
		},
	}, broker, offlineService)
	if err := offlineConsumer.PollOnce(context.Background()); err != nil {
		t.Fatalf("offline consumer pass: %v", err)
	}
	if handles := broker.deletedHandles(); len(handles) != 0 {
		t.Errorf("offline SQS handling deleted %v, want no acknowledgement", handles)
	}
	if changes := broker.visibilityChanges(); len(changes) == 0 {
		t.Error("offline SQS handling did not release the message for redelivery")
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
