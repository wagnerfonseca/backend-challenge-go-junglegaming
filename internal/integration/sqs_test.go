//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/metrics"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/sqs"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/sqs/consumer"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/worker"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// C127 - Provisioning creates wager-transactions.fifo and its dead-letter
// queue with redrive after 5 receives.
func TestQueueProvisioning(t *testing.T) {
	queues := provisionQueues(t)
	client := sqsClient(t)
	attributes := queueAttributes(t, client, queues.IngressURL)
	if attributes["FifoQueue"] != "true" {
		t.Errorf("FifoQueue = %q, want true", attributes["FifoQueue"])
	}
	if attributes["VisibilityTimeout"] != "60" {
		t.Errorf("VisibilityTimeout = %q, want 60", attributes["VisibilityTimeout"])
	}
	if attributes["ReceiveMessageWaitTimeSeconds"] != "20" {
		t.Errorf("ReceiveMessageWaitTimeSeconds = %q, want 20", attributes["ReceiveMessageWaitTimeSeconds"])
	}
	if !strings.Contains(attributes["RedrivePolicy"], `"maxReceiveCount":"5"`) {
		t.Errorf("RedrivePolicy = %q, want maxReceiveCount 5", attributes["RedrivePolicy"])
	}
	if !strings.Contains(attributes["RedrivePolicy"], "wager-transactions-dlq.fifo") {
		t.Errorf("RedrivePolicy = %q, want the dead-letter queue ARN", attributes["RedrivePolicy"])
	}
	dlq := queueAttributes(t, client, queues.DLQURL)
	if dlq["MessageRetentionPeriod"] != "1209600" {
		t.Errorf("DLQ retention = %q, want 1209600 (14 days)", dlq["MessageRetentionPeriod"])
	}
}

// C204 - Provisioning creates wager-events.fifo alongside the ingress queues.
func TestEventQueueProvisioning(t *testing.T) {
	queues := provisionQueues(t)
	if !strings.HasSuffix(queues.EventURL, "wager-events.fifo") {
		t.Errorf("event queue url = %q, want wager-events.fifo", queues.EventURL)
	}
	attributes := queueAttributes(t, sqsClient(t), queues.EventURL)
	if attributes["FifoQueue"] != "true" {
		t.Errorf("event FifoQueue = %q, want true", attributes["FifoQueue"])
	}
}

func queueAttributes(t *testing.T, client *awssqs.Client, queueURL string) map[string]string {
	t.Helper()
	output, err := client.GetQueueAttributes(context.Background(), &awssqs.GetQueueAttributesInput{
		QueueUrl:       awssdk.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameAll},
	})
	if err != nil {
		t.Fatalf("reading queue attributes: %v", err)
	}
	return output.Attributes
}

// C128 - The envelope validates messageId, type, UTC occurredAt and typed data
// including idempotencyKey before the use case.
func TestSQSEnvelopeValidation(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	cases := []struct {
		name   string
		mutate func(*consumer.Envelope)
	}{
		{"messageId missing", func(e *consumer.Envelope) { e.MessageID = "" }},
		{"type unsupported", func(e *consumer.Envelope) { e.Type = "SomethingElse" }},
		{"occurredAt not UTC", func(e *consumer.Envelope) { e.OccurredAt = "2026-01-01T12:00:00+03:00" }},
		{"occurredAt malformed", func(e *consumer.Envelope) { e.OccurredAt = "yesterday" }},
		{"idempotencyKey missing", func(e *consumer.Envelope) { e.Data.IdempotencyKey = "" }},
		{"walletId malformed", func(e *consumer.Envelope) { e.Data.WalletID = "not-a-uuid" }},
		{"money malformed", func(e *consumer.Envelope) { e.Data.Money = consumer.MoneyPayload{Amount: "1.0", Currency: "BRL"} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			command := h.command(view, financial.KindBet, "10.00")
			envelope := envelopeFor(t, command, "message-"+newCorrelation())
			testCase.mutate(&envelope)
			broker := &sqsFake{}
			broker.enqueue(sqsMessage(envelope.MessageID, "sender-a", 1, envelopeJSON(t, envelope)))
			if err := newConsumer(h, broker).PollOnce(context.Background()); err != nil {
				t.Fatalf("consumer pass: %v", err)
			}
			if handles := broker.deletedHandles(); len(handles) != 0 {
				t.Errorf("invalid envelope was acknowledged: %v", handles)
			}
			if got := h.transactionsForWallet(view.ID); got != 1 {
				t.Errorf("transactions = %d, want 1 (only the opening)", got)
			}
			if changes := broker.visibilityChanges(); len(changes) == 0 || changes[len(changes)-1].Timeout != 0 {
				t.Errorf("invalid envelope was not left for redrive: %v", changes)
			}
		})
	}
}

