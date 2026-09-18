//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/failpoint"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/outbox"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/sqs"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// recordingSender is a deterministic event publisher.
type recordingSender struct {
	mu      sync.Mutex
	records []application.OutboxRecord
	fail    bool
}

func (s *recordingSender) Publish(_ context.Context, record application.OutboxRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("broker unavailable")
	}
	s.records = append(s.records, record)
	return nil
}

func (s *recordingSender) published() []application.OutboxRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]application.OutboxRecord(nil), s.records...)
}

func (s *recordingSender) setFail(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = fail
}

// clearOutbox makes earlier tests unable to affect a publisher scenario.
func clearOutbox(t *testing.T) {
	t.Helper()
	if _, err := adminPool.Exec(context.Background(),
		`UPDATE outbox_events SET "publishedAt" = now() WHERE "publishedAt" IS NULL`); err != nil {
		t.Fatalf("clearing outbox: %v", err)
	}
}

// C143 - A financial commit already contains every applicable event snapshot.
func TestOutboxAtCommit(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	bet := h.command(view, financial.KindBet, "25.00")
	result := h.mustSubmit(bet)

	events := h.outboxEventsForWallet(view.ID)
	if len(events) != 4 {
		t.Fatalf("outbox events = %d, want 4 (opening processed+balance, bet processed+balance)", len(events))
	}
	processed, balance := 0, 0
	for _, row := range events {
		decoded := decodeOutbox(t, row)
		switch decoded.EventType {
		case "WagerTransactionProcessed":
			processed++
			if data := decodeProcessedData(t, decoded); data.TransactionID == result.TransactionID.String() {
				if data.Balance.MinorUnits() != 97500 {
					t.Errorf("committed snapshot balance = %d, want 97500", data.Balance.MinorUnits())
				}
			}
		case "WalletBalanceChanged":
			balance++
		}
	}
	if processed != 2 || balance != 2 {
		t.Errorf("processed/balance events = %d/%d, want 2/2", processed, balance)
	}
}

// C145 - A due outbox row is claimed in a batch of at most 50 under a
// 30-second recoverable lease.
func TestOutboxLeaseAndBatch(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	clearOutbox(t)
	for i := 0; i < 55; i++ {
		h.mustSubmit(h.command(view, financial.KindLoss, "0.00"))
	}
	now := time.Now().UTC()
	first, err := h.store.ClaimDueEvents(h.ctx(), now, 50, 30*time.Second)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	second, err := h.store.ClaimDueEvents(h.ctx(), now, 50, 30*time.Second)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(first) != 50 {
		t.Errorf("first batch = %d, want 50", len(first))
	}
	if len(second) != 5 {
		t.Errorf("second batch = %d, want 5", len(second))
	}
	seen := map[string]bool{}
	for _, record := range append(first, second...) {
		if seen[record.EventID] {
			t.Errorf("event %s was claimed twice under one lease", record.EventID)
		}
		seen[record.EventID] = true
	}
	var leased int
	if err := adminPool.QueryRow(h.ctx(),
		`SELECT count(*) FROM outbox_events WHERE "publishedAt" IS NULL AND "claimedUntil" > $1`, now,
	).Scan(&leased); err != nil {
		t.Fatalf("counting leased events: %v", err)
	}
	if leased != 55 {
		t.Errorf("leased events = %d, want 55", leased)
	}
	recovered, err := h.store.ClaimDueEvents(h.ctx(), now.Add(31*time.Second), 50, 30*time.Second)
	if err != nil {
		t.Fatalf("recovery claim: %v", err)
	}
	if len(recovered) != 50 {
		t.Errorf("recovered batch = %d, want 50 after lease expiry", len(recovered))
	}
}

