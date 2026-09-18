// Package financial holds the financial domain: wallet, wager transaction,
// ledger entry and reversal claim. It depends only on the money package and
// the standard library.
package financial

import (
	"errors"
	"fmt"
)

// Sentinel errors. Every failure returned by this package wraps one of them,
// so callers classify with errors.Is.
var (
	ErrInvalidInput         = errors.New("financial: invalid input")
	ErrInvalidTransition    = errors.New("financial: invalid state transition")
	ErrInsufficientFunds    = errors.New("financial: insufficient funds")
	ErrUninitialized        = errors.New("financial: uninitialized value")
	ErrCompensationConsumed = errors.New("financial: direct compensation already consumed")
)

// FailureCode is a stable, documented business failure identifier persisted on
// rejected and failed transactions.
type FailureCode string

// Business rejection codes persisted with a REJECTED transaction.
const (
	FailureInsufficientFunds         FailureCode = "INSUFFICIENT_FUNDS"
	FailureReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	FailureReferenceNotFound         FailureCode = "REFERENCE_NOT_FOUND"
	FailureReferenceNotProcessed     FailureCode = "REFERENCE_NOT_PROCESSED"
	FailureReferenceMismatch         FailureCode = "REFERENCE_MISMATCH"
	FailureReversalAmountMismatch    FailureCode = "REVERSAL_AMOUNT_MISMATCH"
	FailureReferenceKindNotAllowed   FailureCode = "REFERENCE_KIND_NOT_ALLOWED"
	FailureInvalidWinReference       FailureCode = "INVALID_WIN_REFERENCE"
	FailureAlreadyReversed           FailureCode = "ALREADY_REVERSED"
)

// FailurePermanentInfrastructure is persisted with a FAILED transaction.
const FailurePermanentInfrastructure FailureCode = "PERMANENT_INFRASTRUCTURE_FAILURE"

// Valid reports whether the code is one of the documented stable codes.
func (c FailureCode) Valid() bool {
	switch c {
	case FailureInsufficientFunds,
		FailureReversalInsufficientFunds,
		FailureReferenceNotFound,
		FailureReferenceNotProcessed,
		FailureReferenceMismatch,
		FailureReversalAmountMismatch,
		FailureReferenceKindNotAllowed,
		FailureInvalidWinReference,
		FailureAlreadyReversed,
		FailurePermanentInfrastructure:
		return true
	default:
		return false
	}
}

// TransitionError describes a rejected state transition while still matching
// ErrInvalidTransition through errors.Is.
type TransitionError struct {
	From State
	To   State
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("financial: transition %s -> %s is not allowed", e.From, e.To)
}

func (e *TransitionError) Unwrap() error { return ErrInvalidTransition }