// C111 - SQS ingress maps the SenderId to one configured provider and
// requires equality with data.providerId before the financial use case.
func TestSQSSenderIdAuthorization(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")

	t.Run("matching sender is processed", func(t *testing.T) {
		command := h.command(view, financial.KindBet, "10.00")
		messageID := "sender-match-" + newCorrelation()
		broker := &sqsFake{}
		broker.enqueue(sqsMessage(messageID, "sender-a", 1, envelopeJSON(t, envelopeFor(t, command, messageID))))
		if err := newConsumer(h, broker).PollOnce(context.Background()); err != nil {
			t.Fatalf("consumer pass: %v", err)
		}
		if handles := broker.deletedHandles(); len(handles) != 1 {
			t.Errorf("matching sender was not acknowledged: %v", handles)
		}
		if !h.transactionExists("provider-a", command.ExternalTransactionID) {
			t.Error("matching sender did not persist its transaction")
		}
	})

	t.Run("unknown sender is abandoned", func(t *testing.T) {
		command := h.command(view, financial.KindBet, "10.00")
		messageID := "sender-unknown-" + newCorrelation()
		broker := &sqsFake{}
		broker.enqueue(sqsMessage(messageID, "sender-unknown", 1, envelopeJSON(t, envelopeFor(t, command, messageID))))
		if err := newConsumer(h, broker).PollOnce(context.Background()); err != nil {
			t.Fatalf("consumer pass: %v", err)
		}
		if handles := broker.deletedHandles(); len(handles) != 0 {
			t.Errorf("unknown sender was acknowledged: %v", handles)
		}
		if h.transactionExists("provider-a", command.ExternalTransactionID) {
			t.Error("unknown sender persisted a transaction")
		}
	})

	t.Run("sender mapped to another provider is abandoned", func(t *testing.T) {
		command := h.command(view, financial.KindBet, "10.00")
		messageID := "sender-mismatch-" + newCorrelation()
		broker := &sqsFake{}
		broker.enqueue(sqsMessage(messageID, "sender-b", 1, envelopeJSON(t, envelopeFor(t, command, messageID))))
		if err := newConsumer(h, broker).PollOnce(context.Background()); err != nil {
			t.Fatalf("consumer pass: %v", err)
		}
		if handles := broker.deletedHandles(); len(handles) != 0 {
			t.Errorf("mismatched sender was acknowledged: %v", handles)
		}
		if h.transactionExists("provider-a", command.ExternalTransactionID) {
			t.Error("mismatched sender persisted a transaction")
		}
		if changes := broker.visibilityChanges(); len(changes) == 0 || changes[len(changes)-1].Timeout != 0 {
			t.Errorf("mismatched sender was not left for redrive: %v", changes)
		}
	})
}

