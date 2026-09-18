// Package application implements the financial use cases shared by the HTTP
// and SQS ingresses. It depends only on the domain and on the ports declared
// in this package.
package application

import (
	"errors"
	"fmt"
)

// ErrorCode is the stable UPPER_SNAKE code surfaced by transport adapters.
type ErrorCode string

// Stable application error codes. Financial rejections are not errors here:
// they are durable results. These codes describe contract, conflict and
// dependency outcomes only.
const (
	CodeInvalidRequest              ErrorCode = "INVALID_REQUEST"
	CodeInvalidMoney                ErrorCode = "INVALID_MONEY"
	CodeUnsupportedCurrency         ErrorCode = "UNSUPPORTED_CURRENCY"
	CodeIdempotencyKeyRequired      ErrorCode = "IDEMPOTENCY_KEY_REQUIRED"
	CodeReferenceRequired           ErrorCode = "REFERENCE_REQUIRED"
	CodeInvalidLossAmount           ErrorCode = "INVALID_LOSS_AMOUNT"
	CodeInvalidCursor               ErrorCode = "INVALID_CURSOR"
	CodeInvalidLimit                ErrorCode = "INVALID_LIMIT"
	CodeWalletAlreadyExists         ErrorCode = "WALLET_ALREADY_EXISTS"
	CodeIdempotencyConflict         ErrorCode = "IDEMPOTENCY_CONFLICT"
	CodeExternalTransactionConflict ErrorCode = "EXTERNAL_TRANSACTION_CONFLICT"
	CodeNotFound                    ErrorCode = "NOT_FOUND"
	CodeServiceUnavailable          ErrorCode = "SERVICE_UNAVAILABLE"
	CodeInboxPayloadConflict        ErrorCode = "INBOX_PAYLOAD_CONFLICT"
	CodeInvalidMessage              ErrorCode = "INVALID_MESSAGE"
)

// Class sentinels. Every error returned by this package matches exactly one of
// them through errors.Is.
var (
	// ErrContract marks a correctable input error that persists nothing.
	ErrContract = errors.New("application: contract violation")
	// ErrConflict marks a durable identity conflict that preserves the first result.
	ErrConflict = errors.New("application: conflict")
	// ErrNotFound marks an absent resource.
	ErrNotFound = errors.New("application: not found")
	// ErrTransient marks recoverable infrastructure unavailability.
	ErrTransient = errors.New("application: transient infrastructure failure")
)

// Persistence sentinels. Repository ports return these so the use cases can
// classify database outcomes without importing an adapter.
var (
	// ErrWalletExists means the (playerId, currency) wallet row already exists.
	ErrWalletExists = errors.New("application: wallet already exists")
	// ErrConcurrentWrite means a unique constraint collided with a concurrent
	// writer; the caller may retry the transaction and re-read the winner.
	ErrConcurrentWrite = errors.New("application: concurrent write conflict")
	// ErrCorruptPersistedRecord means a persisted row cannot be rebuilt into a
	// valid domain entity.
	ErrCorruptPersistedRecord = errors.New("application: persisted record violates an invariant")
)

// errInvariant marks a broken internal invariant that should be impossible
// after command validation; it is never a business rejection.
var errInvariant = errors.New("application: invariant violation")

// Error is a classified application error carrying a stable code.
type Error struct {
	Code    ErrorCode
	Message string
	class   error
	cause   error
}

// Error renders the code and message without leaking the cause.
func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes both the class sentinel and the wrapped cause so callers can
// classify with errors.Is and errors.As.
func (e *Error) Unwrap() []error {
	return []error{e.class, e.cause}
}

func newError(class error, code ErrorCode, message string, cause error) error {
	return &Error{Code: code, Message: message, class: class, cause: cause}
}

func contractError(code ErrorCode, message string) error {
	return newError(ErrContract, code, message, nil)
}

func conflictError(code ErrorCode, message string, cause error) error {
	return newError(ErrConflict, code, message, cause)
}

func notFoundError(message string) error {
	return newError(ErrNotFound, CodeNotFound, message, nil)
}

func transientError(cause error) error {
	return newError(ErrTransient, CodeServiceUnavailable, "service temporarily unavailable", cause)
}

// ErrorCodeOf reports the stable code of a classified application error.
func ErrorCodeOf(err error) (ErrorCode, bool) {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr.Code, true
	}
	return "", false
}

// IsContract reports whether the error is a correctable input error.
func IsContract(err error) bool { return errors.Is(err, ErrContract) }

// IsConflict reports whether the error is a durable identity conflict.
func IsConflict(err error) bool { return errors.Is(err, ErrConflict) }

// IsNotFound reports whether the error is an absent resource.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// IsTransient reports whether the error is recoverable infrastructure failure.
func IsTransient(err error) bool { return errors.Is(err, ErrTransient) }
