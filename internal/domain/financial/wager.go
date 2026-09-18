package financial

import (
	"fmt"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

// Origin distinguishes internal operations from provider operations.
type Origin string

// Transaction origins.
const (
	OriginInternal Origin = "INTERNAL"
	OriginExternal Origin = "EXTERNAL"
)

// Valid reports whether the origin is one of the documented values.
func (o Origin) Valid() bool { return o == OriginInternal || o == OriginExternal }

// Kind is the operation vocabulary.
type Kind string

// Operation kinds.
const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

// Valid reports whether the kind is one of the documented values.
func (k Kind) Valid() bool {
	switch k {
	case KindOpening, KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return true
	default:
		return false
	}
}

// IsExternal reports whether the kind may be submitted by a provider.
func (k Kind) IsExternal() bool {
	switch k {
	case KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return true
	default:
		return false
	}
}

// State is the transaction lifecycle.
type State string

// Transaction states.
const (
	StatePending          State = "PENDING"
	StatePendingReference State = "PENDING_REFERENCE"
	StateProcessed        State = "PROCESSED"
	StateRejected         State = "REJECTED"
	StateFailed           State = "FAILED"
)

// Valid reports whether the state is one of the documented values.
func (s State) Valid() bool {
	switch s {
	case StatePending, StatePendingReference, StateProcessed, StateRejected, StateFailed:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether no further transition is allowed.
func (s State) IsTerminal() bool {
	return s == StateProcessed || s == StateRejected || s == StateFailed
}

// WagerTransaction is the idempotent record and state machine of one internal
// or external operation.
type WagerTransaction struct {
	id                             TransactionID
	origin                         Origin
	kind                           Kind
	providerID                     ProviderID
	externalTransactionID          ExternalID
	idempotencyKey                 IdempotencyKey
	digestVersion                  string
	digest                         string
	walletID                       WalletID
	playerID                       PlayerID
	roundID                        ExternalID
	gameID                         ExternalID
	amount                         money.Money
	referenceExternalTransactionID ExternalID
	referenceTransactionID         TransactionID
	state                          State
	failureCode                    FailureCode
	observedBalance                money.Money
	referenceDeadline              time.Time
	createdAt                      time.Time
	updatedAt                      time.Time
}

// NewExternalTransactionParams carries the required fields of a provider
// operation.
type NewExternalTransactionParams struct {
	ID                    TransactionID
	ProviderID            ProviderID
	ExternalTransactionID ExternalID
	IdempotencyKey        IdempotencyKey
	DigestVersion         string
	Digest                string
	WalletID              WalletID
	PlayerID              PlayerID
	RoundID               ExternalID
	GameID                ExternalID
	Kind                  Kind
	Amount                money.Money
	ReferenceExternalID   ExternalID
	Now                   time.Time
}

// NewExternalTransaction creates a provider transaction in PENDING.
func NewExternalTransaction(p NewExternalTransactionParams) (WagerTransaction, error) {
	if p.ID.IsZero() || p.WalletID.IsZero() || p.PlayerID.IsZero() {
		return WagerTransaction{}, fmt.Errorf("%w: external transaction carries an empty identity", ErrInvalidInput)
	}
	if p.ProviderID.IsZero() || p.ExternalTransactionID.IsZero() || p.RoundID.IsZero() || p.GameID.IsZero() {
		return WagerTransaction{}, fmt.Errorf("%w: external transaction is missing provider, external, round or game identity", ErrInvalidInput)
	}
	if p.IdempotencyKey.IsZero() || p.DigestVersion == "" || p.Digest == "" {
		return WagerTransaction{}, fmt.Errorf("%w: external transaction is missing idempotency key or digest", ErrInvalidInput)
	}
	if !p.Kind.Valid() || !p.Kind.IsExternal() {
		return WagerTransaction{}, fmt.Errorf("%w: kind %q is not a provider operation", ErrInvalidInput, p.Kind)
	}
	if !p.Amount.IsInitialized() {
		return WagerTransaction{}, fmt.Errorf("%w: transaction amount has no currency", ErrUninitialized)
	}
	if p.Now.IsZero() {
		return WagerTransaction{}, fmt.Errorf("%w: transaction timestamp is zero", ErrInvalidInput)
	}
	switch p.Kind {
	case KindLoss:
		if !p.Amount.IsZero() {
			return WagerTransaction{}, fmt.Errorf("%w: LOSS requires amount 0.00, got %s", ErrInvalidInput, p.Amount)
		}
	case KindBet:
		if !p.Amount.IsPositive() {
			return WagerTransaction{}, fmt.Errorf("%w: %s requires a positive amount, got %s", ErrInvalidInput, p.Kind, p.Amount)
		}
		if !p.ReferenceExternalID.IsZero() {
			return WagerTransaction{}, fmt.Errorf("%w: %s does not accept a reference", ErrInvalidInput, p.Kind)
		}
	default:
		if !p.Amount.IsPositive() {
			return WagerTransaction{}, fmt.Errorf("%w: %s requires a positive amount, got %s", ErrInvalidInput, p.Kind, p.Amount)
		}
	}
	switch p.Kind {
	case KindRefund, KindRollback:
		if p.ReferenceExternalID.IsZero() {
			return WagerTransaction{}, fmt.Errorf("%w: %s requires referenceExternalTransactionId", ErrInvalidInput, p.Kind)
		}
	}
	return WagerTransaction{
		id:                             p.ID,
		origin:                         OriginExternal,
		kind:                           p.Kind,
		providerID:                     p.ProviderID,
		externalTransactionID:          p.ExternalTransactionID,
		idempotencyKey:                 p.IdempotencyKey,
		digestVersion:                  p.DigestVersion,
		digest:                         p.Digest,
		walletID:                       p.WalletID,
		playerID:                       p.PlayerID,
		roundID:                        p.RoundID,
		gameID:                         p.GameID,
		amount:                         p.Amount,
		referenceExternalTransactionID: p.ReferenceExternalID,
		state:                          StatePending,
		createdAt:                      p.Now.UTC(),
		updatedAt:                      p.Now.UTC(),
	}, nil
}

// InternalOpeningParams carries the required fields of an internal wallet
// opening.
type InternalOpeningParams struct {
	ID       TransactionID
	WalletID WalletID
	PlayerID PlayerID
	Amount   money.Money
	Now      time.Time
}

// NewInternalOpening creates the internal OPENING transaction of a positive
// wallet opening. External metadata does not apply to this origin.
func NewInternalOpening(p InternalOpeningParams) (WagerTransaction, error) {
	if p.ID.IsZero() || p.WalletID.IsZero() || p.PlayerID.IsZero() {
		return WagerTransaction{}, fmt.Errorf("%w: opening transaction carries an empty identity", ErrInvalidInput)
	}
	if !p.Amount.IsInitialized() || !p.Amount.IsPositive() {
		return WagerTransaction{}, fmt.Errorf("%w: opening amount must be positive, got %s", ErrInvalidInput, p.Amount)
	}
	if p.Now.IsZero() {
		return WagerTransaction{}, fmt.Errorf("%w: opening timestamp is zero", ErrInvalidInput)
	}
	return WagerTransaction{
		id:        p.ID,
		origin:    OriginInternal,
		kind:      KindOpening,
		walletID:  p.WalletID,
		playerID:  p.PlayerID,
		amount:    p.Amount,
		state:     StatePending,
		createdAt: p.Now.UTC(),
		updatedAt: p.Now.UTC(),
	}, nil
}

// RehydrateTransactionParams carries every persisted field of a transaction.
type RehydrateTransactionParams struct {
	ID                     TransactionID
	Origin                 Origin
	Kind                   Kind
	ProviderID             ProviderID
	ExternalTransactionID  ExternalID
	IdempotencyKey         IdempotencyKey
	DigestVersion          string
	Digest                 string
	WalletID               WalletID
	PlayerID               PlayerID
	RoundID                ExternalID
	GameID                 ExternalID
	Amount                 money.Money
	ReferenceExternalID    ExternalID
	ReferenceTransactionID TransactionID
	State                  State
	FailureCode            FailureCode
	ObservedBalance        money.Money
	ReferenceDeadline      time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// RehydrateTransaction rebuilds a transaction from persisted state. It applies
// no transition, emits no event and increments no version.
func RehydrateTransaction(p RehydrateTransactionParams) (WagerTransaction, error) {
	if p.ID.IsZero() || p.WalletID.IsZero() || p.PlayerID.IsZero() {
		return WagerTransaction{}, fmt.Errorf("%w: transaction carries an empty identity", ErrInvalidInput)
	}
	if !p.Origin.Valid() || !p.Kind.Valid() || !p.State.Valid() {
		return WagerTransaction{}, fmt.Errorf("%w: transaction carries an invalid origin, kind or state", ErrInvalidInput)
	}
	if !p.Amount.IsInitialized() {
		return WagerTransaction{}, fmt.Errorf("%w: transaction amount has no currency", ErrUninitialized)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		return WagerTransaction{}, fmt.Errorf("%w: transaction timestamps are zero", ErrInvalidInput)
	}
	if p.UpdatedAt.Before(p.CreatedAt) {
		return WagerTransaction{}, fmt.Errorf("%w: transaction updatedAt precedes createdAt", ErrInvalidInput)
	}
	if p.FailureCode != "" && !p.FailureCode.Valid() {
		return WagerTransaction{}, fmt.Errorf("%w: failure code %q is not documented", ErrInvalidInput, p.FailureCode)
	}
	if p.Origin == OriginExternal {
		if p.ProviderID.IsZero() || p.ExternalTransactionID.IsZero() || p.IdempotencyKey.IsZero() || p.DigestVersion == "" || p.Digest == "" {
			return WagerTransaction{}, fmt.Errorf("%w: external transaction is missing provider metadata", ErrInvalidInput)
		}
		if !p.Kind.IsExternal() {
			return WagerTransaction{}, fmt.Errorf("%w: external transaction cannot have kind %q", ErrInvalidInput, p.Kind)
		}
		if p.RoundID.IsZero() || p.GameID.IsZero() {
			return WagerTransaction{}, fmt.Errorf("%w: external transaction is missing round or game identity", ErrInvalidInput)
		}
	} else {
		if p.Kind != KindOpening {
			return WagerTransaction{}, fmt.Errorf("%w: internal transaction must be OPENING", ErrInvalidInput)
		}
		if !p.ProviderID.IsZero() || !p.ExternalTransactionID.IsZero() || !p.IdempotencyKey.IsZero() || p.DigestVersion != "" || p.Digest != "" || !p.RoundID.IsZero() || !p.GameID.IsZero() || !p.ReferenceExternalID.IsZero() {
			return WagerTransaction{}, fmt.Errorf("%w: internal transaction carries external metadata", ErrInvalidInput)
		}
	}
	return WagerTransaction{
		id:                             p.ID,
		origin:                         p.Origin,
		kind:                           p.Kind,
		providerID:                     p.ProviderID,
		externalTransactionID:          p.ExternalTransactionID,
		idempotencyKey:                 p.IdempotencyKey,
		digestVersion:                  p.DigestVersion,
		digest:                         p.Digest,
		walletID:                       p.WalletID,
		playerID:                       p.PlayerID,
		roundID:                        p.RoundID,
		gameID:                         p.GameID,
		amount:                         p.Amount,
		referenceExternalTransactionID: p.ReferenceExternalID,
		referenceTransactionID:         p.ReferenceTransactionID,
		state:                          p.State,
		failureCode:                    p.FailureCode,
		observedBalance:                p.ObservedBalance,
		referenceDeadline:              p.ReferenceDeadline,
		createdAt:                      p.CreatedAt.UTC(),
		updatedAt:                      p.UpdatedAt.UTC(),
	}, nil
}

// MarkPendingReference moves a PENDING transaction to PENDING_REFERENCE and
// records the retry deadline.
func (t WagerTransaction) MarkPendingReference(deadline, now time.Time) (WagerTransaction, error) {
	if err := t.checkInitialized(); err != nil {
		return WagerTransaction{}, err
	}
	if t.state != StatePending {
		return WagerTransaction{}, &TransitionError{From: t.state, To: StatePendingReference}
	}
	if deadline.IsZero() || now.IsZero() {
		return WagerTransaction{}, fmt.Errorf("%w: reference deadline or timestamp is zero", ErrInvalidInput)
	}
	if !deadline.After(now) {
		return WagerTransaction{}, fmt.Errorf("%w: reference deadline must be after the transition instant", ErrInvalidInput)
	}
	t.state = StatePendingReference
	t.referenceDeadline = deadline.UTC()
	t.updatedAt = now.UTC()
	return t, nil
}

// MarkProcessed ends a PENDING or PENDING_REFERENCE transaction in PROCESSED
// with the observed wallet balance.
func (t WagerTransaction) MarkProcessed(resolvedReference TransactionID, observedBalance money.Money, now time.Time) (WagerTransaction, error) {
	if err := t.transition(StateProcessed, now); err != nil {
		return WagerTransaction{}, err
	}
	if err := t.validateObservation(resolvedReference, observedBalance); err != nil {
		return WagerTransaction{}, err
	}
	t.state = StateProcessed
	t.referenceTransactionID = resolvedReference
	t.observedBalance = observedBalance
	return t, nil
}

// MarkRejected ends a PENDING or PENDING_REFERENCE transaction in REJECTED
// with a stable failure code and the observed wallet balance.
func (t WagerTransaction) MarkRejected(resolvedReference TransactionID, code FailureCode, observedBalance money.Money, now time.Time) (WagerTransaction, error) {
	if err := t.transition(StateRejected, now); err != nil {
		return WagerTransaction{}, err
	}
	if !code.Valid() || code == FailurePermanentInfrastructure {
		return WagerTransaction{}, fmt.Errorf("%w: failure code %q is not a business rejection", ErrInvalidInput, code)
	}
	if err := t.validateObservation(resolvedReference, observedBalance); err != nil {
		return WagerTransaction{}, err
	}
	t.state = StateRejected
	t.referenceTransactionID = resolvedReference
	t.failureCode = code
	t.observedBalance = observedBalance
	return t, nil
}

// MarkFailed ends a PENDING or PENDING_REFERENCE transaction in FAILED with
// the permanent-infrastructure failure code.
func (t WagerTransaction) MarkFailed(observedBalance money.Money, now time.Time) (WagerTransaction, error) {
	if err := t.transition(StateFailed, now); err != nil {
		return WagerTransaction{}, err
	}
	if !observedBalance.IsInitialized() {
		return WagerTransaction{}, fmt.Errorf("%w: failed transaction needs the observed balance", ErrUninitialized)
	}
	t.state = StateFailed
	t.failureCode = FailurePermanentInfrastructure
	t.observedBalance = observedBalance
	return t, nil
}

// ID returns the transaction identity.
func (t WagerTransaction) ID() TransactionID { return t.id }

// Origin returns the transaction origin.
func (t WagerTransaction) Origin() Origin { return t.origin }

// Kind returns the operation kind.
func (t WagerTransaction) Kind() Kind { return t.kind }

// ProviderID returns the provider identity when applicable.
func (t WagerTransaction) ProviderID() ProviderID { return t.providerID }

// ExternalTransactionID returns the external identity when applicable.
func (t WagerTransaction) ExternalTransactionID() ExternalID { return t.externalTransactionID }

// IdempotencyKey returns the supplied idempotency key when applicable.
func (t WagerTransaction) IdempotencyKey() IdempotencyKey { return t.idempotencyKey }

// DigestVersion returns the canonical projection version.
func (t WagerTransaction) DigestVersion() string { return t.digestVersion }

// Digest returns the canonical projection digest.
func (t WagerTransaction) Digest() string { return t.digest }

// WalletID returns the wallet identity.
func (t WagerTransaction) WalletID() WalletID { return t.walletID }

// PlayerID returns the player identity.
func (t WagerTransaction) PlayerID() PlayerID { return t.playerID }

// RoundID returns the round identity when applicable.
func (t WagerTransaction) RoundID() ExternalID { return t.roundID }

// GameID returns the game identity when applicable.
func (t WagerTransaction) GameID() ExternalID { return t.gameID }

// Amount returns the operation amount.
func (t WagerTransaction) Amount() money.Money { return t.amount }

// ReferenceExternalTransactionID returns the supplied external reference.
func (t WagerTransaction) ReferenceExternalTransactionID() ExternalID {
	return t.referenceExternalTransactionID
}

// ReferenceTransactionID returns the resolved internal reference.
func (t WagerTransaction) ReferenceTransactionID() TransactionID { return t.referenceTransactionID }

// State returns the current state.
func (t WagerTransaction) State() State { return t.state }

// FailureCode returns the persisted failure code when applicable.
func (t WagerTransaction) FailureCode() FailureCode { return t.failureCode }

// ObservedBalance returns the balance observed at the terminal transition.
func (t WagerTransaction) ObservedBalance() money.Money { return t.observedBalance }

// ReferenceDeadline returns the retry deadline while waiting for a reference.
func (t WagerTransaction) ReferenceDeadline() time.Time { return t.referenceDeadline }

// CreatedAt returns the creation instant.
func (t WagerTransaction) CreatedAt() time.Time { return t.createdAt }

// UpdatedAt returns the last transition instant.
func (t WagerTransaction) UpdatedAt() time.Time { return t.updatedAt }

// IsTerminal reports whether the transaction reached a terminal state.
func (t WagerTransaction) IsTerminal() bool { return t.state.IsTerminal() }

func (t WagerTransaction) transition(next State, now time.Time) error {
	if err := t.checkInitialized(); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("%w: transition timestamp is zero", ErrInvalidInput)
	}
	switch t.state {
	case StatePending:
		// PENDING may reach every non-pending state.
	case StatePendingReference:
		// PENDING_REFERENCE may reach every terminal state.
	default:
		return &TransitionError{From: t.state, To: next}
	}
	t.updatedAt = now.UTC()
	return nil
}

func (t WagerTransaction) validateObservation(resolvedReference TransactionID, observedBalance money.Money) error {
	if !observedBalance.IsInitialized() {
		return fmt.Errorf("%w: terminal transaction needs the observed balance", ErrUninitialized)
	}
	if !observedBalance.IsNegative() {
		return nil
	}
	return fmt.Errorf("%w: observed balance %s is negative", ErrInvalidInput, observedBalance)
}

func (t WagerTransaction) checkInitialized() error {
	if t.id.IsZero() || !t.kind.Valid() || !t.state.Valid() || !t.amount.IsInitialized() {
		return fmt.Errorf("%w: transaction has no identity, kind, state or amount", ErrUninitialized)
	}
	return nil
}