// C129 - HTTP and SQS invoke the same financial use case with the same
// canonical business projection.
func TestHTTPAndSQSConvergence(t *testing.T) {
	runtime := newHTTPRuntime(t)
	view := runtime.harness.openWallet("1000.00")
	command := runtime.harness.command(view, financial.KindBet, "25.00")
	status, data := runtime.postWager(wagerJSONOf(command), command.IdempotencyKey.String())
	requireStatus(t, status, 200, data)
	overHTTP := decodeJSONBody[wagerResultJSON](t, data)

	broker := &sqsFake{}
	messageID := "message-" + newCorrelation()
	broker.enqueue(sqsMessage(messageID, "sender-a", 1, envelopeJSON(t, envelopeFor(t, command, messageID))))
	if err := newConsumer(runtime.harness, broker).PollOnce(context.Background()); err != nil {
		t.Fatalf("consumer pass: %v", err)
	}
	if got := runtime.harness.transactionsForWallet(view.ID); got != 2 {
		t.Errorf("transactions = %d, want 2 (opening and one bet)", got)
	}
	if got := runtime.harness.ledgerForWallet(view.ID); got != 2 {
		t.Errorf("ledger entries = %d, want 2", got)
	}
	if got := runtime.harness.outboxCountOfType(view.ID, "WagerTransactionProcessed"); got != 2 {
		t.Errorf("processed events = %d, want 2", got)
	}
	if overHTTP.TransactionID == "" {
		t.Error("HTTP result identity is missing")
	}
}

// C131 - Inbox completion commits in the same SQL transaction as the domain,
// wallet, ledger and outbox changes.
func TestInboxTransactionCommit(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	command := h.command(view, financial.KindBet, "25.00")
	envelope := envelopeFor(t, command, "message-"+newCorrelation())
	body := envelopeJSON(t, envelope)
	broker := &sqsFake{}
	broker.enqueue(sqsMessage(envelope.MessageID, "sender-a", 1, body))
	if err := newConsumer(h, broker).PollOnce(context.Background()); err != nil {
		t.Fatalf("consumer pass: %v", err)
	}
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])

	if got := h.countRows(`SELECT count(*) FROM inbox_deliveries WHERE "consumerName" = $1 AND "messageId" = $2 AND "digest" = $3 AND "completedAt" IS NOT NULL`,
		"wager-transactions", envelope.MessageID, digest); got != 1 {
		t.Errorf("completed inbox rows = %d, want 1", got)
	}
	if got := h.transactionsForWallet(view.ID); got != 2 {
		t.Errorf("transactions = %d, want 2", got)
	}
	if got := h.ledgerForWallet(view.ID); got != 2 {
		t.Errorf("ledger entries = %d, want 2", got)
	}
	if got := h.outboxCountOfType(view.ID, "WagerTransactionProcessed"); got != 2 {
		t.Errorf("processed events = %d, want 2", got)
	}
}

// C132 - A completed inbox message with the same digest is deleted without
// another financial movement.
func TestInboxDuplicateDelete(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	command := h.command(view, financial.KindBet, "25.00")
	envelope := envelopeFor(t, command, "message-"+newCorrelation())
	body := envelopeJSON(t, envelope)
	ledgerBefore := h.ledgerForWallet(view.ID)

	for pass := 0; pass < 2; pass++ {
		broker := &sqsFake{}
		broker.enqueue(sqsMessage(envelope.MessageID, "sender-a", 1, body))
		if err := newConsumer(h, broker).PollOnce(context.Background()); err != nil {
			t.Fatalf("consumer pass %d: %v", pass, err)
		}
		if handles := broker.deletedHandles(); len(handles) != 1 {
			t.Fatalf("pass %d deleted handles = %v, want 1", pass, handles)
		}
	}
	if got := h.ledgerForWallet(view.ID); got != ledgerBefore+1 {
		t.Errorf("ledger entries = %d, want %d (one movement)", got, ledgerBefore+1)
	}
	if got := h.transactionsForWallet(view.ID); got != 2 {
		t.Errorf("transactions = %d, want 2", got)
	}
}

