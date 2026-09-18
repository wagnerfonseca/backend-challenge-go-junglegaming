package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/event"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

// maxConcurrentWriteRetries bounds how often one submit retries after a
// uniqueness collision with a concurrent equivalent command.
const maxConcurrentWriteRetries = 3

// SystemClock reads the wall clock.
type SystemClock struct{}

// Now returns the current instant in UTC.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// WagerService implements the wallet and wager use cases shared by every
// ingress. Construct it with manual injection.
type WagerService struct {
	store       Store
	clock       Clock
	reconciler  Reconciler
	txFailpoint TxFailpoint
}

// NewWagerService wires the use cases to their ports.
func NewWagerService(store Store, clock Clock, options ...Option) *WagerService {
	service := &WagerService{store: store, clock: clock}
	for _, option := range options {
		option(service)
	}
	return service
}

// SubmitWagerFromInbox processes one broker delivery through the same
// financial use case as HTTP. The inbox record and the financial result commit
// in the same SQL transaction, so no durable inbox row exists without its
// domain result. A redelivery of a completed message with the same digest is
// reported as a duplicate without a second financial movement; a different
// digest is a permanent INBOX_PAYLOAD_CONFLICT.
func (s *WagerService) SubmitWagerFromInbox(ctx context.Context, cmd SubmitWagerCommand, delivery InboxDelivery) (WagerResult, bool, error) {
	if err := cmd.Validate(); err != nil {
		return WagerResult{}, false, err
	}
	if err := delivery.Validate(); err != nil {
		return WagerResult{}, false, err
	}
	projection := cmd.projection()
	digest, err := DigestOf(projection)
	if err != nil {
		return WagerResult{}, false, wrapInvariant(err)
	}
	correlation := correlationOrNew(cmd.CorrelationID)
	var result WagerResult
	var duplicate bool
	var lastConflict error
	for attempt := 0; attempt < maxConcurrentWriteRetries; attempt++ {
		duplicate = false
		err := s.store.InTx(ctx, func(ctx context.Context, repos Repositories) error {
			found, storedDigest, err := repos.Inbox.Begin(ctx, delivery)
			if err != nil {
				return err
			}
			if found {
				if storedDigest == delivery.Digest {
					duplicate = true
					return nil
				}
				return conflictError(CodeInboxPayloadConflict, "message id was already received with a different payload", nil)
			}
			result, err = s.submitWithinTx(ctx, repos, cmd, digest, correlation)
			if err != nil {
				return err
			}
			return repos.Inbox.Complete(ctx, delivery.ConsumerName, delivery.MessageID, s.clock.Now())
		})
		if err == nil {
			return result, duplicate, nil
		}
		if errors.Is(err, ErrConcurrentWrite) {
			lastConflict = err
			continue
		}
		return WagerResult{}, false, err
	}
	return WagerResult{}, false, transientError(fmt.Errorf("inbox submission could not observe a stable result after retries: %w", lastConflict))
}

