package financial_test

import (
	"errors"
	"testing"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// C57 - The state machine allows only PENDING -> PENDING_REFERENCE |
// PROCESSED | REJECTED | FAILED and PENDING_REFERENCE -> PROCESSED | REJECTED
// | FAILED.
func TestStateMachineTransitions(t *testing.T) {
	observe := mustMoney(t, "975.00")

	tests := []struct {
		name    string
		from    financial.State
		prepare func(t *testing.T, transaction financial.WagerTransaction) financial.WagerTransaction
		apply   func(transaction financial.WagerTransaction) (financial.WagerTransaction, error)
		wantErr bool
	}{
		{
			name: "PENDING to PENDING_REFERENCE",
			from: financial.StatePending,
			apply: func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
				return transaction.MarkPendingReference(testNow.Add(24*time.Hour), testNow.Add(time.Minute))
			},
		},
		{
			name: "PENDING to PROCESSED",
			from: financial.StatePending,
			apply: func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
				return transaction.MarkProcessed(financial.TransactionID{}, observe, testNow.Add(time.Minute))
			},
		},
		{
			name: "PENDING to REJECTED",
			from: financial.StatePending,
			apply: func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
				return transaction.MarkRejected(financial.TransactionID{}, financial.FailureInsufficientFunds, observe, testNow.Add(time.Minute))
			},
		},
		{
			name: "PENDING to FAILED",
			from: financial.StatePending,
			apply: func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
				return transaction.MarkFailed(observe, testNow.Add(time.Minute))
			},
		},
		{
			name: "PENDING_REFERENCE to PROCESSED",
			from: financial.StatePendingReference,
			prepare: func(t *testing.T, transaction financial.WagerTransaction) financial.WagerTransaction {
				t.Helper()
				updated, err := transaction.MarkPendingReference(testNow.Add(24*time.Hour), testNow.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				return updated
			},
			apply: func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
				return transaction.MarkProcessed(financial.TransactionID{}, observe, testNow.Add(2*time.Minute))
			},
		},
		{
			name: "PENDING_REFERENCE to REJECTED",
			from: financial.StatePendingReference,
			prepare: func(t *testing.T, transaction financial.WagerTransaction) financial.WagerTransaction {
				t.Helper()
				updated, err := transaction.MarkPendingReference(testNow.Add(24*time.Hour), testNow.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				return updated
			},
			apply: func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
				return transaction.MarkRejected(financial.TransactionID{}, financial.FailureReferenceNotFound, observe, testNow.Add(2*time.Minute))
			},
		},
		{
			name: "PENDING_REFERENCE to FAILED",
			from: financial.StatePendingReference,
			prepare: func(t *testing.T, transaction financial.WagerTransaction) financial.WagerTransaction {
				t.Helper()
				updated, err := transaction.MarkPendingReference(testNow.Add(24*time.Hour), testNow.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				return updated
			},
			apply: func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
				return transaction.MarkFailed(observe, testNow.Add(2*time.Minute))
			},
		},
		{
			name: "PENDING_REFERENCE to PENDING_REFERENCE",
			from: financial.StatePendingReference,
			prepare: func(t *testing.T, transaction financial.WagerTransaction) financial.WagerTransaction {
				t.Helper()
				updated, err := transaction.MarkPendingReference(testNow.Add(24*time.Hour), testNow.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				return updated
			},
			apply: func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
				return transaction.MarkPendingReference(testNow.Add(48*time.Hour), testNow.Add(2*time.Minute))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transaction := mustTransaction(t)
			if tt.prepare != nil {
				transaction = tt.prepare(t, transaction)
			}
			if transaction.State() != tt.from {
				t.Fatalf("prepared state = %s, want %s", transaction.State(), tt.from)
			}
			updated, err := tt.apply(transaction)
			if tt.wantErr {
				if !errors.Is(err, financial.ErrInvalidTransition) {
					t.Fatalf("transition error = %v, want ErrInvalidTransition", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("transition: %v", err)
			}
			if updated.IsTerminal() == false && updated.State() != financial.StatePendingReference {
				t.Fatalf("state after transition = %s", updated.State())
			}
		})
	}
}

