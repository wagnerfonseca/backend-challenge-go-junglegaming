// Package outbox publishes committed event snapshots from the durable outbox.
// It claims batches with a recoverable lease and never discards an event.
package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/failpoint"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/metrics"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
)

// Publication policy fixed by the approved plan.
const (
	// Batch is the largest claimed batch.
	Batch = 50
	// Lease is how long a claimed batch stays owned before recovery.
	Lease = 30 * time.Second
	// BackoffBase is the first retry delay.
	BackoffBase = time.Second
	// BackoffCap is the largest retry delay.
	BackoffCap = 5 * time.Minute
	// PollInterval is the pause between empty passes.
	PollInterval = time.Second
)

// Backoff returns the retry delay for a claim count: 1s doubled per attempt,
// capped at 5 minutes, with no attempt limit.
func Backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := BackoffBase
	for i := 1; i < attempts; i++ {
		delay *= 2
		if delay >= BackoffCap {
			return BackoffCap
		}
	}
	return delay
}

// Sender publishes one immutable event snapshot.
type Sender interface {
	Publish(ctx context.Context, record application.OutboxRecord) error
}

// Publisher claims and publishes due outbox events.
type Publisher struct {
	store      application.OutboxStore
	sender     Sender
	metrics    *metrics.Metrics
	logger     *slog.Logger
	failpoints *failpoint.Set
}

// Option customizes the publisher.
type Option func(*Publisher)

// WithMetrics attaches the metric bundle.
func WithMetrics(bundle *metrics.Metrics) Option { return func(p *Publisher) { p.metrics = bundle } }

// WithLogger attaches the structured logger.
func WithLogger(logger *slog.Logger) Option { return func(p *Publisher) { p.logger = logger } }

// WithFailpoints attaches the integration failpoint set.
func WithFailpoints(set *failpoint.Set) Option { return func(p *Publisher) { p.failpoints = set } }

// New builds the publisher.
func New(store application.OutboxStore, sender Sender, options ...Option) *Publisher {
	p := &Publisher{store: store, sender: sender}
	for _, option := range options {
		option(p)
	}
	if p.metrics == nil {
		p.metrics = metrics.New(metrics.NewRegistry())
	}
	if p.logger == nil {
		p.logger = slog.Default()
	}
	if p.failpoints == nil {
		p.failpoints = failpoint.New(nil)
	}
	return p
}

// Run publishes until the context is canceled.
func (p *Publisher) Run(ctx context.Context) error {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := p.PublishOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			p.logger.WarnContext(ctx, "outbox publication pass failed", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// PublishOnce claims and publishes one batch, returning how many events were
// confirmed.
func (p *Publisher) PublishOnce(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	records, err := p.store.ClaimDueEvents(ctx, now, Batch, Lease)
	if err != nil {
		return 0, err
	}
	if age, ok, err := p.store.OldestPendingAge(ctx, now); err == nil && ok {
		p.metrics.OutboxOldestPendingSeconds.Set(age.Seconds())
	}
	published := 0
	for _, record := range records {
		if ctx.Err() != nil {
			return published, nil
		}
		if err := p.sender.Publish(ctx, record); err != nil {
			p.metrics.WagerRetriesTotal.Inc("outbox", "publish")
			nextAttemptAt := now.Add(Backoff(record.Attempts))
			if rescheduleErr := p.store.RescheduleEvent(ctx, record.EventID, now, nextAttemptAt, record.Attempts); rescheduleErr != nil {
				p.logger.WarnContext(ctx, "outbox reschedule failed", "eventId", record.EventID, "error", rescheduleErr.Error())
			}
			continue
		}
		p.failpoints.Hit(failpoint.OutboxAfterPublish)
		if err := p.store.MarkPublished(ctx, record.EventID, now); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}
