package money

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func mustMoney(t *testing.T, amount, currency string) Money {
	t.Helper()
	m, err := Parse(amount, currency)
	if err != nil {
		t.Fatalf("Parse(%q, %q): %v", amount, currency, err)
	}
	return m
}

// C14 - Parsing 25.00 BRL produces exactly 2500 minor units and serializes
// back to {"amount":"25.00","currency":"BRL"}.
func TestMoneyParseAndSerialize(t *testing.T) {
	parsed, err := Parse("25.00", "BRL")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.MinorUnits() != 2500 {
		t.Fatalf("MinorUnits() = %d, want 2500", parsed.MinorUnits())
	}
	encoded, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(encoded) != `{"amount":"25.00","currency":"BRL"}` {
		t.Fatalf("MarshalJSON = %s, want {\"amount\":\"25.00\",\"currency\":\"BRL\"}", encoded)
	}
	if parsed.Currency() != "BRL" {
		t.Fatalf("Currency() = %q, want BRL", parsed.Currency())
	}
}

// C15 - Invalid external amounts are rejected without rounding.
func TestMoneyInvalidInputs(t *testing.T) {
	tests := []struct {
		name   string
		amount string
	}{
		{name: "empty", amount: ""},
		{name: "negative", amount: "-1.00"},
		{name: "NaN", amount: "NaN"},
		{name: "Infinity", amount: "Infinity"},
		{name: "scientific notation", amount: "1e3"},
		{name: "missing decimals", amount: "1"},
		{name: "one decimal place", amount: "1.0"},
		{name: "three decimal places", amount: "1.000"},
		{name: "leading zeroes", amount: "01.00"},
		{name: "explicit sign", amount: "+1.00"},
		{name: "comma separator", amount: "1,00"},
		{name: "surrounding spaces", amount: " 1.00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.amount, "BRL")
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Parse(%q) error = %v, want ErrInvalidInput", tt.amount, err)
			}
			if got.IsInitialized() {
				t.Fatalf("Parse(%q) returned %v alongside the error", tt.amount, got)
			}
		})
	}
}

// C16 - An amount above 92233720368547758.07 returns a classified overflow error.
func TestMoneyOverflow(t *testing.T) {
	tests := []struct {
		name   string
		amount string
	}{
		{name: "one minor unit above maximum", amount: "92233720368547758.08"},
		{name: "far above int64", amount: "99999999999999999999.99"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.amount, "BRL")
			if !errors.Is(err, ErrOverflow) {
				t.Fatalf("Parse(%q) error = %v, want ErrOverflow", tt.amount, err)
			}
			if got.IsInitialized() {
				t.Fatalf("Parse(%q) returned %v alongside the error", tt.amount, got)
			}
		})
	}

	t.Run("maximum is accepted exactly", func(t *testing.T) {
		got, err := Parse("92233720368547758.07", "BRL")
		if err != nil {
			t.Fatalf("Parse(maximum): %v", err)
		}
		if got.MinorUnits() != math.MaxInt64 {
			t.Fatalf("MinorUnits() = %d, want MaxInt64", got.MinorUnits())
		}
	})
}

// C17 - Addition, subtraction, and negation beyond int64 return a classified
// overflow error and no wrapped value.
func TestMoneyArithmeticOverflow(t *testing.T) {
	tests := []struct {
		name  string
		left  int64
		op    func(Money, Money) (Money, error)
		right int64
	}{
		{name: "addition above int64", left: math.MaxInt64, op: Money.Add, right: 1},
		{name: "subtraction below int64", left: math.MinInt64, op: Money.Sub, right: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left, err := FromMinorUnits(tt.left, "BRL")
			if err != nil {
				t.Fatal(err)
			}
			right, err := FromMinorUnits(tt.right, "BRL")
			if err != nil {
				t.Fatal(err)
			}
			got, err := tt.op(left, right)
			if !errors.Is(err, ErrOverflow) {
				t.Fatalf("operation error = %v, want ErrOverflow", err)
			}
			if got.IsInitialized() {
				t.Fatalf("operation returned %v alongside the error", got)
			}
		})
	}

	t.Run("negation below int64", func(t *testing.T) {
		value, err := FromMinorUnits(math.MinInt64, "BRL")
		if err != nil {
			t.Fatal(err)
		}
		got, err := value.Neg()
		if !errors.Is(err, ErrOverflow) {
			t.Fatalf("Neg error = %v, want ErrOverflow", err)
		}
		if got.IsInitialized() {
			t.Fatalf("Neg returned %v alongside the error", got)
		}
	})
}

