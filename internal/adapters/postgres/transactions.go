package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

type transactionRepository struct {
	db executor
}

const transactionColumns = `"id", "origin", "kind", "state", "providerId", "externalTransactionId", "idempotencyKey", "digestVersion", "digest", "walletId", "playerId", "roundId", "gameId", "amount", "currency", "referenceExternalTransactionId", "referenceTransactionId", "failureCode", "observedBalance", "referenceDeadline", "nextAttemptAt", "referenceAttempts", "claimedUntil", "createdAt", "updatedAt"`

func (r *transactionRepository) Insert(ctx context.Context, transaction financial.WagerTransaction) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO wager_transactions (`+transactionColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)`,
		transaction.ID().String(),
		string(transaction.Origin()),
		string(transaction.Kind()),
		string(transaction.State()),
		nullString(transaction.ProviderID().String()),
		nullString(transaction.ExternalTransactionID().String()),
		nullString(transaction.IdempotencyKey().String()),
		nullString(transaction.DigestVersion()),
		nullString(transaction.Digest()),
		transaction.WalletID().String(),
		transaction.PlayerID().String(),
		nullString(transaction.RoundID().String()),
		nullString(transaction.GameID().String()),
		transaction.Amount().MinorUnits(),
		transaction.Amount().Currency(),
		nullString(transaction.ReferenceExternalTransactionID().String()),
		nullUUID(transaction.ReferenceTransactionID()),
		nullString(string(transaction.FailureCode())),
		nullMoney(transaction.ObservedBalance()),
		nullTime(transaction.ReferenceDeadline()),
		nil,
		int64(0),
		nil,
		transaction.CreatedAt().UTC(),
		transaction.UpdatedAt().UTC(),
	)
	return mapError(err)
}

func (r *transactionRepository) Update(ctx context.Context, transaction financial.WagerTransaction, schedule *application.ReferenceSchedule) error {
	var nextAttemptAt any
	attempts := int64(0)
	if schedule != nil {
		nextAttemptAt = schedule.NextAttemptAt.UTC()
		attempts = int64(schedule.Attempts)
	}
	_, err := r.db.Exec(ctx,
		`UPDATE wager_transactions SET
			"state" = $2,
			"failureCode" = $3,
			"observedBalance" = $4,
			"referenceTransactionId" = $5,
			"referenceDeadline" = $6,
			"nextAttemptAt" = $7,
			"referenceAttempts" = $8,
			"claimedUntil" = NULL,
			"updatedAt" = $9
		WHERE "id" = $1`,
		transaction.ID().String(),
		string(transaction.State()),
		nullString(string(transaction.FailureCode())),
		nullMoney(transaction.ObservedBalance()),
		nullUUID(transaction.ReferenceTransactionID()),
		nullTime(transaction.ReferenceDeadline()),
		nextAttemptAt,
		attempts,
		transaction.UpdatedAt().UTC(),
	)
	return mapError(err)
}

func (r *transactionRepository) RescheduleReference(ctx context.Context, id financial.TransactionID, now, nextAttemptAt time.Time, attempts int) error {
	_, err := r.db.Exec(ctx,
		`UPDATE wager_transactions
		SET "nextAttemptAt" = $2, "referenceAttempts" = $3, "claimedUntil" = NULL, "updatedAt" = $1
		WHERE "id" = $4`,
		now.UTC(), nextAttemptAt.UTC(), int64(attempts), id.String(),
	)
	return mapError(err)
}

func (r *transactionRepository) ByID(ctx context.Context, id financial.TransactionID) (financial.WagerTransaction, bool, error) {
	row := r.db.QueryRow(ctx, `SELECT `+transactionColumns+` FROM wager_transactions WHERE "id" = $1`, id.String())
	return scanTransaction(row)
}

func (r *transactionRepository) LockByID(ctx context.Context, id financial.TransactionID) (financial.WagerTransaction, int, bool, error) {
	row := r.db.QueryRow(ctx, `SELECT `+transactionColumns+` FROM wager_transactions WHERE "id" = $1 FOR UPDATE`, id.String())
	return scanTransactionWithAttempts(row)
}

func (r *transactionRepository) ByProviderAndKey(ctx context.Context, providerID financial.ProviderID, key financial.IdempotencyKey) (financial.WagerTransaction, bool, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+transactionColumns+` FROM wager_transactions WHERE "providerId" = $1 AND "idempotencyKey" = $2`,
		providerID.String(), key.String(),
	)
	return scanTransaction(row)
}

func (r *transactionRepository) ByProviderAndExternalID(ctx context.Context, providerID financial.ProviderID, externalID financial.ExternalID) (financial.WagerTransaction, bool, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+transactionColumns+` FROM wager_transactions WHERE "providerId" = $1 AND "externalTransactionId" = $2`,
		providerID.String(), externalID.String(),
	)
	return scanTransaction(row)
}

