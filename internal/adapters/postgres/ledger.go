package postgres

import (
	"context"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

type ledgerRepository struct {
	db executor
}

func (r *ledgerRepository) Insert(ctx context.Context, entry financial.WalletLedgerEntry) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO wallet_ledger_entries ("id", "walletId", "transactionId", "direction", "amount", "balanceBefore", "balanceAfter", "createdAt")
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		entry.ID().String(),
		entry.WalletID().String(),
		entry.TransactionID().String(),
		string(entry.Direction()),
		entry.Amount().MinorUnits(),
		entry.BalanceBefore().MinorUnits(),
		entry.BalanceAfter().MinorUnits(),
		entry.CreatedAt().UTC(),
	)
	return mapError(err)
}