// OpenWallet creates one wallet per player/currency and, for a positive
// balance, records the internal OPENING transaction, its ledger entry and its
// outbox events in the same commit. Zero-balance wallets persist no financial
// row at all.
func (s *WagerService) OpenWallet(ctx context.Context, cmd OpenWalletCommand) (WalletView, error) {
	if err := cmd.Validate(); err != nil {
		return WalletView{}, err
	}
	now := s.clock.Now()
	wallet, err := financial.NewWallet(financial.NewWalletID(), cmd.PlayerID, cmd.InitialBalance.Currency(), cmd.InitialBalance, now)
	if err != nil {
		return WalletView{}, contractError(CodeInvalidRequest, err.Error())
	}
	correlation := correlationOrNew(cmd.CorrelationID)

	err = s.store.InTx(ctx, func(ctx context.Context, repos Repositories) error {
		if err := repos.Wallets.Insert(ctx, wallet); err != nil {
			if errors.Is(err, ErrWalletExists) {
				return conflictError(CodeWalletAlreadyExists, "wallet already exists for player and currency", err)
			}
			return err
		}
		if !wallet.Balance().IsPositive() {
			return nil
		}
		opening, err := financial.NewInternalOpening(financial.InternalOpeningParams{
			ID:       financial.NewTransactionID(),
			WalletID: wallet.ID(),
			PlayerID: wallet.PlayerID(),
			Amount:   wallet.Balance(),
			Now:      now,
		})
		if err != nil {
			return wrapInvariant(err)
		}
		processed, err := opening.MarkProcessed(financial.TransactionID{}, wallet.Balance(), now)
		if err != nil {
			return wrapInvariant(err)
		}
		if err := repos.Transactions.Insert(ctx, processed); err != nil {
			return err
		}
		zero, err := money.Zero(wallet.Currency())
		if err != nil {
			return wrapInvariant(err)
		}
		entry, err := financial.NewWalletLedgerEntry(
			financial.NewLedgerEntryID(), wallet.ID(), processed.ID(),
			financial.DirectionCredit, wallet.Balance(), zero, wallet.Balance(), now,
		)
		if err != nil {
			return wrapInvariant(err)
		}
		if err := repos.Ledger.Insert(ctx, entry); err != nil {
			return err
		}
		if err := s.emitProcessed(ctx, repos, wallet, processed, wallet.Balance(), now, correlation); err != nil {
			return err
		}
		return s.emitBalanceChanged(ctx, repos, wallet, processed, &movement{
			direction: financial.DirectionCredit,
			amount:    wallet.Balance(),
		}, zero, wallet.Balance(), now, correlation)
	})
	if err != nil {
		return WalletView{}, err
	}
	return walletView(wallet), nil
}

// SubmitWagerTransaction validates an external command, applies idempotency
// and processes the operation in one financial transaction. A replay returns
// the persisted result snapshot; identity conflicts never mutate the first
// result.
func (s *WagerService) SubmitWagerTransaction(ctx context.Context, cmd SubmitWagerCommand) (WagerResult, error) {
	if err := cmd.Validate(); err != nil {
		return WagerResult{}, err
	}
	projection := cmd.projection()
	digest, err := DigestOf(projection)
	if err != nil {
		return WagerResult{}, wrapInvariant(err)
	}
	correlation := correlationOrNew(cmd.CorrelationID)
	var result WagerResult
	var lastConflict error
	for attempt := 0; attempt < maxConcurrentWriteRetries; attempt++ {
		err := s.store.InTx(ctx, func(ctx context.Context, repos Repositories) error {
			var err error
			result, err = s.submitWithinTx(ctx, repos, cmd, digest, correlation)
			if err != nil {
				return err
			}
			return s.hitFailpoint("before_commit")
		})
		if err == nil {
			return result, nil
		}
		if errors.Is(err, ErrConcurrentWrite) {
			lastConflict = err
			continue
		}
		return WagerResult{}, err
	}
	return WagerResult{}, transientError(fmt.Errorf("wager submission could not observe a stable result after retries: %w", lastConflict))
}

func (s *WagerService) submitWithinTx(ctx context.Context, repos Repositories, cmd SubmitWagerCommand, digest, correlation string) (WagerResult, error) {
	existing, found, err := repos.Transactions.ByProviderAndKey(ctx, cmd.ProviderID, cmd.IdempotencyKey)
	if err != nil {
		return WagerResult{}, err
	}
	if found {
		if existing.Digest() == digest && existing.DigestVersion() == DigestVersion {
			return resultOf(existing, true), nil
		}
		return WagerResult{}, conflictError(CodeIdempotencyConflict, "idempotency key already used with a different business projection", nil)
	}
	conflicting, found, err := repos.Transactions.ByProviderAndExternalID(ctx, cmd.ProviderID, cmd.ExternalTransactionID)
	if err != nil {
		return WagerResult{}, err
	}
	if found {
		// A concurrent equivalent command can commit between the two lookups.
		// The stored key decides whether this is our own result or a genuine
		// external-identity conflict.
		if conflicting.IdempotencyKey() == cmd.IdempotencyKey {
			if conflicting.Digest() == digest && conflicting.DigestVersion() == DigestVersion {
				return resultOf(conflicting, true), nil
			}
			return WagerResult{}, conflictError(CodeIdempotencyConflict, "idempotency key already used with a different business projection", nil)
		}
		return WagerResult{}, conflictError(CodeExternalTransactionConflict, "external transaction already used under another idempotency key", nil)
	}
	wall, found, err := repos.Wallets.LockByID(ctx, cmd.WalletID)
	if err != nil {
		return WagerResult{}, err
	}
	if !found {
		return WagerResult{}, notFoundError("wallet not found")
	}
	if wall.Currency() != cmd.Amount.Currency() {
		return WagerResult{}, contractError(CodeUnsupportedCurrency, "movement currency does not match the wallet currency")
	}
	now := s.clock.Now()
	txn, err := financial.NewExternalTransaction(financial.NewExternalTransactionParams{
		ID:                    financial.NewTransactionID(),
		ProviderID:            cmd.ProviderID,
		ExternalTransactionID: cmd.ExternalTransactionID,
		IdempotencyKey:        cmd.IdempotencyKey,
		DigestVersion:         DigestVersion,
		Digest:                digest,
		WalletID:              cmd.WalletID,
		PlayerID:              cmd.PlayerID,
		RoundID:               cmd.RoundID,
		GameID:                cmd.GameID,
		Kind:                  cmd.Kind,
		Amount:                cmd.Amount,
		ReferenceExternalID:   cmd.ReferenceExternalID,
		Now:                   now,
	})
	if err != nil {
		return WagerResult{}, contractError(CodeInvalidRequest, err.Error())
	}
	if err := repos.Transactions.Insert(ctx, txn); err != nil {
		return WagerResult{}, err
	}
	return s.resolveAndExecute(ctx, repos, wall, txn, now, correlation)
}

