//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/event"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// C28 - Opening a wallet with a positive balance returns its identity, player,
// balance and version 1.
func TestWalletOpenPositiveBalance(t *testing.T) {
	h := newHarness(t)
	player := financial.NewPlayerID()
	view, err := h.service.OpenWallet(h.ctx(), application.OpenWalletCommand{
		PlayerID:       player,
		InitialBalance: mustMoney(t, "1000.00", "BRL"),
		CorrelationID:  newCorrelation(),
	})
	if err != nil {
		t.Fatalf("opening wallet: %v", err)
	}
	if view.ID.IsZero() {
		t.Error("wallet id is empty")
	}
	if view.PlayerID != player {
		t.Errorf("playerId = %s, want %s", view.PlayerID, player)
	}
	if view.Balance.MinorUnits() != 100000 || view.Balance.Currency() != "BRL" {
		t.Errorf("balance = %s %s, want 1000.00 BRL", view.Balance, view.Balance.Currency())
	}
	if view.Version != 1 {
		t.Errorf("version = %d, want 1", view.Version)
	}

	// C28 transport boundary: POST /wallets returns 201 with the wallet view.
	runtime := newHTTPRuntime(t)
	httpPlayer := financial.NewPlayerID()
	status, data := runtime.postWallet(openWalletJSON{
		PlayerID:       httpPlayer.String(),
		InitialBalance: moneyJSON{Amount: "1000.00", Currency: "BRL"},
	})
	requireStatus(t, status, 201, data)
	opened := decodeJSONBody[walletJSON](t, data)
	if opened.ID == "" || opened.PlayerID != httpPlayer.String() {
		t.Errorf("HTTP wallet identity = %q/%q, want a new id for %s", opened.ID, opened.PlayerID, httpPlayer)
	}
	if opened.Balance.Amount != "1000.00" || opened.Balance.Currency != "BRL" || opened.Version != 1 {
		t.Errorf("HTTP wallet balance/version = %+v/%d, want 1000.00 BRL/1", opened.Balance, opened.Version)
	}
}

