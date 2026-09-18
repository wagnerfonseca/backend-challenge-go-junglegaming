package event_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/domain/event"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/domain/money"
)

var testNow = time.Date(2026, 9, 18, 12, 0, 0, 123000000, time.UTC)

func mustMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	parsed, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse(%q): %v", amount, err)
	}
	return parsed
}

func meta() event.Metadata {
	return event.Metadata{
		EventID:       "0192f298-345e-7e38-af88-e43f851a819d",
		AggregateID:   "0192f291-27dd-7d3f-8071-5f8685deef37",
		CorrelationID: "0192f291-27dd-7d3f-8071-5f8685deef38",
		OccurredAt:    testNow,
	}
}

func marshalEnvelope(t *testing.T, envelope event.Envelope) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return decoded
}

// C149 - The integration event envelope contains eventId, eventType,
// aggregateId, correlationId, optional causationId, occurredAt, version, and
// typed data.
func TestEventEnvelopeStructure(t *testing.T) {
	envelope, err := event.NewWalletBalanceChanged(meta(), event.BalanceChangedData{
		WalletID:      "wallet-1",
		TransactionID: "transaction-1",
		Direction:     "DEBIT",
		Money:         mustMoney(t, "25.00"),
		BalanceBefore: mustMoney(t, "1000.00"),
		BalanceAfter:  mustMoney(t, "975.00"),
		WalletVersion: 2,
	})
	if err != nil {
		t.Fatalf("NewWalletBalanceChanged: %v", err)
	}

	decoded := marshalEnvelope(t, envelope)
	for _, key := range []string{"eventId", "eventType", "aggregateId", "correlationId", "occurredAt", "version", "data"} {
		if _, present := decoded[key]; !present {
			t.Errorf("envelope is missing %q: %v", key, decoded)
		}
	}
	if _, present := decoded["causationId"]; present {
		t.Errorf("empty optional causationId must be omitted: %v", decoded)
	}

	withCause, err := event.NewWagerTransactionProcessed(event.Metadata{
		EventID:       meta().EventID,
		AggregateID:   meta().AggregateID,
		CorrelationID: meta().CorrelationID,
		CausationID:   "msg-123",
		OccurredAt:    testNow,
	}, event.ProcessedData{
		TransactionID: "transaction-1",
		Origin:        "EXTERNAL",
		WalletID:      "wallet-1",
		PlayerID:      "player-1",
		Kind:          "BET",
		Money:         mustMoney(t, "25.00"),
		Balance:       mustMoney(t, "975.00"),
	})
	if err != nil {
		t.Fatalf("NewWagerTransactionProcessed: %v", err)
	}
	if decoded := marshalEnvelope(t, withCause); decoded["causationId"] != "msg-123" {
		t.Errorf("causationId = %v, want msg-123", decoded["causationId"])
	}
}

// C150 - The event serialization uses version 1, UTC RFC 3339 timestamps with
// milliseconds, and decimal-string money.
func TestEventSerialization(t *testing.T) {
	envelope, err := event.NewWalletBalanceChanged(meta(), event.BalanceChangedData{
		WalletID:      "wallet-1",
		TransactionID: "transaction-1",
		Direction:     "DEBIT",
		Money:         mustMoney(t, "25.00"),
		BalanceBefore: mustMoney(t, "1000.00"),
		BalanceAfter:  mustMoney(t, "975.00"),
		WalletVersion: 2,
	})
	if err != nil {
		t.Fatalf("NewWalletBalanceChanged: %v", err)
	}

	if envelope.Version != 1 {
		t.Fatalf("version = %d, want 1", envelope.Version)
	}
	decoded := marshalEnvelope(t, envelope)
	if decoded["version"] != float64(1) {
		t.Fatalf("serialized version = %v, want 1", decoded["version"])
	}
	if decoded["occurredAt"] != "2026-09-18T12:00:00.123Z" {
		t.Fatalf("occurredAt = %v, want 2026-09-18T12:00:00.123Z", decoded["occurredAt"])
	}
	data := decoded["data"].(map[string]any)
	if data["money"] != `{"amount":"25.00","currency":"BRL"}` && data["money"].(map[string]any)["amount"] != "25.00" {
		t.Fatalf("money = %v, want an object with amount 25.00", data["money"])
	}
}