// ResolvePendingReference advances one accepted PENDING_REFERENCE transaction:
// it resolves the reference, applies the movement or rejection, reschedules
// with deterministic backoff while the reference is absent, and expires it at
// the 24-hour deadline. A terminal transaction is returned unchanged.
func (s *WagerService) ResolvePendingReference(ctx context.Context, cmd ResolvePendingReferenceCommand) (WagerResult, error) {
	if err := cmd.Validate(); err != nil {
		return WagerResult{}, err
	}
	correlation := correlationOrNew(cmd.CorrelationID)
	var result WagerResult
	err := s.store.InTx(ctx, func(ctx context.Context, repos Repositories) error {
		txn, attempts, found, err := repos.Transactions.LockByID(ctx, cmd.TransactionID)
		if err != nil {
			return err
		}
		if !found {
			return notFoundError("transaction not found")
		}
		if txn.IsTerminal() {
			result = resultOf(txn, false)
			return nil
		}
		if txn.State() != financial.StatePendingReference {
			return contractError(CodeInvalidRequest, "transaction is not waiting for a reference")
		}
		wall, found, err := repos.Wallets.LockByID(ctx, txn.WalletID())
		if err != nil {
			return err
		}
		if !found {
			return corruptRecord(fmt.Errorf("wallet %s of transaction %s is missing", txn.WalletID(), txn.ID()))
		}
		now := s.clock.Now()
		ref, err := s.loadReference(ctx, repos, txn)
		if err != nil {
			if errors.Is(err, ErrCorruptPersistedRecord) {
				failed, ferr := txn.MarkFailed(wall.Balance(), now)
				if ferr != nil {
					return wrapInvariant(ferr)
				}
				if uerr := repos.Transactions.Update(ctx, failed, nil); uerr != nil {
					return uerr
				}
				result = resultOf(failed, false)
				return nil
			}
			return err
		}
		plan, err := s.planOperation(ctx, repos, txn, ref)
		if err != nil {
			return err
		}
		if plan.pending {
			if !now.Before(txn.ReferenceDeadline()) {
				return s.rejectReferenceNotFound(ctx, repos, wall, txn, now, correlation, &result)
			}
			return s.rescheduleReference(ctx, repos, txn, attempts, now, &result)
		}
		return s.executePlan(ctx, repos, wall, txn, plan, now, correlation, &result)
	})
	if err != nil {
		return WagerResult{}, err
	}
	return result, nil
}

// WalletByID reads one wallet.
func (s *WagerService) WalletByID(ctx context.Context, id financial.WalletID) (WalletView, error) {
	if id.IsZero() {
		return WalletView{}, contractError(CodeInvalidRequest, "walletId is required")
	}
	wallet, found, err := s.store.Repositories().Wallets.ByID(ctx, id)
	if err != nil {
		return WalletView{}, err
	}
	if !found {
		return WalletView{}, notFoundError("wallet not found")
	}
	return walletView(wallet), nil
}