// C29 - A positive opening persists exactly one PROCESSED OPENING transaction
// with a stable internal identity. C212 - the database rejects a second
// OPENING for the same wallet.
func TestWalletOpenPersistsOpening(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	if got := h.transactionsForWallet(view.ID); got != 1 {
		t.Fatalf("wager_transactions for wallet = %d, want 1", got)
	}
	rows, err := adminPool.Query(h.ctx(),
		`SELECT "id", "origin", "kind", "state", "providerId", "externalTransactionId", "idempotencyKey", "digestVersion", "digest", "roundId", "gameId", "referenceExternalTransactionId"
		FROM wager_transactions WHERE "walletId" = $1`, view.ID.String())
	if err != nil {
		t.Fatalf("reading opening: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("opening row is missing")
	}
	var id, origin, kind, state string
	var providerID, externalID, key, digestVersion, digest, roundID, gameID, reference *string
	if err := rows.Scan(&id, &origin, &kind, &state, &providerID, &externalID, &key, &digestVersion, &digest, &roundID, &gameID, &reference); err != nil {
		t.Fatalf("scanning opening: %v", err)
	}
	if _, err := uuid.Parse(id); err != nil {
		t.Errorf("opening id %q is not a canonical UUID: %v", id, err)
	}
	if origin != "INTERNAL" || kind != "OPENING" || state != "PROCESSED" {
		t.Errorf("opening = %s/%s/%s, want INTERNAL/OPENING/PROCESSED", origin, kind, state)
	}
	if providerID != nil || externalID != nil || key != nil || digestVersion != nil || digest != nil || roundID != nil || gameID != nil || reference != nil {
		t.Error("internal opening persisted external metadata")
	}

	_, err = adminPool.Exec(h.ctx(), `INSERT INTO wager_transactions
		("id", "origin", "kind", "state", "walletId", "playerId", "amount", "currency", "observedBalance", "createdAt", "updatedAt")
		VALUES ($1, 'INTERNAL', 'OPENING', 'PROCESSED', $2, $3, 100, 'BRL', 100, now(), now())`,
		uuid.NewString(), view.ID.String(), view.PlayerID.String())
	assertConstraintViolation(t, err, "wager_transactions_opening_walletid_idx")
}

// C30 - A positive opening appends one CREDIT ledger entry from 0.00 to the
// initial balance. C42 - the schema rejects a second entry for the same
// wallet/transaction.
func TestWalletOpenLedgerEntry(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	entries := h.ledgerRows(view.ID)
	if len(entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Direction != "CREDIT" {
		t.Errorf("direction = %s, want CREDIT", entry.Direction)
	}
	if entry.Amount != 100000 || entry.BalanceBefore != 0 || entry.BalanceAfter != 100000 {
		t.Errorf("entry = %d %d -> %d, want 100000 0 -> 100000", entry.Amount, entry.BalanceBefore, entry.BalanceAfter)
	}
	if _, err := uuid.Parse(entry.ID); err != nil {
		t.Errorf("ledger id %q is not a canonical UUID: %v", entry.ID, err)
	}

	_, err := adminPool.Exec(h.ctx(), `INSERT INTO wallet_ledger_entries
		("id", "walletId", "transactionId", "direction", "amount", "balanceBefore", "balanceAfter", "createdAt")
		VALUES ($1, $2, $3, 'CREDIT', 1, 0, 1, now())`,
		uuid.NewString(), view.ID.String(), entry.TransactionID)
	assertConstraintViolation(t, err, "wallet_ledger_entries_walletid_transactionid_key")
}

// C31 - A positive opening persists exactly one WagerTransactionProcessed
// event, paired with the balance change its ledger entry records.
func TestWalletOpenOutboxEvent(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	if got := h.outboxCountOfType(view.ID, string(event.TypeWagerTransactionProcessed)); got != 1 {
		t.Fatalf("WagerTransactionProcessed events = %d, want 1", got)
	}
	if got := h.outboxCountOfType(view.ID, string(event.TypeWalletBalanceChanged)); got != 1 {
		t.Fatalf("WalletBalanceChanged events = %d, want 1", got)
	}
	events := h.outboxEventsForWallet(view.ID)
	if len(events) != 2 {
		t.Fatalf("outbox events = %d, want 2", len(events))
	}
	for _, row := range events {
		decoded := decodeOutbox(t, row)
		if decoded.Version != event.Version {
			t.Errorf("event version = %d, want %d", decoded.Version, event.Version)
		}
		if decoded.AggregateID != view.ID.String() {
			t.Errorf("aggregateId = %s, want %s", decoded.AggregateID, view.ID)
		}
		switch decoded.EventType {
		case string(event.TypeWagerTransactionProcessed):
			data := decodeProcessedData(t, decoded)
			if data.Origin != "INTERNAL" || data.Kind != "OPENING" {
				t.Errorf("processed data = %s/%s, want INTERNAL/OPENING", data.Origin, data.Kind)
			}
			if data.Money.MinorUnits() != 100000 || data.Balance.MinorUnits() != 100000 {
				t.Errorf("processed money/balance = %d/%d, want 100000/100000", data.Money.MinorUnits(), data.Balance.MinorUnits())
			}
			if data.ProviderID != "" || data.ExternalTransactionID != "" {
				t.Error("opening event carries provider metadata")
			}
		case string(event.TypeWalletBalanceChanged):
			data := decodeBalanceChangedData(t, decoded)
			if data.Direction != "CREDIT" || data.BalanceBefore.MinorUnits() != 0 || data.BalanceAfter.MinorUnits() != 100000 {
				t.Errorf("balance changed = %s %d -> %d, want CREDIT 0 -> 100000", data.Direction, data.BalanceBefore.MinorUnits(), data.BalanceAfter.MinorUnits())
			}
			if data.WalletVersion != 1 {
				t.Errorf("walletVersion = %d, want 1", data.WalletVersion)
			}
		}
	}
}

// C32 - Opening a wallet with 0.00 returns balance 0.00 and version 1.
func TestWalletOpenZeroBalance(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("0.00")
	if view.Balance.MinorUnits() != 0 || view.Balance.Currency() != "BRL" {
		t.Errorf("balance = %s %s, want 0.00 BRL", view.Balance, view.Balance.Currency())
	}
	if view.Version != 1 {
		t.Errorf("version = %d, want 1", view.Version)
	}
}

// C33 - Opening with 0.00 persists no OPENING transaction.
func TestWalletOpenZeroNoOpening(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("0.00")
	if got := h.transactionsForWallet(view.ID); got != 0 {
		t.Fatalf("wager_transactions = %d, want 0", got)
	}
}

// C34 - Opening with 0.00 persists no ledger entry.
func TestWalletOpenZeroNoLedger(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("0.00")
	if got := h.ledgerForWallet(view.ID); got != 0 {
		t.Fatalf("ledger entries = %d, want 0", got)
	}
}

// C35 - Opening with 0.00 persists no financial outbox event.
func TestWalletOpenZeroNoOutbox(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("0.00")
	if got := h.outboxForWallet(view.ID); got != 0 {
		t.Fatalf("outbox events = %d, want 0", got)
	}
}

// C36 - A duplicate (playerId, currency) is a 409 WALLET_ALREADY_EXISTS and
// adds no credit.
func TestWalletDuplicate(t *testing.T) {
	h := newHarness(t)
	player := financial.NewPlayerID()
	first := h.openWalletFor(player, "1000.00", "BRL")
	_, err := h.service.OpenWallet(h.ctx(), application.OpenWalletCommand{
		PlayerID:       player,
		InitialBalance: mustMoney(t, "500.00", "BRL"),
		CorrelationID:  newCorrelation(),
	})
	if err == nil {
		t.Fatal("duplicate wallet open succeeded, want conflict")
	}
	if !errors.Is(err, application.ErrConflict) {
		t.Fatalf("error %v does not classify as ErrConflict", err)
	}
	code, ok := application.ErrorCodeOf(err)
	if !ok || code != application.CodeWalletAlreadyExists {
		t.Fatalf("code = %s, want %s", code, application.CodeWalletAlreadyExists)
	}
	if got := h.walletBalance(first.ID).MinorUnits(); got != 100000 {
		t.Errorf("balance = %d, want 100000 (no credit added)", got)
	}
	if got := h.transactionsForWallet(first.ID); got != 1 {
		t.Errorf("wager_transactions = %d, want 1", got)
	}
	if got := h.ledgerForWallet(first.ID); got != 1 {
		t.Errorf("ledger entries = %d, want 1", got)
	}

	// C36 transport boundary: the duplicate open returns 409 WALLET_ALREADY_EXISTS.
	runtime := newHTTPRuntime(t)
	status, data := runtime.postWallet(openWalletJSON{
		PlayerID:       player.String(),
		InitialBalance: moneyJSON{Amount: "500.00", Currency: "BRL"},
	})
	requireStatus(t, status, 409, data)
	envelope := decodeError(t, data)
	if envelope.Error.Code != "WALLET_ALREADY_EXISTS" {
		t.Errorf("code = %s, want WALLET_ALREADY_EXISTS", envelope.Error.Code)
	}
	if got := h.walletBalance(first.ID).MinorUnits(); got != 100000 {
		t.Errorf("balance after HTTP duplicate = %d, want 100000", got)
	}
}

// C38 - A movement increments the wallet version by exactly one; operations
// without movement preserve it, and a rejection changes nothing.
func TestWalletVersionIncrement(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	if got := h.walletVersion(view.ID); got != 1 {
		t.Fatalf("version after opening = %d, want 1", got)
	}
	bet := h.command(view, financial.KindBet, "25.00")
	h.mustSubmit(bet)
	if got := h.walletVersion(view.ID); got != 2 {
		t.Fatalf("version after BET = %d, want 2", got)
	}
	loss := h.command(view, financial.KindLoss, "0.00")
	h.mustSubmit(loss)
	if got := h.walletVersion(view.ID); got != 2 {
		t.Fatalf("version after LOSS = %d, want 2", got)
	}
	rejected := h.command(view, financial.KindBet, "5000.00")
	result := h.mustSubmit(rejected)
	if result.State != financial.StateRejected {
		t.Fatalf("expected REJECTED, got %s", result.State)
	}
	if got := h.walletVersion(view.ID); got != 2 {
		t.Fatalf("version after rejection = %d, want 2", got)
	}
}

// C39 - A movement whose currency differs from the wallet currency is
// rejected before the balance changes.
func TestCrossCurrencyRejection(t *testing.T) {
	h := newHarness(t)
	usdWallet := h.openWalletFor(financial.NewPlayerID(), "100.00", "USD")
	brlWallet := h.openWalletFor(financial.NewPlayerID(), "100.00", "BRL")

	foreign := h.command(usdWallet, financial.KindBet, "25.00")
	foreign.Amount = mustMoney(t, "25.00", "BRL")
	h.mustFailWith(foreign, application.CodeUnsupportedCurrency)

	reverse := h.command(brlWallet, financial.KindBet, "25.00")
	reverse.Amount = mustMoney(t, "25.00", "USD")
	h.mustFailWith(reverse, application.CodeUnsupportedCurrency)

	if got := h.walletBalance(usdWallet.ID).MinorUnits(); got != 10000 {
		t.Errorf("USD balance = %d, want 10000", got)
	}
	if got := h.walletBalance(brlWallet.ID).MinorUnits(); got != 10000 {
		t.Errorf("BRL balance = %d, want 10000", got)
	}
	if h.transactionExists("provider-a", foreign.ExternalTransactionID) || h.transactionExists("provider-a", reverse.ExternalTransactionID) {
		t.Error("a cross-currency command persisted a transaction")
	}
}

// C40 - A debit that would go below zero ends REJECTED with
// INSUFFICIENT_FUNDS, creates no ledger entry and moves no balance.
func TestInsufficientFundsRejection(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	cmd := h.command(view, financial.KindBet, "150.00")
	result := h.mustSubmit(cmd)
	if result.State != financial.StateRejected {
		t.Fatalf("state = %s, want REJECTED", result.State)
	}
	if result.FailureCode != financial.FailureInsufficientFunds {
		t.Fatalf("failureCode = %s, want INSUFFICIENT_FUNDS", result.FailureCode)
	}
	if result.ObservedBalance.MinorUnits() != 10000 {
		t.Errorf("observedBalance = %d, want 10000", result.ObservedBalance.MinorUnits())
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 10000 {
		t.Errorf("balance = %d, want 10000", got)
	}
	if got := h.ledgerForWallet(view.ID); got != 1 {
		t.Errorf("ledger entries = %d, want 1 (only the opening)", got)
	}
	if got := h.walletVersion(view.ID); got != 1 {
		t.Errorf("version = %d, want 1", got)
	}
}

// C43 - the application database role is denied UPDATE and DELETE on the
// ledger independently of repository code.
func TestLedgerWriteDeniedForAppRole(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	_, err := h.appPool.Exec(h.ctx(),
		`UPDATE wallet_ledger_entries SET "amount" = 1 WHERE "walletId" = $1`, view.ID.String())
	assertInsufficientPrivilege(t, err)
	_, err = h.appPool.Exec(h.ctx(),
		`DELETE FROM wallet_ledger_entries WHERE "walletId" = $1`, view.ID.String())
	assertInsufficientPrivilege(t, err)
}

func assertConstraintViolation(t *testing.T, err error, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want a PostgreSQL constraint violation", err)
	}
	if pgErr.Code != "23505" {
		t.Fatalf("SQLSTATE = %s, want 23505", pgErr.Code)
	}
	if pgErr.ConstraintName != constraint {
		t.Fatalf("constraint = %s, want %s", pgErr.ConstraintName, constraint)
	}
}

func assertInsufficientPrivilege(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want a PostgreSQL authorization error", err)
	}
	if pgErr.Code != "42501" {
		t.Fatalf("SQLSTATE = %s, want 42501 (insufficient_privilege)", pgErr.Code)
	}
}
