package application

import (
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

// OpenWalletCommand asks for the creation of one wallet per player/currency.
type OpenWalletCommand struct {
	PlayerID       financial.PlayerID
	InitialBalance money.Money
	CorrelationID  string
}

// Validate reports whether the command is a well-formed internal opening.
func (c OpenWalletCommand) Validate() error {
	if c.PlayerID.IsZero() {
		return contractError(CodeInvalidRequest, "playerId is required")
	}
	if !c.InitialBalance.IsInitialized() {
		return contractError(CodeInvalidMoney, "initialBalance is required")
	}
	if c.InitialBalance.IsNegative() {
		return contractError(CodeInvalidMoney, "initialBalance must not be negative")
	}
	return nil
}

// SubmitWagerCommand is the transport-neutral external wager command submitted
// by a provider through either ingress.
type SubmitWagerCommand struct {
	ProviderID            financial.ProviderID
	ExternalTransactionID financial.ExternalID
	IdempotencyKey        financial.IdempotencyKey
	WalletID              financial.WalletID
	PlayerID              financial.PlayerID
	RoundID               financial.ExternalID
	GameID                financial.ExternalID
	Kind                  financial.Kind
	Amount                money.Money
	ReferenceExternalID   financial.ExternalID
	CorrelationID         string
}

// Validate applies the contract checks that must reject a command before any
// row is persisted. Domain constructors re-check the same invariants.
func (c SubmitWagerCommand) Validate() error {
	if c.ProviderID.IsZero() {
		return contractError(CodeInvalidRequest, "providerId is required")
	}
	if c.ExternalTransactionID.IsZero() {
		return contractError(CodeInvalidRequest, "externalTransactionId is required")
	}
	if c.WalletID.IsZero() {
		return contractError(CodeInvalidRequest, "walletId is required")
	}
	if c.PlayerID.IsZero() {
		return contractError(CodeInvalidRequest, "playerId is required")
	}
	if c.RoundID.IsZero() {
		return contractError(CodeInvalidRequest, "roundId is required")
	}
	if c.GameID.IsZero() {
		return contractError(CodeInvalidRequest, "gameId is required")
	}
	if c.IdempotencyKey.IsZero() {
		return contractError(CodeIdempotencyKeyRequired, "Idempotency-Key is required")
	}
	if !c.Kind.Valid() || !c.Kind.IsExternal() {
		return contractError(CodeInvalidRequest, "kind is not a provider operation")
	}
	if !c.Amount.IsInitialized() {
		return contractError(CodeInvalidMoney, "amount is required")
	}
	switch c.Kind {
	case financial.KindLoss:
		if !c.Amount.IsZero() {
			return contractError(CodeInvalidLossAmount, "LOSS requires amount 0.00")
		}
		if !c.ReferenceExternalID.IsZero() {
			return contractError(CodeInvalidRequest, "LOSS does not accept a reference")
		}
	case financial.KindBet:
		if !c.Amount.IsPositive() {
			return contractError(CodeInvalidRequest, "BET requires a positive amount")
		}
		if !c.ReferenceExternalID.IsZero() {
			return contractError(CodeInvalidRequest, "BET does not accept a reference")
		}
	case financial.KindRefund, financial.KindRollback:
		if !c.Amount.IsPositive() {
			return contractError(CodeInvalidRequest, string(c.Kind)+" requires a positive amount")
		}
		if c.ReferenceExternalID.IsZero() {
			return contractError(CodeReferenceRequired, "referenceExternalTransactionId is required")
		}
	default:
		if !c.Amount.IsPositive() {
			return contractError(CodeInvalidRequest, string(c.Kind)+" requires a positive amount")
		}
	}
	return nil
}

// projection returns the canonical business projection of the command.
func (c SubmitWagerCommand) projection() ProjectionInput {
	return ProjectionInput{
		ProviderID:            c.ProviderID,
		ExternalTransactionID: c.ExternalTransactionID,
		PlayerID:              c.PlayerID,
		WalletID:              c.WalletID,
		RoundID:               c.RoundID,
		GameID:                c.GameID,
		Kind:                  c.Kind,
		Amount:                c.Amount,
		ReferenceExternalID:   c.ReferenceExternalID,
	}
}

// ResolvePendingReferenceCommand asks the reference recovery use case to
// advance one accepted PENDING_REFERENCE transaction.
type ResolvePendingReferenceCommand struct {
	TransactionID financial.TransactionID
	CorrelationID string
}

// Validate reports whether the command identifies a transaction.
func (c ResolvePendingReferenceCommand) Validate() error {
	if c.TransactionID.IsZero() {
		return contractError(CodeInvalidRequest, "transactionId is required")
	}
	return nil
}

// Validate reports whether the delivery carries a broker identity and digest.
func (d InboxDelivery) Validate() error {
	if d.ConsumerName == "" {
		return contractError(CodeInvalidRequest, "consumer name is required")
	}
	if d.MessageID == "" {
		return contractError(CodeInvalidRequest, "messageId is required")
	}
	if len(d.Digest) != 64 {
		return contractError(CodeInvalidRequest, "payload digest must be a sha256 hex value")
	}
	if d.ReceivedAt.IsZero() {
		return contractError(CodeInvalidRequest, "receipt time is required")
	}
	return nil
}

// WalletView is the application-level representation of a wallet.
type WalletView struct {
	ID        financial.WalletID
	PlayerID  financial.PlayerID
	Currency  string
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// WagerResult is the application-level representation of one operation
// outcome. ObservedBalance stays uninitialized while the transaction is
// PENDING_REFERENCE, so no terminal balance is reported before resolution.
type WagerResult struct {
	TransactionID     financial.TransactionID
	Kind              financial.Kind
	State             financial.State
	ObservedBalance   money.Money
	FailureCode       financial.FailureCode
	ReferenceDeadline time.Time
	IdempotentReplay  bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// IsTerminal reports whether the operation reached a final state.
func (r WagerResult) IsTerminal() bool { return r.State.IsTerminal() }