// TransactionByID reads one transaction result. A PENDING_REFERENCE result
// carries its deadline and no terminal balance.
func (s *WagerService) TransactionByID(ctx context.Context, id financial.TransactionID) (WagerResult, error) {
	if id.IsZero() {
		return WagerResult{}, contractError(CodeInvalidRequest, "transactionId is required")
	}
	txn, found, err := s.store.Repositories().Transactions.ByID(ctx, id)
	if err != nil {
		return WagerResult{}, err
	}
	if !found {
		return WagerResult{}, notFoundError("transaction not found")
	}
	return resultOf(txn, false), nil
}

// resolveAndExecute is the shared path of a first submission and a reference
// recovery pass: it loads the dependency, or applies the operation directly
// when no reference is required.
func (s *WagerService) resolveAndExecute(ctx context.Context, repos Repositories, wall financial.Wallet, txn financial.WagerTransaction, now time.Time, correlation string) (WagerResult, error) {
	if !needsReference(txn) {
		var result WagerResult
		if err := s.executeWithReference(ctx, repos, wall, txn, nil, now, correlation, &result); err != nil {
			return WagerResult{}, err
		}
		return result, nil
	}
	ref, err := s.loadReference(ctx, repos, txn)
	if err != nil {
		if errors.Is(err, ErrCorruptPersistedRecord) {
			failed, ferr := txn.MarkFailed(wall.Balance(), now)
			if ferr != nil {
				return WagerResult{}, wrapInvariant(ferr)
			}
			if uerr := repos.Transactions.Update(ctx, failed, nil); uerr != nil {
				return WagerResult{}, uerr
			}
			return resultOf(failed, false), nil
		}
		return WagerResult{}, err
	}
	var result WagerResult
	if err := s.executeWithReference(ctx, repos, wall, txn, ref, now, correlation, &result); err != nil {
		return WagerResult{}, err
	}
	return result, nil
}

func (s *WagerService) loadReference(ctx context.Context, repos Repositories, txn financial.WagerTransaction) (*financial.WagerTransaction, error) {
	if !needsReference(txn) {
		return nil, nil
	}
	ref, found, err := repos.Transactions.ByProviderAndExternalID(ctx, txn.ProviderID(), txn.ReferenceExternalTransactionID())
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &ref, nil
}

// planOperation classifies one operation against its optional resolved
// reference. It performs no persistence and fills the terminal reference
// identity required by C211.
func (s *WagerService) planOperation(ctx context.Context, repos Repositories, txn financial.WagerTransaction, ref *financial.WagerTransaction) (operationPlan, error) {
	claimExists := false
	if ref != nil && isClaimingKind(txn.Kind()) && ref.State() == financial.StateProcessed {
		_, found, err := repos.ReversalClaims.ByReference(ctx, ref.ID())
		if err != nil {
			return operationPlan{}, err
		}
		claimExists = found
	}
	plan, err := classify(txn, ref, claimExists)
	if err != nil {
		return operationPlan{}, wrapInvariant(err)
	}
	if ref != nil && !plan.pending && plan.referenceID.IsZero() {
		// C211: any reference that resolves to an existing transaction is
		// persisted with the resulting terminal state in the same commit.
		plan.referenceID = ref.ID()
	}
	return plan, nil
}

// executeWithReference classifies and applies one operation for the first
// submission path, persisting the terminal state with its reference identity,
// movement, ledger entry, claim and outbox events.
func (s *WagerService) executeWithReference(ctx context.Context, repos Repositories, wall financial.Wallet, txn financial.WagerTransaction, ref *financial.WagerTransaction, now time.Time, correlation string, result *WagerResult) error {
	plan, err := s.planOperation(ctx, repos, txn, ref)
	if err != nil {
		return err
	}
	return s.executePlan(ctx, repos, wall, txn, plan, now, correlation, result)
}