// C133 - A known (consumerName, messageId) redelivered with a different digest
// is permanently classified as INBOX_PAYLOAD_CONFLICT with no domain change.
func TestInboxPayloadConflict(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	first := h.command(view, financial.KindBet, "25.00")
	envelope := envelopeFor(t, first, "message-"+newCorrelation())
	broker := &sqsFake{}
	broker.enqueue(sqsMessage(envelope.MessageID, "sender-a", 1, envelopeJSON(t, envelope)))
	if err := newConsumer(h, broker).PollOnce(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	second := h.command(view, financial.KindBet, "30.00")
	conflicting := envelopeFor(t, second, envelope.MessageID)
	broker = &sqsFake{}
	broker.enqueue(sqsMessage(conflicting.MessageID, "sender-a", 1, envelopeJSON(t, conflicting)))
	if err := newConsumer(h, broker).PollOnce(context.Background()); err != nil {
		t.Fatalf("conflicting pass: %v", err)
	}
	if handles := broker.deletedHandles(); len(handles) != 0 {
		t.Errorf("conflicting digest was acknowledged: %v", handles)
	}
	if got := h.transactionsForWallet(view.ID); got != 2 {
		t.Errorf("transactions = %d, want 2 (no domain change)", got)
	}
	if got := h.ledgerForWallet(view.ID); got != 2 {
		t.Errorf("ledger entries = %d, want 2 (no movement)", got)
	}
}

// C134 - SQS handling without a durable commit never deletes its message.
func TestInboxNoDeleteOnFailure(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	command := h.command(view, financial.KindBet, "25.00")
	envelope := envelopeFor(t, command, "message-"+newCorrelation())

	broker := &sqsFake{}
	broker.enqueue(sqsMessage(envelope.MessageID, "sender-a", 1, envelopeJSON(t, envelope)))
	offlineConsumer := consumer.New(consumer.Config{
		QueueURL:     "http://fake/wager-transactions.fifo",
		ConsumerName: "wager-transactions",
		ProviderForSender: func(string) (string, bool) {
			return "provider-a", true
		},
	}, broker, transientUseCase{})
	if err := offlineConsumer.PollOnce(context.Background()); err != nil {
		t.Fatalf("offline pass: %v", err)
	}
	if handles := broker.deletedHandles(); len(handles) != 0 {
		t.Errorf("message without a durable commit was deleted: %v", handles)
	}
}

// C135 - A durable business rejection deletes its input message.
func TestInboxDeleteOnRejection(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	command := h.command(view, financial.KindBet, "150.00")
	envelope := envelopeFor(t, command, "message-"+newCorrelation())
	broker := &sqsFake{}
	broker.enqueue(sqsMessage(envelope.MessageID, "sender-a", 1, envelopeJSON(t, envelope)))
	if err := newConsumer(h, broker).PollOnce(context.Background()); err != nil {
		t.Fatalf("consumer pass: %v", err)
	}
	if handles := broker.deletedHandles(); len(handles) != 1 {
		t.Errorf("deleted handles = %v, want the rejection acknowledged", handles)
	}
	record := h.transactionByExternal(command.ProviderID.String(), command.ExternalTransactionID)
	if record.State != "REJECTED" {
		t.Errorf("persisted state = %s, want REJECTED", record.State)
	}
}

// C136 - A transient SQS handling error leaves the message for redelivery.
func TestInboxRetryOnTransient(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	command := h.command(view, financial.KindBet, "10.00")
	envelope := envelopeFor(t, command, "message-"+newCorrelation())
	broker := &sqsFake{}
	broker.enqueue(sqsMessage(envelope.MessageID, "sender-a", 1, envelopeJSON(t, envelope)))
	failing := consumer.New(consumer.Config{
		QueueURL:     "http://fake/wager-transactions.fifo",
		ConsumerName: "wager-transactions",
		ProviderForSender: func(string) (string, bool) {
			return "provider-a", true
		},
	}, broker, transientUseCase{})
	if err := failing.PollOnce(context.Background()); err != nil {
		t.Fatalf("transient pass: %v", err)
	}
	if handles := broker.deletedHandles(); len(handles) != 0 {
		t.Errorf("transient failure acknowledged the message: %v", handles)
	}
	changes := broker.visibilityChanges()
	if len(changes) != 1 || changes[0].Timeout != 5 {
		t.Errorf("visibility changes = %v, want one 5s backoff", changes)
	}
}

// C202 - Transient failures at receive counts 1 to 4 set visibility to 5, 10,
// 20 and 40 seconds.
func TestSQSVisibilityBackoff(t *testing.T) {
	want := map[int]int32{1: 5, 2: 10, 3: 20, 4: 40}
	for count, timeout := range want {
		t.Run(fmt.Sprintf("receive-%d", count), func(t *testing.T) {
			h := newHarness(t)
			view := h.openWallet("100.00")
			command := h.command(view, financial.KindBet, "10.00")
			envelope := envelopeFor(t, command, "message-"+newCorrelation())
			broker := &sqsFake{}
			broker.enqueue(sqsMessage(envelope.MessageID, "sender-a", count, envelopeJSON(t, envelope)))
			failing := consumer.New(consumer.Config{
				QueueURL:     "http://fake/wager-transactions.fifo",
				ConsumerName: "wager-transactions",
				ProviderForSender: func(string) (string, bool) {
					return "provider-a", true
				},
			}, broker, transientUseCase{})
			if err := failing.PollOnce(context.Background()); err != nil {
				t.Fatalf("pass: %v", err)
			}
			changes := broker.visibilityChanges()
			if len(changes) != 1 || changes[0].Timeout != timeout {
				t.Errorf("visibility = %v, want %d", changes, timeout)
			}
		})
	}
}

// C138 - Polling uses long poll 20s, batches up to 10, visibility 60s and
// renewal every 20s.
func TestSQSPollingParameters(t *testing.T) {
	if consumer.ReceiveWaitTime != 20*time.Second || consumer.ReceiveBatch != 10 ||
		consumer.VisibilityTimeout != 60*time.Second || consumer.VisibilityRenewal != 20*time.Second {
		t.Fatalf("polling constants = %s/%d/%s/%s, want 20s/10/60s/20s",
			consumer.ReceiveWaitTime, consumer.ReceiveBatch, consumer.VisibilityTimeout, consumer.VisibilityRenewal)
	}
	h := newHarness(t)
	broker := &sqsFake{}
	selected := consumer.New(consumer.Config{
		QueueURL:     "http://fake/wager-transactions.fifo",
		ConsumerName: "wager-transactions",
		ProviderForSender: func(string) (string, bool) {
			return "provider-a", true
		},
	}, broker, transientUseCase{})
	if err := selected.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll pass: %v", err)
	}
	calls := broker.receiveCalls()
	if len(calls) != 1 {
		t.Fatalf("receive calls = %d, want 1", len(calls))
	}
	if calls[0].MaxNumberOfMessages != 10 || calls[0].WaitTimeSeconds != 20 || calls[0].VisibilityTimeout != 60 {
		t.Errorf("receive parameters = %d/%d/%d, want 10/20/60",
			calls[0].MaxNumberOfMessages, calls[0].WaitTimeSeconds, calls[0].VisibilityTimeout)
	}
	attributes := map[types.MessageSystemAttributeName]bool{}
	for _, name := range calls[0].MessageSystemAttributeNames {
		attributes[name] = true
	}
	if !attributes[types.MessageSystemAttributeNameSenderId] || !attributes[types.MessageSystemAttributeNameApproximateReceiveCount] {
		t.Errorf("system attributes = %v, want SenderId and ApproximateReceiveCount", attributes)
	}

	// The 20-second renewal keeps the visibility in place during slow work.
	blocking := &blockingInboxUseCase{entered: make(chan struct{}), release: make(chan struct{})}
	renewCommand := h.command(h.openWallet("100.00"), financial.KindBet, "10.00")
	renewBroker := &sqsFake{}
	renewBroker.enqueue(sqsMessage("message-renew", "sender-a", 1, envelopeJSON(t, envelopeFor(t, renewCommand, "message-renew"))))
	slow := consumer.New(consumer.Config{
		QueueURL:     "http://fake/wager-transactions.fifo",
		ConsumerName: "wager-transactions",
		ProviderForSender: func(string) (string, bool) {
			return "provider-a", true
		},
		RenewEvery: 10 * time.Millisecond,
	}, renewBroker, blocking)
	done := make(chan struct{})
	go func() {
		_ = slow.PollOnce(context.Background())
		close(done)
	}()
	<-blocking.entered
	time.Sleep(50 * time.Millisecond)
	close(blocking.release)
	<-done
	renewed := false
	for _, change := range renewBroker.visibilityChanges() {
		if change.Timeout == 60 {
			renewed = true
		}
	}
	if !renewed {
		t.Errorf("visibility was not renewed at 60s: %v", renewBroker.visibilityChanges())
	}
}