// C18 - Arithmetic and comparison combining different currencies return a
// classified currency-mismatch error.
func TestMoneyCurrencyMismatch(t *testing.T) {
	brl := mustMoney(t, "10.00", "BRL")
	usd := mustMoney(t, "10.00", "USD")

	t.Run("addition", func(t *testing.T) {
		if _, err := brl.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
			t.Fatalf("Add error = %v, want ErrCurrencyMismatch", err)
		}
	})

	t.Run("subtraction", func(t *testing.T) {
		if _, err := brl.Sub(usd); !errors.Is(err, ErrCurrencyMismatch) {
			t.Fatalf("Sub error = %v, want ErrCurrencyMismatch", err)
		}
	})

	t.Run("comparison", func(t *testing.T) {
		if _, err := brl.Cmp(usd); !errors.Is(err, ErrCurrencyMismatch) {
			t.Fatalf("Cmp error = %v, want ErrCurrencyMismatch", err)
		}
	})
}

// C19 - The Money value is immutable after construction.
func TestMoneyImmutability(t *testing.T) {
	original := mustMoney(t, "10.00", "BRL")
	if _, err := original.Add(mustMoney(t, "2.50", "BRL")); err != nil {
		t.Fatal(err)
	}
	if _, err := original.Neg(); err != nil {
		t.Fatal(err)
	}
	if original.String() != "10.00" {
		t.Fatalf("receiver mutated to %s, want 10.00", original)
	}
}

// C190 - Zero for BRL serializes as {"amount":"0.00","currency":"BRL"}.
func TestMoneyZeroSerialization(t *testing.T) {
	zero, err := Zero("BRL")
	if err != nil {
		t.Fatalf("Zero: %v", err)
	}
	encoded, err := json.Marshal(zero)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(encoded) != `{"amount":"0.00","currency":"BRL"}` {
		t.Fatalf("MarshalJSON = %s, want {\"amount\":\"0.00\",\"currency\":\"BRL\"}", encoded)
	}
}

// C191 - Adding 10.00 BRL and 2.50 BRL returns 12.50 BRL.
func TestMoneyAddition(t *testing.T) {
	got, err := mustMoney(t, "10.00", "BRL").Add(mustMoney(t, "2.50", "BRL"))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got.String() != "12.50" {
		t.Fatalf("Add = %s, want 12.50", got)
	}
}

// C192 - Subtracting 2.50 BRL from 10.00 BRL returns 7.50 BRL.
func TestMoneySubtraction(t *testing.T) {
	got, err := mustMoney(t, "10.00", "BRL").Sub(mustMoney(t, "2.50", "BRL"))
	if err != nil {
		t.Fatalf("Sub: %v", err)
	}
	if got.String() != "7.50" {
		t.Fatalf("Sub = %s, want 7.50", got)
	}
}

// C193 - Negating 2.50 BRL returns -2.50 BRL.
func TestMoneyNegation(t *testing.T) {
	got, err := mustMoney(t, "2.50", "BRL").Neg()
	if err != nil {
		t.Fatalf("Neg: %v", err)
	}
	if got.String() != "-2.50" {
		t.Fatalf("Neg = %s, want -2.50", got)
	}
}

// C194 - Comparing 2.50 BRL with 10.00 BRL reports the first as less than the second.
func TestMoneyComparison(t *testing.T) {
	cmp, err := mustMoney(t, "2.50", "BRL").Cmp(mustMoney(t, "10.00", "BRL"))
	if err != nil {
		t.Fatalf("Cmp: %v", err)
	}
	if cmp != -1 {
		t.Fatalf("Cmp = %d, want -1", cmp)
	}
}

