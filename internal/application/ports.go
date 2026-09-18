package application

import (
	"context"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/event"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
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

// LedgerRepository appends immutable postings and pages them by keyset.
type LedgerRepository interface {
	Insert(ctx context.Context, entry financial.WalletLedgerEntry) error
	// ListPage reads one ascending (createdAt,id) page after the cursor. It
	// returns hasMore when another entry exists beyond the returned page.
	ListPage(ctx context.Context, walletID financial.WalletID, currency string, after *LedgerCursor, limit int) ([]financial.WalletLedgerEntry, bool, error)
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

// InboxDelivery identifies one received broker message.
type InboxDelivery struct {
	ConsumerName string
	MessageID    string
	Digest       string
	ReceivedAt   time.Time
}

// InboxRepository records received deliveries so a redelivery never reapplies
// money. Completion commits in the same SQL transaction as the financial
// change it acknowledges.
type InboxRepository interface {
	// Begin inserts the delivery record when it is new. When the delivery
	// already exists it reports found=true and the stored payload digest.
	Begin(ctx context.Context, delivery InboxDelivery) (found bool, digest string, err error)
	// Complete marks the delivery as durably handled.
	Complete(ctx context.Context, consumerName, messageID string, completedAt time.Time) error
}

// OutboxRecord is one claimed event snapshot awaiting publication.
type OutboxRecord struct {
	EventID     string
	EventType   string
	AggregateID string
	Payload     []byte
	Attempts    int
}

// OutboxStore claims and confirms outbox publications with a recoverable
// lease. It is implemented by the PostgreSQL adapter and consumed by the
// outbox publisher.
type OutboxStore interface {
	ClaimDueEvents(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]OutboxRecord, error)
	ConfirmEvent(ctx context.Context, eventID string, confirmedAt time.Time) error
	RescheduleEvent(ctx context.Context, eventID string, now, nextAttemptAt time.Time, attempts int) error
	OldestPendingAge(ctx context.Context, now time.Time) (time.Duration, bool, error)
}

// Repositories bundles the ports bound to one transaction or connection.
type Repositories struct {
	Wallets        WalletRepository
	Transactions   TransactionRepository
	Ledger         LedgerRepository
	ReversalClaims ReversalClaimRepository
	Outbox         OutboxRepository
	Inbox          InboxRepository
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

// ReconciliationEntry is one ledger posting summarized for reconciliation.
type ReconciliationEntry struct {
	Direction financial.Direction
	Amount    money.Money
}

// ReconciliationSnapshot is one consistent view of a wallet and its ledger.
type ReconciliationSnapshot struct {
	Balance money.Money
	Entries []ReconciliationEntry
}

// Reconciler reads a wallet and its complete ledger in one repeatable-read
// snapshot without taking write locks.
type Reconciler interface {
	ReconcileRead(ctx context.Context, walletID financial.WalletID) (ReconciliationSnapshot, bool, error)
}
