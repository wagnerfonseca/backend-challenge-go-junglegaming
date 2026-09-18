package application

import (
	"context"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

// Option customizes the wager service without breaking constructor callers.
type Option func(*WagerService)

// WithReconciler enables the reconciliation use case.
func WithReconciler(reconciler Reconciler) Option {
	return func(s *WagerService) { s.reconciler = reconciler }
}

// TxFailpoint is an integration-only hook evaluated immediately before a
// financial transaction commits. Returning an error aborts the transaction
// and leaves no partial state.
type TxFailpoint func(stage string) error

// WithTxFailpoint installs the transaction-boundary failpoint. Production
// composition never installs one; the configuration rejects failpoint
// activation outside integration binaries.
func WithTxFailpoint(failpoint TxFailpoint) Option {
	return func(s *WagerService) { s.txFailpoint = failpoint }
}

func (s *WagerService) hitFailpoint(stage string) error {
	if s.txFailpoint == nil {
		return nil
	}
	return s.txFailpoint(stage)
}

// ReconciliationReport is the auditable difference between the stored wallet
// balance and the one derived from the complete ledger.
type ReconciliationReport struct {
	WalletID          financial.WalletID
	StoredBalance     money.Money
	CalculatedBalance money.Money
	Difference        money.Money
	Consistent        bool
	CheckedEntries    int
}

// ReconcileWallet reads the wallet and its complete ledger in one repeatable
// snapshot, performs no write, and reports stored minus calculated. The
// difference equals stored balance minus credits plus debits.
func (s *WagerService) ReconcileWallet(ctx context.Context, walletID financial.WalletID) (ReconciliationReport, error) {
	if walletID.IsZero() {
		return ReconciliationReport{}, contractError(CodeInvalidRequest, "walletId is required")
	}
	if s.reconciler == nil {
		return ReconciliationReport{}, transientError(nil)
	}
	snapshot, found, err := s.reconciler.ReconcileRead(ctx, walletID)
	if err != nil {
		return ReconciliationReport{}, err
	}
	if !found {
		return ReconciliationReport{}, notFoundError("wallet not found")
	}
	calculated, err := money.Zero(snapshot.Balance.Currency())
	if err != nil {
		return ReconciliationReport{}, wrapInvariant(err)
	}
	for _, entry := range snapshot.Entries {
		switch entry.Direction {
		case financial.DirectionCredit:
			calculated, err = calculated.Add(entry.Amount)
		case financial.DirectionDebit:
			calculated, err = calculated.Sub(entry.Amount)
		default:
			return ReconciliationReport{}, wrapInvariant(nil)
		}
		if err != nil {
			return ReconciliationReport{}, wrapInvariant(err)
		}
	}
	difference, err := snapshot.Balance.Sub(calculated)
	if err != nil {
		return ReconciliationReport{}, wrapInvariant(err)
	}
	return ReconciliationReport{
		WalletID:          walletID,
		StoredBalance:     snapshot.Balance,
		CalculatedBalance: calculated,
		Difference:        difference,
		Consistent:        difference.IsZero(),
		CheckedEntries:    len(snapshot.Entries),
	}, nil
}
