package financial_test

import (
	"testing"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/domain/money"
)

var testNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func mustMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	parsed, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse(%q): %v", amount, err)
	}
	return parsed
}

func mustWallet(t *testing.T, balance string) financial.Wallet {
	t.Helper()
	wallet, err := financial.NewWallet(financial.NewWalletID(), financial.NewPlayerID(), "BRL", mustMoney(t, balance), testNow)
	if err != nil {
		t.Fatalf("NewWallet(%q): %v", balance, err)
	}
	return wallet
}

type transactionOption func(*financial.NewExternalTransactionParams)

func withKind(kind financial.Kind) transactionOption {
	return func(p *financial.NewExternalTransactionParams) { p.Kind = kind }
}

func withAmount(amount money.Money) transactionOption {
	return func(p *financial.NewExternalTransactionParams) { p.Amount = amount }
}

func withReference(reference financial.ExternalID) transactionOption {
	return func(p *financial.NewExternalTransactionParams) { p.ReferenceExternalID = reference }
}

func mustTransaction(t *testing.T, opts ...transactionOption) financial.WagerTransaction {
	t.Helper()
	params := financial.NewExternalTransactionParams{
		ID:                    financial.NewTransactionID(),
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		DigestVersion:         "sha256-jcs-v1",
		Digest:                "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		WalletID:              financial.NewWalletID(),
		PlayerID:              financial.NewPlayerID(),
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  financial.KindBet,
		Amount:                mustMoney(t, "25.00"),
		Now:                   testNow,
	}
	for _, opt := range opts {
		opt(&params)
	}
	transaction, err := financial.NewExternalTransaction(params)
	if err != nil {
		t.Fatalf("NewExternalTransaction: %v", err)
	}
	return transaction
}