func TestZero(t *testing.T) {
	t.Run("valid currency", func(t *testing.T) {
		got, err := Zero("BRL")
		if err != nil {
			t.Fatalf("Zero(BRL): %v", err)
		}
		if !got.IsInitialized() || !got.IsZero() {
			t.Fatalf("Zero(BRL) = %+v, want initialized zero", got)
		}
	})

	t.Run("invalid currency", func(t *testing.T) {
		if _, err := Zero("brl"); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Zero(brl) error = %v, want ErrInvalidInput", err)
		}
	})
}

func TestFromMinorUnits(t *testing.T) {
	tests := []struct {
		name       string
		minorUnits int64
		want       string
	}{
		{name: "positive", minorUnits: 2500, want: "25.00"},
		{name: "negative internal difference", minorUnits: -750, want: "-7.50"},
		{name: "one minor unit", minorUnits: 5, want: "0.05"},
		{name: "minimum int64", minorUnits: math.MinInt64, want: "-92233720368547758.08"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromMinorUnits(tt.minorUnits, "BRL")
			if err != nil {
				t.Fatalf("FromMinorUnits: %v", err)
			}
			if got.String() != tt.want {
				t.Fatalf("String() = %q, want %q", got.String(), tt.want)
			}
		})
	}

	t.Run("invalid currency", func(t *testing.T) {
		if _, err := FromMinorUnits(1, "BR"); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("FromMinorUnits error = %v, want ErrInvalidInput", err)
		}
	})
}

func TestMoneyComparisonHelpers(t *testing.T) {
	small := mustMoney(t, "2.50", "BRL")
	large := mustMoney(t, "10.00", "BRL")

	if equal, err := small.Equal(small); err != nil || !equal {
		t.Fatalf("Equal(same) = %v, %v, want true", equal, err)
	}
	if less, err := small.LessThan(large); err != nil || !less {
		t.Fatalf("LessThan = %v, %v, want true", less, err)
	}
	if greater, err := large.GreaterThan(small); err != nil || !greater {
		t.Fatalf("GreaterThan = %v, %v, want true", greater, err)
	}
}

func TestMoney_AddSubNegRoundTrip(t *testing.T) {
	a := mustMoney(t, "10.00", "BRL")
	b := mustMoney(t, "2.50", "BRL")

	sum, err := a.Add(b)
	if err != nil {
		t.Fatal(err)
	}
	back, err := sum.Sub(b)
	if err != nil {
		t.Fatal(err)
	}
	if back.String() != "10.00" {
		t.Fatalf("round trip = %s, want 10.00", back)
	}

	negated, err := b.Neg()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := negated.Neg()
	if err != nil {
		t.Fatal(err)
	}
	if restored.String() != b.String() {
		t.Fatalf("double negation = %s, want %s", restored, b)
	}
}

func TestMoney_UninitializedArithmetic(t *testing.T) {
	t.Run("addition", func(t *testing.T) {
		if _, err := (Money{}).Add(mustMoney(t, "2.50", "BRL")); !errors.Is(err, ErrUninitialized) {
			t.Fatalf("Add error = %v, want ErrUninitialized", err)
		}
	})

	t.Run("negation", func(t *testing.T) {
		if _, err := (Money{}).Neg(); !errors.Is(err, ErrUninitialized) {
			t.Fatalf("Neg error = %v, want ErrUninitialized", err)
		}
	})
}

func TestMoney_MarshalUninitialized(t *testing.T) {
	if _, err := json.Marshal(Money{}); !errors.Is(err, ErrUninitialized) {
		t.Fatalf("MarshalJSON error = %v, want ErrUninitialized", err)
	}
}

func TestMoney_UnmarshalJSON(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		var got Money
		if err := json.Unmarshal([]byte(`{"amount":"25.00","currency":"BRL"}`), &got); err != nil {
			t.Fatalf("UnmarshalJSON: %v", err)
		}
		if got.MinorUnits() != 2500 || got.Currency() != "BRL" {
			t.Fatalf("UnmarshalJSON = %+v, want 2500 BRL", got)
		}
	})

	t.Run("invalid amount", func(t *testing.T) {
		var got Money
		err := json.Unmarshal([]byte(`{"amount":"25","currency":"BRL"}`), &got)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("UnmarshalJSON error = %v, want ErrInvalidInput", err)
		}
	})
}