// C140 - A missing reference commits PENDING_REFERENCE, completes its inbox
// and transfers continuation to the reference worker.
func TestInboxToReferenceWorker(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	refund := h.command(view, financial.KindRefund, "25.00")
	refund.ReferenceExternalID = newExternalID("bet")
	envelope := envelopeFor(t, refund, "message-"+newCorrelation())
	broker := &sqsFake{}
	broker.enqueue(sqsMessage(envelope.MessageID, "sender-a", 1, envelopeJSON(t, envelope)))
	if err := newConsumer(h, broker).PollOnce(context.Background()); err != nil {
		t.Fatalf("consumer pass: %v", err)
	}
	if handles := broker.deletedHandles(); len(handles) != 1 {
		t.Fatalf("pending reference message was not deleted: %v", handles)
	}
	if got := h.countRows(`SELECT count(*) FROM inbox_deliveries WHERE "messageId" = $1 AND "completedAt" IS NOT NULL`, envelope.MessageID); got != 1 {
		t.Errorf("completed inbox rows = %d, want 1", got)
	}
	pending := h.transactionByExternal(refund.ProviderID.String(), refund.ExternalTransactionID)
	if pending.State != "PENDING_REFERENCE" {
		t.Fatalf("state = %s, want PENDING_REFERENCE", pending.State)
	}

	bet := h.command(view, financial.KindBet, "25.00")
	bet.ExternalTransactionID = refund.ReferenceExternalID
	bet.RoundID = refund.RoundID
	h.mustSubmit(bet)

	referenceWorker := worker.New(h.store, h.service, worker.WithMetrics(metrics.New(metrics.NewRegistry())))
	for i := 0; i < 3; i++ {
		resolved, err := referenceWorker.ResolveOnce(h.ctx())
		if err != nil {
			t.Fatalf("reference worker pass: %v", err)
		}
		if resolved > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	result, err := h.service.TransactionByID(h.ctx(), mustParseTransaction(t, pending.ID))
	if err != nil {
		t.Fatalf("reading resolved transaction: %v", err)
	}
	if result.State != financial.StateProcessed {
		t.Errorf("state after worker = %s, want PROCESSED", result.State)
	}
}

// C141 - SIGTERM during SQS work stops polling and finishes or releases every
// in-flight message within 30 seconds.
func TestSQSShutdownGrace(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	command := h.command(view, financial.KindBet, "10.00")
	envelope := envelopeFor(t, command, "message-"+newCorrelation())
	broker := &sqsFake{}
	broker.enqueue(sqsMessage(envelope.MessageID, "sender-a", 1, envelopeJSON(t, envelope)))
	blocking := &blockingInboxUseCase{entered: make(chan struct{}), release: make(chan struct{})}
	graceful := consumer.New(consumer.Config{
		QueueURL:     "http://fake/wager-transactions.fifo",
		ConsumerName: "wager-transactions",
		ProviderForSender: func(string) (string, bool) {
			return "provider-a", true
		},
	}, broker, blocking)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- graceful.Run(ctx) }()
	<-blocking.entered
	select {
	case <-runDone:
		t.Fatal("consumer returned before shutdown began")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("consumer shutdown: %v", err)
		}
	case <-time.After(consumer.ShutdownGrace):
		t.Fatal("consumer did not stop within the 30-second shutdown grace")
	}
	if handles := broker.deletedHandles(); len(handles) != 0 {
		t.Errorf("in-flight message was acknowledged during shutdown: %v", handles)
	}
	released := false
	for _, change := range broker.visibilityChanges() {
		if change.Timeout == 0 {
			released = true
		}
	}
	if !released {
		t.Errorf("in-flight message visibility was not released: %v", broker.visibilityChanges())
	}
}

