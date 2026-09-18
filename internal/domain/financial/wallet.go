package financial

import (
	"fmt"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

// Wallet is the root of the financial aggregate. It carries identity, player,
// currency, balance, version and timestamps, and keeps every balance change
// under its own control.
type Wallet struct {
	id        WalletID
	playerID  PlayerID
	currency  string
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// NewWallet creates an initialized wallet. A positive initial balance is
// allowed; a negative one is rejected.
func NewWallet(id WalletID, playerID PlayerID, currency string, initialBalance money.Money, now time.Time) (Wallet, error) {
	if id.IsZero() {
		return Wallet{}, fmt.Errorf("%w: wallet id is empty", ErrInvalidInput)
	}
	if playerID.IsZero() {
		return Wallet{}, fmt.Errorf("%w: player id is empty", ErrInvalidInput)
	}
	if now.IsZero() {
		return Wallet{}, fmt.Errorf("%w: creation timestamp is zero", ErrInvalidInput)
	}
	if err := validateMovement(currency, initialBalance); err != nil {
		return Wallet{}, err
	}
	if initialBalance.IsNegative() {
		return Wallet{}, fmt.Errorf("%w: initial balance %s is negative", ErrInvalidInput, initialBalance)
	}
	return Wallet{
		id:        id,
		playerID:  playerID,
		currency:  currency,
		balance:   initialBalance,
		version:   1,
		createdAt: now.UTC(),
		updatedAt: now.UTC(),
	}, nil
}

// RehydrateWallet rebuilds a wallet from persisted state. It applies no
// movement, emits no event and increments no version.
func RehydrateWallet(id WalletID, playerID PlayerID, currency string, balance money.Money, version int64, createdAt, updatedAt time.Time) (Wallet, error) {
	if id.IsZero() {
		return Wallet{}, fmt.Errorf("%w: wallet id is empty", ErrInvalidInput)
	}
	if playerID.IsZero() {
		return Wallet{}, fmt.Errorf("%w: player id is empty", ErrInvalidInput)
	}
	if version < 1 {
		return Wallet{}, fmt.Errorf("%w: wallet version %d is below 1", ErrInvalidInput, version)
	}
	if createdAt.IsZero() || updatedAt.IsZero() {
		return Wallet{}, fmt.Errorf("%w: wallet timestamps are zero", ErrInvalidInput)
	}
	if updatedAt.Before(createdAt) {
		return Wallet{}, fmt.Errorf("%w: wallet updatedAt precedes createdAt", ErrInvalidInput)
	}
	if err := validateMovement(currency, balance); err != nil {
		return Wallet{}, err
	}
	if balance.IsNegative() {
		return Wallet{}, fmt.Errorf("%w: wallet balance %s is negative", ErrInvalidInput, balance)
	}
	return Wallet{
		id:        id,
		playerID:  playerID,
		currency:  currency,
		balance:   balance,
		version:   version,
		createdAt: createdAt.UTC(),
		updatedAt: updatedAt.UTC(),
	}, nil
}

// Credit returns a wallet with the amount added and the version incremented by
// exactly one.
func (w Wallet) Credit(amount money.Money, now time.Time) (Wallet, error) {
	if err := w.checkInitialized(); err != nil {
		return Wallet{}, err
	}
	if now.IsZero() {
		return Wallet{}, fmt.Errorf("%w: operation timestamp is zero", ErrInvalidInput)
	}
	if err := validateMovement(w.currency, amount); err != nil {
		return Wallet{}, err
	}
	if !amount.IsPositive() {
		return Wallet{}, fmt.Errorf("%w: credit amount must be positive, got %s", ErrInvalidInput, amount)
	}
	balance, err := w.balance.Add(amount)
	if err != nil {
		return Wallet{}, err
	}
	return w.withBalance(balance, now), nil
}

// Debit returns a wallet with the amount subtracted and the version
// incremented by exactly one. It rejects any debit that would go below zero.
func (w Wallet) Debit(amount money.Money, now time.Time) (Wallet, error) {
	if err := w.checkInitialized(); err != nil {
		return Wallet{}, err
	}
	if now.IsZero() {
		return Wallet{}, fmt.Errorf("%w: operation timestamp is zero", ErrInvalidInput)
	}
	if err := validateMovement(w.currency, amount); err != nil {
		return Wallet{}, err
	}
	if !amount.IsPositive() {
		return Wallet{}, fmt.Errorf("%w: debit amount must be positive, got %s", ErrInvalidInput, amount)
	}
	insufficient, err := w.balance.LessThan(amount)
	if err != nil {
		return Wallet{}, err
	}
	if insufficient {
		return Wallet{}, fmt.Errorf("%w: balance %s is below debit %s", ErrInsufficientFunds, w.balance, amount)
	}
	balance, err := w.balance.Sub(amount)
	if err != nil {
		return Wallet{}, err
	}
	return w.withBalance(balance, now), nil
}

// ID returns the wallet identity.
func (w Wallet) ID() WalletID { return w.id }

// PlayerID returns the player identity.
func (w Wallet) PlayerID() PlayerID { return w.playerID }

// Currency returns the wallet currency.
func (w Wallet) Currency() string { return w.currency }

// Balance returns the current balance.
func (w Wallet) Balance() money.Money { return w.balance }

// Version returns the aggregate version.
func (w Wallet) Version() int64 { return w.version }

// CreatedAt returns the creation instant.
func (w Wallet) CreatedAt() time.Time { return w.createdAt }

// UpdatedAt returns the last update instant.
func (w Wallet) UpdatedAt() time.Time { return w.updatedAt }

func (w Wallet) withBalance(balance money.Money, now time.Time) Wallet {
	return Wallet{
		id:        w.id,
		playerID:  w.playerID,
		currency:  w.currency,
		balance:   balance,
		version:   w.version + 1,
		createdAt: w.createdAt,
		updatedAt: now.UTC(),
	}
}

func (w Wallet) checkInitialized() error {
	if w.id.IsZero() || w.currency == "" || !w.balance.IsInitialized() {
		return fmt.Errorf("%w: wallet has no identity, currency or balance", ErrUninitialized)
	}
	return nil
}

func validateMovement(currency string, amount money.Money) error {
	if !amount.IsInitialized() {
		return fmt.Errorf("%w: money value has no currency", ErrUninitialized)
	}
	if amount.Currency() != currency {
		return fmt.Errorf("%w: movement in %s on a %s wallet", money.ErrCurrencyMismatch, amount.Currency(), currency)
	}
	return nil
}
