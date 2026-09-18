package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

// ReconcileRead reads a wallet and its complete ledger in one REPEATABLE READ
// snapshot without taking any write lock.
func (s *Store) ReconcileRead(ctx context.Context, walletID financial.WalletID) (application.ReconciliationSnapshot, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return application.ReconciliationSnapshot{}, false, mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		balance  int64
		currency string
	)
	err = tx.QueryRow(ctx,
		`SELECT "balance", "currency" FROM wallets WHERE "id" = $1`, walletID.String(),
	).Scan(&balance, &currency)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ReconciliationSnapshot{}, false, nil
		}
		return application.ReconciliationSnapshot{}, false, mapError(err)
	}
	balanceMoney, err := money.FromMinorUnits(balance, currency)
	if err != nil {
		return application.ReconciliationSnapshot{}, false, corruptRow(err)
	}

	rows, err := tx.Query(ctx,
		`SELECT "direction", "amount" FROM wallet_ledger_entries WHERE "walletId" = $1 ORDER BY "createdAt", "id"`,
		walletID.String(),
	)
	if err != nil {
		return application.ReconciliationSnapshot{}, false, mapError(err)
	}
	defer rows.Close()
	var entries []application.ReconciliationEntry
	for rows.Next() {
		var (
			direction string
			amount    int64
		)
		if err := rows.Scan(&direction, &amount); err != nil {
			return application.ReconciliationSnapshot{}, false, mapError(err)
		}
		value, err := money.FromMinorUnits(amount, currency)
		if err != nil {
			return application.ReconciliationSnapshot{}, false, corruptRow(err)
		}
		entries = append(entries, application.ReconciliationEntry{
			Direction: financial.Direction(direction),
			Amount:    value,
		})
	}
	if err := rows.Err(); err != nil {
		return application.ReconciliationSnapshot{}, false, mapError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return application.ReconciliationSnapshot{}, false, mapError(err)
	}
	return application.ReconciliationSnapshot{Balance: balanceMoney, Entries: entries}, true, nil
}
