package financial_test

import (
	"errors"
	"testing"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

// C41 - A WalletLedgerEntry requires balanceAfter = balanceBefore + money for
// CREDIT or balanceAfter = balanceBefore - money for DEBIT.
func TestLedgerArithmetic(t *testing.T) {
	walletID := financial.NewWalletID()
	transactionID := financial.NewTransactionID()

	tests := []struct {
		name          string
		direction     financial.Direction
		amount        string
		balanceBefore string
		balanceAfter  string
		wantErr       error
	}{
		{name: "credit matches", direction: financial.DirectionCredit, amount: "1000.00", balanceBefore: "0.00", balanceAfter: "1000.00"},
		{name: "debit matches", direction: financial.DirectionDebit, amount: "25.00", balanceBefore: "1000.00", balanceAfter: "975.00"},
		{name: "credit mismatch", direction: financial.DirectionCredit, amount: "1000.00", balanceBefore: "0.00", balanceAfter: "999.00", wantErr: financial.ErrInvalidInput},
		{name: "debit mismatch", direction: financial.DirectionDebit, amount: "25.00", balanceBefore: "1000.00", balanceAfter: "980.00", wantErr: financial.ErrInvalidInput},
		{name: "debit below zero", direction: financial.DirectionDebit, amount: "25.00", balanceBefore: "10.00", balanceAfter: "0.00", wantErr: financial.ErrInvalidInput},
		{name: "zero amount", direction: financial.DirectionCredit, amount: "0.00", balanceBefore: "0.00", balanceAfter: "0.00", wantErr: financial.ErrInvalidInput},
		{name: "invalid direction", direction: financial.Direction("TRANSFER"), amount: "1.00", balanceBefore: "0.00", balanceAfter: "1.00", wantErr: financial.ErrInvalidInput},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := financial.NewWalletLedgerEntry(
				financial.NewLedgerEntryID(),
				walletID,
				transactionID,
				tt.direction,
				mustMoney(t, tt.amount),
				mustMoney(t, tt.balanceBefore),
				mustMoney(t, tt.balanceAfter),
				testNow,
			)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("NewWalletLedgerEntry error = %v, want %v", err, tt.wantErr)
				}
				if !entry.ID().IsZero() {
					t.Fatal("rejected entry returned a usable value")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewWalletLedgerEntry: %v", err)
			}
			if entry.Direction() != tt.direction {
				t.Fatalf("direction = %s, want %s", entry.Direction(), tt.direction)
			}
		})
	}

	t.Run("mixed currencies", func(t *testing.T) {
		usd, err := money.Parse("1.00", "USD")
		if err != nil {
			t.Fatal(err)
		}
		_, err = financial.NewWalletLedgerEntry(
			financial.NewLedgerEntryID(),
			walletID,
			transactionID,
			financial.DirectionCredit,
			usd,
			mustMoney(t, "0.00"),
			mustMoney(t, "1.00"),
			testNow,
		)
		if !errors.Is(err, money.ErrCurrencyMismatch) {
			t.Fatalf("error = %v, want ErrCurrencyMismatch", err)
		}
	})
}

func TestLedgerRehydration(t *testing.T) {
	entry, err := financial.RehydrateWalletLedgerEntry(
		financial.NewLedgerEntryID(),
		financial.NewWalletID(),
		financial.NewTransactionID(),
		financial.DirectionCredit,
		mustMoney(t, "1000.00"),
		mustMoney(t, "0.00"),
		mustMoney(t, "1000.00"),
		testNow,
	)
	if err != nil {
		t.Fatalf("RehydrateWalletLedgerEntry: %v", err)
	}
	if entry.BalanceAfter().String() != "1000.00" || entry.Amount().String() != "1000.00" {
		t.Fatalf("rehydrated entry = %+v", entry)
	}
}
