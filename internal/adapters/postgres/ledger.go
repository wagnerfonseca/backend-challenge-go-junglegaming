package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
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

// ListPage reads one ascending (createdAt,id) keyset page. The extra row
// beyond the limit reports whether a continuation cursor exists.
func (r *ledgerRepository) ListPage(ctx context.Context, walletID financial.WalletID, currency string, after *application.LedgerCursor, limit int) ([]financial.WalletLedgerEntry, bool, error) {
	query := `SELECT "id", "walletId", "transactionId", "direction", "amount", "balanceBefore", "balanceAfter", "createdAt"
		FROM wallet_ledger_entries WHERE "walletId" = $1`
	args := []any{walletID.String()}
	if after != nil {
		query += ` AND ("createdAt", "id") > ($2, $3)`
		args = append(args, after.CreatedAt.UTC(), after.ID.String())
	}
	query += fmt.Sprintf(` ORDER BY "createdAt", "id" LIMIT $%d`, len(args)+1)
	args = append(args, limit+1)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, false, mapError(err)
	}
	defer rows.Close()
	var entries []financial.WalletLedgerEntry
	for rows.Next() {
		var (
			id, wallet, transactionID string
			direction                 string
			amount                    int64
			balanceBefore             int64
			balanceAfter              int64
			createdAt                 time.Time
		)
		if err := rows.Scan(&id, &wallet, &transactionID, &direction, &amount, &balanceBefore, &balanceAfter, &createdAt); err != nil {
			return nil, false, mapError(err)
		}
		entry, err := rehydrateLedgerEntry(id, wallet, transactionID, direction, amount, balanceBefore, balanceAfter, currency, createdAt)
		if err != nil {
			return nil, false, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, false, mapError(err)
	}
	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}
	return entries, hasMore, nil
}

func rehydrateLedgerEntry(id, walletID, transactionID, direction string, amount, balanceBefore, balanceAfter int64, currency string, createdAt time.Time) (financial.WalletLedgerEntry, error) {
	entryID, err := financial.ParseLedgerEntryID(id)
	if err != nil {
		return financial.WalletLedgerEntry{}, corruptRow(err)
	}
	wallet, err := financial.ParseWalletID(walletID)
	if err != nil {
		return financial.WalletLedgerEntry{}, corruptRow(err)
	}
	transaction, err := financial.ParseTransactionID(transactionID)
	if err != nil {
		return financial.WalletLedgerEntry{}, corruptRow(err)
	}
	amountMoney, err := money.FromMinorUnits(amount, currency)
	if err != nil {
		return financial.WalletLedgerEntry{}, corruptRow(err)
	}
	beforeMoney, err := money.FromMinorUnits(balanceBefore, currency)
	if err != nil {
		return financial.WalletLedgerEntry{}, corruptRow(err)
	}
	afterMoney, err := money.FromMinorUnits(balanceAfter, currency)
	if err != nil {
		return financial.WalletLedgerEntry{}, corruptRow(err)
	}
	entry, err := financial.RehydrateWalletLedgerEntry(entryID, wallet, transaction, financial.Direction(direction), amountMoney, beforeMoney, afterMoney, createdAt)
	if err != nil {
		return financial.WalletLedgerEntry{}, corruptRow(err)
	}
	return entry, nil
}
