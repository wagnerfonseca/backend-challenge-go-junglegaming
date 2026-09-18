// Package event defines the integration event contract: a versioned envelope
// with typed data. Constructors own eventType and version.
package event

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

// Type is the event type discriminator.
type Type string

// Event types.
const (
	TypeWagerTransactionProcessed        Type = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         Type = "WagerTransactionRejected"
	TypeWalletBalanceChanged             Type = "WalletBalanceChanged"
	TypeWagerTransactionPendingReference Type = "WagerTransactionPendingReference"
)

// Version is the envelope contract version of this delivery.
const Version = 1

// Timestamp serializes as UTC RFC 3339 with milliseconds.
type Timestamp time.Time

// MarshalJSON renders the value with exactly three fractional digits in UTC.
func (t Timestamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).UTC().Format("2006-01-02T15:04:05.000Z07:00"))
}

// UnmarshalJSON parses an RFC 3339 timestamp.
func (t *Timestamp) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("event: invalid timestamp: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return fmt.Errorf("event: invalid timestamp %q: %w", raw, err)
	}
	*t = Timestamp(parsed.UTC())
	return nil
}

// Time returns the wrapped instant.
func (t Timestamp) Time() time.Time { return time.Time(t) }

// Envelope is the stable integration event shape. version and eventType are
// set by the constructors and cannot be supplied by callers.
type Envelope struct {
	EventID       string    `json:"eventId"`
	EventType     Type      `json:"eventType"`
	AggregateID   string    `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    Timestamp `json:"occurredAt"`
	Version       int       `json:"version"`
	Data          any       `json:"data"`
}

// ProcessedData is the payload of WagerTransactionProcessed.
type ProcessedData struct {
	TransactionID         string      `json:"transactionId"`
	Origin                string      `json:"origin"`
	ProviderID            string      `json:"providerId,omitempty"`
	ExternalTransactionID string      `json:"externalTransactionId,omitempty"`
	WalletID              string      `json:"walletId"`
	PlayerID              string      `json:"playerId"`
	RoundID               string      `json:"roundId,omitempty"`
	GameID                string      `json:"gameId,omitempty"`
	Kind                  string      `json:"kind"`
	Money                 money.Money `json:"money"`
	Balance               money.Money `json:"balance"`
}

// RejectedData is the payload of WagerTransactionRejected.
type RejectedData struct {
	TransactionID                  string      `json:"transactionId"`
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	WalletID                       string      `json:"walletId"`
	Kind                           string      `json:"kind"`
	Money                          money.Money `json:"money"`
	FailureCode                    string      `json:"failureCode"`
	Balance                        money.Money `json:"balance"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
}

// BalanceChangedData is the payload of WalletBalanceChanged.
type BalanceChangedData struct {
	WalletID      string      `json:"walletId"`
	TransactionID string      `json:"transactionId"`
	Direction     string      `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
}

// PendingReferenceData is the payload of WagerTransactionPendingReference.
type PendingReferenceData struct {
	TransactionID                  string      `json:"transactionId"`
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	WalletID                       string      `json:"walletId"`
	Kind                           string      `json:"kind"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId"`
	ExpiresAt                      Timestamp   `json:"expiresAt"`
}

// Metadata carries the identity fields shared by every event.
type Metadata struct {
	EventID       string
	AggregateID   string
	CorrelationID string
	CausationID   string
	OccurredAt    time.Time
}

func (m Metadata) validate() error {
	if m.EventID == "" {
		return fmt.Errorf("event: empty event id")
	}
	if m.AggregateID == "" {
		return fmt.Errorf("event: empty aggregate id")
	}
	if m.CorrelationID == "" {
		return fmt.Errorf("event: empty correlation id")
	}
	if m.OccurredAt.IsZero() {
		return fmt.Errorf("event: zero occurrence time")
	}
	return nil
}

// NewWagerTransactionProcessed builds a processed event.
func NewWagerTransactionProcessed(meta Metadata, data ProcessedData) (Envelope, error) {
	return newEnvelope(TypeWagerTransactionProcessed, meta, data)
}

// NewWagerTransactionRejected builds a rejected event.
func NewWagerTransactionRejected(meta Metadata, data RejectedData) (Envelope, error) {
	return newEnvelope(TypeWagerTransactionRejected, meta, data)
}

// NewWalletBalanceChanged builds a balance change event.
func NewWalletBalanceChanged(meta Metadata, data BalanceChangedData) (Envelope, error) {
	return newEnvelope(TypeWalletBalanceChanged, meta, data)
}

// NewWagerTransactionPendingReference builds a pending-reference event.
func NewWagerTransactionPendingReference(meta Metadata, data PendingReferenceData) (Envelope, error) {
	return newEnvelope(TypeWagerTransactionPendingReference, meta, data)
}

func newEnvelope(eventType Type, meta Metadata, data any) (Envelope, error) {
	if err := meta.validate(); err != nil {
		return Envelope{}, err
	}
	return Envelope{
		EventID:       meta.EventID,
		EventType:     eventType,
		AggregateID:   meta.AggregateID,
		CorrelationID: meta.CorrelationID,
		CausationID:   meta.CausationID,
		OccurredAt:    Timestamp(meta.OccurredAt.UTC()),
		Version:       Version,
		Data:          data,
	}, nil
}