// C155 - WalletBalanceChanged.data contains walletId, transactionId,
// direction, money, balanceBefore, balanceAfter, and walletVersion.
func TestBalanceChangedPayload(t *testing.T) {
	envelope, err := event.NewWalletBalanceChanged(meta(), event.BalanceChangedData{
		WalletID:      "wallet-1",
		TransactionID: "transaction-1",
		Direction:     "DEBIT",
		Money:         mustMoney(t, "25.00"),
		BalanceBefore: mustMoney(t, "1000.00"),
		BalanceAfter:  mustMoney(t, "975.00"),
		WalletVersion: 2,
	})
	if err != nil {
		t.Fatalf("NewWalletBalanceChanged: %v", err)
	}
	data := marshalEnvelope(t, envelope)["data"].(map[string]any)

	for _, key := range []string{"walletId", "transactionId", "direction", "money", "balanceBefore", "balanceAfter", "walletVersion"} {
		if _, present := data[key]; !present {
			t.Errorf("WalletBalanceChanged.data is missing %q: %v", key, data)
		}
	}
	if data["direction"] != "DEBIT" {
		t.Errorf("direction = %v, want DEBIT", data["direction"])
	}
	if data["walletVersion"] != float64(2) {
		t.Errorf("walletVersion = %v, want 2", data["walletVersion"])
	}
}

// C156 - Event data schemas match the documented field sets.
func TestEventDataSchemas(t *testing.T) {
	stringSet := func(raw map[string]any) map[string]bool {
		keys := make(map[string]bool, len(raw))
		for key := range raw {
			keys[key] = true
		}
		return keys
	}
	assertKeys := func(t *testing.T, data map[string]any, required, optional []string) {
		t.Helper()
		present := stringSet(data)
		for _, key := range required {
			if !present[key] {
				t.Errorf("missing required key %q in %v", key, present)
			}
		}
		for _, key := range optional {
			delete(present, key)
		}
		for _, key := range required {
			delete(present, key)
		}
		for key := range present {
			t.Errorf("unexpected key %q in %v", key, data)
		}
	}

	t.Run("processed", func(t *testing.T) {
		envelope, err := event.NewWagerTransactionProcessed(meta(), event.ProcessedData{
			TransactionID: "transaction-1",
			Origin:        "EXTERNAL",
			WalletID:      "wallet-1",
			PlayerID:      "player-1",
			Kind:          "BET",
			Money:         mustMoney(t, "25.00"),
			Balance:       mustMoney(t, "975.00"),
		})
		if err != nil {
			t.Fatal(err)
		}
		assertKeys(t, marshalEnvelope(t, envelope)["data"].(map[string]any),
			[]string{"transactionId", "origin", "walletId", "playerId", "kind", "money", "balance"},
			[]string{"providerId", "externalTransactionId", "roundId", "gameId"})
	})

	t.Run("rejected", func(t *testing.T) {
		envelope, err := event.NewWagerTransactionRejected(meta(), event.RejectedData{
			TransactionID:         "transaction-1",
			ProviderID:            "provider-a",
			ExternalTransactionID: "transaction-123",
			WalletID:              "wallet-1",
			Kind:                  "BET",
			Money:                 mustMoney(t, "25.00"),
			FailureCode:           "INSUFFICIENT_FUNDS",
			Balance:               mustMoney(t, "10.00"),
		})
		if err != nil {
			t.Fatal(err)
		}
		assertKeys(t, marshalEnvelope(t, envelope)["data"].(map[string]any),
			[]string{"transactionId", "providerId", "externalTransactionId", "walletId", "kind", "money", "failureCode", "balance"},
			[]string{"referenceExternalTransactionId"})
	})

	t.Run("pending reference", func(t *testing.T) {
		envelope, err := event.NewWagerTransactionPendingReference(meta(), event.PendingReferenceData{
			TransactionID:                  "transaction-1",
			ProviderID:                     "provider-a",
			ExternalTransactionID:          "transaction-123",
			WalletID:                       "wallet-1",
			Kind:                           "REFUND",
			Money:                          mustMoney(t, "25.00"),
			ReferenceExternalTransactionID: "transaction-000",
			ExpiresAt:                      event.Timestamp(testNow.Add(24 * time.Hour)),
		})
		if err != nil {
			t.Fatal(err)
		}
		data := marshalEnvelope(t, envelope)["data"].(map[string]any)
		assertKeys(t, data,
			[]string{"transactionId", "providerId", "externalTransactionId", "walletId", "kind", "money", "referenceExternalTransactionId", "expiresAt"},
			nil)
		if data["expiresAt"] != "2026-09-19T12:00:00.123Z" {
			t.Errorf("expiresAt = %v, want 2026-09-19T12:00:00.123Z", data["expiresAt"])
		}
	})
}

