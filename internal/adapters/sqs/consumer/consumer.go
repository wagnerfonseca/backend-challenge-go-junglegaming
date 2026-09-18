package consumer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/failpoint"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/metrics"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
)

// Approved polling and redelivery parameters.
const (
	ReceiveWaitTime      = 20 * time.Second
	ReceiveBatch         = 10
	VisibilityTimeout    = 60 * time.Second
	VisibilityRenewal    = 20 * time.Second
	ShutdownGrace        = 30 * time.Second
	TransientBackoffBase = 5 * time.Second
	MaxReceiveCount      = 5
)

// TransientVisibility returns the visibility backoff for a transient failure
// at the given receive count: 5s, 10s, 20s and 40s for counts 1 to 4. From the
// fifth receive on the message is left to the configured redrive.
func TransientVisibility(receiveCount int) (time.Duration, bool) {
	if receiveCount < 1 {
		receiveCount = 1
	}
	if receiveCount >= MaxReceiveCount {
		return 0, false
	}
	return TransientBackoffBase * time.Duration(1<<(receiveCount-1)), true
}

// API is the subset of the SQS client used by the consumer.
type API interface {
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, params *sqs.ChangeMessageVisibilityInput, optFns ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

// UseCase is the shared financial application surface invoked by this ingress.
type UseCase interface {
	SubmitWagerFromInbox(ctx context.Context, cmd application.SubmitWagerCommand, delivery application.InboxDelivery) (application.WagerResult, bool, error)
}

// Config wires one consumer instance.
type Config struct {
	QueueURL          string
	ConsumerName      string
	ProviderForSender func(senderID string) (string, bool)
	ReceiveWaitTime   time.Duration
	ReceiveBatch      int
	VisibilityTimeout time.Duration
	RenewEvery        time.Duration
	ShutdownGrace     time.Duration
}

// Consumer polls the ingress queue and confirms only durably committed work.
type Consumer struct {
	client     API
	useCase    UseCase
	cfg        Config
	metrics    *metrics.Metrics
	logger     *slog.Logger
	failpoints *failpoint.Set
}

// Option customizes the consumer.
type Option func(*Consumer)

// WithMetrics attaches the metric bundle.
func WithMetrics(bundle *metrics.Metrics) Option { return func(c *Consumer) { c.metrics = bundle } }

// WithLogger attaches the structured logger.
func WithLogger(logger *slog.Logger) Option { return func(c *Consumer) { c.logger = logger } }

// WithFailpoints attaches the integration failpoint set.
func WithFailpoints(set *failpoint.Set) Option { return func(c *Consumer) { c.failpoints = set } }

// New builds the consumer with the approved defaults for unset fields.
func New(cfg Config, client API, useCase UseCase, options ...Option) *Consumer {
	if cfg.ReceiveWaitTime <= 0 {
		cfg.ReceiveWaitTime = ReceiveWaitTime
	}
	if cfg.ReceiveBatch <= 0 {
		cfg.ReceiveBatch = ReceiveBatch
	}
	if cfg.VisibilityTimeout <= 0 {
		cfg.VisibilityTimeout = VisibilityTimeout
	}
	if cfg.RenewEvery <= 0 {
		cfg.RenewEvery = VisibilityRenewal
	}
	if cfg.ShutdownGrace <= 0 {
		cfg.ShutdownGrace = ShutdownGrace
	}
	c := &Consumer{client: client, useCase: useCase, cfg: cfg}
	for _, option := range options {
		option(c)
	}
	if c.metrics == nil {
		c.metrics = metrics.New(metrics.NewRegistry())
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.failpoints == nil {
		c.failpoints = failpoint.New(nil)
	}
	return c
}

// Run polls until the context is canceled, then returns without claiming
// further work.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := c.PollOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.logger.WarnContext(ctx, "sqs receive failed", "error", err.Error())
			if !c.sleep(ctx, time.Second) {
				return nil
			}
		}
	}
}

// PollOnce receives and processes one batch. Exported for deterministic tests.
func (c *Consumer) PollOnce(ctx context.Context) error {
	output, err := c.receive(ctx)
	if err != nil {
		return err
	}
	for i := range output.Messages {
		if ctx.Err() != nil {
			return nil
		}
		c.process(ctx, &output.Messages[i])
	}
	return nil
}

func (c *Consumer) receive(ctx context.Context) (*sqs.ReceiveMessageOutput, error) {
	return c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(c.cfg.QueueURL),
		MaxNumberOfMessages: int32(c.cfg.ReceiveBatch),
		WaitTimeSeconds:     int32(c.cfg.ReceiveWaitTime.Seconds()),
		VisibilityTimeout:   int32(c.cfg.VisibilityTimeout.Seconds()),
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameSenderId,
			types.MessageSystemAttributeNameApproximateReceiveCount,
		},
	})
}

