// Package worker recovers accepted PENDING_REFERENCE transactions durably:
// each item is claimed with a 30-second lease in batches of at most 50 and
// resolved through the shared application use case.
package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/failpoint"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/metrics"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// Claim policy fixed by the approved plan.
const (
	// ClaimBatch is the largest claimed batch.
	ClaimBatch = 50
	// ClaimLease is how long a claimed batch stays owned.
	ClaimLease = 30 * time.Second
	// PollInterval is the pause between passes.
	PollInterval = time.Second
)

// ReferenceStore claims due PENDING_REFERENCE transactions.
type ReferenceStore interface {
	ClaimDueReferences(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]financial.WagerTransaction, error)
}

// Resolver is the shared reference recovery use case.
type Resolver interface {
	ResolvePendingReference(ctx context.Context, cmd application.ResolvePendingReferenceCommand) (application.WagerResult, error)
}

// ReferenceWorker advances due pending references.
type ReferenceWorker struct {
	store      ReferenceStore
	resolver   Resolver
	metrics    *metrics.Metrics
	logger     *slog.Logger
	failpoints *failpoint.Set
}

// Option customizes the worker.
type Option func(*ReferenceWorker)

// WithMetrics attaches the metric bundle.
func WithMetrics(bundle *metrics.Metrics) Option {
	return func(w *ReferenceWorker) { w.metrics = bundle }
}

// WithLogger attaches the structured logger.
func WithLogger(logger *slog.Logger) Option { return func(w *ReferenceWorker) { w.logger = logger } }

// WithFailpoints attaches the integration failpoint set.
func WithFailpoints(set *failpoint.Set) Option {
	return func(w *ReferenceWorker) { w.failpoints = set }
}

// New builds the reference worker.
func New(store ReferenceStore, resolver Resolver, options ...Option) *ReferenceWorker {
	w := &ReferenceWorker{store: store, resolver: resolver}
	for _, option := range options {
		option(w)
	}
	if w.metrics == nil {
		w.metrics = metrics.New(metrics.NewRegistry())
	}
	if w.logger == nil {
		w.logger = slog.Default()
	}
	if w.failpoints == nil {
		w.failpoints = failpoint.New(nil)
	}
	return w
}

// Run resolves due references until the context is canceled.
func (w *ReferenceWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := w.ResolveOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.logger.WarnContext(ctx, "reference worker pass failed", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// ResolveOnce claims and resolves one batch, returning how many reached a
// terminal state.
func (w *ReferenceWorker) ResolveOnce(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	claimed, err := w.store.ClaimDueReferences(ctx, now, ClaimBatch, ClaimLease)
	if err != nil {
		return 0, err
	}
	resolved := 0
	for _, transaction := range claimed {
		if ctx.Err() != nil {
			return resolved, nil
		}
		result, err := w.resolver.ResolvePendingReference(ctx, application.ResolvePendingReferenceCommand{
			TransactionID: transaction.ID(),
			// The transaction identity is the stable correlation anchor of the
			// accepted work when no ingress correlation survives.
			CorrelationID: transaction.ID().String(),
		})
		if err != nil {
			w.metrics.WagerRetriesTotal.Inc("reference", "transient")
			w.logger.WarnContext(ctx, "reference resolution failed", "transactionId", transaction.ID().String(), "error", err.Error())
			continue
		}
		if result.IsTerminal() {
			resolved++
			w.metrics.WagerTransactionsTotal.Inc(string(result.Kind), string(result.State), "reference")
		}
	}
	return resolved, nil
}
