package financial_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/domain/money"
)

// C22 - Entity creation rejects an empty identity, invalid initial state, zero
// timestamp, or required zero Money before returning it.
func TestEntityInvalidConstruction(t *testing.T) {
	validWalletID := financial.NewWalletID()
	validPlayerID := financial.NewPlayerID()
	validTransactionID := financial.NewTransactionID()

	t.Run("wallet", func(t *testing.T) {
		tests := []struct {
			name    string
			id      financial.WalletID
			player  financial.PlayerID
			balance money.Money
			now     time.Time
			wantErr error
		}{
			{name: "empty identity", player: validPlayerID, balance: mustMoney(t, "0.00"), now: testNow, wantErr: financial.ErrInvalidInput},
			{name: "empty player", id: validWalletID, balance: mustMoney(t, "0.00"), now: testNow, wantErr: financial.ErrInvalidInput},
			{name: "zero timestamp", id: validWalletID, player: validPlayerID, balance: mustMoney(t, "0.00"), wantErr: financial.ErrInvalidInput},
			{name: "required zero money", id: validWalletID, player: validPlayerID, now: testNow, wantErr: financial.ErrUninitialized},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				wallet, err := financial.NewWallet(tt.id, tt.player, "BRL", tt.balance, tt.now)
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("NewWallet error = %v, want %v", err, tt.wantErr)
				}
				if !wallet.ID().IsZero() {
					t.Fatal("rejected construction returned a wallet")
				}
			})
		}
	})

	t.Run("external transaction", func(t *testing.T) {
		base := financial.NewExternalTransactionParams{
			ID:                    validTransactionID,
			ProviderID:            "provider-a",
			ExternalTransactionID: "transaction-123",
			IdempotencyKey:        "key-1",
			DigestVersion:         "sha256-jcs-v1",
			Digest:                "abc",
			WalletID:              validWalletID,
			PlayerID:              validPlayerID,
			RoundID:               "round-1",
			GameID:                "game-1",
			Kind:                  financial.KindBet,
			Amount:                mustMoney(t, "25.00"),
			Now:                   testNow,
		}
		mutate := func(change func(p *financial.NewExternalTransactionParams)) financial.NewExternalTransactionParams {
			p := base
			change(&p)
			return p
		}
		tests := []struct {
			name    string
			params  financial.NewExternalTransactionParams
			wantErr error
		}{
			{name: "empty identity", params: mutate(func(p *financial.NewExternalTransactionParams) { p.ID = financial.TransactionID{} }), wantErr: financial.ErrInvalidInput},
			{name: "empty wallet", params: mutate(func(p *financial.NewExternalTransactionParams) { p.WalletID = financial.WalletID{} }), wantErr: financial.ErrInvalidInput},
			{name: "empty player", params: mutate(func(p *financial.NewExternalTransactionParams) { p.PlayerID = financial.PlayerID{} }), wantErr: financial.ErrInvalidInput},
			{name: "empty provider", params: mutate(func(p *financial.NewExternalTransactionParams) { p.ProviderID = "" }), wantErr: financial.ErrInvalidInput},
			{name: "empty external identity", params: mutate(func(p *financial.NewExternalTransactionParams) { p.ExternalTransactionID = "" }), wantErr: financial.ErrInvalidInput},
			{name: "empty round", params: mutate(func(p *financial.NewExternalTransactionParams) { p.RoundID = "" }), wantErr: financial.ErrInvalidInput},
			{name: "empty game", params: mutate(func(p *financial.NewExternalTransactionParams) { p.GameID = "" }), wantErr: financial.ErrInvalidInput},
			{name: "missing idempotency key", params: mutate(func(p *financial.NewExternalTransactionParams) { p.IdempotencyKey = "" }), wantErr: financial.ErrInvalidInput},
			{name: "missing digest", params: mutate(func(p *financial.NewExternalTransactionParams) { p.Digest = "" }), wantErr: financial.ErrInvalidInput},
			{name: "internal kind", params: mutate(func(p *financial.NewExternalTransactionParams) { p.Kind = financial.KindOpening }), wantErr: financial.ErrInvalidInput},
			{name: "unknown kind", params: mutate(func(p *financial.NewExternalTransactionParams) { p.Kind = "TRANSFER" }), wantErr: financial.ErrInvalidInput},
			{name: "zero timestamp", params: mutate(func(p *financial.NewExternalTransactionParams) { p.Now = time.Time{} }), wantErr: financial.ErrInvalidInput},
			{name: "required zero money", params: mutate(func(p *financial.NewExternalTransactionParams) { p.Amount = money.Money{} }), wantErr: financial.ErrUninitialized},
			{name: "zero BET amount", params: mutate(func(p *financial.NewExternalTransactionParams) { p.Amount = mustMoney(t, "0.00") }), wantErr: financial.ErrInvalidInput},
			{name: "positive LOSS amount", params: mutate(func(p *financial.NewExternalTransactionParams) { p.Kind = financial.KindLoss }), wantErr: financial.ErrInvalidInput},
			{name: "REFUND without reference", params: mutate(func(p *financial.NewExternalTransactionParams) { p.Kind = financial.KindRefund }), wantErr: financial.ErrInvalidInput},
			{name: "ROLLBACK without reference", params: mutate(func(p *financial.NewExternalTransactionParams) { p.Kind = financial.KindRollback }), wantErr: financial.ErrInvalidInput},
			{name: "BET with reference", params: mutate(func(p *financial.NewExternalTransactionParams) { p.ReferenceExternalID = "transaction-000" }), wantErr: financial.ErrInvalidInput},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				transaction, err := financial.NewExternalTransaction(tt.params)
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("NewExternalTransaction error = %v, want %v", err, tt.wantErr)
				}
				if !transaction.ID().IsZero() {
					t.Fatal("rejected construction returned a transaction")
				}
			})
		}

		t.Run("LOSS with zero amount is accepted", func(t *testing.T) {
			transaction, err := financial.NewExternalTransaction(mutate(func(p *financial.NewExternalTransactionParams) {
				p.Kind = financial.KindLoss
				p.Amount = mustMoney(t, "0.00")
			}))
			if err != nil {
				t.Fatalf("NewExternalTransaction: %v", err)
			}
			if transaction.Kind() != financial.KindLoss || transaction.State() != financial.StatePending {
				t.Fatalf("transaction = %+v", transaction)
			}
		})
	})

	t.Run("internal opening", func(t *testing.T) {
		params := financial.InternalOpeningParams{
			ID:       validTransactionID,
			WalletID: validWalletID,
			PlayerID: validPlayerID,
			Amount:   mustMoney(t, "1000.00"),
			Now:      testNow,
		}
		tests := []struct {
			name    string
			params  financial.InternalOpeningParams
			wantErr error
		}{
			{name: "empty identity", params: financial.InternalOpeningParams{WalletID: validWalletID, PlayerID: validPlayerID, Amount: mustMoney(t, "1.00"), Now: testNow}, wantErr: financial.ErrInvalidInput},
			{name: "empty wallet", params: financial.InternalOpeningParams{ID: validTransactionID, PlayerID: validPlayerID, Amount: mustMoney(t, "1.00"), Now: testNow}, wantErr: financial.ErrInvalidInput},
			{name: "zero amount", params: financial.InternalOpeningParams{ID: validTransactionID, WalletID: validWalletID, PlayerID: validPlayerID, Amount: mustMoney(t, "0.00"), Now: testNow}, wantErr: financial.ErrInvalidInput},
			{name: "required zero money", params: financial.InternalOpeningParams{ID: validTransactionID, WalletID: validWalletID, PlayerID: validPlayerID, Now: testNow}, wantErr: financial.ErrInvalidInput},
			{name: "zero timestamp", params: financial.InternalOpeningParams{ID: validTransactionID, WalletID: validWalletID, PlayerID: validPlayerID, Amount: mustMoney(t, "1.00")}, wantErr: financial.ErrInvalidInput},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if _, err := financial.NewInternalOpening(tt.params); !errors.Is(err, tt.wantErr) {
					t.Fatalf("NewInternalOpening error = %v, want %v", err, tt.wantErr)
				}
			})
		}

		t.Run("valid opening has no external metadata", func(t *testing.T) {
			transaction, err := financial.NewInternalOpening(params)
			if err != nil {
				t.Fatalf("NewInternalOpening: %v", err)
			}
			if transaction.Origin() != financial.OriginInternal || transaction.Kind() != financial.KindOpening {
				t.Fatalf("opening = %+v", transaction)
			}
			if !transaction.ProviderID().IsZero() || !transaction.ExternalTransactionID().IsZero() || !transaction.IdempotencyKey().IsZero() {
				t.Fatal("internal opening carries external metadata")
			}
		})
	})

	t.Run("ledger entry", func(t *testing.T) {
		tests := []struct {
			name    string
			id      financial.LedgerEntryID
			wallet  financial.WalletID
			now     time.Time
			wantErr error
		}{
			{name: "empty identity", wallet: validWalletID, now: testNow, wantErr: financial.ErrInvalidInput},
			{name: "empty wallet", id: financial.NewLedgerEntryID(), now: testNow, wantErr: financial.ErrInvalidInput},
			{name: "zero timestamp", id: financial.NewLedgerEntryID(), wallet: validWalletID, wantErr: financial.ErrInvalidInput},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := financial.NewWalletLedgerEntry(tt.id, tt.wallet, validTransactionID, financial.DirectionCredit, mustMoney(t, "1.00"), mustMoney(t, "0.00"), mustMoney(t, "1.00"), tt.now)
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("NewWalletLedgerEntry error = %v, want %v", err, tt.wantErr)
				}
			})
		}
	})

	t.Run("reversal claim", func(t *testing.T) {
		tests := []struct {
			name      string
			id        financial.ReversalClaimID
			reference financial.TransactionID
			reversal  financial.TransactionID
			now       time.Time
			wantErr   error
		}{
			{name: "empty identity", reference: validTransactionID, reversal: financial.NewTransactionID(), now: testNow, wantErr: financial.ErrInvalidInput},
			{name: "empty reference", id: financial.NewReversalClaimID(), reversal: financial.NewTransactionID(), now: testNow, wantErr: financial.ErrInvalidInput},
			{name: "self compensation", id: financial.NewReversalClaimID(), reference: validTransactionID, reversal: validTransactionID, now: testNow, wantErr: financial.ErrInvalidInput},
			{name: "zero timestamp", id: financial.NewReversalClaimID(), reference: validTransactionID, reversal: financial.NewTransactionID(), wantErr: financial.ErrInvalidInput},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if _, err := financial.NewReversalClaim(tt.id, tt.reference, tt.reversal, tt.now); !errors.Is(err, tt.wantErr) {
					t.Fatalf("NewReversalClaim error = %v, want %v", err, tt.wantErr)
				}
			})
		}
	})
}