// executePlan persists one classified outcome inside the current transaction.
func (s *WagerService) executePlan(ctx context.Context, repos Repositories, wall financial.Wallet, txn financial.WagerTransaction, plan operationPlan, now time.Time, correlation string, result *WagerResult) error {
	if plan.pending {
		deadline := now.Add(ReferenceTTL)
		pending, err := txn.MarkPendingReference(deadline, now)
		if err != nil {
			return wrapInvariant(err)
		}
		schedule := &ReferenceSchedule{
			NextAttemptAt: ReferenceNextAttemptAt(0, txn.ID().String(), now),
			Attempts:      0,
		}
		if err := repos.Transactions.Update(ctx, pending, schedule); err != nil {
			return err
		}
		if err := s.emitPendingReference(ctx, repos, pending, now, correlation); err != nil {
			return err
		}
		*result = resultOf(pending, false)
		return nil
	}
	if plan.failure != "" {
		rejected, err := txn.MarkRejected(plan.referenceID, plan.failure, wall.Balance(), now)
		if err != nil {
			return wrapInvariant(err)
		}
		if err := repos.Transactions.Update(ctx, rejected, nil); err != nil {
			return err
		}
		if err := s.emitRejected(ctx, repos, wall, rejected, now, correlation); err != nil {
			return err
		}
		*result = resultOf(rejected, false)
		return nil
	}
	if plan.movement == nil {
		processed, err := txn.MarkProcessed(plan.referenceID, wall.Balance(), now)
		if err != nil {
			return wrapInvariant(err)
		}
		if err := repos.Transactions.Update(ctx, processed, nil); err != nil {
			return err
		}
		if err := s.emitProcessed(ctx, repos, wall, processed, wall.Balance(), now, correlation); err != nil {
			return err
		}
		*result = resultOf(processed, false)
		return nil
	}
	return s.applyMovement(ctx, repos, wall, txn, plan, now, correlation, result)
}

func (s *WagerService) applyMovement(ctx context.Context, repos Repositories, wall financial.Wallet, txn financial.WagerTransaction, plan operationPlan, now time.Time, correlation string, result *WagerResult) error {
	var updated financial.Wallet
	var ledgerEntry financial.WalletLedgerEntry
	var err error
	switch plan.movement.direction {
	case financial.DirectionCredit:
		updated, err = wall.Credit(plan.movement.amount, now)
	case financial.DirectionDebit:
		updated, err = wall.Debit(plan.movement.amount, now)
	default:
		return wrapInvariant(fmt.Errorf("unsupported ledger direction %q", plan.movement.direction))
	}
	if err != nil {
		if errors.Is(err, financial.ErrInsufficientFunds) {
			code := financial.FailureInsufficientFunds
			if txn.Kind() != financial.KindBet {
				code = financial.FailureReversalInsufficientFunds
			}
			rejected, rerr := txn.MarkRejected(plan.referenceID, code, wall.Balance(), now)
			if rerr != nil {
				return wrapInvariant(rerr)
			}
			if uerr := repos.Transactions.Update(ctx, rejected, nil); uerr != nil {
				return uerr
			}
			if eerr := s.emitRejected(ctx, repos, wall, rejected, now, correlation); eerr != nil {
				return eerr
			}
			*result = resultOf(rejected, false)
			return nil
		}
		return err
	}
	entry, err := financial.NewWalletLedgerEntry(
		financial.NewLedgerEntryID(), updated.ID(), txn.ID(),
		plan.movement.direction, plan.movement.amount,
		wall.Balance(), updated.Balance(), now,
	)
	if err != nil {
		return wrapInvariant(err)
	}
	ledgerEntry = entry
	processed, err := txn.MarkProcessed(plan.referenceID, updated.Balance(), now)
	if err != nil {
		return wrapInvariant(err)
	}
	if err := repos.Wallets.SaveBalance(ctx, updated); err != nil {
		return err
	}
	if err := repos.Ledger.Insert(ctx, ledgerEntry); err != nil {
		return err
	}
	if plan.claim {
		claim, err := financial.NewReversalClaim(financial.NewReversalClaimID(), plan.referenceID, txn.ID(), now)
		if err != nil {
			return wrapInvariant(err)
		}
		if err := repos.ReversalClaims.Insert(ctx, claim); err != nil {
			return err
		}
	}
	if err := repos.Transactions.Update(ctx, processed, nil); err != nil {
		return err
	}
	if err := s.emitProcessed(ctx, repos, updated, processed, updated.Balance(), now, correlation); err != nil {
		return err
	}
	if err := s.emitBalanceChanged(ctx, repos, updated, processed, plan.movement, wall.Balance(), updated.Balance(), now, correlation); err != nil {
		return err
	}
	*result = resultOf(processed, false)
	return nil
}

