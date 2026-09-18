package financial

import (
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

// UUID-based internal identities. New identifiers are UUIDv7 for temporal
// locality; parsing accepts any canonical UUID representation.
type (
	WalletID        uuid.UUID
	PlayerID        uuid.UUID
	TransactionID   uuid.UUID
	LedgerEntryID   uuid.UUID
	ReversalClaimID uuid.UUID
)

type uuidIdentifiable interface {
	~[16]byte
}

func mustNewUUIDv7[T uuidIdentifiable]() T {
	id, err := uuid.NewV7()
	if err != nil {
		// Entropy failure is not a business rejection; the process cannot
		// produce auditable identities and must not continue silently.
		panic(fmt.Sprintf("financial: generating uuid v7: %v", err))
	}
	return T(id)
}

func parseUUIDID[T uuidIdentifiable](kind, raw string) (T, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%w: %s %q is not a canonical UUID", ErrInvalidInput, kind, raw)
	}
	return T(id), nil
}

// NewWalletID generates a UUIDv7 wallet identity.
func NewWalletID() WalletID { return mustNewUUIDv7[WalletID]() }

// ParseWalletID validates a canonical UUID wallet identity.
func ParseWalletID(raw string) (WalletID, error) { return parseUUIDID[WalletID]("wallet id", raw) }

// String renders the canonical UUID representation.
func (id WalletID) String() string { return uuid.UUID(id).String() }

// IsZero reports whether the identity is unset.
func (id WalletID) IsZero() bool { return uuid.UUID(id) == uuid.Nil }

// NewPlayerID generates a UUIDv7 player identity.
func NewPlayerID() PlayerID { return mustNewUUIDv7[PlayerID]() }

// ParsePlayerID validates a canonical UUID player identity.
func ParsePlayerID(raw string) (PlayerID, error) { return parseUUIDID[PlayerID]("player id", raw) }

// String renders the canonical UUID representation.
func (id PlayerID) String() string { return uuid.UUID(id).String() }

// IsZero reports whether the identity is unset.
func (id PlayerID) IsZero() bool { return uuid.UUID(id) == uuid.Nil }

// NewTransactionID generates a UUIDv7 transaction identity.
func NewTransactionID() TransactionID { return mustNewUUIDv7[TransactionID]() }

// ParseTransactionID validates a canonical UUID transaction identity.
func ParseTransactionID(raw string) (TransactionID, error) {
	return parseUUIDID[TransactionID]("transaction id", raw)
}

// String renders the canonical UUID representation.
func (id TransactionID) String() string { return uuid.UUID(id).String() }

// IsZero reports whether the identity is unset.
func (id TransactionID) IsZero() bool { return uuid.UUID(id) == uuid.Nil }

// NewLedgerEntryID generates a UUIDv7 ledger entry identity.
func NewLedgerEntryID() LedgerEntryID { return mustNewUUIDv7[LedgerEntryID]() }

// ParseLedgerEntryID validates a canonical UUID ledger entry identity.
func ParseLedgerEntryID(raw string) (LedgerEntryID, error) {
	return parseUUIDID[LedgerEntryID]("ledger entry id", raw)
}

// String renders the canonical UUID representation.
func (id LedgerEntryID) String() string { return uuid.UUID(id).String() }

// IsZero reports whether the identity is unset.
func (id LedgerEntryID) IsZero() bool { return uuid.UUID(id) == uuid.Nil }

// NewReversalClaimID generates a UUIDv7 reversal claim identity.
func NewReversalClaimID() ReversalClaimID { return mustNewUUIDv7[ReversalClaimID]() }

// ParseReversalClaimID validates a canonical UUID reversal claim identity.
func ParseReversalClaimID(raw string) (ReversalClaimID, error) {
	return parseUUIDID[ReversalClaimID]("reversal claim id", raw)
}

// String renders the canonical UUID representation.
func (id ReversalClaimID) String() string { return uuid.UUID(id).String() }

// IsZero reports whether the identity is unset.
func (id ReversalClaimID) IsZero() bool { return uuid.UUID(id) == uuid.Nil }

var (
	providerIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	externalIDPattern     = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	idempotencyKeyPattern = regexp.MustCompile(`^[\x21-\x7E]{1,255}$`)
)

// ProviderID is the bounded identity of an external provider.
type ProviderID string

// ParseProviderID validates the documented provider identity contract.
func ParseProviderID(raw string) (ProviderID, error) {
	if !providerIDPattern.MatchString(raw) {
		return "", fmt.Errorf("%w: provider id %q is outside the documented format", ErrInvalidInput, raw)
	}
	return ProviderID(raw), nil
}

// String renders the provider identity.
func (p ProviderID) String() string { return string(p) }

// IsZero reports whether the identity is unset.
func (p ProviderID) IsZero() bool { return p == "" }

// ExternalID is a bounded identity supplied by an external system, such as an
// external transaction, round or game identifier.
type ExternalID string

// ParseExternalID validates the documented external identity contract. The
// kind is only used to make the error message actionable.
func ParseExternalID(kind, raw string) (ExternalID, error) {
	if !externalIDPattern.MatchString(raw) {
		return "", fmt.Errorf("%w: %s %q is outside the documented format", ErrInvalidInput, kind, raw)
	}
	return ExternalID(raw), nil
}

// String renders the external identity.
func (e ExternalID) String() string { return string(e) }

// IsZero reports whether the identity is unset.
func (e ExternalID) IsZero() bool { return e == "" }

// IdempotencyKey is the caller-supplied key, stored and used byte for byte.
type IdempotencyKey string

// ParseIdempotencyKey validates the visible-ASCII key contract.
func ParseIdempotencyKey(raw string) (IdempotencyKey, error) {
	if !idempotencyKeyPattern.MatchString(raw) {
		return "", fmt.Errorf("%w: idempotency key is outside the documented format", ErrInvalidInput)
	}
	return IdempotencyKey(raw), nil
}

// String renders the key.
func (k IdempotencyKey) String() string { return string(k) }

// IsZero reports whether the key is unset.
func (k IdempotencyKey) IsZero() bool { return k == "" }
