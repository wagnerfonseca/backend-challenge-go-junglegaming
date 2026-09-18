// Package consumer validates and handles ingress queue messages. It requests
// the SenderId and ApproximateReceiveCount system attributes and delegates to
// the shared financial use case through the durable inbox.
package consumer

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

// TypeWagerTransactionRequested is the only accepted ingress message type.
const TypeWagerTransactionRequested = "WagerTransactionRequested"

// ErrInvalidEnvelope classifies a permanently invalid message.
var ErrInvalidEnvelope = errors.New("sqs consumer: invalid envelope")

// Envelope is the ingress message contract.
type Envelope struct {
	MessageID     string `json:"messageId"`
	Type          string `json:"type"`
	OccurredAt    string `json:"occurredAt"`
	CorrelationID string `json:"correlationId,omitempty"`
	Data          Data   `json:"data"`
}

// Data is the typed business payload; idempotencyKey drives the same
// provider-scoped lookup as the HTTP header.
type Data struct {
	ProviderID                     string       `json:"providerId"`
	ExternalTransactionID          string       `json:"externalTransactionId"`
	IdempotencyKey                 string       `json:"idempotencyKey"`
	PlayerID                       string       `json:"playerId"`
	WalletID                       string       `json:"walletId"`
	RoundID                        string       `json:"roundId"`
	GameID                         string       `json:"gameId"`
	Kind                           string       `json:"kind"`
	Money                          MoneyPayload `json:"money"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId,omitempty"`
}

// MoneyPayload is the canonical decimal-string money shape.
type MoneyPayload struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// ParseEnvelope decodes one raw message body.
func ParseEnvelope(raw []byte) (Envelope, error) {
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Envelope{}, fmt.Errorf("%w: %s", ErrInvalidEnvelope, err)
	}
	return envelope, nil
}

// Command validates the typed payload and builds the shared use-case command.
func (e Envelope) Command() (application.SubmitWagerCommand, error) {
	if e.MessageID == "" {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: messageId is required", ErrInvalidEnvelope)
	}
	if e.Type != TypeWagerTransactionRequested {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: type %q is not supported", ErrInvalidEnvelope, e.Type)
	}
	if e.OccurredAt == "" {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: occurredAt is required", ErrInvalidEnvelope)
	}
	occurredAt, err := time.Parse(time.RFC3339, e.OccurredAt)
	if err != nil {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: occurredAt is not RFC 3339", ErrInvalidEnvelope)
	}
	if _, offset := occurredAt.Zone(); offset != 0 {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: occurredAt must be UTC", ErrInvalidEnvelope)
	}
	if e.Data.IdempotencyKey == "" {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: data.idempotencyKey is required", ErrInvalidEnvelope)
	}
	if len(e.Data.IdempotencyKey) > 255 {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: data.idempotencyKey exceeds 255 bytes", ErrInvalidEnvelope)
	}
	providerID, err := financial.ParseProviderID(e.Data.ProviderID)
	if err != nil {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: %s", ErrInvalidEnvelope, err)
	}
	externalID, err := financial.ParseExternalID("externalTransactionId", e.Data.ExternalTransactionID)
	if err != nil {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: %s", ErrInvalidEnvelope, err)
	}
	idempotencyKey, err := financial.ParseIdempotencyKey(e.Data.IdempotencyKey)
	if err != nil {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: %s", ErrInvalidEnvelope, err)
	}
	walletID, err := financial.ParseWalletID(e.Data.WalletID)
	if err != nil {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: %s", ErrInvalidEnvelope, err)
	}
	playerID, err := financial.ParsePlayerID(e.Data.PlayerID)
	if err != nil {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: %s", ErrInvalidEnvelope, err)
	}
	roundID, err := financial.ParseExternalID("roundId", e.Data.RoundID)
	if err != nil {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: %s", ErrInvalidEnvelope, err)
	}
	gameID, err := financial.ParseExternalID("gameId", e.Data.GameID)
	if err != nil {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: %s", ErrInvalidEnvelope, err)
	}
	amount, err := money.Parse(e.Data.Money.Amount, e.Data.Money.Currency)
	if err != nil {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: money is not canonical", ErrInvalidEnvelope)
	}
	var reference financial.ExternalID
	if e.Data.ReferenceExternalTransactionID != "" {
		reference, err = financial.ParseExternalID("referenceExternalTransactionId", e.Data.ReferenceExternalTransactionID)
		if err != nil {
			return application.SubmitWagerCommand{}, fmt.Errorf("%w: %s", ErrInvalidEnvelope, err)
		}
	}
	correlation := e.CorrelationID
	if correlation == "" {
		correlation = e.MessageID
	}
	command := application.SubmitWagerCommand{
		ProviderID:            providerID,
		ExternalTransactionID: externalID,
		IdempotencyKey:        idempotencyKey,
		WalletID:              walletID,
		PlayerID:              playerID,
		RoundID:               roundID,
		GameID:                gameID,
		Kind:                  financial.Kind(e.Data.Kind),
		Amount:                amount,
		ReferenceExternalID:   reference,
		CorrelationID:         correlation,
	}
	if err := command.Validate(); err != nil {
		return application.SubmitWagerCommand{}, fmt.Errorf("%w: %s", ErrInvalidEnvelope, err)
	}
	return command, nil
}