// C146 - A publishing failure retains the event and retries from 1 second to a
// 5-minute cap without an attempt limit.
func TestOutboxRetryBackoff(t *testing.T) {
	h := newHarness(t)
	clearOutbox(t)
	view := h.openWallet("1000.00")
	h.mustSubmit(h.command(view, financial.KindBet, "10.00"))

	sender := &recordingSender{fail: true}
	publisher := outbox.New(h.store, sender)
	if _, err := publisher.PublishOnce(h.ctx()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	var (
		eventID       string
		attempts      int
		nextAttemptAt time.Time
	)
	if err := adminPool.QueryRow(h.ctx(),
		`SELECT "eventId", "attempts", "nextAttemptAt" FROM outbox_events
		WHERE "aggregateId" = $1 AND "eventType" = 'WagerTransactionProcessed' AND "publishedAt" IS NULL
		ORDER BY "createdAt" LIMIT 1`,
		view.ID.String(),
	).Scan(&eventID, &attempts, &nextAttemptAt); err != nil {
		t.Fatalf("reading retry state: %v", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	delay := time.Until(nextAttemptAt)
	if delay < 500*time.Millisecond || delay > 2*time.Second {
		t.Errorf("first retry delay = %s, want about 1s", delay)
	}
	if outbox.Backoff(1) != time.Second || outbox.Backoff(2) != 2*time.Second {
		t.Errorf("backoff = %s/%s, want 1s/2s", outbox.Backoff(1), outbox.Backoff(2))
	}
	if outbox.Backoff(30) != 5*time.Minute {
		t.Errorf("backoff cap = %s, want 5m", outbox.Backoff(30))
	}
	if _, err := adminPool.Exec(h.ctx(), `UPDATE outbox_events SET "nextAttemptAt" = now() - interval '1 second' WHERE "eventId" = $1`, eventID); err != nil {
		t.Fatalf("forcing a retry: %v", err)
	}
	if _, err := publisher.PublishOnce(h.ctx()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if err := adminPool.QueryRow(h.ctx(), `SELECT "attempts" FROM outbox_events WHERE "eventId" = $1`, eventID).Scan(&attempts); err != nil {
		t.Fatalf("reading attempts: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (retry without limit)", attempts)
	}

	sender.setFail(false)
	if _, err := adminPool.Exec(h.ctx(), `UPDATE outbox_events SET "nextAttemptAt" = now() - interval '1 second' WHERE "eventId" = $1`, eventID); err != nil {
		t.Fatalf("forcing the final retry: %v", err)
	}
	if _, err := publisher.PublishOnce(h.ctx()); err != nil {
		t.Fatalf("final pass: %v", err)
	}
	if got := len(sender.published()); got != 1 {
		t.Errorf("published events = %d, want 1", got)
	}
	var publishedAt *time.Time
	if err := adminPool.QueryRow(h.ctx(), `SELECT "publishedAt" FROM outbox_events WHERE "eventId" = $1`, eventID).Scan(&publishedAt); err != nil {
		t.Fatalf("reading publication: %v", err)
	}
	if publishedAt == nil {
		t.Error("event was not confirmed after the retry succeeded")
	}
}

// C147 - A process stopping after commit and before publication lets another
// publisher instance recover the pending event after the lease expires.
func TestOutboxRecovery(t *testing.T) {
	h := newHarness(t)
	clearOutbox(t)
	view := h.openWallet("1000.00")
	h.mustSubmit(h.command(view, financial.KindBet, "10.00"))
	now := time.Now().UTC()
	claimed, err := h.store.ClaimDueEvents(h.ctx(), now, 50, 30*time.Second)
	if err != nil {
		t.Fatalf("crashed claim: %v", err)
	}
	if len(claimed) == 0 {
		t.Fatal("no events were claimed")
	}
	second, err := h.store.ClaimDueEvents(h.ctx(), now, 50, 30*time.Second)
	if err != nil {
		t.Fatalf("premature claim: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("second publisher claimed %d leased events, want 0", len(second))
	}
	recovered, err := h.store.ClaimDueEvents(h.ctx(), now.Add(31*time.Second), 50, 30*time.Second)
	if err != nil {
		t.Fatalf("recovery claim: %v", err)
	}
	if len(recovered) == 0 {
		t.Error("another instance did not recover the abandoned events")
	}

	sender := &recordingSender{}
	publisher := outbox.New(h.store, sender, outbox.WithClock(func() time.Time { return now.Add(62 * time.Second) }))
	if published, err := publisher.PublishOnce(h.ctx()); err != nil {
		t.Fatalf("publishing recovered events: %v", err)
	} else if published == 0 {
		t.Error("recovered events were not published")
	}
}

// C148 - A process stopping after SQS accepts an event and before outbox
// confirmation republishes the same eventId and payload bytes.
func TestOutboxStableIdRepublish(t *testing.T) {
	h := newHarness(t)
	clearOutbox(t)
	view := h.openWallet("1000.00")
	h.mustSubmit(h.command(view, financial.KindBet, "10.00"))

	sendBroker := &sendAPIFake{}
	sender := sqs.NewEventSender(sendBroker, "http://fake/wager-events.fifo")
	crashing := outbox.New(h.store, sender, outbox.WithFailpoints(failpoint.New([]string{failpoint.OutboxAfterPublish})))
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("publish-confirmation failpoint did not stop the publisher")
			}
		}()
		_, _ = crashing.PublishOnce(h.ctx())
	}()
	if len(sendBroker.inputs) == 0 {
		t.Fatal("event was not published before the crash")
	}
	first := sendBroker.inputs[0]

	if _, err := adminPool.Exec(h.ctx(), `UPDATE outbox_events SET "claimedUntil" = NULL WHERE "publishedAt" IS NULL`); err != nil {
		t.Fatalf("releasing the abandoned lease: %v", err)
	}
	restarted := outbox.New(h.store, sender)
	if _, err := restarted.PublishOnce(h.ctx()); err != nil {
		t.Fatalf("restarted publisher: %v", err)
	}

	byID := map[string][]string{}
	for _, input := range sendBroker.inputs {
		id := awssdk.ToString(input.MessageDeduplicationId)
		byID[id] = append(byID[id], awssdk.ToString(input.MessageBody))
	}
	republished := 0
	for id, bodies := range byID {
		if len(bodies) > 1 {
			republished++
		}
		for _, body := range bodies[1:] {
			if body != bodies[0] {
				t.Errorf("event %s was republished with different payload bytes", id)
			}
		}
	}
	if republished == 0 {
		t.Error("no event was republished after the crash before confirmation")
	}
	if awssdk.ToString(first.MessageDeduplicationId) == "" || awssdk.ToString(first.MessageGroupId) != view.ID.String() {
		t.Errorf("MessageGroupId = %q, want wallet %s", awssdk.ToString(first.MessageGroupId), view.ID)
	}
}

// C151 - A processed external operation and a positive-balance OPENING each
// persist exactly one WagerTransactionProcessed.
func TestProcessedEventCount(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	h.mustSubmit(h.command(view, financial.KindBet, "25.00"))
	if got := h.outboxCountOfType(view.ID, "WagerTransactionProcessed"); got != 2 {
		t.Errorf("processed events = %d, want 2 (opening and bet)", got)
	}
	records := h.outboxEventsForWallet(view.ID)
	ids := map[string]bool{}
	for _, row := range records {
		decoded := decodeOutbox(t, row)
		if decoded.EventType != "WagerTransactionProcessed" {
			continue
		}
		data := decodeProcessedData(t, decoded)
		if ids[data.TransactionID] {
			t.Errorf("transaction %s produced more than one processed event", data.TransactionID)
		}
		ids[data.TransactionID] = true
	}
}

// C152 - An accepted operation reaching REJECTED persists exactly one
// WagerTransactionRejected.
func TestRejectedEventCount(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	bet := h.command(view, financial.KindBet, "150.00")
	result := h.mustSubmit(bet)
	if result.State != financial.StateRejected {
		t.Fatalf("state = %s, want REJECTED", result.State)
	}
	if got := h.outboxCountOfType(view.ID, "WagerTransactionRejected"); got != 1 {
		t.Errorf("rejected events = %d, want 1", got)
	}
	if got := h.outboxCountOfType(view.ID, "WagerTransactionProcessed"); got != 1 {
		t.Errorf("processed events = %d, want 1 (only the opening)", got)
	}
}

// C153 - WalletBalanceChanged is persisted exactly when the balance changes.
func TestBalanceChangeEvent(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	if got := h.outboxCountOfType(view.ID, "WalletBalanceChanged"); got != 1 {
		t.Fatalf("balance events after opening = %d, want 1", got)
	}
	h.mustSubmit(h.command(view, financial.KindBet, "25.00"))
	if got := h.outboxCountOfType(view.ID, "WalletBalanceChanged"); got != 2 {
		t.Fatalf("balance events after BET = %d, want 2", got)
	}
	h.mustSubmit(h.command(view, financial.KindLoss, "0.00"))
	if got := h.outboxCountOfType(view.ID, "WalletBalanceChanged"); got != 2 {
		t.Errorf("balance events after LOSS = %d, want 2 (no movement)", got)
	}
	h.mustSubmit(h.command(view, financial.KindBet, "5000.00"))
	if got := h.outboxCountOfType(view.ID, "WalletBalanceChanged"); got != 2 {
		t.Errorf("balance events after rejection = %d, want 2 (no movement)", got)
	}
}

// C154 - The first entry into PENDING_REFERENCE persists exactly one
// WagerTransactionPendingReference.
func TestPendingReferenceEventCount(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	refund := h.command(view, financial.KindRefund, "25.00")
	refund.ReferenceExternalID = newExternalID("bet")
	result := h.mustSubmit(refund)
	if result.State != financial.StatePendingReference {
		t.Fatalf("state = %s, want PENDING_REFERENCE", result.State)
	}
	if got := h.outboxCountOfType(view.ID, "WagerTransactionPendingReference"); got != 1 {
		t.Errorf("pending reference events = %d, want 1", got)
	}
}

// C158 - Retrying publication preserves event identity, type, version,
// aggregate, occurrence time and payload bytes.
func TestOutboxMetadataStability(t *testing.T) {
	h := newHarness(t)
	clearOutbox(t)
	view := h.openWallet("1000.00")
	h.mustSubmit(h.command(view, financial.KindBet, "10.00"))
	now := time.Now().UTC()
	claimed, err := h.store.ClaimDueEvents(h.ctx(), now, 50, 30*time.Second)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("claim = %d records, err %v", len(claimed), err)
	}
	original := claimed[0]
	if err := h.store.RescheduleEvent(h.ctx(), original.EventID, now, now.Add(time.Second), original.Attempts); err != nil {
		t.Fatalf("rescheduling: %v", err)
	}
	reclaimed, err := h.store.ClaimDueEvents(h.ctx(), now.Add(2*time.Second), 50, 30*time.Second)
	if err != nil {
		t.Fatalf("reclaiming: %v", err)
	}
	var again application.OutboxRecord
	for _, record := range reclaimed {
		if record.EventID == original.EventID {
			again = record
		}
	}
	if again.EventID == "" {
		t.Fatal("event was not reclaimed after the retry delay")
	}
	if !bytes.Equal(original.Payload, again.Payload) {
		t.Error("payload bytes changed between publication attempts")
	}
	if again.EventType != original.EventType || again.AggregateID != original.AggregateID {
		t.Error("event metadata changed between publication attempts")
	}
	var originalDecoded, againDecoded decodedEvent
	if err := decodeRecord(original, &originalDecoded); err != nil {
		t.Fatalf("decoding original: %v", err)
	}
	if err := decodeRecord(again, &againDecoded); err != nil {
		t.Fatalf("decoding reclaimed: %v", err)
	}
	if originalDecoded.EventID != againDecoded.EventID || originalDecoded.OccurredAt != againDecoded.OccurredAt ||
		originalDecoded.Version != againDecoded.Version || originalDecoded.AggregateID != againDecoded.AggregateID {
		t.Errorf("envelope changed: %+v vs %+v", originalDecoded, againDecoded)
	}
}

// C100 - PostgreSQL or SQS becoming temporarily unavailable retains committed
// results and outbox events for retry.
func TestInfrastructureUnavailableRecovery(t *testing.T) {
	h := newHarness(t)
	clearOutbox(t)
	view := h.openWallet("1000.00")
	result := h.mustSubmit(h.command(view, financial.KindBet, "25.00"))
	balance := h.walletBalance(view.ID)

	sender := &recordingSender{fail: true}
	publisher := outbox.New(h.store, sender)
	if _, err := publisher.PublishOnce(h.ctx()); err != nil {
		t.Fatalf("pass with the broker offline: %v", err)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != balance.MinorUnits() {
		t.Errorf("balance = %d, want the committed %d", got, balance.MinorUnits())
	}
	var unpublished int
	if err := adminPool.QueryRow(h.ctx(), `SELECT count(*) FROM outbox_events WHERE "publishedAt" IS NULL`).Scan(&unpublished); err != nil {
		t.Fatalf("counting unpublished events: %v", err)
	}
	if unpublished == 0 {
		t.Fatal("committed outbox events were lost while the broker was unavailable")
	}

	sender.setFail(false)
	if _, err := adminPool.Exec(h.ctx(), `UPDATE outbox_events SET "nextAttemptAt" = now() - interval '1 second' WHERE "publishedAt" IS NULL`); err != nil {
		t.Fatalf("forcing retries: %v", err)
	}
	if published, err := publisher.PublishOnce(h.ctx()); err != nil {
		t.Fatalf("recovery pass: %v", err)
	} else if published == 0 {
		t.Error("no event was published after the broker recovered")
	}
	if stored := h.transactionState(result.TransactionID); stored != financial.StateProcessed {
		t.Errorf("transaction state = %s, want PROCESSED preserved", stored)
	}
}

// C228 - A persisted ledger entry stores its identity, wallet, transaction,
// direction, money, balances and creation time.
func TestLedgerEntryFields(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	bet := h.command(view, financial.KindBet, "25.00")
	result := h.mustSubmit(bet)
	entries := h.ledgerRows(view.ID)
	if len(entries) != 2 {
		t.Fatalf("ledger entries = %d, want 2", len(entries))
	}
	opening, debit := entries[0], entries[1]
	if opening.ID == "" || opening.WalletID != view.ID.String() || opening.TransactionID == "" {
		t.Errorf("opening entry identity incomplete: %+v", opening)
	}
	if opening.Direction != "CREDIT" || opening.Amount != 100000 || opening.BalanceBefore != 0 || opening.BalanceAfter != 100000 {
		t.Errorf("opening entry = %+v", opening)
	}
	if opening.CreatedAt.IsZero() {
		t.Error("opening entry creation time is missing")
	}
	if debit.TransactionID != result.TransactionID.String() || debit.Direction != "DEBIT" {
		t.Errorf("debit entry = %+v, want transaction %s DEBIT", debit, result.TransactionID)
	}
	if debit.Amount != 2500 || debit.BalanceBefore != 100000 || debit.BalanceAfter != 97500 {
		t.Errorf("debit arithmetic = %+v", debit)
	}
}

// C229 - A persisted inbox delivery stores consumer name, message ID, digest,
// receipt time and completion time.
func TestInboxDeliveryFields(t *testing.T) {
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
	var (
		consumerName, messageID, digest string
		receivedAt                      time.Time
		completedAt                     *time.Time
	)
	if err := adminPool.QueryRow(h.ctx(),
		`SELECT "consumerName", "messageId", "digest", "receivedAt", "completedAt" FROM inbox_deliveries WHERE "messageId" = $1`,
		envelope.MessageID,
	).Scan(&consumerName, &messageID, &digest, &receivedAt, &completedAt); err != nil {
		t.Fatalf("reading inbox delivery: %v", err)
	}
	if consumerName != "wager-transactions" || messageID != envelope.MessageID {
		t.Errorf("inbox identity = %q/%q", consumerName, messageID)
	}
	if digest != hex.EncodeToString(sum[:]) {
		t.Errorf("digest = %s, want the sha256 of the received payload", digest)
	}
	if receivedAt.IsZero() {
		t.Error("inbox receipt time is missing")
	}
	if completedAt == nil {
		t.Error("inbox completion time is missing")
	}
}

type sendAPIFake struct {
	mu     sync.Mutex
	inputs []*awssqs.SendMessageInput
}

func (f *sendAPIFake) SendMessage(_ context.Context, params *awssqs.SendMessageInput, _ ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, params)
	return &awssqs.SendMessageOutput{}, nil
}

func decodeRecord(record application.OutboxRecord, target *decodedEvent) error {
	return json.Unmarshal(record.Payload, target)
}
