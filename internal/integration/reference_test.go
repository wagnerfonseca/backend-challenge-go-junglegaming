//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// C84 - An absent reference retries with exponential backoff from 1s to the
// 15-minute cap and deterministic jitter up to 10%, durably scheduled within
// the 24-hour window.
func TestReferenceRetryBackoff(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	refund := h.command(view, financial.KindRefund, "25.00")
	refund.ReferenceExternalID = newExternalID("bet")
	result := h.mustSubmit(refund)
	if result.State != financial.StatePendingReference {
		t.Fatalf("state = %s, want PENDING_REFERENCE", result.State)
	}
	record := h.transactionByID(result.TransactionID)
	if delay := record.NextAttemptAt.Sub(record.CreatedAt); delay < time.Second || delay > time.Second+100*time.Millisecond {
		t.Fatalf("attempt 0 delay = %s, want within [1s, 1.1s]", delay)
	}

	expectedAttempts := []struct {
		attempt int
		base    time.Duration
		maxAdd  time.Duration
	}{
		{attempt: 1, base: 2 * time.Second, maxAdd: 200 * time.Millisecond},
		{attempt: 2, base: 4 * time.Second, maxAdd: 400 * time.Millisecond},
		{attempt: 3, base: 8 * time.Second, maxAdd: 800 * time.Millisecond},
	}
	for _, step := range expectedAttempts {
		h.clock.Advance(record.NextAttemptAt.Sub(h.clock.Now()))
		resolved, err := h.service.ResolvePendingReference(h.ctx(), application.ResolvePendingReferenceCommand{
			TransactionID: result.TransactionID,
			CorrelationID: newCorrelation(),
		})
		if err != nil {
			t.Fatalf("resolving pending reference: %v", err)
		}
		if resolved.State != financial.StatePendingReference {
			t.Fatalf("state = %s, want PENDING_REFERENCE", resolved.State)
		}
		record = h.transactionByID(result.TransactionID)
		if record.ReferenceAttempts != step.attempt {
			t.Fatalf("referenceAttempts = %d, want %d", record.ReferenceAttempts, step.attempt)
		}
		delay := record.NextAttemptAt.Sub(h.clock.Now())
		if delay < step.base || delay > step.base+step.maxAdd {
			t.Fatalf("attempt %d delay = %s, want within [%s, %s]", step.attempt, delay, step.base, step.base+step.maxAdd)
		}
		want := h.clock.Now().Add(application.ReferenceBackoff(step.attempt, result.TransactionID.String())).Truncate(time.Microsecond)
		if !record.NextAttemptAt.Equal(want) {
			t.Errorf("attempt %d nextAttemptAt = %s, want the deterministic %s", step.attempt, record.NextAttemptAt, want)
		}
	}
	if !record.ReferenceDeadline.After(*record.NextAttemptAt) {
		t.Errorf("deadline %s must stay after the retry schedule %s", record.ReferenceDeadline, record.NextAttemptAt)
	}
}

