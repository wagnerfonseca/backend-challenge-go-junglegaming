// Package postgres implements the application ports with pgx/v5 and explicit
// parameterized SQL. No ORM is involved.
package postgres

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
)

const (
	sqlStateUniqueViolation     = "23505"
	sqlStateForeignKeyViolation = "23503"
	sqlStateCheckViolation      = "23514"
	sqlStateSerialization       = "40001"
	sqlStateDeadlock            = "40P01"
)

const (
	// PostgreSQL folds unquoted identifiers to lower case, so catalog
	// constraint and index names arrive lower-cased.
	constraintWalletsPlayerCurrency      = "wallets_playerid_currency_key"
	constraintProviderKeyIndex           = "wager_transactions_provider_key_idx"
	constraintProviderExternalIndex      = "wager_transactions_provider_externaltransactionid_idx"
	constraintOpeningWalletIndex         = "wager_transactions_opening_walletid_idx"
	constraintLedgerWalletTransactionKey = "wallet_ledger_entries_walletid_transactionid_key"
	constraintClaimReferenceKey          = "reversal_claims_referencetransactionid_key"
	constraintClaimReversalKey           = "reversal_claims_reversaltransactionid_key"
)

// mapError translates driver failures into the port sentinels the application
// classifies. Anything not recognized is transient by default: an unknown
// database failure must never produce a durable financial outcome.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case sqlStateUniqueViolation:
			return uniqueViolationError(pgErr)
		case sqlStateSerialization, sqlStateDeadlock:
			return fmt.Errorf("%w: %w", application.ErrConcurrentWrite, err)
		case sqlStateCheckViolation, sqlStateForeignKeyViolation:
			return fmt.Errorf("%w: %s", application.ErrCorruptPersistedRecord, pgErr.Message)
		}
	}
	return fmt.Errorf("%w: %w", application.ErrTransient, err)
}

func uniqueViolationError(pgErr *pgconn.PgError) error {
	switch strings.ToLower(pgErr.ConstraintName) {
	case constraintWalletsPlayerCurrency:
		return fmt.Errorf("%w: %w", application.ErrWalletExists, pgErr)
	case constraintProviderKeyIndex, constraintProviderExternalIndex, constraintOpeningWalletIndex,
		constraintLedgerWalletTransactionKey, constraintClaimReferenceKey, constraintClaimReversalKey:
		return fmt.Errorf("%w: %w", application.ErrConcurrentWrite, pgErr)
	default:
		return fmt.Errorf("%w: unique constraint %s: %w", application.ErrConcurrentWrite, pgErr.ConstraintName, pgErr)
	}
}

func corruptRow(err error) error {
	return fmt.Errorf("%w: %w", application.ErrCorruptPersistedRecord, err)
}
