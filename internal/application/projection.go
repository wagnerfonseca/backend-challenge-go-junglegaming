package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

// DigestVersion identifies the canonical idempotency projection contract.
const DigestVersion = "sha256-jcs-v1"

// ProjectionInput carries exactly the business fields of an external command
// that participate in the idempotency projection. Transport metadata and the
// idempotency key are deliberately absent.
type ProjectionInput struct {
	ProviderID            financial.ProviderID
	ExternalTransactionID financial.ExternalID
	PlayerID              financial.PlayerID
	WalletID              financial.WalletID
	RoundID               financial.ExternalID
	GameID                financial.ExternalID
	Kind                  financial.Kind
	Amount                money.Money
	ReferenceExternalID   financial.ExternalID
}

// CanonicalJSON renders the business projection as sha256-jcs-v1 canonical
// JSON: lexicographically ordered members, no insignificant whitespace, money
// as exact decimal strings, and the optional reference omitted when absent.
//
// The member values are restricted by the domain contracts to characters that
// need no escaping beyond what encoding/json already emits, so marshalling the
// sorted string map is byte-identical to RFC 8785 JCS for this projection.
func CanonicalJSON(in ProjectionInput) (string, error) {
	members := map[string]string{
		"providerId":            in.ProviderID.String(),
		"externalTransactionId": in.ExternalTransactionID.String(),
		"playerId":              in.PlayerID.String(),
		"walletId":              in.WalletID.String(),
		"roundId":               in.RoundID.String(),
		"gameId":                in.GameID.String(),
		"kind":                  string(in.Kind),
		"amount":                in.Amount.String(),
		"currency":              in.Amount.Currency(),
	}
	if !in.ReferenceExternalID.IsZero() {
		members["referenceExternalTransactionId"] = in.ReferenceExternalID.String()
	}
	encoded, err := json.Marshal(members)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// DigestOf returns the lowercase hex SHA-256 of the canonical projection.
func DigestOf(in ProjectionInput) (string, error) {
	canonical, err := CanonicalJSON(in)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), nil
}
