package financial

import (
	"fmt"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/domain/money"
)

// Direction is the sign of a ledger movement.
type Direction string

// Ledger movement directions.
const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

// Valid reports whether the direction is one of the documented values.
func (d Direction) Valid() bool {
	return d == DirectionDebit || d == DirectionCredit
}

// WalletLedgerEntry is an immutable financial posting. Its constructor proves
// the arithmetic invariant balanceAfter = balanceBefore ± amount.
type WalletLedgerEntry struct {
	id            LedgerEntryID
	walletID      WalletID
	transactionID TransactionID
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

// NewWalletLedgerEntry creates a posting for a wallet movement.
func NewWalletLedgerEntry(
	id LedgerEntryID,
	walletID WalletID,
	transactionID TransactionID,
	direction Direction,
	amount, balanceBefore, balanceAfter money.Money,
	now time.Time,
) (WalletLedgerEntry, error) {
	entry := WalletLedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     now.UTC(),
	}
	if err := entry.validate(); err != nil {
		return WalletLedgerEntry{}, err
	}
	return entry, nil
}

// RehydrateWalletLedgerEntry rebuilds a posting from persisted state. It
// applies no movement and emits nothing.
func RehydrateWalletLedgerEntry(
	id LedgerEntryID,
	walletID WalletID,
	transactionID TransactionID,
	direction Direction,
	amount, balanceBefore, balanceAfter money.Money,
	createdAt time.Time,
) (WalletLedgerEntry, error) {
	entry := WalletLedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     createdAt.UTC(),
	}
	if err := entry.validate(); err != nil {
		return WalletLedgerEntry{}, err
	}
	return entry, nil
}

// ID returns the entry identity.
func (e WalletLedgerEntry) ID() LedgerEntryID { return e.id }

// WalletID returns the wallet identity.
func (e WalletLedgerEntry) WalletID() WalletID { return e.walletID }

// TransactionID returns the originating transaction identity.
func (e WalletLedgerEntry) TransactionID() TransactionID { return e.transactionID }

// Direction returns the movement direction.
func (e WalletLedgerEntry) Direction() Direction { return e.direction }

// Amount returns the movement amount.
func (e WalletLedgerEntry) Amount() money.Money { return e.amount }

// BalanceBefore returns the balance before the movement.
func (e WalletLedgerEntry) BalanceBefore() money.Money { return e.balanceBefore }

// BalanceAfter returns the balance after the movement.
func (e WalletLedgerEntry) BalanceAfter() money.Money { return e.balanceAfter }

// CreatedAt returns the creation instant.
func (e WalletLedgerEntry) CreatedAt() time.Time { return e.createdAt }

func (e WalletLedgerEntry) validate() error {
	if e.id.IsZero() {
		return fmt.Errorf("%w: ledger entry id is empty", ErrInvalidInput)
	}
	if e.walletID.IsZero() {
		return fmt.Errorf("%w: ledger entry wallet id is empty", ErrInvalidInput)
	}
	if e.transactionID.IsZero() {
		return fmt.Errorf("%w: ledger entry transaction id is empty", ErrInvalidInput)
	}
	if !e.direction.Valid() {
		return fmt.Errorf("%w: ledger direction %q is not DEBIT or CREDIT", ErrInvalidInput, e.direction)
	}
	if e.createdAt.IsZero() {
		return fmt.Errorf("%w: ledger entry timestamp is zero", ErrInvalidInput)
	}
	if !e.amount.IsInitialized() || !e.balanceBefore.IsInitialized() || !e.balanceAfter.IsInitialized() {
		return fmt.Errorf("%w: ledger entry carries an uninitialized money value", ErrUninitialized)
	}
	if !e.amount.IsPositive() {
		return fmt.Errorf("%w: ledger entry amount must be positive, got %s", ErrInvalidInput, e.amount)
	}
	if e.balanceBefore.Currency() != e.amount.Currency() || e.balanceAfter.Currency() != e.amount.Currency() {
		return fmt.Errorf("%w: ledger entry mixes currencies", money.ErrCurrencyMismatch)
	}
	if e.balanceBefore.IsNegative() || e.balanceAfter.IsNegative() {
		return fmt.Errorf("%w: ledger entry balance is negative", ErrInvalidInput)
	}

	expected := e.balanceAfter
	var err error
	switch e.direction {
	case DirectionCredit:
		expected, err = e.balanceBefore.Add(e.amount)
	case DirectionDebit:
		expected, err = e.balanceBefore.Sub(e.amount)
	}
	if err != nil {
		return err
	}
	equal, err := expected.Equal(e.balanceAfter)
	if err != nil {
		return err
	}
	if !equal {
		return fmt.Errorf("%w: balanceAfter %s does not match balanceBefore %s %s %s", ErrInvalidInput, e.balanceAfter, e.balanceBefore, e.direction, e.amount)
	}
	return nil
}
