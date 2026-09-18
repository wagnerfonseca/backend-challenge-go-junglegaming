package application

import (
	"fmt"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

// movement is one planned ledger movement.
type movement struct {
	direction financial.Direction
	amount    money.Money
}

// operationPlan is the classified outcome of one external operation: exactly
// one of pending, failure or a movement (possibly absent for LOSS).
type operationPlan struct {
	movement    *movement
	claim       bool
	referenceID financial.TransactionID
	failure     financial.FailureCode
	pending     bool
}

// classify decides the durable outcome of an operation against its optional
// resolved reference. It performs no I/O; claimExists carries the result of
// the direct-compensation lookup for claiming kinds.
func classify(txn financial.WagerTransaction, ref *financial.WagerTransaction, claimExists bool) (operationPlan, error) {
	if ref == nil && needsReference(txn) {
		return operationPlan{pending: true}, nil
	}
	switch txn.Kind() {
	case financial.KindLoss:
		return operationPlan{}, nil
	case financial.KindBet:
		return operationPlan{movement: &movement{direction: financial.DirectionDebit, amount: txn.Amount()}}, nil
	case financial.KindWin:
		if ref == nil {
			return operationPlan{movement: &movement{direction: financial.DirectionCredit, amount: txn.Amount()}}, nil
		}
		if ref.Kind() != financial.KindBet {
			return operationPlan{failure: financial.FailureInvalidWinReference}, nil
		}
		matches, err := identityMatches(txn, *ref)
		if err != nil {
			return operationPlan{}, err
		}
		if !matches {
			return operationPlan{failure: financial.FailureInvalidWinReference}, nil
		}
		switch ref.State() {
		case financial.StatePending, financial.StatePendingReference:
			return operationPlan{pending: true}, nil
		case financial.StateRejected, financial.StateFailed:
			return operationPlan{failure: financial.FailureReferenceNotProcessed}, nil
		case financial.StateProcessed:
			return operationPlan{
				movement:    &movement{direction: financial.DirectionCredit, amount: txn.Amount()},
				referenceID: ref.ID(),
			}, nil
		default:
			return operationPlan{}, fmt.Errorf("%w: reference state %q is not documented", errInvariant, ref.State())
		}
	case financial.KindRefund, financial.KindRollback:
		return classifyReversal(txn, ref, claimExists)
	default:
		return operationPlan{}, fmt.Errorf("%w: kind %q is not processed by the application", errInvariant, txn.Kind())
	}
}

func classifyReversal(txn financial.WagerTransaction, ref *financial.WagerTransaction, claimExists bool) (operationPlan, error) {
	if ref == nil {
		return operationPlan{pending: true}, nil
	}
	allowed := false
	switch txn.Kind() {
	case financial.KindRefund:
		allowed = ref.Kind() == financial.KindBet
	case financial.KindRollback:
		allowed = ref.Kind() == financial.KindBet || ref.Kind() == financial.KindWin || ref.Kind() == financial.KindRefund
	}
	if !allowed {
		return operationPlan{failure: financial.FailureReferenceKindNotAllowed}, nil
	}
	matches, err := identityMatches(txn, *ref)
	if err != nil {
		return operationPlan{}, err
	}
	if !matches {
		return operationPlan{failure: financial.FailureReferenceMismatch}, nil
	}
	equal, err := txn.Amount().Equal(ref.Amount())
	if err != nil {
		return operationPlan{}, err
	}
	if !equal {
		return operationPlan{failure: financial.FailureReversalAmountMismatch}, nil
	}
	switch ref.State() {
	case financial.StatePending, financial.StatePendingReference:
		return operationPlan{pending: true}, nil
	case financial.StateRejected, financial.StateFailed:
		return operationPlan{failure: financial.FailureReferenceNotProcessed}, nil
	case financial.StateProcessed:
		if claimExists {
			return operationPlan{failure: financial.FailureAlreadyReversed}, nil
		}
		direction := financial.DirectionCredit
		if txn.Kind() == financial.KindRollback && ref.Kind() != financial.KindBet {
			direction = financial.DirectionDebit
		}
		return operationPlan{
			movement:    &movement{direction: direction, amount: ref.Amount()},
			claim:       true,
			referenceID: ref.ID(),
		}, nil
	default:
		return operationPlan{}, fmt.Errorf("%w: reference state %q is not documented", errInvariant, ref.State())
	}
}

// identityMatches verifies the player, wallet, currency and round shared by a
// dependent operation and its resolved reference.
func identityMatches(txn, ref financial.WagerTransaction) (bool, error) {
	if txn.WalletID() != ref.WalletID() || txn.PlayerID() != ref.PlayerID() || txn.RoundID() != ref.RoundID() {
		return false, nil
	}
	if txn.Amount().Currency() != ref.Amount().Currency() {
		return false, nil
	}
	return true, nil
}
