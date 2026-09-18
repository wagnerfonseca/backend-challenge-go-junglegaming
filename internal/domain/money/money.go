// Package money provides an immutable monetary value object with exact
// fixed-scale arithmetic. Amounts are stored as int64 minor units (cents for
// two-decimal currencies) and never pass through floating point.
package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
)

// Sentinel errors. Every failure returned by this package wraps one of them,
// so callers classify with errors.Is.
var (
	ErrInvalidInput     = errors.New("money: invalid input")
	ErrOverflow         = errors.New("money: overflow")
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
	ErrUninitialized    = errors.New("money: uninitialized value")
)

// amountPattern is the canonical external format: no leading zeroes, exactly
// two decimal places, no sign, no scientific notation.
var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.[0-9]{2}$`)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// MaxMinorUnits is the largest representable amount in minor units.
const MaxMinorUnits int64 = math.MaxInt64

// MinMinorUnits is the smallest representable amount in minor units.
const MinMinorUnits int64 = math.MinInt64

// Money is an immutable amount of a single currency. Its zero value is
// invalid by design: it carries no currency and every operation rejects it.
type Money struct {
	minorUnits int64
	currency   string
}

// Parse builds a Money from a canonical non-negative decimal string and an
// ISO 4217 currency code. Invalid input is rejected without rounding.
func Parse(amount, currency string) (Money, error) {
	if !currencyPattern.MatchString(currency) {
		return Money{}, fmt.Errorf("%w: currency %q is not an ISO 4217 code", ErrInvalidInput, currency)
	}
	if !amountPattern.MatchString(amount) {
		return Money{}, fmt.Errorf("%w: amount %q is not a canonical two-decimal string", ErrInvalidInput, amount)
	}
	intPart := amount[:len(amount)-3]
	fraction := int64(amount[len(amount)-2]-'0')*10 + int64(amount[len(amount)-1]-'0')
	parsed, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: amount %q does not fit in int64 minor units", ErrOverflow, amount)
	}
	if parsed > (math.MaxInt64-fraction)/100 {
		return Money{}, fmt.Errorf("%w: amount %q exceeds %s", ErrOverflow, amount, MaxMinorUnitsString())
	}
	return Money{minorUnits: parsed*100 + fraction, currency: currency}, nil
}

// Zero returns the zero amount for a currency.
func Zero(currency string) (Money, error) {
	if !currencyPattern.MatchString(currency) {
		return Money{}, fmt.Errorf("%w: currency %q is not an ISO 4217 code", ErrInvalidInput, currency)
	}
	return Money{minorUnits: 0, currency: currency}, nil
}

// FromMinorUnits builds a Money from an exact amount in minor units, including
// negative values used by internal differences.
func FromMinorUnits(minorUnits int64, currency string) (Money, error) {
	if !currencyPattern.MatchString(currency) {
		return Money{}, fmt.Errorf("%w: currency %q is not an ISO 4217 code", ErrInvalidInput, currency)
	}
	return Money{minorUnits: minorUnits, currency: currency}, nil
}

// MinorUnits returns the exact amount in minor units.
func (m Money) MinorUnits() int64 { return m.minorUnits }

// Currency returns the ISO 4217 currency code.
func (m Money) Currency() string { return m.currency }

// IsInitialized reports whether the value was built by this package.
func (m Money) IsInitialized() bool { return m.currency != "" }

// IsZero reports whether the amount is exactly zero. It returns false for an
// uninitialized value; callers that need to reject uninitialized values must
// use an operation that validates first.
func (m Money) IsZero() bool { return m.minorUnits == 0 }

// IsNegative reports whether the amount is below zero.
func (m Money) IsNegative() bool { return m.minorUnits < 0 }

// IsPositive reports whether the amount is above zero.
func (m Money) IsPositive() bool { return m.minorUnits > 0 }

// Add returns m + other for identical currencies, rejecting overflow.
func (m Money) Add(other Money) (Money, error) {
	if err := m.checkCompatible(other); err != nil {
		return Money{}, err
	}
	sum := m.minorUnits + other.minorUnits
	if (other.minorUnits > 0 && sum < m.minorUnits) || (other.minorUnits < 0 && sum > m.minorUnits) {
		return Money{}, fmt.Errorf("%w: adding %s to %s exceeds the int64 minor-unit range", ErrOverflow, other, m)
	}
	return Money{minorUnits: sum, currency: m.currency}, nil
}

// Sub returns m - other for identical currencies, rejecting overflow.
func (m Money) Sub(other Money) (Money, error) {
	negated, err := other.Neg()
	if err != nil {
		return Money{}, err
	}
	return m.Add(negated)
}

// Neg returns the additive inverse of m, rejecting the one unrepresentable case.
func (m Money) Neg() (Money, error) {
	if err := m.checkInitialized(); err != nil {
		return Money{}, err
	}
	if m.minorUnits == math.MinInt64 {
		return Money{}, fmt.Errorf("%w: negating %s exceeds the int64 minor-unit range", ErrOverflow, m)
	}
	return Money{minorUnits: -m.minorUnits, currency: m.currency}, nil
}

// Cmp compares m with other for identical currencies. It returns -1, 0 or 1.
func (m Money) Cmp(other Money) (int, error) {
	if err := m.checkCompatible(other); err != nil {
		return 0, err
	}
	switch {
	case m.minorUnits < other.minorUnits:
		return -1, nil
	case m.minorUnits > other.minorUnits:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal reports whether two initialized amounts of the same currency are equal.
func (m Money) Equal(other Money) (bool, error) {
	cmp, err := m.Cmp(other)
	if err != nil {
		return false, err
	}
	return cmp == 0, nil
}

// LessThan reports whether m is smaller than other in the same currency.
func (m Money) LessThan(other Money) (bool, error) {
	cmp, err := m.Cmp(other)
	if err != nil {
		return false, err
	}
	return cmp < 0, nil
}

// GreaterThan reports whether m is larger than other in the same currency.
func (m Money) GreaterThan(other Money) (bool, error) {
	cmp, err := m.Cmp(other)
	if err != nil {
		return false, err
	}
	return cmp > 0, nil
}

// String renders the canonical external form, e.g. "25.00" or "-7.50".
func (m Money) String() string {
	if err := m.checkInitialized(); err != nil {
		return ""
	}
	units := uint64(m.minorUnits)
	sign := ""
	if m.minorUnits < 0 {
		sign = "-"
		units = -units
	}
	return sign + strconv.FormatUint(units/100, 10) + "." + fmt.Sprintf("%02d", units%100)
}

// MarshalJSON emits {"amount":"25.00","currency":"BRL"} with no float involved.
func (m Money) MarshalJSON() ([]byte, error) {
	if err := m.checkInitialized(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}{Amount: m.String(), Currency: m.currency})
}

// UnmarshalJSON parses the canonical external representation.
func (m *Money) UnmarshalJSON(data []byte) error {
	var wire struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}
	parsed, err := Parse(wire.Amount, wire.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// MaxMinorUnitsString renders the largest representable amount for messages.
func MaxMinorUnitsString() string {
	return Money{minorUnits: math.MaxInt64, currency: "XXX"}.String()
}

func (m Money) checkInitialized() error {
	if !m.IsInitialized() {
		return fmt.Errorf("%w: operation on a Money without currency", ErrUninitialized)
	}
	return nil
}

func (m Money) checkCompatible(other Money) error {
	if err := m.checkInitialized(); err != nil {
		return err
	}
	if err := other.checkInitialized(); err != nil {
		return err
	}
	if m.currency != other.currency {
		return fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	return nil
}