func (s *WagerService) rejectReferenceNotFound(ctx context.Context, repos Repositories, wall financial.Wallet, txn financial.WagerTransaction, now time.Time, correlation string, result *WagerResult) error {
	rejected, err := txn.MarkRejected(financial.TransactionID{}, financial.FailureReferenceNotFound, wall.Balance(), now)
	if err != nil {
		return wrapInvariant(err)
	}
	if err := repos.Transactions.Update(ctx, rejected, nil); err != nil {
		return err
	}
	if err := s.emitRejected(ctx, repos, wall, rejected, now, correlation); err != nil {
		return err
	}
	*result = resultOf(rejected, false)
	return nil
}

func (s *WagerService) rescheduleReference(ctx context.Context, repos Repositories, txn financial.WagerTransaction, attempts int, now time.Time, result *WagerResult) error {
	nextAttempt := attempts + 1
	nextAt := ReferenceNextAttemptAt(nextAttempt, txn.ID().String(), now)
	if err := repos.Transactions.RescheduleReference(ctx, txn.ID(), now, nextAt, nextAttempt); err != nil {
		return err
	}
	rescheduled, err := financial.RehydrateTransaction(financial.RehydrateTransactionParams{
		ID:                     txn.ID(),
		Origin:                 txn.Origin(),
		Kind:                   txn.Kind(),
		ProviderID:             txn.ProviderID(),
		ExternalTransactionID:  txn.ExternalTransactionID(),
		IdempotencyKey:         txn.IdempotencyKey(),
		DigestVersion:          txn.DigestVersion(),
		Digest:                 txn.Digest(),
		WalletID:               txn.WalletID(),
		PlayerID:               txn.PlayerID(),
		RoundID:                txn.RoundID(),
		GameID:                 txn.GameID(),
		Amount:                 txn.Amount(),
		ReferenceExternalID:    txn.ReferenceExternalTransactionID(),
		ReferenceTransactionID: txn.ReferenceTransactionID(),
		State:                  txn.State(),
		FailureCode:            txn.FailureCode(),
		ObservedBalance:        txn.ObservedBalance(),
		ReferenceDeadline:      txn.ReferenceDeadline(),
		CreatedAt:              txn.CreatedAt(),
		UpdatedAt:              now,
	})
	if err != nil {
		return wrapInvariant(err)
	}
	*result = resultOf(rescheduled, false)
	return nil
}

func (s *WagerService) emitProcessed(ctx context.Context, repos Repositories, wallet financial.Wallet, txn financial.WagerTransaction, balance money.Money, now time.Time, correlation string) error {
	envelope, err := event.NewWagerTransactionProcessed(event.Metadata{
		EventID:       newEventID(),
		AggregateID:   wallet.ID().String(),
		CorrelationID: correlation,
		OccurredAt:    now,
	}, event.ProcessedData{
		TransactionID:         txn.ID().String(),
		Origin:                string(txn.Origin()),
		ProviderID:            txn.ProviderID().String(),
		ExternalTransactionID: txn.ExternalTransactionID().String(),
		WalletID:              wallet.ID().String(),
		PlayerID:              wallet.PlayerID().String(),
		RoundID:               txn.RoundID().String(),
		GameID:                txn.GameID().String(),
		Kind:                  string(txn.Kind()),
		Money:                 txn.Amount(),
		Balance:               balance,
	})
	if err != nil {
		return wrapInvariant(err)
	}
	return repos.Outbox.Insert(ctx, envelope, now)
}

func (s *WagerService) emitRejected(ctx context.Context, repos Repositories, wallet financial.Wallet, txn financial.WagerTransaction, now time.Time, correlation string) error {
	envelope, err := event.NewWagerTransactionRejected(event.Metadata{
		EventID:       newEventID(),
		AggregateID:   wallet.ID().String(),
		CorrelationID: correlation,
		OccurredAt:    now,
	}, event.RejectedData{
		TransactionID:                  txn.ID().String(),
		ProviderID:                     txn.ProviderID().String(),
		ExternalTransactionID:          txn.ExternalTransactionID().String(),
		WalletID:                       wallet.ID().String(),
		Kind:                           string(txn.Kind()),
		Money:                          txn.Amount(),
		FailureCode:                    string(txn.FailureCode()),
		Balance:                        txn.ObservedBalance(),
		ReferenceExternalTransactionID: txn.ReferenceExternalTransactionID().String(),
	})
	if err != nil {
		return wrapInvariant(err)
	}
	return repos.Outbox.Insert(ctx, envelope, now)
}

