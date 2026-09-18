package application

import (
	"context"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/event"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// WalletRepository persists the wallet aggregate.
type WalletRepository interface {
	Insert(ctx context.Context, wallet financial.Wallet) error
	// LockByID locks the wallet row with SELECT ... FOR UPDATE before the
	// balance used by a decision is read.
	LockByID(ctx context.Context, id financial.WalletID) (financial.Wallet, bool, error)
	ByID(ctx context.Context, id financial.WalletID) (financial.Wallet, bool, error)
	SaveBalance(ctx context.Context, wallet financial.Wallet) error
}

// ReferenceSchedule is the durable retry schedule of a PENDING_REFERENCE
// transaction.
type ReferenceSchedule struct {
	NextAttemptAt time.Time
	Attempts      int
}

// TransactionRepository persists wager transactions and claims due reference
// recovery work.
type TransactionRepository interface {
	Insert(ctx context.Context, transaction financial.WagerTransaction) error
	// Update persists a state transition. A non-nil schedule is required when
	// the transition enters PENDING_REFERENCE.
	Update(ctx context.Context, transaction financial.WagerTransaction, schedule *ReferenceSchedule) error
	// RescheduleReference records one failed resolution attempt: it increments
	// the attempt counter, stores the next attempt, releases the lease and
	// refreshes updatedAt.
	RescheduleReference(ctx context.Context, id financial.TransactionID, now, nextAttemptAt time.Time, attempts int) error
	ByID(ctx context.Context, id financial.TransactionID) (financial.WagerTransaction, bool, error)
	// LockByID locks the transaction row with SELECT ... FOR UPDATE and also
	// returns the persisted reference attempt counter.
	LockByID(ctx context.Context, id financial.TransactionID) (financial.WagerTransaction, int, bool, error)
	ByProviderAndKey(ctx context.Context, providerID financial.ProviderID, key financial.IdempotencyKey) (financial.WagerTransaction, bool, error)
	ByProviderAndExternalID(ctx context.Context, providerID financial.ProviderID, externalID financial.ExternalID) (financial.WagerTransaction, bool, error)
	// ClaimDueReferences locks a batch of due PENDING_REFERENCE rows with
	// SKIP LOCKED and leases them for the given duration.
	ClaimDueReferences(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]financial.WagerTransaction, error)
}

// LedgerRepository appends immutable postings.
type LedgerRepository interface {
	Insert(ctx context.Context, entry financial.WalletLedgerEntry) error
}

// ReversalClaimRepository records consumed direct compensation rights.
type ReversalClaimRepository interface {
	Insert(ctx context.Context, claim financial.ReversalClaim) error
	ByReference(ctx context.Context, referenceTransactionID financial.TransactionID) (financial.ReversalClaim, bool, error)
}

// OutboxRepository stores immutable event snapshots.
type OutboxRepository interface {
	Insert(ctx context.Context, envelope event.Envelope, now time.Time) error
}

// Repositories bundles the ports bound to one transaction or connection.
type Repositories struct {
	Wallets        WalletRepository
	Transactions   TransactionRepository
	Ledger         LedgerRepository
	ReversalClaims ReversalClaimRepository
	Outbox         OutboxRepository
}

// Store opens transactions and hands out repositories bound to them.
type Store interface {
	Repositories() Repositories
	InTx(ctx context.Context, fn func(context.Context, Repositories) error) error
}

// Clock supplies the current instant so time-dependent behavior stays
// testable without changing the domain.
type Clock interface {
	Now() time.Time
}
