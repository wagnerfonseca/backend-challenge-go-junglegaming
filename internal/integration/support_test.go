//go:build integration

package integration

import (
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

type ledgerRow struct {
	ID            string
	WalletID      string
	TransactionID string
	Direction     string
	Amount        int64
	BalanceBefore int64
	BalanceAfter  int64
	CreatedAt     time.Time
}

func (h *harness) ledgerRows(walletID financial.WalletID) []ledgerRow {
	h.t.Helper()
	rows, err := adminPool.Query(h.ctx(),
		`SELECT "id", "walletId", "transactionId", "direction", "amount", "balanceBefore", "balanceAfter", "createdAt"
		FROM wallet_ledger_entries WHERE "walletId" = $1 ORDER BY "createdAt", "id"`,
		walletID.String(),
	)
	if err != nil {
		h.t.Fatalf("reading ledger: %v", err)
	}
	defer rows.Close()
	var entries []ledgerRow
	for rows.Next() {
		var entry ledgerRow
		if err := rows.Scan(&entry.ID, &entry.WalletID, &entry.TransactionID, &entry.Direction, &entry.Amount, &entry.BalanceBefore, &entry.BalanceAfter, &entry.CreatedAt); err != nil {
			h.t.Fatalf("scanning ledger entry: %v", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("iterating ledger: %v", err)
	}
	return entries
}

type persistedTransaction struct {
	ID                     string
	Origin                 string
	Kind                   string
	State                  string
	ProviderID             *string
	ExternalTransactionID  *string
	IdempotencyKey         *string
	DigestVersion          *string
	Digest                 *string
	WalletID               string
	PlayerID               string
	RoundID                *string
	GameID                 *string
	Amount                 int64
	Currency               string
	ReferenceExternalID    *string
	ReferenceTransactionID *string
	FailureCode            *string
	ObservedBalance        *int64
	ReferenceDeadline      *time.Time
	NextAttemptAt          *time.Time
	ReferenceAttempts      int
	ClaimedUntil           *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

const persistedTransactionColumns = `"id", "origin", "kind", "state", "providerId", "externalTransactionId", "idempotencyKey", "digestVersion", "digest", "walletId", "playerId", "roundId", "gameId", "amount", "currency", "referenceExternalTransactionId", "referenceTransactionId", "failureCode", "observedBalance", "referenceDeadline", "nextAttemptAt", "referenceAttempts", "claimedUntil", "createdAt", "updatedAt"`

func (h *harness) transactionByID(id financial.TransactionID) persistedTransaction {
	h.t.Helper()
	row := adminPool.QueryRow(h.ctx(),
		`SELECT `+persistedTransactionColumns+` FROM wager_transactions WHERE "id" = $1`, id.String())
	return h.scanPersisted(row)
}

func (h *harness) transactionByExternal(provider string, external financial.ExternalID) persistedTransaction {
	h.t.Helper()
	row := adminPool.QueryRow(h.ctx(),
		`SELECT `+persistedTransactionColumns+` FROM wager_transactions WHERE "providerId" = $1 AND "externalTransactionId" = $2`,
		provider, external.String())
	return h.scanPersisted(row)
}

func (h *harness) scanPersisted(row pgx.Row) persistedTransaction {
	h.t.Helper()
	var record persistedTransaction
	err := row.Scan(
		&record.ID, &record.Origin, &record.Kind, &record.State,
		&record.ProviderID, &record.ExternalTransactionID, &record.IdempotencyKey,
		&record.DigestVersion, &record.Digest,
		&record.WalletID, &record.PlayerID,
		&record.RoundID, &record.GameID,
		&record.Amount, &record.Currency,
		&record.ReferenceExternalID, &record.ReferenceTransactionID,
		&record.FailureCode, &record.ObservedBalance,
		&record.ReferenceDeadline, &record.NextAttemptAt, &record.ReferenceAttempts, &record.ClaimedUntil,
		&record.CreatedAt, &record.UpdatedAt,
	)
	if err != nil {
		h.t.Fatalf("reading persisted transaction: %v", err)
	}
	return record
}

func (h *harness) transactionExists(provider string, external financial.ExternalID) bool {
	h.t.Helper()
	return h.countRows(
		`SELECT count(*) FROM wager_transactions WHERE "providerId" = $1 AND "externalTransactionId" = $2`,
		provider, external.String(),
	) == 1
}

func (h *harness) claimRows(referenceTransactionID financial.TransactionID) int {
	h.t.Helper()
	return h.countRows(
		`SELECT count(*) FROM reversal_claims WHERE "referenceTransactionId" = $1`,
		referenceTransactionID.String(),
	)
}
