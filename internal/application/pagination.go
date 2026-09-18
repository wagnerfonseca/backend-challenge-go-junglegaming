package application

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// Ledger pagination contract fixed by the approved plan.
const (
	// LedgerDefaultLimit is the page size when no limit is supplied.
	LedgerDefaultLimit = 50
	// LedgerMaxLimit is the largest accepted page size.
	LedgerMaxLimit = 100
	// ledgerCursorVersion is the version prefix of the opaque cursor payload.
	ledgerCursorVersion = 1
)

// LedgerCursor is the keyset position of a ledger page: the last entry of the
// previous page, ordered ascending by (createdAt, id).
type LedgerCursor struct {
	CreatedAt time.Time
	ID        financial.LedgerEntryID
}

// ledgerCursorPayload is the versioned cursor wire shape.
type ledgerCursorPayload struct {
	Version   int       `json:"v"`
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"id"`
}

// LedgerPage is one ascending ledger page with an opaque continuation cursor.
type LedgerPage struct {
	Items      []financial.WalletLedgerEntry
	NextCursor string
}

// EncodeLedgerCursor renders an opaque versioned base64url cursor.
func EncodeLedgerCursor(cursor LedgerCursor) string {
	payload, err := json.Marshal(ledgerCursorPayload{
		Version:   ledgerCursorVersion,
		CreatedAt: cursor.CreatedAt.UTC(),
		ID:        cursor.ID.String(),
	})
	if err != nil {
		// The payload is built from an already-validated entry, so encoding
		// cannot fail for a typed value.
		panic("application: encoding a ledger cursor: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

// DecodeLedgerCursor parses an opaque cursor, rejecting unknown versions,
// malformed payloads and identities outside the canonical UUID contract.
func DecodeLedgerCursor(raw string) (LedgerCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return LedgerCursor{}, errors.New("application: cursor is not base64url")
	}
	var payload ledgerCursorPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return LedgerCursor{}, errors.New("application: cursor payload is not valid JSON")
	}
	if payload.Version != ledgerCursorVersion {
		return LedgerCursor{}, errors.New("application: cursor version is not supported")
	}
	if payload.CreatedAt.IsZero() {
		return LedgerCursor{}, errors.New("application: cursor carries no creation time")
	}
	id, err := financial.ParseLedgerEntryID(payload.ID)
	if err != nil {
		return LedgerCursor{}, errors.New("application: cursor identity is not a canonical UUID")
	}
	return LedgerCursor{CreatedAt: payload.CreatedAt.UTC(), ID: id}, nil
}