func (c *Consumer) process(ctx context.Context, message *types.Message) {
	handle := aws.ToString(message.ReceiptHandle)
	receiveCount := ReceiveCount(message)
	body := []byte(aws.ToString(message.Body))
	stopRenewal := c.renewVisibility(ctx, handle)
	defer stopRenewal()

	envelope, err := ParseEnvelope(body)
	if err != nil {
		c.abandon(ctx, handle, "invalid_message", err)
		return
	}
	senderID := message.Attributes[string(types.MessageSystemAttributeNameSenderId)]
	provider, ok := c.cfg.ProviderForSender(senderID)
	if !ok || provider != envelope.Data.ProviderID {
		c.abandon(ctx, handle, "unauthorized_sender", errors.New("sender identity does not match the payload provider"))
		return
	}
	command, err := envelope.Command()
	if err != nil {
		c.abandon(ctx, handle, "invalid_message", err)
		return
	}
	digest := sha256.Sum256(body)
	started := time.Now()
	outcome := c.submit(ctx, command, application.InboxDelivery{
		ConsumerName: c.cfg.ConsumerName,
		MessageID:    envelope.MessageID,
		Digest:       hex.EncodeToString(digest[:]),
		ReceivedAt:   started.UTC(),
	})
	if outcome.timedOut {
		// The in-flight work could not finish within the shutdown grace:
		// release the message visibility for redelivery.
		c.release(ctx, handle)
		return
	}
	if outcome.err != nil {
		if ctx.Err() != nil {
			c.release(ctx, handle)
			return
		}
		if application.IsTransient(outcome.err) {
			c.metrics.WagerRetriesTotal.Inc("sqs", "transient")
			c.backoff(ctx, handle, receiveCount)
			return
		}
		reason := "invalid_message"
		if code, ok := application.ErrorCodeOf(outcome.err); ok && code == application.CodeInboxPayloadConflict {
			reason = "inbox_payload_conflict"
		}
		c.abandon(ctx, handle, reason, outcome.err)
		return
	}
	c.failpoints.Hit(failpoint.SQSAfterCommit)
	if err := c.delete(ctx, handle); err != nil {
		// The commit is durable: redelivery is deduplicated by the inbox.
		c.logger.WarnContext(ctx, "sqs delete failed after durable commit", "messageId", envelope.MessageID)
		return
	}
	c.failpoints.Hit(failpoint.SQSAfterDelete)
	if outcome.duplicate {
		c.metrics.WagerIdempotencyDuplicatesTotal.Inc("sqs")
		return
	}
	c.metrics.WagerTransactionsTotal.Inc(string(outcome.result.Kind), string(outcome.result.State), "sqs")
	c.metrics.WagerProcessingDurationSeconds.Observe(time.Since(started).Seconds(), "sqs", string(outcome.result.Kind), string(outcome.result.State))
}

// processOutcome is the result of one inbox submission.
type processOutcome struct {
	result    application.WagerResult
	duplicate bool
	err       error
	timedOut  bool
}

// submit runs the use case and, after cancellation, waits up to the shutdown
// grace for it to finish before releasing the in-flight message.
func (c *Consumer) submit(ctx context.Context, command application.SubmitWagerCommand, delivery application.InboxDelivery) processOutcome {
	results := make(chan processOutcome, 1)
	go func() {
		result, duplicate, err := c.useCase.SubmitWagerFromInbox(ctx, command, delivery)
		results <- processOutcome{result: result, duplicate: duplicate, err: err}
	}()
	select {
	case outcome := <-results:
		return outcome
	case <-ctx.Done():
		timer := time.NewTimer(c.cfg.ShutdownGrace)
		defer timer.Stop()
		select {
		case outcome := <-results:
			return outcome
		case <-timer.C:
			return processOutcome{timedOut: true}
		}
	}
}

// ReceiveCount parses the ApproximateReceiveCount system attribute.
func ReceiveCount(message *types.Message) int {
	raw := message.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]
	count := 0
	for _, digit := range raw {
		if digit < '0' || digit > '9' {
			return 1
		}
		count = count*10 + int(digit-'0')
	}
	if count < 1 {
		return 1
	}
	return count
}

func (c *Consumer) renewVisibility(ctx context.Context, handle string) func() {
	if handle == "" {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(c.cfg.RenewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				_, err := c.client.ChangeMessageVisibility(renewCtx, &sqs.ChangeMessageVisibilityInput{
					QueueUrl:          aws.String(c.cfg.QueueURL),
					ReceiptHandle:     aws.String(handle),
					VisibilityTimeout: int32(c.cfg.VisibilityTimeout.Seconds()),
				})
				cancel()
				if err != nil {
					return
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

func (c *Consumer) delete(ctx context.Context, handle string) error {
	_, err := c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.cfg.QueueURL),
		ReceiptHandle: aws.String(handle),
	})
	return err
}

func (c *Consumer) release(ctx context.Context, handle string) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = c.client.ChangeMessageVisibility(releaseCtx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(c.cfg.QueueURL),
		ReceiptHandle:     aws.String(handle),
		VisibilityTimeout: 0,
	})
}

func (c *Consumer) abandon(ctx context.Context, handle, reason string, err error) {
	c.metrics.WagerDLQTotal.Inc(reason)
	c.logger.WarnContext(ctx, "sqs message abandoned to redrive", "reason", reason, "error", err.Error())
	// Keep the message unacknowledged and available so the configured redrive
	// can move it to the dead-letter queue.
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = c.client.ChangeMessageVisibility(releaseCtx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(c.cfg.QueueURL),
		ReceiptHandle:     aws.String(handle),
		VisibilityTimeout: 0,
	})
}

func (c *Consumer) backoff(ctx context.Context, handle string, receiveCount int) {
	delay, ok := TransientVisibility(receiveCount)
	if !ok {
		c.metrics.WagerDLQTotal.Inc("redrive")
		return
	}
	backoffCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = c.client.ChangeMessageVisibility(backoffCtx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(c.cfg.QueueURL),
		ReceiptHandle:     aws.String(handle),
		VisibilityTimeout: int32(delay.Seconds()),
	})
}

func (c *Consumer) sleep(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