func (s *WagerService) emitPendingReference(ctx context.Context, repos Repositories, txn financial.WagerTransaction, now time.Time, correlation string) error {
	envelope, err := event.NewWagerTransactionPendingReference(event.Metadata{
		EventID:       newEventID(),
		AggregateID:   txn.WalletID().String(),
		CorrelationID: correlation,
		OccurredAt:    now,
	}, event.PendingReferenceData{
		TransactionID:                  txn.ID().String(),
		ProviderID:                     txn.ProviderID().String(),
		ExternalTransactionID:          txn.ExternalTransactionID().String(),
		WalletID:                       txn.WalletID().String(),
		Kind:                           string(txn.Kind()),
		Money:                          txn.Amount(),
		ReferenceExternalTransactionID: txn.ReferenceExternalTransactionID().String(),
		ExpiresAt:                      event.Timestamp(txn.ReferenceDeadline()),
	})
	if err != nil {
		return wrapInvariant(err)
	}
	return repos.Outbox.Insert(ctx, envelope, now)
}

func (s *WagerService) emitBalanceChanged(ctx context.Context, repos Repositories, wallet financial.Wallet, txn financial.WagerTransaction, move *movement, balanceBefore, balanceAfter money.Money, now time.Time, correlation string) error {
	envelope, err := event.NewWalletBalanceChanged(event.Metadata{
		EventID:       newEventID(),
		AggregateID:   wallet.ID().String(),
		CorrelationID: correlation,
		OccurredAt:    now,
	}, event.BalanceChangedData{
		WalletID:      wallet.ID().String(),
		TransactionID: txn.ID().String(),
		Direction:     string(move.direction),
		Money:         move.amount,
		BalanceBefore: balanceBefore,
		BalanceAfter:  balanceAfter,
		WalletVersion: wallet.Version(),
	})
	if err != nil {
		return wrapInvariant(err)
	}
	return repos.Outbox.Insert(ctx, envelope, now)
}

func walletView(wallet financial.Wallet) WalletView {
	return WalletView{
		ID:        wallet.ID(),
		PlayerID:  wallet.PlayerID(),
		Currency:  wallet.Currency(),
		Balance:   wallet.Balance(),
		Version:   wallet.Version(),
		CreatedAt: wallet.CreatedAt(),
		UpdatedAt: wallet.UpdatedAt(),
	}
}

func resultOf(txn financial.WagerTransaction, replay bool) WagerResult {
	return WagerResult{
		TransactionID:     txn.ID(),
		Kind:              txn.Kind(),
		State:             txn.State(),
		ObservedBalance:   txn.ObservedBalance(),
		FailureCode:       txn.FailureCode(),
		ReferenceDeadline: txn.ReferenceDeadline(),
		IdempotentReplay:  replay,
		CreatedAt:         txn.CreatedAt(),
		UpdatedAt:         txn.UpdatedAt(),
	}
}

func needsReference(txn financial.WagerTransaction) bool {
	if !txn.ReferenceExternalTransactionID().IsZero() {
		return true
	}
	return false
}

func isClaimingKind(kind financial.Kind) bool {
	switch kind {
	case financial.KindRefund, financial.KindRollback:
		return true
	default:
		return false
	}
}

func correlationOrNew(correlation string) string {
	if correlation != "" {
		return correlation
	}
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

func newEventID() string {
	id, err := uuid.NewV7()
	if err != nil {
		panic(fmt.Sprintf("application: generating event identity: %v", err))
	}
	return id.String()
}

func wrapInvariant(err error) error {
	return fmt.Errorf("%w: %w", errInvariant, err)
}

func corruptRecord(err error) error {
	return fmt.Errorf("%w: %w", ErrCorruptPersistedRecord, err)
}
