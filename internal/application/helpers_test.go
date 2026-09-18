package application_test

import (
	"testing"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	value, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatalf("parsing money %s %s: %v", amount, currency, err)
	}
	return value
}

func moneyZeroValue() money.Money { return money.Money{} }