// C200 - An absent, empty or oversized data.idempotencyKey is a permanent
// INVALID_MESSAGE with no domain change.
func TestInvalidMessagePermanent(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	for name, key := range map[string]string{"absent": "", "too-long": strings.Repeat("k", 256)} {
		t.Run(name, func(t *testing.T) {
			command := h.command(view, financial.KindBet, "10.00")
			envelope := envelopeFor(t, command, "message-"+newCorrelation())
			envelope.Data.IdempotencyKey = key
			broker := &sqsFake{}
			broker.enqueue(sqsMessage(envelope.MessageID, "sender-a", 1, envelopeJSON(t, envelope)))
			if err := newConsumer(h, broker).PollOnce(context.Background()); err != nil {
				t.Fatalf("pass: %v", err)
			}
			if handles := broker.deletedHandles(); len(handles) != 0 {
				t.Errorf("permanent message was acknowledged: %v", handles)
			}
			if got := h.transactionExists(command.ProviderID.String(), command.ExternalTransactionID); got {
				t.Error("permanent message persisted a transaction")
			}
		})
	}
}

// C201 - data.idempotencyKey is used unchanged in the same provider-scoped
// lookup as the HTTP header.
func TestIdempotencyKeyConsistency(t *testing.T) {
	runtime := newHTTPRuntime(t)
	view := runtime.harness.openWallet("1000.00")
	command := runtime.harness.command(view, financial.KindBet, "25.00")
	status, data := runtime.postWager(wagerJSONOf(command), command.IdempotencyKey.String())
	requireStatus(t, status, 200, data)
	ledgerBefore := runtime.harness.ledgerForWallet(view.ID)

	broker := &sqsFake{}
	messageID := "message-" + newCorrelation()
	broker.enqueue(sqsMessage(messageID, "sender-a", 1, envelopeJSON(t, envelopeFor(t, command, messageID))))
	if err := newConsumer(runtime.harness, broker).PollOnce(context.Background()); err != nil {
		t.Fatalf("SQS pass: %v", err)
	}
	if handles := broker.deletedHandles(); len(handles) != 1 {
		t.Errorf("duplicate key message was not deleted: %v", handles)
	}
	if got := runtime.harness.ledgerForWallet(view.ID); got != ledgerBefore {
		t.Errorf("ledger entries = %d, want %d (no second movement)", got, ledgerBefore)
	}
	if got := runtime.harness.transactionsForWallet(view.ID); got != 2 {
		t.Errorf("transactions = %d, want 2", got)
	}
}

