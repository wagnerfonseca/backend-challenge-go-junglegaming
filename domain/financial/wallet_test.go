package financial_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/domain/money"
)

// C21 - The Wallet never exposes a balance below 0.00.
func TestWalletNegativeBalance(t *testing.T) {
	t.Run("negative initial balance is rejected", func(t *testing.T) {
		negative, err := money.FromMinorUnits(-100, "BRL")
		if err != nil {
			t.Fatal(err)
		}
		_, err = financial.NewWallet(financial.NewWalletID(), financial.NewPlayerID(), "BRL", negative, testNow)
		if !errors.Is(err, financial.ErrInvalidInput) {
			t.Fatalf("NewWallet error = %v, want ErrInvalidInput", err)
		}
	})

	t.Run("debit above balance is rejected", func(t *testing.T) {
		wallet := mustWallet(t, "100.00")
		updated, err := wallet.Debit(mustMoney(t, "100.01"), testNow.Add(time.Minute))
		if !errors.Is(err, financial.ErrInsufficientFunds) {
			t.Fatalf("Debit error = %v, want ErrInsufficientFunds", err)
		}
		if updated.Balance().IsInitialized() || updated.Version() != 0 {
			t.Fatalf("rejected debit returned a modified wallet: %+v", updated)
		}
		if wallet.Balance().String() != "100.00" {
			t.Fatalf("original wallet mutated to %s", wallet.Balance())
		}
	})

	t.Run("debit of the exact balance reaches zero", func(t *testing.T) {
		wallet := mustWallet(t, "100.00")
		updated, err := wallet.Debit(mustMoney(t, "100.00"), testNow.Add(time.Minute))
		if err != nil {
			t.Fatalf("Debit: %v", err)
		}
		if !updated.Balance().IsZero() || updated.Balance().IsNegative() {
			t.Fatalf("balance = %s, want 0.00", updated.Balance())
		}
	})
}

func TestWalletCredit(t *testing.T) {
	wallet := mustWallet(t, "100.00")
	updated, err := wallet.Credit(mustMoney(t, "25.00"), testNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if updated.Balance().String() != "125.00" {
		t.Fatalf("balance = %s, want 125.00", updated.Balance())
	}
	if updated.Version() != wallet.Version()+1 {
		t.Fatalf("version = %d, want %d", updated.Version(), wallet.Version()+1)
	}
}

func TestWalletInitialVersion(t *testing.T) {
	wallet := mustWallet(t, "1000.00")
	if wallet.Version() != 1 {
		t.Fatalf("version = %d, want 1", wallet.Version())
	}
	zero := mustWallet(t, "0.00")
	if zero.Version() != 1 {
		t.Fatalf("zero-balance version = %d, want 1", zero.Version())
	}
}

func TestWalletCurrencyMismatch(t *testing.T) {
	wallet := mustWallet(t, "100.00")
	usd, err := money.Parse("10.00", "USD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wallet.Debit(usd, testNow.Add(time.Minute)); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("Debit error = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := wallet.Credit(usd, testNow.Add(time.Minute)); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("Credit error = %v, want ErrCurrencyMismatch", err)
	}
}

func TestWalletOverflowOnCredit(t *testing.T) {
	wallet := mustWallet(t, "0.00")
	huge, err := money.FromMinorUnits(math.MaxInt64-10, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := wallet.Credit(huge, testNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("first credit: %v", err)
	}
	if _, err := updated.Credit(mustMoney(t, "0.11"), testNow.Add(2*time.Minute)); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("second credit error = %v, want ErrOverflow", err)
	}
}