// C205 - A WIN referencing a processed BET with matching provider, player,
// wallet, currency and round persists the resolved reference and wins.
func TestWINReferenceResolved(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	bet := h.command(view, financial.KindBet, "100.00")
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
	if deref(record.ReferenceExternalID) != bet.ExternalTransactionID.String() {
		t.Errorf("referenceExternalTransactionId = %v, want %s", record.ReferenceExternalID, bet.ExternalTransactionID)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 95000 {
		t.Errorf("balance = %d, want 95000", got)
	}
}

// C206 - A WIN whose reference resolves to another kind or a mismatched
// identity ends REJECTED with INVALID_WIN_REFERENCE.
func TestWINReferenceInvalid(t *testing.T) {
	t.Run("reference of another kind", func(t *testing.T) {
		h := newHarness(t)
		view := h.openWallet("1000.00")
		win := h.command(view, financial.KindWin, "50.00")
		h.mustSubmit(win)
		second := h.command(view, financial.KindWin, "50.00")
		second.ReferenceExternalID = win.ExternalTransactionID
		second.RoundID = win.RoundID
		second.RoundID = win.RoundID
		result := h.mustSubmit(second)
		if result.State != financial.StateRejected || result.FailureCode != financial.FailureInvalidWinReference {
			t.Fatalf("result = %s/%s, want REJECTED/INVALID_WIN_REFERENCE", result.State, result.FailureCode)
		}
	})
	t.Run("mismatched round", func(t *testing.T) {
		h := newHarness(t)
		view := h.openWallet("1000.00")
		bet := h.command(view, financial.KindBet, "100.00")
		h.mustSubmit(bet)
		win := h.command(view, financial.KindWin, "50.00")
		win.RoundID = newExternalID("round")
		win.ReferenceExternalID = bet.ExternalTransactionID
		result := h.mustSubmit(win)
		if result.State != financial.StateRejected || result.FailureCode != financial.FailureInvalidWinReference {
			t.Fatalf("result = %s/%s, want REJECTED/INVALID_WIN_REFERENCE", result.State, result.FailureCode)
		}
	})
}

// C207 - A REFUND whose reference resolves to a kind other than BET ends
// REJECTED with REFERENCE_KIND_NOT_ALLOWED.
func TestREFUNDReferenceInvalid(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	win := h.command(view, financial.KindWin, "50.00")
	h.mustSubmit(win)
	refund := h.command(view, financial.KindRefund, "50.00")
	refund.ReferenceExternalID = win.ExternalTransactionID
	refund.RoundID = win.RoundID
	refund.RoundID = win.RoundID
	result := h.mustSubmit(refund)
	if result.State != financial.StateRejected || result.FailureCode != financial.FailureReferenceKindNotAllowed {
		t.Fatalf("result = %s/%s, want REJECTED/REFERENCE_KIND_NOT_ALLOWED", result.State, result.FailureCode)
	}
}

// C208 - A ROLLBACK whose reference resolves to a kind other than BET, WIN or
// REFUND ends REJECTED with REFERENCE_KIND_NOT_ALLOWED.
func TestROLLBACKReferenceInvalid(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	loss := h.command(view, financial.KindLoss, "0.00")
	h.mustSubmit(loss)
	rollback := h.command(view, financial.KindRollback, "1.00")
	rollback.ReferenceExternalID = loss.ExternalTransactionID
	rollback.RoundID = loss.RoundID
	rollback.RoundID = loss.RoundID
	result := h.mustSubmit(rollback)
	if result.State != financial.StateRejected || result.FailureCode != financial.FailureReferenceKindNotAllowed {
		t.Fatalf("result = %s/%s, want REJECTED/REFERENCE_KIND_NOT_ALLOWED", result.State, result.FailureCode)
	}
}

// C209 - A processed WIN receiving a second ROLLBACK after one successful
// rollback ends REJECTED with ALREADY_REVERSED.
func TestWINDoubleRollback(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	win := h.command(view, financial.KindWin, "50.00")
	h.mustSubmit(win)
	first := h.command(view, financial.KindRollback, "50.00")
	first.ReferenceExternalID = win.ExternalTransactionID
	first.RoundID = win.RoundID
	first.RoundID = win.RoundID
	h.mustSubmit(first)
	second := h.command(view, financial.KindRollback, "50.00")
	second.ReferenceExternalID = win.ExternalTransactionID
	second.RoundID = win.RoundID
	second.RoundID = win.RoundID
	result := h.mustSubmit(second)
	if result.State != financial.StateRejected || result.FailureCode != financial.FailureAlreadyReversed {
		t.Fatalf("result = %s/%s, want REJECTED/ALREADY_REVERSED", result.State, result.FailureCode)
	}
}

// C210 - A processed REFUND receiving a second ROLLBACK after one successful
// rollback ends REJECTED with ALREADY_REVERSED.
func TestREFUNDDoubleRollback(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	bet := h.command(view, financial.KindBet, "100.00")
	h.mustSubmit(bet)
	refund := h.command(view, financial.KindRefund, "100.00")
	refund.ReferenceExternalID = bet.ExternalTransactionID
	refund.RoundID = bet.RoundID
	h.mustSubmit(refund)
	first := h.command(view, financial.KindRollback, "100.00")
	first.ReferenceExternalID = refund.ExternalTransactionID
	first.RoundID = refund.RoundID
	h.mustSubmit(first)
	second := h.command(view, financial.KindRollback, "100.00")
	second.ReferenceExternalID = refund.ExternalTransactionID
	second.RoundID = refund.RoundID
	result := h.mustSubmit(second)
	if result.State != financial.StateRejected || result.FailureCode != financial.FailureAlreadyReversed {
		t.Fatalf("result = %s/%s, want REJECTED/ALREADY_REVERSED", result.State, result.FailureCode)
	}
}

// C211 - Any supplied reference that resolves to an existing transaction
// persists that internal identity in the same commit as its PROCESSED or
// REJECTED state.
func TestReferenceCommitAtomicity(t *testing.T) {
	t.Run("processed dependent persists the internal reference", func(t *testing.T) {
		h := newHarness(t)
		view := h.openWallet("1000.00")
		bet := h.command(view, financial.KindBet, "100.00")
		betResult := h.mustSubmit(bet)
		refund := h.command(view, financial.KindRefund, "100.00")
		refund.ReferenceExternalID = bet.ExternalTransactionID
		refund.RoundID = bet.RoundID
		result := h.mustSubmit(refund)
		record := h.transactionByID(result.TransactionID)
		if record.State != "PROCESSED" || deref(record.ReferenceTransactionID) != betResult.TransactionID.String() {
			t.Errorf("persisted = %s/%v, want PROCESSED/%s", record.State, record.ReferenceTransactionID, betResult.TransactionID)
		}
	})
	t.Run("rejected dependent persists the internal reference", func(t *testing.T) {
		h := newHarness(t)
		view := h.openWallet("1000.00")
		bet := h.command(view, financial.KindBet, "100.00")
		betResult := h.mustSubmit(bet)
		refund := h.command(view, financial.KindRefund, "100.00")
		refund.ReferenceExternalID = bet.ExternalTransactionID
		refund.RoundID = newExternalID("round")
		result := h.mustSubmit(refund)
		record := h.transactionByID(result.TransactionID)
		if record.State != "REJECTED" || deref(record.ReferenceTransactionID) != betResult.TransactionID.String() {
			t.Errorf("persisted = %s/%v, want REJECTED/%s", record.State, record.ReferenceTransactionID, betResult.TransactionID)
		}
		if deref(record.FailureCode) != "REFERENCE_MISMATCH" {
			t.Errorf("failureCode = %v, want REFERENCE_MISMATCH", record.FailureCode)
		}
	})
}
