package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// executor is the subset shared by pgxpool.Pool and pgx.Tx.
type executor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store binds every repository either to the pool or to one transaction.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore builds a store over an existing pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// ClaimDueReferences leases due PENDING_REFERENCE transactions for the
// reference worker.
func (s *Store) ClaimDueReferences(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]financial.WagerTransaction, error) {
	return (&transactionRepository{db: s.pool}).ClaimDueReferences(ctx, now, limit, lease)
}

// Repositories returns ports bound to the pool for non-transactional reads.
func (s *Store) Repositories() application.Repositories {
	return repositoriesFor(s.pool)
}

// InTx runs fn inside one transaction and commits exactly once. The closure
// owns the financial transaction boundary: wallet lock, domain application,
// balance/version update, ledger append, claim, outbox insert, then commit.
func (s *Store) InTx(ctx context.Context, fn func(context.Context, application.Repositories) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(ctx, repositoriesFor(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return mapError(err)
	}
	return nil
}

func repositoriesFor(db executor) application.Repositories {
	return application.Repositories{
		Wallets:        &walletRepository{db: db},
		Transactions:   &transactionRepository{db: db},
		Ledger:         &ledgerRepository{db: db},
		ReversalClaims: &claimRepository{db: db},
		Outbox:         &outboxRepository{db: db},
		Inbox:          &inboxRepository{db: db},
	}
}
