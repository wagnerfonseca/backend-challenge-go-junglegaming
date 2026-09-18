package financial

import (
	"fmt"
	"time"
)

// ReversalClaim records that a direct compensation right has been consumed:
// one successful REFUND or ROLLBACK per referenced transaction.
type ReversalClaim struct {
	id                     ReversalClaimID
	referenceTransactionID TransactionID
	reversalTransactionID  TransactionID
	createdAt              time.Time
}

// NewReversalClaim creates the claim persisted by the first successful direct
// compensation of a referenced transaction.
func NewReversalClaim(id ReversalClaimID, referenceTransactionID, reversalTransactionID TransactionID, now time.Time) (ReversalClaim, error) {
	if id.IsZero() {
		return ReversalClaim{}, fmt.Errorf("%w: reversal claim id is empty", ErrInvalidInput)
	}
	if referenceTransactionID.IsZero() || reversalTransactionID.IsZero() {
		return ReversalClaim{}, fmt.Errorf("%w: reversal claim carries an empty transaction identity", ErrInvalidInput)
	}
	if referenceTransactionID == reversalTransactionID {
		return ReversalClaim{}, fmt.Errorf("%w: a transaction cannot compensate itself", ErrInvalidInput)
	}
	if now.IsZero() {
		return ReversalClaim{}, fmt.Errorf("%w: reversal claim timestamp is zero", ErrInvalidInput)
	}
	return ReversalClaim{
		id:                     id,
		referenceTransactionID: referenceTransactionID,
		reversalTransactionID:  reversalTransactionID,
		createdAt:              now.UTC(),
	}, nil
}

// RehydrateReversalClaim rebuilds a claim from persisted state.
func RehydrateReversalClaim(id ReversalClaimID, referenceTransactionID, reversalTransactionID TransactionID, createdAt time.Time) (ReversalClaim, error) {
	return NewReversalClaim(id, referenceTransactionID, reversalTransactionID, createdAt)
}

// ID returns the claim identity.
func (c ReversalClaim) ID() ReversalClaimID { return c.id }

// ReferenceTransactionID returns the compensated transaction identity.
func (c ReversalClaim) ReferenceTransactionID() TransactionID { return c.referenceTransactionID }

// ReversalTransactionID returns the compensating transaction identity.
func (c ReversalClaim) ReversalTransactionID() TransactionID { return c.reversalTransactionID }

// CreatedAt returns the creation instant.
func (c ReversalClaim) CreatedAt() time.Time { return c.createdAt }