// C137 - A message that reaches 5 receives without durable completion arrives
// in the dead-letter queue.
func TestDLQAfterFiveReceives(t *testing.T) {
	queues, client := provisionIsolatedQueues(t)
	messageID := "dlq-" + newCorrelation()
	sender := sqs.NewIngressSender(client, queues.IngressURL)
	if err := sender.SendWagerRequest(context.Background(), messageID, financial.NewWalletID().String(), `{"not":"a valid envelope"}`); err != nil {
		t.Fatalf("sending message: %v", err)
	}
	for receive := 0; receive < 6; receive++ {
		message := receiveAndRelease(t, client, queues.IngressURL)
		if message == nil {
			if receive < 5 {
				t.Fatalf("receive %d returned no message", receive)
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := receiveFromQueue(t, client, queues.DLQURL, 10); !strings.Contains(got, "a valid envelope") {
		t.Errorf("dead-letter queue did not receive the abandoned message: %q", got)
	}
}

// C203 - A permanently invalid message stays unacknowledged until the
// configured redrive moves it to the dead-letter queue.
func TestPermanentMessageDLQ(t *testing.T) {
	h := newHarness(t)
	queues, client := provisionIsolatedQueues(t)
	view := h.openWallet("100.00")
	command := h.command(view, financial.KindBet, "10.00")
	envelope := envelopeFor(t, command, "message-"+newCorrelation())
	envelope.MessageID = "" // permanently invalid
	messageID := "permanent-" + newCorrelation()
	sender := sqs.NewIngressSender(client, queues.IngressURL)
	if err := sender.SendWagerRequest(context.Background(), messageID, view.ID.String(), envelopeJSON(t, envelope)); err != nil {
		t.Fatalf("sending message: %v", err)
	}
	realConsumer := consumer.New(consumer.Config{
		QueueURL:          queues.IngressURL,
		ConsumerName:      "wager-transactions",
		ReceiveWaitTime:   time.Second,
		ProviderForSender: func(string) (string, bool) { return "provider-a", true },
	}, client, h.service)
	for pass := 0; pass < 6; pass++ {
		if err := realConsumer.PollOnce(context.Background()); err != nil {
			t.Fatalf("consumer pass %d: %v", pass, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := h.transactionExists(command.ProviderID.String(), command.ExternalTransactionID); got {
		t.Error("permanently invalid message persisted a transaction")
	}
	if got := receiveFromQueue(t, client, queues.DLQURL, 10); !strings.Contains(got, command.ExternalTransactionID.String()) {
		t.Errorf("dead-letter queue did not receive the permanent message: %q", got)
	}
}

// receiveAndRelease receives one message and immediately makes it visible
// again so the receive count advances deterministically.
func receiveAndRelease(t *testing.T, client *awssqs.Client, queueURL string) *types.Message {
	t.Helper()
	for attempt := 0; attempt < 10; attempt++ {
		output, err := client.ReceiveMessage(context.Background(), &awssqs.ReceiveMessageInput{
			QueueUrl:            awssdk.String(queueURL),
			MaxNumberOfMessages: 1,
			WaitTimeSeconds:     1,
		})
		if err != nil {
			t.Fatalf("receiving from %s: %v", queueURL, err)
		}
		if len(output.Messages) == 0 {
			continue
		}
		message := output.Messages[0]
		if _, err := client.ChangeMessageVisibility(context.Background(), &awssqs.ChangeMessageVisibilityInput{
			QueueUrl:          awssdk.String(queueURL),
			ReceiptHandle:     message.ReceiptHandle,
			VisibilityTimeout: 0,
		}); err != nil {
			t.Fatalf("releasing message: %v", err)
		}
		return &message
	}
	return nil
}

func receiveFromQueue(t *testing.T, client *awssqs.Client, queueURL string, attempts int) string {
	t.Helper()
	for i := 0; i < attempts; i++ {
		output, err := client.ReceiveMessage(context.Background(), &awssqs.ReceiveMessageInput{
			QueueUrl:            awssdk.String(queueURL),
			MaxNumberOfMessages: 1,
			WaitTimeSeconds:     1,
		})
		if err != nil {
			t.Fatalf("receiving from %s: %v", queueURL, err)
		}
		if len(output.Messages) > 0 {
			return awssdk.ToString(output.Messages[0].Body)
		}
	}
	return ""
}

// transientUseCase always reports a transient infrastructure failure.
type transientUseCase struct{}

func (transientUseCase) SubmitWagerFromInbox(context.Context, application.SubmitWagerCommand, application.InboxDelivery) (application.WagerResult, bool, error) {
	return application.WagerResult{}, false, fmt.Errorf("%w: database offline", application.ErrTransient)
}

// blockingInboxUseCase blocks until the context is canceled or released.
type blockingInboxUseCase struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingInboxUseCase) SubmitWagerFromInbox(ctx context.Context, _ application.SubmitWagerCommand, _ application.InboxDelivery) (application.WagerResult, bool, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return application.WagerResult{State: financial.StateProcessed, Kind: financial.KindBet}, false, nil
	case <-ctx.Done():
		return application.WagerResult{}, false, fmt.Errorf("%w: %w", application.ErrTransient, ctx.Err())
	}
}

func mustParseTransaction(t *testing.T, raw string) financial.TransactionID {
	t.Helper()
	id, err := financial.ParseTransactionID(raw)
	if err != nil {
		t.Fatalf("parsing transaction id %q: %v", raw, err)
	}
	return id
}
