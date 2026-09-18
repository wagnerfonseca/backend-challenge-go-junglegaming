//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/event"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// C64 - A BET with sufficient balance debits exactly its amount and ends in
// PROCESSED.
func TestBetSufficientBalance(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	result := h.mustSubmit(h.command(view, financial.KindBet, "25.00"))
	if result.State != financial.StateProcessed {
		t.Fatalf("state = %s, want PROCESSED", result.State)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 7500 {
		t.Errorf("balance = %d, want 7500", got)
	}
	entries := h.ledgerRows(view.ID)
	if len(entries) != 2 {
		t.Fatalf("ledger entries = %d, want 2", len(entries))
	}
	debit := entries[1]
	if debit.TransactionID != result.TransactionID.String() || debit.Direction != "DEBIT" || debit.Amount != 2500 {
		t.Errorf("debit = %s %s %d, want %s DEBIT 2500", debit.TransactionID, debit.Direction, debit.Amount, result.TransactionID)
	}
}

// C65 - A BET exceeding the available balance ends REJECTED with
// INSUFFICIENT_FUNDS and creates no ledger entry for it.
func TestBetInsufficientFunds(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	result := h.mustSubmit(h.command(view, financial.KindBet, "150.00"))
	if result.State != financial.StateRejected || result.FailureCode != financial.FailureInsufficientFunds {
		t.Fatalf("result = %s/%s, want REJECTED/INSUFFICIENT_FUNDS", result.State, result.FailureCode)
	}
	if got := h.countRows(`SELECT count(*) FROM wallet_ledger_entries WHERE "transactionId" = $1`, result.TransactionID.String()); got != 0 {
		t.Errorf("ledger entries for the rejected transaction = %d, want 0", got)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 10000 {
		t.Errorf("balance = %d, want 10000", got)
	}
}

// C66 - A valid WIN credits exactly its amount and ends in PROCESSED, with
// and without a reference.
func TestWinValid(t *testing.T) {
	t.Run("without reference", func(t *testing.T) {
		h := newHarness(t)
		view := h.openWallet("100.00")
		result := h.mustSubmit(h.command(view, financial.KindWin, "50.00"))
		if result.State != financial.StateProcessed {
			t.Fatalf("state = %s, want PROCESSED", result.State)
		}
		if got := h.walletBalance(view.ID).MinorUnits(); got != 15000 {
			t.Errorf("balance = %d, want 15000", got)
		}
	})
	t.Run("with a processed BET reference", func(t *testing.T) {
		h := newHarness(t)
		view := h.openWallet("100.00")
		bet := h.command(view, financial.KindBet, "25.00")
		betResult := h.mustSubmit(bet)
		win := h.command(view, financial.KindWin, "50.00")
		win.RoundID = bet.RoundID
		win.ReferenceExternalID = bet.ExternalTransactionID
		win.RoundID = bet.RoundID
		result := h.mustSubmit(win)
		if result.State != financial.StateProcessed {
			t.Fatalf("state = %s, want PROCESSED", result.State)
		}
		record := h.transactionByID(result.TransactionID)
		if deref(record.ReferenceTransactionID) != betResult.TransactionID.String() {
			t.Errorf("referenceTransactionId = %v, want %s", record.ReferenceTransactionID, betResult.TransactionID)
		}
		if got := h.walletBalance(view.ID).MinorUnits(); got != 12500 {
			t.Errorf("balance = %d, want 12500", got)
		}
	})
}

// C67 - A WIN whose reference is not yet present enters PENDING_REFERENCE.
func TestWinPendingReference(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	win := h.command(view, financial.KindWin, "50.00")
	win.ReferenceExternalID = newExternalID("bet")
	result := h.mustSubmit(win)
	if result.State != financial.StatePendingReference {
		t.Fatalf("state = %s, want PENDING_REFERENCE", result.State)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 10000 {
		t.Errorf("balance = %d, want 10000", got)
	}
}

// C68 - A LOSS whose money is not exactly 0.00 is a contract error
// INVALID_LOSS_AMOUNT and persists no transaction. The HTTP 422 status is
// deferred to the HTTP adapter (batch C).
func TestLossInvalidAmount(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	loss := h.command(view, financial.KindLoss, "1.00")
	h.mustFailWith(loss, application.CodeInvalidLossAmount)
	if h.transactionExists(loss.ProviderID.String(), loss.ExternalTransactionID) {
		t.Error("an invalid LOSS persisted a transaction")
	}
}

// C69 - A processed LOSS appends no ledger entry.
func TestLossNoLedger(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	result := h.mustSubmit(h.command(view, financial.KindLoss, "0.00"))
	if got := h.countRows(`SELECT count(*) FROM wallet_ledger_entries WHERE "transactionId" = $1`, result.TransactionID.String()); got != 0 {
		t.Errorf("ledger entries = %d, want 0", got)
	}
}

// C70 - A processed LOSS preserves wallet balance and version.
func TestLossPreservesBalance(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	h.mustSubmit(h.command(view, financial.KindLoss, "0.00"))
	if got := h.walletBalance(view.ID).MinorUnits(); got != 10000 {
		t.Errorf("balance = %d, want 10000", got)
	}
	if got := h.walletVersion(view.ID); got != 1 {
		t.Errorf("version = %d, want 1", got)
	}
}

// C71 - A processed LOSS emits WagerTransactionProcessed and not
// WalletBalanceChanged.
func TestLossEventMismatch(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	result := h.mustSubmit(h.command(view, financial.KindLoss, "0.00"))
	processed := 0
	balanceChanged := 0
	for _, row := range h.outboxEventsForWallet(view.ID) {
		decoded := decodeOutbox(t, row)
		switch decoded.EventType {
		case string(event.TypeWagerTransactionProcessed):
			if decodeProcessedData(t, decoded).TransactionID == result.TransactionID.String() {
				processed++
			}
		case string(event.TypeWalletBalanceChanged):
			if decodeBalanceChangedData(t, decoded).TransactionID == result.TransactionID.String() {
				balanceChanged++
			}
		}
	}
	if processed != 1 {
		t.Errorf("processed events = %d, want 1", processed)
	}
	if balanceChanged != 0 {
		t.Errorf("balance changed events = %d, want 0", balanceChanged)
	}
}

// C72 - A valid REFUND of a processed BET credits exactly the referenced bet
// amount.
func TestRefundValid(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	bet := h.command(view, financial.KindBet, "100.00")
	h.mustSubmit(bet)
	refund := h.command(view, financial.KindRefund, "100.00")
	refund.ReferenceExternalID = bet.ExternalTransactionID
	refund.RoundID = bet.RoundID
	result := h.mustSubmit(refund)
	if result.State != financial.StateProcessed {
		t.Fatalf("state = %s, want PROCESSED", result.State)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 100000 {
		t.Errorf("balance = %d, want 100000", got)
	}
	entries := h.ledgerRows(view.ID)
	last := entries[len(entries)-1]
	if last.TransactionID != result.TransactionID.String() || last.Direction != "CREDIT" || last.Amount != 10000 {
		t.Errorf("refund entry = %s %s %d, want %s CREDIT 10000", last.TransactionID, last.Direction, last.Amount, result.TransactionID)
	}
}

// C73 - A valid ROLLBACK applies exactly the opposite movement of its
// referenced BET, WIN or REFUND.
func TestRollbackValid(t *testing.T) {
	t.Run("rollback of a BET credits the bet amount", func(t *testing.T) {
		h := newHarness(t)
		view := h.openWallet("1000.00")
		bet := h.command(view, financial.KindBet, "100.00")
		h.mustSubmit(bet)
		rollback := h.command(view, financial.KindRollback, "100.00")
		rollback.ReferenceExternalID = bet.ExternalTransactionID
		rollback.RoundID = bet.RoundID
		result := h.mustSubmit(rollback)
		if result.State != financial.StateProcessed {
			t.Fatalf("state = %s, want PROCESSED", result.State)
		}
		if got := h.walletBalance(view.ID).MinorUnits(); got != 100000 {
			t.Errorf("balance = %d, want 100000", got)
		}
	})
	t.Run("rollback of a WIN debits the win amount", func(t *testing.T) {
		h := newHarness(t)
		view := h.openWallet("1000.00")
		win := h.command(view, financial.KindWin, "50.00")
		h.mustSubmit(win)
		rollback := h.command(view, financial.KindRollback, "50.00")
		rollback.ReferenceExternalID = win.ExternalTransactionID
		rollback.RoundID = win.RoundID
		rollback.RoundID = win.RoundID
		result := h.mustSubmit(rollback)
		if result.State != financial.StateProcessed {
			t.Fatalf("state = %s, want PROCESSED", result.State)
		}
		if got := h.walletBalance(view.ID).MinorUnits(); got != 100000 {
			t.Errorf("balance = %d, want 100000", got)
		}
	})
	t.Run("rollback of a REFUND debits the refund amount", func(t *testing.T) {
		h := newHarness(t)
		view := h.openWallet("1000.00")
		bet := h.command(view, financial.KindBet, "100.00")
		h.mustSubmit(bet)
		refund := h.command(view, financial.KindRefund, "100.00")
		refund.ReferenceExternalID = bet.ExternalTransactionID
		refund.RoundID = bet.RoundID
		h.mustSubmit(refund)
		rollback := h.command(view, financial.KindRollback, "100.00")
		rollback.ReferenceExternalID = refund.ExternalTransactionID
		rollback.RoundID = refund.RoundID
		result := h.mustSubmit(rollback)
		if result.State != financial.StateProcessed {
			t.Fatalf("state = %s, want PROCESSED", result.State)
		}
		if got := h.walletBalance(view.ID).MinorUnits(); got != 90000 {
			t.Errorf("balance = %d, want 90000", got)
		}
	})
}

// C74 - BET, WIN, REFUND or ROLLBACK with amount 0.00 is a contract error.
// The HTTP 422 status is deferred to the HTTP adapter (batch C).
func TestZeroAmountRejection(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	for _, kind := range []financial.Kind{financial.KindBet, financial.KindWin, financial.KindRefund, financial.KindRollback} {
		cmd := h.command(view, kind, "0.00")
		if kind == financial.KindRefund || kind == financial.KindRollback {
			cmd.ReferenceExternalID = newExternalID("bet")
		}
		h.mustFailWith(cmd, application.CodeInvalidRequest)
		if h.transactionExists(cmd.ProviderID.String(), cmd.ExternalTransactionID) {
			t.Errorf("%s with zero amount persisted a transaction", kind)
		}
	}
}

// C75 - REFUND or ROLLBACK without referenceExternalTransactionId is a
// contract error REFERENCE_REQUIRED. The HTTP 422 status is deferred to the
// HTTP adapter (batch C).
func TestReversalMissingReference(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	for _, kind := range []financial.Kind{financial.KindRefund, financial.KindRollback} {
		cmd := h.command(view, kind, "25.00")
		h.mustFailWith(cmd, application.CodeReferenceRequired)
	}
	if got := h.transactionsForWallet(view.ID); got != 1 {
		t.Errorf("transactions = %d, want 1 (only the opening)", got)
	}
}

// C76 - An external reference is resolved only by
// (providerId, referenceExternalTransactionId).
func TestReferenceResolutionByIdentity(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	bet := h.command(view, financial.KindBet, "100.00")
	bet.ProviderID = "provider-a"
	h.mustSubmit(bet)

	foreign := h.command(view, financial.KindRefund, "100.00")
	foreign.ProviderID = "provider-b"
	foreign.ReferenceExternalID = bet.ExternalTransactionID
	foreign.RoundID = bet.RoundID
	pending := h.mustSubmit(foreign)
	if pending.State != financial.StatePendingReference {
		t.Fatalf("provider-b refund state = %s, want PENDING_REFERENCE", pending.State)
	}

	same := h.command(view, financial.KindRefund, "100.00")
	same.ProviderID = "provider-a"
	same.ReferenceExternalID = bet.ExternalTransactionID
	same.RoundID = bet.RoundID
	resolved := h.mustSubmit(same)
	if resolved.State != financial.StateProcessed {
		t.Fatalf("provider-a refund state = %s, want PROCESSED", resolved.State)
	}
}

// C77 - A reversal whose resolved reference differs in player, wallet,
// currency or round ends REJECTED with REFERENCE_MISMATCH.
func TestReversalReferenceMismatch(t *testing.T) {
	t.Run("round mismatch", func(t *testing.T) {
		h := newHarness(t)
		view := h.openWallet("1000.00")
		bet := h.command(view, financial.KindBet, "100.00")
		h.mustSubmit(bet)
		refund := h.command(view, financial.KindRefund, "100.00")
		refund.ReferenceExternalID = bet.ExternalTransactionID
		refund.RoundID = newExternalID("round")
		result := h.mustSubmit(refund)
		if result.State != financial.StateRejected || result.FailureCode != financial.FailureReferenceMismatch {
			t.Fatalf("result = %s/%s, want REJECTED/REFERENCE_MISMATCH", result.State, result.FailureCode)
		}
	})
	t.Run("wallet mismatch", func(t *testing.T) {
		h := newHarness(t)
		first := h.openWallet("1000.00")
		second := h.openWallet("1000.00")
		bet := h.command(first, financial.KindBet, "100.00")
		h.mustSubmit(bet)
		refund := h.command(second, financial.KindRefund, "100.00")
		refund.ReferenceExternalID = bet.ExternalTransactionID
		refund.RoundID = bet.RoundID
		result := h.mustSubmit(refund)
		if result.State != financial.StateRejected || result.FailureCode != financial.FailureReferenceMismatch {
			t.Fatalf("result = %s/%s, want REJECTED/REFERENCE_MISMATCH", result.State, result.FailureCode)
		}
	})
	t.Run("currency mismatch", func(t *testing.T) {
		h := newHarness(t)
		usd := h.openWalletFor(financial.NewPlayerID(), "1000.00", "USD")
		bet := h.command(usd, financial.KindBet, "100.00")
		h.mustSubmit(bet)
		brl := h.openWallet("1000.00")
		refund := h.command(brl, financial.KindRefund, "100.00")
		refund.ReferenceExternalID = bet.ExternalTransactionID
		refund.RoundID = bet.RoundID
		result := h.mustSubmit(refund)
		if result.State != financial.StateRejected || result.FailureCode != financial.FailureReferenceMismatch {
			t.Fatalf("result = %s/%s, want REJECTED/REFERENCE_MISMATCH", result.State, result.FailureCode)
		}
	})
}

// C78 - A reversal whose amount differs from its reference amount ends
// REJECTED with REVERSAL_AMOUNT_MISMATCH.
func TestReversalAmountMismatch(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	bet := h.command(view, financial.KindBet, "100.00")
	h.mustSubmit(bet)
	refund := h.command(view, financial.KindRefund, "99.00")
	refund.ReferenceExternalID = bet.ExternalTransactionID
	refund.RoundID = bet.RoundID
	result := h.mustSubmit(refund)
	if result.State != financial.StateRejected || result.FailureCode != financial.FailureReversalAmountMismatch {
		t.Fatalf("result = %s/%s, want REJECTED/REVERSAL_AMOUNT_MISMATCH", result.State, result.FailureCode)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 90000 {
		t.Errorf("balance = %d, want 90000", got)
	}
}

// C79 - The first successful REFUND or ROLLBACK consumes the bet's direct
// compensation right permanently.
func TestCompensationRightConsumed(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	bet := h.command(view, financial.KindBet, "100.00")
	betResult := h.mustSubmit(bet)
	refund := h.command(view, financial.KindRefund, "100.00")
	refund.ReferenceExternalID = bet.ExternalTransactionID
	refund.RoundID = bet.RoundID
	refundResult := h.mustSubmit(refund)
	if refundResult.State != financial.StateProcessed {
		t.Fatalf("refund state = %s, want PROCESSED", refundResult.State)
	}
	if got := h.claimRows(betResult.TransactionID); got != 1 {
		t.Fatalf("claims for the bet = %d, want 1", got)
	}
	var reversalID string
	if err := adminPool.QueryRow(h.ctx(),
		`SELECT "reversalTransactionId" FROM reversal_claims WHERE "referenceTransactionId" = $1`,
		betResult.TransactionID.String()).Scan(&reversalID); err != nil {
		t.Fatalf("reading claim: %v", err)
	}
	if reversalID != refundResult.TransactionID.String() {
		t.Errorf("claim reversal = %s, want %s", reversalID, refundResult.TransactionID)
	}
}

// C80 - A second direct compensation of a consumed BET ends REJECTED with
// ALREADY_REVERSED.
func TestAlreadyReversed(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	bet := h.command(view, financial.KindBet, "100.00")
	h.mustSubmit(bet)
	refund := h.command(view, financial.KindRefund, "100.00")
	refund.ReferenceExternalID = bet.ExternalTransactionID
	refund.RoundID = bet.RoundID
	h.mustSubmit(refund)
	rollback := h.command(view, financial.KindRollback, "100.00")
	rollback.ReferenceExternalID = bet.ExternalTransactionID
	rollback.RoundID = bet.RoundID
	result := h.mustSubmit(rollback)
	if result.State != financial.StateRejected || result.FailureCode != financial.FailureAlreadyReversed {
		t.Fatalf("result = %s/%s, want REJECTED/ALREADY_REVERSED", result.State, result.FailureCode)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 100000 {
		t.Errorf("balance = %d, want 100000", got)
	}
}

// C81 - A ROLLBACK of a processed REFUND debits the refund amount without
// reopening the referenced BET.
func TestRollbackRefund(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	bet := h.command(view, financial.KindBet, "100.00")
	betResult := h.mustSubmit(bet)
	refund := h.command(view, financial.KindRefund, "100.00")
	refund.ReferenceExternalID = bet.ExternalTransactionID
	refund.RoundID = bet.RoundID
	refundResult := h.mustSubmit(refund)
	rollback := h.command(view, financial.KindRollback, "100.00")
	rollback.ReferenceExternalID = refund.ExternalTransactionID
	rollback.RoundID = refund.RoundID
	rollbackResult := h.mustSubmit(rollback)
	if rollbackResult.State != financial.StateProcessed {
		t.Fatalf("rollback state = %s, want PROCESSED", rollbackResult.State)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 90000 {
		t.Errorf("balance = %d, want 90000", got)
	}
	if got := h.claimRows(betResult.TransactionID); got != 1 {
		t.Errorf("bet claims = %d, want 1 (rollback of refund must not delete it)", got)
	}
	if got := h.claimRows(refundResult.TransactionID); got != 1 {
		t.Errorf("refund claims = %d, want 1", got)
	}
	second := h.command(view, financial.KindRefund, "100.00")
	second.ReferenceExternalID = bet.ExternalTransactionID
	second.RoundID = bet.RoundID
	result := h.mustSubmit(second)
	if result.State != financial.StateRejected || result.FailureCode != financial.FailureAlreadyReversed {
		t.Errorf("refund after rollback of refund = %s/%s, want REJECTED/ALREADY_REVERSED", result.State, result.FailureCode)
	}
}

// C82 - A reversal debit exceeding the available balance ends REJECTED with
// REVERSAL_INSUFFICIENT_FUNDS.
func TestReversalInsufficientFunds(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	win := h.command(view, financial.KindWin, "100.00")
	h.mustSubmit(win)
	bet := h.command(view, financial.KindBet, "150.00")
	h.mustSubmit(bet)
	if got := h.walletBalance(view.ID).MinorUnits(); got != 5000 {
		t.Fatalf("balance = %d, want 5000", got)
	}
	rollback := h.command(view, financial.KindRollback, "100.00")
	rollback.ReferenceExternalID = win.ExternalTransactionID
	rollback.RoundID = win.RoundID
	rollback.RoundID = win.RoundID
	result := h.mustSubmit(rollback)
	if result.State != financial.StateRejected || result.FailureCode != financial.FailureReversalInsufficientFunds {
		t.Fatalf("result = %s/%s, want REJECTED/REVERSAL_INSUFFICIENT_FUNDS", result.State, result.FailureCode)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 5000 {
		t.Errorf("balance = %d, want 5000", got)
	}
	if got := h.countRows(`SELECT count(*) FROM reversal_claims WHERE "reversalTransactionId" = $1`, result.TransactionID.String()); got != 0 {
		t.Errorf("claims = %d, want 0 for a rejected reversal", got)
	}
}

// C83 - An absent required reference persists PENDING_REFERENCE and exactly
// one WagerTransactionPendingReference event, also after a retry reschedule.
func TestPendingReferenceEvent(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	refund := h.command(view, financial.KindRefund, "25.00")
	refund.ReferenceExternalID = newExternalID("bet")
	result := h.mustSubmit(refund)
	if result.State != financial.StatePendingReference {
		t.Fatalf("state = %s, want PENDING_REFERENCE", result.State)
	}
	countPending := func() int {
		count := 0
		for _, row := range h.outboxEventsForWallet(view.ID) {
			decoded := decodeOutbox(t, row)
			if decoded.EventType == string(event.TypeWagerTransactionPendingReference) {
				data := decodePendingReferenceData(t, decoded)
				if data.TransactionID == result.TransactionID.String() {
					count++
				}
			}
		}
		return count
	}
	if got := countPending(); got != 1 {
		t.Fatalf("pending reference events = %d, want 1", got)
	}
	record := h.transactionByID(result.TransactionID)
	h.clock.Advance(record.NextAttemptAt.Sub(record.CreatedAt))
	if _, err := h.service.ResolvePendingReference(h.ctx(), application.ResolvePendingReferenceCommand{
		TransactionID: result.TransactionID,
		CorrelationID: newCorrelation(),
	}); err != nil {
		t.Fatalf("rescheduling pending reference: %v", err)
	}
	if got := countPending(); got != 1 {
		t.Errorf("pending reference events after reschedule = %d, want 1", got)
	}
}

// C85 - A reference still in PENDING or PENDING_REFERENCE keeps the dependent
// transaction in PENDING_REFERENCE.
func TestPendingReferenceState(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	win := h.command(view, financial.KindWin, "50.00")
	win.ReferenceExternalID = newExternalID("bet")
	winResult := h.mustSubmit(win)
	if winResult.State != financial.StatePendingReference {
		t.Fatalf("win state = %s, want PENDING_REFERENCE", winResult.State)
	}
	rollback := h.command(view, financial.KindRollback, "50.00")
	rollback.ReferenceExternalID = win.ExternalTransactionID
	rollback.RoundID = win.RoundID
	rollback.RoundID = win.RoundID
	result := h.mustSubmit(rollback)
	if result.State != financial.StatePendingReference {
		t.Fatalf("dependent rollback state = %s, want PENDING_REFERENCE", result.State)
	}
	record := h.transactionByID(result.TransactionID)
	if record.State != "PENDING_REFERENCE" || record.ReferenceDeadline == nil {
		t.Errorf("persisted dependent = %s deadline=%v, want PENDING_REFERENCE with deadline", record.State, record.ReferenceDeadline)
	}

	h.clock.Advance(record.NextAttemptAt.Sub(h.clock.Now()))
	advanced, err := h.service.ResolvePendingReference(h.ctx(), application.ResolvePendingReferenceCommand{
		TransactionID: result.TransactionID,
		CorrelationID: newCorrelation(),
	})
	if err != nil {
		t.Fatalf("advancing a dependent whose reference is still pending: %v", err)
	}
	if advanced.State != financial.StatePendingReference {
		t.Fatalf("state = %s, want PENDING_REFERENCE", advanced.State)
	}
	record = h.transactionByID(result.TransactionID)
	if record.ReferenceAttempts != 1 {
		t.Errorf("referenceAttempts = %d, want 1 after one retry", record.ReferenceAttempts)
	}

	invalid := h.command(view, financial.KindRefund, "50.00")
	invalid.ReferenceExternalID = win.ExternalTransactionID
	invalid.RoundID = win.RoundID
	rejected := h.mustSubmit(invalid)
	if rejected.State != financial.StateRejected || rejected.FailureCode != financial.FailureReferenceKindNotAllowed {
		t.Errorf("refund of a pending WIN = %s/%s, want REJECTED/REFERENCE_KIND_NOT_ALLOWED", rejected.State, rejected.FailureCode)
	}
}

// C86 - A reference that ends REJECTED or FAILED ends the dependent
// transaction REJECTED with REFERENCE_NOT_PROCESSED.
func TestReferenceFailedDependent(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	refund := h.command(view, financial.KindRefund, "5000.00")
	referenceExternal := newExternalID("bet")
	refund.ReferenceExternalID = referenceExternal
	pending := h.mustSubmit(refund)
	if pending.State != financial.StatePendingReference {
		t.Fatalf("refund state = %s, want PENDING_REFERENCE", pending.State)
	}
	bet := h.command(view, financial.KindBet, "5000.00")
	bet.ExternalTransactionID = referenceExternal
	bet.RoundID = refund.RoundID
	rejected := h.mustSubmit(bet)
	if rejected.State != financial.StateRejected {
		t.Fatalf("reference state = %s, want REJECTED", rejected.State)
	}
	resolved, err := h.service.ResolvePendingReference(h.ctx(), application.ResolvePendingReferenceCommand{
		TransactionID: pending.TransactionID,
		CorrelationID: newCorrelation(),
	})
	if err != nil {
		t.Fatalf("resolving dependent: %v", err)
	}
	if resolved.State != financial.StateRejected || resolved.FailureCode != financial.FailureReferenceNotProcessed {
		t.Fatalf("dependent = %s/%s, want REJECTED/REFERENCE_NOT_PROCESSED", resolved.State, resolved.FailureCode)
	}
}

// C87 - An unresolved reference at its 24-hour deadline ends REJECTED with
// REFERENCE_NOT_FOUND and exactly one WagerTransactionRejected event.
func TestReferenceExpiry(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	refund := h.command(view, financial.KindRefund, "25.00")
	refund.ReferenceExternalID = newExternalID("bet")
	pending := h.mustSubmit(refund)
	if pending.State != financial.StatePendingReference {
		t.Fatalf("state = %s, want PENDING_REFERENCE", pending.State)
	}
	h.clock.Advance(24 * time.Hour)
	resolved, err := h.service.ResolvePendingReference(h.ctx(), application.ResolvePendingReferenceCommand{
		TransactionID: pending.TransactionID,
		CorrelationID: newCorrelation(),
	})
	if err != nil {
		t.Fatalf("resolving expired reference: %v", err)
	}
	if resolved.State != financial.StateRejected || resolved.FailureCode != financial.FailureReferenceNotFound {
		t.Fatalf("resolved = %s/%s, want REJECTED/REFERENCE_NOT_FOUND", resolved.State, resolved.FailureCode)
	}
	rejected := 0
	for _, row := range h.outboxEventsForWallet(view.ID) {
		decoded := decodeOutbox(t, row)
		if decoded.EventType == string(event.TypeWagerTransactionRejected) {
			if decodeRejectedData(t, decoded).TransactionID == pending.TransactionID.String() {
				rejected++
			}
		}
	}
	if rejected != 1 {
		t.Errorf("rejected events = %d, want 1", rejected)
	}
}

// C87 - An unresolved reference that still exists in a waiting state at the
// dependent's deadline also ends REJECTED with REFERENCE_NOT_FOUND.
func TestReferenceExpiryWhileWaiting(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	win := h.command(view, financial.KindWin, "50.00")
	win.ReferenceExternalID = newExternalID("bet")
	winResult := h.mustSubmit(win)
	if winResult.State != financial.StatePendingReference {
		t.Fatalf("win state = %s, want PENDING_REFERENCE", winResult.State)
	}
	rollback := h.command(view, financial.KindRollback, "50.00")
	rollback.ReferenceExternalID = win.ExternalTransactionID
	rollback.RoundID = win.RoundID
	pending := h.mustSubmit(rollback)
	if pending.State != financial.StatePendingReference {
		t.Fatalf("dependent state = %s, want PENDING_REFERENCE", pending.State)
	}
	h.clock.Advance(24 * time.Hour)
	resolved, err := h.service.ResolvePendingReference(h.ctx(), application.ResolvePendingReferenceCommand{
		TransactionID: pending.TransactionID,
		CorrelationID: newCorrelation(),
	})
	if err != nil {
		t.Fatalf("resolving expired dependent: %v", err)
	}
	if resolved.State != financial.StateRejected || resolved.FailureCode != financial.FailureReferenceNotFound {
		t.Fatalf("resolved = %s/%s, want REJECTED/REFERENCE_NOT_FOUND", resolved.State, resolved.FailureCode)
	}
}

// C88 - A reference arriving after the dependent transaction is terminal
// leaves the terminal result unchanged.
func TestLateReferenceIgnored(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	refund := h.command(view, financial.KindRefund, "100.00")
	referenceExternal := newExternalID("bet")
	refund.ReferenceExternalID = referenceExternal
	pending := h.mustSubmit(refund)
	h.clock.Advance(24 * time.Hour)
	expired, err := h.service.ResolvePendingReference(h.ctx(), application.ResolvePendingReferenceCommand{
		TransactionID: pending.TransactionID,
		CorrelationID: newCorrelation(),
	})
	if err != nil {
		t.Fatalf("expiring dependent: %v", err)
	}
	if expired.State != financial.StateRejected {
		t.Fatalf("state = %s, want REJECTED", expired.State)
	}
	bet := h.command(view, financial.KindBet, "100.00")
	bet.ExternalTransactionID = referenceExternal
	h.mustSubmit(bet)
	ledger := h.ledgerForWallet(view.ID)

	again, err := h.service.ResolvePendingReference(h.ctx(), application.ResolvePendingReferenceCommand{
		TransactionID: pending.TransactionID,
		CorrelationID: newCorrelation(),
	})
	if err != nil {
		t.Fatalf("resolving a terminal transaction: %v", err)
	}
	if again.State != financial.StateRejected || again.FailureCode != financial.FailureReferenceNotFound {
		t.Errorf("terminal result changed: %s/%s", again.State, again.FailureCode)
	}
	if got := h.ledgerForWallet(view.ID); got != ledger {
		t.Errorf("ledger entries = %d, want %d", got, ledger)
	}
}