// C23 - A terminal transaction rejects another transition, preserving its
// terminal state.
func TestTerminalStateTransition(t *testing.T) {
	observe := mustMoney(t, "975.00")

	terminals := []struct {
		name      string
		terminal  financial.State
		thenApply func(transaction financial.WagerTransaction) (financial.WagerTransaction, error)
	}{
		{
			name:     "PROCESSED then rejected",
			terminal: financial.StateProcessed,
			thenApply: func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
				return transaction.MarkRejected(financial.TransactionID{}, financial.FailureInsufficientFunds, observe, testNow.Add(2*time.Minute))
			},
		},
		{
			name:     "REJECTED then processed",
			terminal: financial.StateRejected,
			thenApply: func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
				return transaction.MarkProcessed(financial.TransactionID{}, observe, testNow.Add(2*time.Minute))
			},
		},
		{
			name:     "FAILED then processed",
			terminal: financial.StateFailed,
			thenApply: func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
				return transaction.MarkProcessed(financial.TransactionID{}, observe, testNow.Add(2*time.Minute))
			},
		},
	}

	for _, tt := range terminals {
		t.Run(tt.name, func(t *testing.T) {
			transaction := mustTransaction(t)
			var err error
			switch tt.terminal {
			case financial.StateProcessed:
				transaction, err = transaction.MarkProcessed(financial.TransactionID{}, observe, testNow.Add(time.Minute))
			case financial.StateRejected:
				transaction, err = transaction.MarkRejected(financial.TransactionID{}, financial.FailureInsufficientFunds, observe, testNow.Add(time.Minute))
			case financial.StateFailed:
				transaction, err = transaction.MarkFailed(observe, testNow.Add(time.Minute))
			}
			if err != nil {
				t.Fatalf("setup terminal state: %v", err)
			}
			_, err = tt.thenApply(transaction)
			if !errors.Is(err, financial.ErrInvalidTransition) {
				t.Fatalf("terminal transition error = %v, want ErrInvalidTransition", err)
			}
			if transaction.State() != tt.terminal {
				t.Fatalf("terminal state was not preserved: got %s, want %s", transaction.State(), tt.terminal)
			}
		})
	}
}

// C58 - Once a WagerTransaction reaches PROCESSED, REJECTED or FAILED no later
// state change is permitted.
func TestTerminalStateImmutable(t *testing.T) {
	observe := mustMoney(t, "975.00")
	deadline := testNow.Add(24 * time.Hour)

	transitions := map[string]func(financial.WagerTransaction) (financial.WagerTransaction, error){
		"pending reference": func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
			return transaction.MarkPendingReference(deadline, testNow.Add(2*time.Minute))
		},
		"processed": func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
			return transaction.MarkProcessed(financial.TransactionID{}, observe, testNow.Add(2*time.Minute))
		},
		"rejected": func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
			return transaction.MarkRejected(financial.TransactionID{}, financial.FailureInsufficientFunds, observe, testNow.Add(2*time.Minute))
		},
		"failed": func(transaction financial.WagerTransaction) (financial.WagerTransaction, error) {
			return transaction.MarkFailed(observe, testNow.Add(2*time.Minute))
		},
	}

	for _, terminal := range []financial.State{financial.StateProcessed, financial.StateRejected, financial.StateFailed} {
		transaction := mustTransaction(t)
		var err error
		switch terminal {
		case financial.StateProcessed:
			transaction, err = transaction.MarkProcessed(financial.TransactionID{}, observe, testNow.Add(time.Minute))
		case financial.StateRejected:
			transaction, err = transaction.MarkRejected(financial.TransactionID{}, financial.FailureInsufficientFunds, observe, testNow.Add(time.Minute))
		case financial.StateFailed:
			transaction, err = transaction.MarkFailed(observe, testNow.Add(time.Minute))
		}
		if err != nil {
			t.Fatalf("setup %s: %v", terminal, err)
		}

		for name, apply := range transitions {
			t.Run(string(terminal)+" blocks "+name, func(t *testing.T) {
				stateBefore := transaction.State()
				codeBefore := transaction.FailureCode()
				balanceBefore := transaction.ObservedBalance().String()
				_, err := apply(transaction)
				if !errors.Is(err, financial.ErrInvalidTransition) {
					t.Fatalf("error = %v, want ErrInvalidTransition", err)
				}
				if transaction.State() != stateBefore || transaction.FailureCode() != codeBefore || transaction.ObservedBalance().String() != balanceBefore {
					t.Fatalf("terminal snapshot changed: state=%s code=%s balance=%s", transaction.State(), transaction.FailureCode(), transaction.ObservedBalance())
				}
			})
		}
	}
}

func TestPendingReferenceDeadline(t *testing.T) {
	transaction := mustTransaction(t)
	deadline := testNow.Add(24 * time.Hour)
	updated, err := transaction.MarkPendingReference(deadline, testNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("MarkPendingReference: %v", err)
	}
	if !updated.ReferenceDeadline().Equal(deadline) {
		t.Fatalf("deadline = %s, want %s", updated.ReferenceDeadline(), deadline)
	}
}

func TestMarkFailedSetsPermanentCode(t *testing.T) {
	transaction := mustTransaction(t)
	updated, err := transaction.MarkFailed(mustMoney(t, "975.00"), testNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if updated.FailureCode() != financial.FailurePermanentInfrastructure {
		t.Fatalf("failure code = %s, want PERMANENT_INFRASTRUCTURE_FAILURE", updated.FailureCode())
	}
}

func TestMarkRejectedRejectsPermanentCode(t *testing.T) {
	transaction := mustTransaction(t)
	_, err := transaction.MarkRejected(financial.TransactionID{}, financial.FailurePermanentInfrastructure, mustMoney(t, "975.00"), testNow.Add(time.Minute))
	if !errors.Is(err, financial.ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
}