// C216 - An event constructor sets its own eventType and version:1 without
// caller-supplied overrides.
func TestEventConstructorVersion(t *testing.T) {
	tests := []struct {
		name      string
		envelope  event.Envelope
		eventType event.Type
	}{
		{
			name: "processed",
			envelope: func() event.Envelope {
				envelope, err := event.NewWagerTransactionProcessed(meta(), event.ProcessedData{})
				if err != nil {
					t.Fatal(err)
				}
				return envelope
			}(),
			eventType: event.TypeWagerTransactionProcessed,
		},
		{
			name: "rejected",
			envelope: func() event.Envelope {
				envelope, err := event.NewWagerTransactionRejected(meta(), event.RejectedData{})
				if err != nil {
					t.Fatal(err)
				}
				return envelope
			}(),
			eventType: event.TypeWagerTransactionRejected,
		},
		{
			name: "balance changed",
			envelope: func() event.Envelope {
				envelope, err := event.NewWalletBalanceChanged(meta(), event.BalanceChangedData{})
				if err != nil {
					t.Fatal(err)
				}
				return envelope
			}(),
			eventType: event.TypeWalletBalanceChanged,
		},
		{
			name: "pending reference",
			envelope: func() event.Envelope {
				envelope, err := event.NewWagerTransactionPendingReference(meta(), event.PendingReferenceData{})
				if err != nil {
					t.Fatal(err)
				}
				return envelope
			}(),
			eventType: event.TypeWagerTransactionPendingReference,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envelope.EventType != tt.eventType {
				t.Errorf("eventType = %s, want %s", tt.envelope.EventType, tt.eventType)
			}
			if tt.envelope.Version != 1 {
				t.Errorf("version = %d, want 1", tt.envelope.Version)
			}
		})
	}
}

func TestEventTimestampRoundTrip(t *testing.T) {
	envelope, err := event.NewWalletBalanceChanged(meta(), event.BalanceChangedData{
		WalletID:      "wallet-1",
		TransactionID: "transaction-1",
		Direction:     "DEBIT",
		Money:         mustMoney(t, "25.00"),
		BalanceBefore: mustMoney(t, "1000.00"),
		BalanceAfter:  mustMoney(t, "975.00"),
		WalletVersion: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decoded event.Envelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !decoded.OccurredAt.Time().Equal(testNow) {
		t.Fatalf("occurredAt = %s, want %s", decoded.OccurredAt.Time(), testNow)
	}
}

func TestEnvelopeValidation(t *testing.T) {
	tests := []struct {
		name string
		meta event.Metadata
	}{
		{name: "empty event id", meta: event.Metadata{AggregateID: "a", CorrelationID: "c", OccurredAt: testNow}},
		{name: "empty aggregate", meta: event.Metadata{EventID: "e", CorrelationID: "c", OccurredAt: testNow}},
		{name: "empty correlation", meta: event.Metadata{EventID: "e", AggregateID: "a", OccurredAt: testNow}},
		{name: "zero time", meta: event.Metadata{EventID: "e", AggregateID: "a", CorrelationID: "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := event.NewWalletBalanceChanged(tt.meta, event.BalanceChangedData{}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