// C24 - Entity rehydration emits no event, applies no movement, and increments
// no version.
func TestRehydrationNoEffects(t *testing.T) {
	t.Run("wallet", func(t *testing.T) {
		id := financial.NewWalletID()
		player := financial.NewPlayerID()
		created := testNow.Add(-time.Hour)
		updated := testNow.Add(-time.Minute)
		wallet, err := financial.RehydrateWallet(id, player, "BRL", mustMoney(t, "975.00"), 3, created, updated)
		if err != nil {
			t.Fatalf("RehydrateWallet: %v", err)
		}
		if wallet.Balance().String() != "975.00" || wallet.Version() != 3 || !wallet.CreatedAt().Equal(created) || !wallet.UpdatedAt().Equal(updated) {
			t.Fatalf("rehydration changed state: %+v", wallet)
		}
	})

	t.Run("transaction", func(t *testing.T) {
		original := mustTransaction(t)
		processed, err := original.MarkProcessed(financial.TransactionID{}, mustMoney(t, "975.00"), testNow.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		rehydrated, err := financial.RehydrateTransaction(financial.RehydrateTransactionParams{
			ID:                    processed.ID(),
			Origin:                processed.Origin(),
			Kind:                  processed.Kind(),
			ProviderID:            processed.ProviderID(),
			ExternalTransactionID: processed.ExternalTransactionID(),
			IdempotencyKey:        processed.IdempotencyKey(),
			DigestVersion:         processed.DigestVersion(),
			Digest:                processed.Digest(),
			WalletID:              processed.WalletID(),
			PlayerID:              processed.PlayerID(),
			RoundID:               processed.RoundID(),
			GameID:                processed.GameID(),
			Amount:                processed.Amount(),
			State:                 processed.State(),
			ObservedBalance:       processed.ObservedBalance(),
			CreatedAt:             processed.CreatedAt(),
			UpdatedAt:             processed.UpdatedAt(),
		})
		if err != nil {
			t.Fatalf("RehydrateTransaction: %v", err)
		}
		if rehydrated.State() != processed.State() || !rehydrated.UpdatedAt().Equal(processed.UpdatedAt()) {
			t.Fatalf("rehydration changed the transaction: %+v", rehydrated)
		}
	})

	t.Run("ledger entry", func(t *testing.T) {
		created := testNow.Add(-time.Minute)
		entry, err := financial.RehydrateWalletLedgerEntry(financial.NewLedgerEntryID(), financial.NewWalletID(), financial.NewTransactionID(), financial.DirectionDebit, mustMoney(t, "25.00"), mustMoney(t, "1000.00"), mustMoney(t, "975.00"), created)
		if err != nil {
			t.Fatalf("RehydrateWalletLedgerEntry: %v", err)
		}
		if !entry.CreatedAt().Equal(created) || entry.BalanceAfter().String() != "975.00" {
			t.Fatalf("rehydration changed the entry: %+v", entry)
		}
	})
}

// C25 - Uninitialized Money, Wallet, WagerTransaction, or WalletLedgerEntry
// used by a public domain operation is rejected.
func TestUninitializedRejection(t *testing.T) {
	t.Run("money", func(t *testing.T) {
		if _, err := (money.Money{}).Add(mustMoney(t, "1.00")); !errors.Is(err, money.ErrUninitialized) {
			t.Fatalf("Add error = %v, want ErrUninitialized", err)
		}
		if _, err := (money.Money{}).Neg(); !errors.Is(err, money.ErrUninitialized) {
			t.Fatalf("Neg error = %v, want ErrUninitialized", err)
		}
		if _, err := json.Marshal(money.Money{}); !errors.Is(err, money.ErrUninitialized) {
			t.Fatalf("MarshalJSON error = %v, want ErrUninitialized", err)
		}
	})

	t.Run("wallet", func(t *testing.T) {
		if _, err := (financial.Wallet{}).Debit(mustMoney(t, "1.00"), testNow); !errors.Is(err, financial.ErrUninitialized) {
			t.Fatalf("Debit error = %v, want ErrUninitialized", err)
		}
		if _, err := (financial.Wallet{}).Credit(mustMoney(t, "1.00"), testNow); !errors.Is(err, financial.ErrUninitialized) {
			t.Fatalf("Credit error = %v, want ErrUninitialized", err)
		}
	})

	t.Run("wager transaction", func(t *testing.T) {
		if _, err := (financial.WagerTransaction{}).MarkProcessed(financial.TransactionID{}, mustMoney(t, "1.00"), testNow); !errors.Is(err, financial.ErrUninitialized) {
			t.Fatalf("MarkProcessed error = %v, want ErrUninitialized", err)
		}
	})

	t.Run("ledger entry", func(t *testing.T) {
		_, err := financial.RehydrateWalletLedgerEntry(financial.NewLedgerEntryID(), financial.NewWalletID(), financial.NewTransactionID(), financial.DirectionCredit, money.Money{}, mustMoney(t, "0.00"), mustMoney(t, "1.00"), testNow)
		if !errors.Is(err, financial.ErrUninitialized) {
			t.Fatalf("rehydration error = %v, want ErrUninitialized", err)
		}
	})
}

// C26 - The domain error API is classifiable through errors.Is or errors.As.
func TestErrorClassification(t *testing.T) {
	t.Run("sentinel through errors.Is", func(t *testing.T) {
		wallet := mustWallet(t, "10.00")
		_, err := wallet.Debit(mustMoney(t, "11.00"), testNow.Add(time.Minute))
		if !errors.Is(err, financial.ErrInsufficientFunds) {
			t.Fatalf("error = %v, want ErrInsufficientFunds", err)
		}
	})

	t.Run("typed transition error through errors.As", func(t *testing.T) {
		transaction := mustTransaction(t)
		rejected, err := transaction.MarkRejected(financial.TransactionID{}, financial.FailureInsufficientFunds, mustMoney(t, "0.00"), testNow.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		_, err = rejected.MarkProcessed(financial.TransactionID{}, mustMoney(t, "0.00"), testNow.Add(2*time.Minute))
		var transition *financial.TransitionError
		if !errors.As(err, &transition) {
			t.Fatalf("errors.As failed for %v", err)
		}
		if transition.From != financial.StateRejected || transition.To != financial.StateProcessed {
			t.Fatalf("transition error = %+v", transition)
		}
		if !errors.Is(err, financial.ErrInvalidTransition) {
			t.Fatalf("errors.Is(ErrInvalidTransition) failed for %v", err)
		}
	})

	t.Run("invalid input through errors.Is", func(t *testing.T) {
		_, err := financial.NewWallet(financial.WalletID{}, financial.NewPlayerID(), "BRL", mustMoney(t, "0.00"), testNow)
		if !errors.Is(err, financial.ErrInvalidInput) {
			t.Fatalf("error = %v, want ErrInvalidInput", err)
		}
	})
}

// C27 - The business rejection path returns a classified domain result instead
// of invoking panic.
func TestBusinessRejectionNoPanic(t *testing.T) {
	rejections := map[string]func() error{
		"insufficient funds": func() error {
			_, err := mustWallet(t, "10.00").Debit(mustMoney(t, "11.00"), testNow.Add(time.Minute))
			return err
		},
		"currency mismatch": func() error {
			wallet := mustWallet(t, "10.00")
			usd, err := money.Parse("1.00", "USD")
			if err != nil {
				return err
			}
			_, err = wallet.Debit(usd, testNow.Add(time.Minute))
			return err
		},
		"invalid transition": func() error {
			transaction := mustTransaction(t)
			processed, err := transaction.MarkProcessed(financial.TransactionID{}, mustMoney(t, "0.00"), testNow.Add(time.Minute))
			if err != nil {
				return err
			}
			_, err = processed.MarkFailed(mustMoney(t, "0.00"), testNow.Add(2*time.Minute))
			return err
		},
		"invalid construction": func() error {
			_, err := financial.NewWallet(financial.WalletID{}, financial.PlayerID{}, "BRL", money.Money{}, time.Time{})
			return err
		},
	}

	for name, reject := range rejections {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("business rejection panicked: %v", recovered)
				}
			}()
			if err := reject(); err == nil {
				t.Fatal("expected a classified error, got nil")
			}
		})
	}
}
