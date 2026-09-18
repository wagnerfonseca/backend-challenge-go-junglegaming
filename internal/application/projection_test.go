package application_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

func mustProjection(t *testing.T, reference financial.ExternalID, amount string) application.ProjectionInput {
	t.Helper()
	value, err := financial.ParseWalletID("018f2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5b")
	if err != nil {
		t.Fatalf("parsing wallet id: %v", err)
	}
	player, err := financial.ParsePlayerID("018f2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5c")
	if err != nil {
		t.Fatalf("parsing player id: %v", err)
	}
	source := mustMoney(t, amount, "BRL")
	return application.ProjectionInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "ext-001",
		PlayerID:              player,
		WalletID:              value,
		RoundID:               "round-7",
		GameID:                "game-9",
		Kind:                  financial.KindBet,
		Amount:                source,
		ReferenceExternalID:   reference,
	}
}

// C47 - The idempotency digest is SHA-256 over the canonical JSON
// sha256-jcs-v1 projection of the business fields only.
func TestIdempotencyProjection(t *testing.T) {
	t.Run("canonical JSON is sorted and excludes transport metadata", func(t *testing.T) {
		got, err := application.CanonicalJSON(mustProjection(t, "", "25.00"))
		if err != nil {
			t.Fatalf("canonical JSON: %v", err)
		}
		want := `{"amount":"25.00","currency":"BRL","externalTransactionId":"ext-001",` +
			`"gameId":"game-9","kind":"BET","playerId":"018f2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5c",` +
			`"providerId":"provider-a","roundId":"round-7","walletId":"018f2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5b"}`
		if got != want {
			t.Errorf("canonical JSON = %s, want %s", got, want)
		}
	})

	t.Run("optional reference is omitted when absent and included when present", func(t *testing.T) {
		without, err := application.CanonicalJSON(mustProjection(t, "", "25.00"))
		if err != nil {
			t.Fatalf("canonical JSON without reference: %v", err)
		}
		with, err := application.CanonicalJSON(mustProjection(t, "bet-42", "25.00"))
		if err != nil {
			t.Fatalf("canonical JSON with reference: %v", err)
		}
		if without == with {
			t.Fatal("projection did not change when the reference was added")
		}
		want := `"referenceExternalTransactionId":"bet-42"`
		if !strings.Contains(with, want) {
			t.Errorf("canonical JSON %s does not contain %s", with, want)
		}
		if strings.Contains(without, "referenceExternalTransactionId") {
			t.Errorf("canonical JSON %s must omit an absent reference", without)
		}
	})

	t.Run("digest is the lowercase hex sha256 of the canonical projection", func(t *testing.T) {
		projection := mustProjection(t, "bet-42", "25.00")
		canonical, err := application.CanonicalJSON(projection)
		if err != nil {
			t.Fatalf("canonical JSON: %v", err)
		}
		sum := sha256.Sum256([]byte(canonical))
		want := hex.EncodeToString(sum[:])
		got, err := application.DigestOf(projection)
		if err != nil {
			t.Fatalf("digest: %v", err)
		}
		if got != want {
			t.Errorf("digest = %s, want %s", got, want)
		}
		if len(got) != 64 {
			t.Errorf("digest length = %d, want 64", len(got))
		}
		again, err := application.DigestOf(projection)
		if err != nil {
			t.Fatalf("digest repeat: %v", err)
		}
		if again != got {
			t.Errorf("digest is not deterministic: %s then %s", got, again)
		}
	})

	t.Run("different business fields produce different digests", func(t *testing.T) {
		first, err := application.DigestOf(mustProjection(t, "", "25.00"))
		if err != nil {
			t.Fatalf("first digest: %v", err)
		}
		second, err := application.DigestOf(mustProjection(t, "", "25.01"))
		if err != nil {
			t.Fatalf("second digest: %v", err)
		}
		if first == second {
			t.Error("digest collided for different canonical money")
		}
	})
}