func (r *transactionRepository) ClaimDueReferences(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]financial.WagerTransaction, error) {
	rows, err := r.db.Query(ctx,
		`UPDATE wager_transactions
		SET "claimedUntil" = $2, "updatedAt" = $1
		WHERE "id" IN (
			SELECT "id" FROM wager_transactions
			WHERE "state" = 'PENDING_REFERENCE'
				AND "nextAttemptAt" <= $1
				AND ("claimedUntil" IS NULL OR "claimedUntil" <= $1)
			ORDER BY "nextAttemptAt", "id"
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+transactionColumns,
		now.UTC(), now.Add(lease).UTC(), int64(limit),
	)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var claimed []financial.WagerTransaction
	for rows.Next() {
		transaction, _, _, err := scanTransactionWithAttempts(rows)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, transaction)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	return claimed, nil
}

func scanTransaction(row pgx.Row) (financial.WagerTransaction, bool, error) {
	transaction, _, found, err := scanTransactionWithAttempts(row)
	return transaction, found, err
}

func scanTransactionWithAttempts(row pgx.Row) (financial.WagerTransaction, int, bool, error) {
	var (
		id                               string
		origin, kind, state              string
		providerID, externalID, key      *string
		digestVersion, digest            *string
		walletID, playerID               string
		roundID, gameID                  *string
		amount                           int64
		currency                         string
		referenceExternalID              *string
		referenceTransactionID           *string
		failureCode                      *string
		observedBalance                  *int64
		referenceDeadline, nextAttemptAt *time.Time
		referenceAttempts                int64
		claimedUntil                     *time.Time
		createdAt, updatedAt             time.Time
	)
	err := row.Scan(
		&id, &origin, &kind, &state,
		&providerID, &externalID, &key,
		&digestVersion, &digest,
		&walletID, &playerID,
		&roundID, &gameID,
		&amount, &currency,
		&referenceExternalID, &referenceTransactionID,
		&failureCode, &observedBalance,
		&referenceDeadline, &nextAttemptAt, &referenceAttempts, &claimedUntil,
		&createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return financial.WagerTransaction{}, 0, false, nil
		}
		return financial.WagerTransaction{}, 0, false, mapError(err)
	}
	transaction, err := rehydrateTransaction(transactionRow{
		id: id, origin: origin, kind: kind, state: state,
		providerID: providerID, externalID: externalID, key: key,
		digestVersion: digestVersion, digest: digest,
		walletID: walletID, playerID: playerID,
		roundID: roundID, gameID: gameID,
		amount: amount, currency: currency,
		referenceExternalID: referenceExternalID, referenceTransactionID: referenceTransactionID,
		failureCode: failureCode, observedBalance: observedBalance,
		referenceDeadline: referenceDeadline,
		createdAt:         createdAt, updatedAt: updatedAt,
	})
	if err != nil {
		return financial.WagerTransaction{}, 0, false, err
	}
	return transaction, int(referenceAttempts), true, nil
}

type transactionRow struct {
	id, origin, kind, state    string
	providerID, externalID     *string
	key, digestVersion, digest *string
	walletID, playerID         string
	roundID, gameID            *string
	amount                     int64
	currency                   string
	referenceExternalID        *string
	referenceTransactionID     *string
	failureCode                *string
	observedBalance            *int64
	referenceDeadline          *time.Time
	createdAt, updatedAt       time.Time
}

func rehydrateTransaction(row transactionRow) (financial.WagerTransaction, error) {
	transactionID, err := financial.ParseTransactionID(row.id)
	if err != nil {
		return financial.WagerTransaction{}, corruptRow(err)
	}
	walletID, err := financial.ParseWalletID(row.walletID)
	if err != nil {
		return financial.WagerTransaction{}, corruptRow(err)
	}
	playerID, err := financial.ParsePlayerID(row.playerID)
	if err != nil {
		return financial.WagerTransaction{}, corruptRow(err)
	}
	amount, err := money.FromMinorUnits(row.amount, row.currency)
	if err != nil {
		return financial.WagerTransaction{}, corruptRow(err)
	}
	params := financial.RehydrateTransactionParams{
		ID:                    transactionID,
		Origin:                financial.Origin(row.origin),
		Kind:                  financial.Kind(row.kind),
		ProviderID:            financial.ProviderID(derefString(row.providerID)),
		ExternalTransactionID: financial.ExternalID(derefString(row.externalID)),
		IdempotencyKey:        financial.IdempotencyKey(derefString(row.key)),
		DigestVersion:         derefString(row.digestVersion),
		Digest:                derefString(row.digest),
		WalletID:              walletID,
		PlayerID:              playerID,
		RoundID:               financial.ExternalID(derefString(row.roundID)),
		GameID:                financial.ExternalID(derefString(row.gameID)),
		Amount:                amount,
		ReferenceExternalID:   financial.ExternalID(derefString(row.referenceExternalID)),
		State:                 financial.State(row.state),
		FailureCode:           financial.FailureCode(derefString(row.failureCode)),
		CreatedAt:             row.createdAt,
		UpdatedAt:             row.updatedAt,
	}
	if row.referenceTransactionID != nil {
		referenceID, err := financial.ParseTransactionID(*row.referenceTransactionID)
		if err != nil {
			return financial.WagerTransaction{}, corruptRow(err)
		}
		params.ReferenceTransactionID = referenceID
	}
	if row.observedBalance != nil {
		observed, err := money.FromMinorUnits(*row.observedBalance, row.currency)
		if err != nil {
			return financial.WagerTransaction{}, corruptRow(err)
		}
		params.ObservedBalance = observed
	}
	if row.referenceDeadline != nil {
		params.ReferenceDeadline = *row.referenceDeadline
	}
	transaction, err := financial.RehydrateTransaction(params)
	if err != nil {
		return financial.WagerTransaction{}, corruptRow(err)
	}
	return transaction, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullUUID(id financial.TransactionID) any {
	if id.IsZero() {
		return nil
	}
	return id.String()
}

func nullMoney(value money.Money) any {
	if !value.IsInitialized() {
		return nil
	}
	return value.MinorUnits()
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}
