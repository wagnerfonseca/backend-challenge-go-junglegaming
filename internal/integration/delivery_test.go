//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/fx"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/app"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/failpoint"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/outbox"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/sqs"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/sqs/consumer"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// freeAddr reserves one loopback port for a service subprocess.
func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a loopback port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the loopback port: %v", err)
	}
	return addr
}

// serviceProcessEnv builds the environment of one real service subprocess.
func serviceProcessEnv(t *testing.T, addr string, queues sqs.Queues, instance string) []string {
	t.Helper()
	return append(os.Environ(),
		"APP_ENV=integration",
		"DATABASE_URL="+applicationDSN(t),
		"SERVICE_NAME=wager-delivery",
		"INSTANCE_ID="+instance,
		"LOG_LEVEL=info",
		"HTTP_ADDR="+addr,
		"OIDC_ISSUER_URL="+keycloakIssuer,
		"OIDC_AUDIENCE=wager-api",
		"SQS_ENDPOINT="+localstackEndpoint,
		"SQS_INGRESS_QUEUE_URL="+queues.IngressURL,
		"SQS_EVENT_QUEUE_URL="+queues.EventURL,
		"SQS_PROVIDER_SENDER_IDS=000000000000=provider-a",
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"AWS_REGION=us-east-1",
	)
}

// serviceInstance is one running service subprocess.
type serviceInstance struct {
	t      *testing.T
	cmd    *exec.Cmd
	addr   string
	output *safeBuffer
}

// startServiceInstance builds (once) and starts one real service process.
func startServiceInstance(t *testing.T, queues sqs.Queues, instance string, extraEnv ...string) *serviceInstance {
	t.Helper()
	binary := buildServiceBinary(t)
	addr := freeAddr(t)
	output := &safeBuffer{}
	cmd := exec.Command(binary)
	cmd.Env = append(serviceProcessEnv(t, addr, queues, instance), extraEnv...)
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the service instance: %v", err)
	}
	instanceHandle := &serviceInstance{t: t, cmd: cmd, addr: addr, output: output}
	t.Cleanup(func() { instanceHandle.stop() })
	return instanceHandle
}

// waitForReady polls the readiness route until it returns 200.
func (s *serviceInstance) waitForReady(timeout time.Duration) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + s.addr + "/health/ready")
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.t.Fatalf("service instance on %s did not become ready: %s", s.addr, s.output.String())
}

// stop sends SIGTERM and waits for the process to exit.
func (s *serviceInstance) stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
	}
	s.cmd = nil
}

// httpCall performs one HTTP call against a service instance.
func (s *serviceInstance) httpCall(method, path, body string, headers map[string]string) (int, []byte) {
	s.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, "http://"+s.addr+path, reader)
	if err != nil {
		s.t.Fatalf("building the request: %v", err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		s.t.Fatalf("calling %s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		s.t.Fatalf("reading the response: %v", err)
	}
	return response.StatusCode, data
}

// C174 - The integration suite processes distinct wallets concurrently without
// waiting on one wallet-global lock.
func TestDistinctWalletsConcurrent(t *testing.T) {
	h := newHarness(t)
	const wallets = 8
	views := make([]application.WalletView, wallets)
	for i := range views {
		views[i] = h.openWallet("100.00")
	}

	// Hold a row lock on the first wallet for the whole test; every other
	// wallet must still commit independently.
	tx, err := adminPool.Begin(h.ctx())
	if err != nil {
		t.Fatalf("beginning the lock transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(h.ctx()) }()
	if _, err := tx.Exec(h.ctx(), `SELECT 1 FROM wallets WHERE "id" = $1 FOR UPDATE`, views[0].ID.String()); err != nil {
		t.Fatalf("locking the first wallet: %v", err)
	}

	commands := make([]application.SubmitWagerCommand, wallets-1)
	for i := 1; i < wallets; i++ {
		commands[i-1] = h.command(views[i], financial.KindBet, "10.00")
	}
	done := make(chan error, len(commands))
	for _, command := range commands {
		go func(cmd application.SubmitWagerCommand) {
			_, err := h.service.SubmitWagerTransaction(context.Background(), cmd)
			done <- err
		}(command)
	}
	for range commands {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("independent wallet operation failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("distinct wallet operations were blocked by one wallet lock")
		}
	}
	for i := 1; i < wallets; i++ {
		if got := h.walletBalance(views[i].ID).MinorUnits(); got != 9000 {
			t.Errorf("wallet %d balance = %d, want 9000", i, got)
		}
		if got := h.ledgerForWallet(views[i].ID); got != 2 {
			t.Errorf("wallet %d ledger entries = %d, want 2", i, got)
		}
	}
}

// C175 - The integration suite runs concurrency scenarios through at least
// three independent application processes with separate memory and
// connections.
func TestThreeProcessIntegration(t *testing.T) {
	queues, _ := provisionIsolatedQueues(t)
	instances := []*serviceInstance{
		startServiceInstance(t, queues, "instance-1"),
		startServiceInstance(t, queues, "instance-2"),
		startServiceInstance(t, queues, "instance-3"),
	}
	for _, instance := range instances {
		instance.waitForReady(60 * time.Second)
	}

	internal := keycloakToken(t, internalAccount)
	provider := keycloakToken(t, providerAAccount)

	playerID := financial.NewPlayerID()
	status, body := instances[0].httpCall(http.MethodPost, "/wallets",
		fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":"100.00","currency":"BRL"}}`, playerID.String()),
		bearer(internal))
	requireStatus(t, status, 201, body)
	wallet := decodeJSONBody[walletJSON](t, body)

	commands := make([]application.SubmitWagerCommand, 20)
	for i := range commands {
		commands[i] = application.SubmitWagerCommand{
			ProviderID:            "provider-a",
			ExternalTransactionID: newExternalID("ext"),
			IdempotencyKey:        financial.IdempotencyKey("key-" + uuid.NewString()),
			WalletID:              mustParseWallet(wallet.ID),
			PlayerID:              playerID,
			RoundID:               newExternalID("round"),
			GameID:                newExternalID("game"),
			Kind:                  financial.KindBet,
			Amount:                mustMoney(t, "10.00", "BRL"),
		}
	}
	results := make([]int, len(commands))
	var wait sync.WaitGroup
	for i, command := range commands {
		wait.Add(1)
		go func(index int, cmd application.SubmitWagerCommand) {
			defer wait.Done()
			instance := instances[index%len(instances)]
			encoded, err := json.Marshal(wagerJSONOf(cmd))
			if err != nil {
				t.Errorf("encoding command %d: %v", index, err)
				return
			}
			results[index], _ = instance.httpCall(http.MethodPost, "/wagering/transactions", string(encoded),
				withBearer(provider, map[string]string{"Idempotency-Key": cmd.IdempotencyKey.String()}))
		}(i, command)
	}
	wait.Wait()

	processed, rejected := 0, 0
	for i := range commands {
		if results[i] != 200 {
			t.Fatalf("command %d status = %d, want 200", i, results[i])
		}
	}
	// Read the persisted outcome per command instead of trusting the client
	// decode: exactly ten debits must have committed.
	for _, command := range commands {
		record := transactionRecord(t, "provider-a", command.ExternalTransactionID.String())
		switch record.State {
		case "PROCESSED":
			processed++
		case "REJECTED":
			rejected++
		}
	}
	if processed != 10 || rejected != 10 {
		t.Fatalf("processed/rejected = %d/%d, want 10/10", processed, rejected)
	}
	harness := newHarness(t)
	if got := harness.walletBalance(mustParseWallet(wallet.ID)).MinorUnits(); got != 0 {
		t.Errorf("balance = %d, want 0", got)
	}
	if got := harness.ledgerForWallet(mustParseWallet(wallet.ID)); got != 11 {
		t.Errorf("ledger entries = %d, want 11 (opening and ten debits)", got)
	}
	if got := harness.transactionsForWallet(mustParseWallet(wallet.ID)); got != 21 {
		t.Errorf("transactions = %d, want 21 (opening and twenty commands)", got)
	}

	for _, instance := range instances {
		instance.stop()
	}
}

// transactionRecord reads one persisted transaction state by provider and
// external identity.
func transactionRecord(t *testing.T, provider, external string) persistedTransaction {
	t.Helper()
	row := adminPool.QueryRow(context.Background(),
		`SELECT `+persistedTransactionColumns+` FROM wager_transactions WHERE "providerId" = $1 AND "externalTransactionId" = $2`,
		provider, external)
	var record persistedTransaction
	err := row.Scan(
		&record.ID, &record.Origin, &record.Kind, &record.State,
		&record.ProviderID, &record.ExternalTransactionID, &record.IdempotencyKey,
		&record.DigestVersion, &record.Digest,
		&record.WalletID, &record.PlayerID,
		&record.RoundID, &record.GameID,
		&record.Amount, &record.Currency,
		&record.ReferenceExternalID, &record.ReferenceTransactionID,
		&record.FailureCode, &record.ObservedBalance,
		&record.ReferenceDeadline, &record.NextAttemptAt, &record.ReferenceAttempts, &record.ClaimedUntil,
		&record.CreatedAt, &record.UpdatedAt,
	)
	if err != nil {
		t.Fatalf("reading persisted transaction %s/%s: %v", provider, external, err)
	}
	return record
}

// C176 - The integration suite interrupts a consumer after commit and before
// the SQS delete and observes safe redelivery.
func TestConsumerInterruption(t *testing.T) {
	h := newHarness(t)
	queues, client := provisionIsolatedQueues(t)
	h.failpoints = failpoint.New([]string{failpoint.SQSAfterCommit})

	view := h.openWallet("1000.00")
	command := h.command(view, financial.KindBet, "25.00")
	messageID := "interrupt-" + newCorrelation()
	sender := sqs.NewIngressSender(client, queues.IngressURL)
	if err := sender.SendWagerRequest(context.Background(), messageID, view.ID.String(), envelopeJSON(t, envelopeFor(t, command, messageID))); err != nil {
		t.Fatalf("sending the ingress message: %v", err)
	}

	interrupted := consumer.New(consumer.Config{
		QueueURL:          queues.IngressURL,
		ConsumerName:      "wager-transactions",
		ReceiveWaitTime:   5 * time.Second,
		VisibilityTimeout: time.Second,
		RenewEvery:        500 * time.Millisecond,
		ProviderForSender: func(string) (string, bool) { return "provider-a", true },
	}, client, h.service, consumer.WithFailpoints(h.failpoints))
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("the post-commit failpoint did not interrupt the consumer")
			}
		}()
		_ = interrupted.PollOnce(context.Background())
	}()

	if !h.transactionExists(command.ProviderID.String(), command.ExternalTransactionID) {
		t.Fatal("the committed transaction is missing after the interruption")
	}
	ledgerAfterCommit := h.ledgerForWallet(view.ID)

	// The message was never acknowledged; after the one-second visibility it
	// is redelivered and deduplicated by the durable inbox.
	h.failpoints = failpoint.New(nil)
	time.Sleep(1500 * time.Millisecond)
	redelivered := consumer.New(consumer.Config{
		QueueURL:          queues.IngressURL,
		ConsumerName:      "wager-transactions",
		ReceiveWaitTime:   5 * time.Second,
		VisibilityTimeout: 5 * time.Second,
		ProviderForSender: func(string) (string, bool) { return "provider-a", true },
	}, client, h.service)
	if err := redelivered.PollOnce(context.Background()); err != nil {
		t.Fatalf("redelivery pass: %v", err)
	}
	if got := h.ledgerForWallet(view.ID); got != ledgerAfterCommit {
		t.Errorf("ledger entries = %d, want %d (no second movement)", got, ledgerAfterCommit)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 97500 {
		t.Errorf("balance = %d, want 97500", got)
	}
	if remaining := receiveFromQueue(t, client, queues.IngressURL, 3); remaining != "" {
		t.Errorf("redelivered message was not acknowledged: %q", remaining)
	}
}

// C177 - The integration suite runs two publishers against one outbox and
// observes abandoned-lease recovery.
func TestOutboxPublisherRace(t *testing.T) {
	h := newHarness(t)
	// The shared test database accumulates unpublished events from earlier
	// tests; confirm them so this scenario observes only its own batch.
	if _, err := adminPool.Exec(h.ctx(),
		`UPDATE outbox_events SET "publishedAt" = now(), "claimedUntil" = NULL WHERE "publishedAt" IS NULL`); err != nil {
		t.Fatalf("draining earlier outbox events: %v", err)
	}
	view := h.openWallet("1000.00")
	h.mustSubmit(h.command(view, financial.KindBet, "25.00"))
	if got := h.outboxForWallet(view.ID); got == 0 {
		t.Fatal("the scenario produced no outbox events")
	}

	base := time.Now().UTC()
	// Publisher A claims the batch and stops before publishing.
	claimed, err := h.store.ClaimDueEvents(h.ctx(), base, 50, 30*time.Second)
	if err != nil {
		t.Fatalf("first publisher claim: %v", err)
	}
	if len(claimed) == 0 {
		t.Fatal("first publisher claimed no events")
	}

	// Publisher B cannot claim the abandoned lease yet.
	blocked, err := h.store.ClaimDueEvents(h.ctx(), base.Add(10*time.Second), 50, 30*time.Second)
	if err != nil {
		t.Fatalf("second publisher early claim: %v", err)
	}
	if len(blocked) != 0 {
		t.Fatalf("second publisher claimed %d leased events, want 0", len(blocked))
	}

	// After the lease expires the second publisher recovers the events.
	recorder := &publishRecorder{}
	publisher := outbox.New(h.store, recorder, outbox.WithClock(func() time.Time { return base.Add(31 * time.Second) }))
	published, err := publisher.PublishOnce(h.ctx())
	if err != nil {
		t.Fatalf("second publisher pass: %v", err)
	}
	if published != len(claimed) {
		t.Errorf("recovered publications = %d, want %d", published, len(claimed))
	}
	if len(recorder.eventIDs()) != len(claimed) {
		t.Errorf("published events = %d, want %d", len(recorder.eventIDs()), len(claimed))
	}
	if got := h.countRows(`SELECT count(*) FROM outbox_events WHERE "aggregateId" = $1 AND "publishedAt" IS NULL`, view.ID.String()); got != 0 {
		t.Errorf("unpublished events = %d, want 0 after recovery", got)
	}
}

// publishRecorder records published event identities.
type publishRecorder struct {
	mu    sync.Mutex
	ids   []string
	types []string
}

func (r *publishRecorder) Publish(_ context.Context, record application.OutboxRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, record.EventID)
	r.types = append(r.types, record.EventType)
	return nil
}

func (r *publishRecorder) eventIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

// C178 - The integration suite delivers an early REFUND and ROLLBACK, resolves
// one by a late reference and rejects the other at its 24-hour expiry.
func TestReferenceTimingIntegration(t *testing.T) {
	h := newHarness(t)
	refundWallet := h.openWallet("1000.00")
	rollbackWallet := h.openWallet("1000.00")

	refund := h.command(refundWallet, financial.KindRefund, "100.00")
	refund.ReferenceExternalID = newExternalID("missing-bet")
	pendingRefund := h.mustSubmit(refund)
	if pendingRefund.State != financial.StatePendingReference {
		t.Fatalf("refund state = %s, want PENDING_REFERENCE", pendingRefund.State)
	}

	rollback := h.command(rollbackWallet, financial.KindRollback, "100.00")
	lateReference := newExternalID("late-bet")
	rollback.ReferenceExternalID = lateReference
	pendingRollback := h.mustSubmit(rollback)
	if pendingRollback.State != financial.StatePendingReference {
		t.Fatalf("rollback state = %s, want PENDING_REFERENCE", pendingRollback.State)
	}

	// The late reference resolves the rollback; it must share the reversal's
	// player, wallet, currency and round identity.
	bet := h.command(rollbackWallet, financial.KindBet, "100.00")
	bet.ExternalTransactionID = lateReference
	bet.RoundID = rollback.RoundID
	bet.GameID = rollback.GameID
	processedBet := h.mustSubmit(bet)
	if processedBet.State != financial.StateProcessed {
		t.Fatalf("late bet state = %s, want PROCESSED", processedBet.State)
	}
	resolved, err := h.service.ResolvePendingReference(h.ctx(), application.ResolvePendingReferenceCommand{
		TransactionID: pendingRollback.TransactionID,
		CorrelationID: newCorrelation(),
	})
	if err != nil {
		t.Fatalf("resolving the rollback: %v", err)
	}
	if resolved.State != financial.StateProcessed {
		t.Errorf("rollback state = %s, want PROCESSED", resolved.State)
	}
	if got := h.walletBalance(rollbackWallet.ID).MinorUnits(); got != 100000 {
		t.Errorf("rollback wallet balance = %d, want 100000 (debit and reversal)", got)
	}

	// The refund expires at its 24-hour deadline.
	h.clock.Advance(25 * time.Hour)
	expired, err := h.service.ResolvePendingReference(h.ctx(), application.ResolvePendingReferenceCommand{
		TransactionID: pendingRefund.TransactionID,
		CorrelationID: newCorrelation(),
	})
	if err != nil {
		t.Fatalf("expiring the refund: %v", err)
	}
	if expired.State != financial.StateRejected || expired.FailureCode != financial.FailureReferenceNotFound {
		t.Errorf("expired refund = %s/%s, want REJECTED/REFERENCE_NOT_FOUND", expired.State, expired.FailureCode)
	}
	if got := h.walletBalance(refundWallet.ID).MinorUnits(); got != 100000 {
		t.Errorf("refund wallet balance = %d, want 100000 (no movement)", got)
	}
	if got := h.ledgerForWallet(refundWallet.ID); got != 1 {
		t.Errorf("refund ledger entries = %d, want 1 (only the opening)", got)
	}
}

// C179 - The integration suite restarts all application processes and
// preserves idempotency, pending references, outbox work, balances and ledger
// consistency.
func TestProcessRestartIntegration(t *testing.T) {
	queues, _ := provisionIsolatedQueues(t)
	instances := []*serviceInstance{
		startServiceInstance(t, queues, "restart-1"),
		startServiceInstance(t, queues, "restart-2"),
		startServiceInstance(t, queues, "restart-3"),
	}
	for _, instance := range instances {
		instance.waitForReady(60 * time.Second)
	}
	internal := keycloakToken(t, internalAccount)
	provider := keycloakToken(t, providerAAccount)

	playerID := financial.NewPlayerID()
	status, body := instances[0].httpCall(http.MethodPost, "/wallets",
		fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":"1000.00","currency":"BRL"}}`, playerID.String()),
		bearer(internal))
	requireStatus(t, status, 201, body)
	wallet := decodeJSONBody[walletJSON](t, body)
	walletID := mustParseWallet(wallet.ID)

	bet := application.SubmitWagerCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: newExternalID("restart-bet"),
		IdempotencyKey:        financial.IdempotencyKey("key-" + uuid.NewString()),
		WalletID:              walletID,
		PlayerID:              playerID,
		RoundID:               newExternalID("round"),
		GameID:                newExternalID("game"),
		Kind:                  financial.KindBet,
		Amount:                mustMoney(t, "25.00", "BRL"),
	}
	encodedBet, _ := json.Marshal(wagerJSONOf(bet))
	status, body = instances[0].httpCall(http.MethodPost, "/wagering/transactions", string(encodedBet),
		withBearer(provider, map[string]string{"Idempotency-Key": bet.IdempotencyKey.String()}))
	requireStatus(t, status, 200, body)
	betResult := decodeJSONBody[wagerResultJSON](t, body)

	win := application.SubmitWagerCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: newExternalID("restart-win"),
		IdempotencyKey:        financial.IdempotencyKey("key-" + uuid.NewString()),
		WalletID:              walletID,
		PlayerID:              playerID,
		RoundID:               newExternalID("round"),
		GameID:                newExternalID("game"),
		Kind:                  financial.KindWin,
		Amount:                mustMoney(t, "10.00", "BRL"),
		ReferenceExternalID:   newExternalID("missing-win-bet"),
	}
	encodedWin, _ := json.Marshal(wagerJSONOf(win))
	status, body = instances[1].httpCall(http.MethodPost, "/wagering/transactions", string(encodedWin),
		withBearer(provider, map[string]string{"Idempotency-Key": win.IdempotencyKey.String()}))
	requireStatus(t, status, 202, body)
	winResult := decodeJSONBody[wagerResultJSON](t, body)

	for _, instance := range instances {
		instance.stop()
	}

	restarted := []*serviceInstance{
		startServiceInstance(t, queues, "restart-1b"),
		startServiceInstance(t, queues, "restart-2b"),
		startServiceInstance(t, queues, "restart-3b"),
	}
	for _, instance := range restarted {
		instance.waitForReady(60 * time.Second)
	}

	// The replayed bet keeps its identity and adds no movement.
	status, body = restarted[0].httpCall(http.MethodPost, "/wagering/transactions", string(encodedBet),
		withBearer(provider, map[string]string{"Idempotency-Key": bet.IdempotencyKey.String()}))
	requireStatus(t, status, 200, body)
	replay := decodeJSONBody[wagerResultJSON](t, body)
	if !replay.IdempotentReplay || replay.TransactionID != betResult.TransactionID {
		t.Errorf("replay = %+v, want idempotent replay of %s", replay, betResult.TransactionID)
	}

	// The pending reference survives the restart and is resolved by the late
	// bet submitted after it.
	lateBet := application.SubmitWagerCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: financial.ExternalID(win.ReferenceExternalID.String()),
		IdempotencyKey:        financial.IdempotencyKey("key-" + uuid.NewString()),
		WalletID:              walletID,
		PlayerID:              playerID,
		RoundID:               win.RoundID,
		GameID:                win.GameID,
		Kind:                  financial.KindBet,
		Amount:                mustMoney(t, "10.00", "BRL"),
	}
	encodedLateBet, _ := json.Marshal(wagerJSONOf(lateBet))
	status, body = restarted[1].httpCall(http.MethodPost, "/wagering/transactions", string(encodedLateBet),
		withBearer(provider, map[string]string{"Idempotency-Key": lateBet.IdempotencyKey.String()}))
	requireStatus(t, status, 200, body)

	// The reference worker resolves the pending win and the outbox drains.
	harness := newHarness(t)
	waitFor(t, 30*time.Second, func() bool {
		return harness.transactionState(mustParseTransaction(t, winResult.TransactionID)) == financial.StateProcessed
	}, "the pending win was not resolved after the restart")
	waitFor(t, 30*time.Second, func() bool {
		return harness.countRows(`SELECT count(*) FROM outbox_events WHERE "aggregateId" = $1 AND "publishedAt" IS NULL`, walletID.String()) == 0
	}, "the outbox did not drain after the restart")

	if got := harness.walletBalance(walletID).MinorUnits(); got != 97500 {
		t.Errorf("balance = %d, want 97500 (1000 - 25 - 10 + 10)", got)
	}
	if got := harness.ledgerForWallet(walletID); got != 4 {
		t.Errorf("ledger entries = %d, want 4 (opening, bet, late bet, win)", got)
	}
	if got := harness.transactionsForWallet(walletID); got != 4 {
		t.Errorf("transactions = %d, want 4 (opening, bet, win, late bet)", got)
	}
	for _, instance := range restarted {
		instance.stop()
	}
}

// waitFor polls one condition until it holds or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal(message)
}

// C180 - The integration suite crosses HTTP and SQS for the same operation and
// observes one financial result.
func TestHTTPAndSQSIntegration(t *testing.T) {
	h := newHarness(t)
	runtime := newHTTPRuntimeWith(t, h, allScopes("provider-a"), nil)
	queues, client := provisionIsolatedQueues(t)
	view := h.openWallet("1000.00")
	command := h.command(view, financial.KindBet, "25.00")

	status, body := runtime.postWager(wagerJSONOf(command), command.IdempotencyKey.String())
	requireStatus(t, status, 200, body)
	overHTTP := decodeJSONBody[wagerResultJSON](t, body)

	messageID := "cross-" + newCorrelation()
	sender := sqs.NewIngressSender(client, queues.IngressURL)
	if err := sender.SendWagerRequest(context.Background(), messageID, view.ID.String(), envelopeJSON(t, envelopeFor(t, command, messageID))); err != nil {
		t.Fatalf("sending the SQS delivery: %v", err)
	}
	realConsumer := consumer.New(consumer.Config{
		QueueURL:          queues.IngressURL,
		ConsumerName:      "wager-transactions",
		ReceiveWaitTime:   5 * time.Second,
		VisibilityTimeout: 5 * time.Second,
		ProviderForSender: func(string) (string, bool) { return "provider-a", true },
	}, client, h.service)
	if err := realConsumer.PollOnce(context.Background()); err != nil {
		t.Fatalf("SQS pass: %v", err)
	}
	if got := h.transactionsForWallet(view.ID); got != 2 {
		t.Errorf("transactions = %d, want 2 (opening and one bet)", got)
	}
	if got := h.ledgerForWallet(view.ID); got != 2 {
		t.Errorf("ledger entries = %d, want 2 (one debit)", got)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 97500 {
		t.Errorf("balance = %d, want 97500", got)
	}
	if remaining := receiveFromQueue(t, client, queues.IngressURL, 3); remaining != "" {
		t.Errorf("SQS delivery was not acknowledged: %q", remaining)
	}
	if overHTTP.TransactionID == "" {
		t.Error("HTTP ingress returned no transaction id")
	}
}

// C181 - The integration suite compares every stored balance with opening plus
// ledger credits minus ledger debits.
func TestBalanceVerification(t *testing.T) {
	h := newHarness(t)
	type scenario struct {
		wallet application.WalletView
	}
	scenarios := make([]scenario, 0, 3)
	for i := 0; i < 3; i++ {
		view := h.openWallet("1000.00")
		h.mustSubmit(h.command(view, financial.KindBet, "25.00"))
		h.mustSubmit(h.command(view, financial.KindWin, "10.00"))
		loss := h.command(view, financial.KindLoss, "0.00")
		h.mustSubmit(loss)
		scenarios = append(scenarios, scenario{wallet: view})
	}
	// A refund and a rollback exercise credit and debit paths.
	refundWallet := h.openWallet("500.00")
	bet := h.command(refundWallet, financial.KindBet, "100.00")
	h.mustSubmit(bet)
	refund := h.command(refundWallet, financial.KindRefund, "100.00")
	refund.ReferenceExternalID = bet.ExternalTransactionID
	h.mustSubmit(refund)
	scenarios = append(scenarios, scenario{wallet: refundWallet})

	for _, entry := range scenarios {
		stored := h.walletBalance(entry.wallet.ID)
		var credits, debits int64
		if err := adminPool.QueryRow(h.ctx(),
			`SELECT COALESCE(SUM(CASE WHEN "direction" = 'CREDIT' THEN "amount" ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN "direction" = 'DEBIT' THEN "amount" ELSE 0 END), 0)
			FROM wallet_ledger_entries WHERE "walletId" = $1`, entry.wallet.ID.String(),
		).Scan(&credits, &debits); err != nil {
			t.Fatalf("summing the ledger of %s: %v", entry.wallet.ID, err)
		}
		calculated := credits - debits
		if stored.MinorUnits() != calculated {
			t.Errorf("wallet %s stored balance = %d, want %d (credits minus debits)", entry.wallet.ID, stored.MinorUnits(), calculated)
		}
		var lastBalance int64
		if err := adminPool.QueryRow(h.ctx(),
			`SELECT "balanceAfter" FROM wallet_ledger_entries WHERE "walletId" = $1 ORDER BY "createdAt" DESC, "id" DESC LIMIT 1`,
			entry.wallet.ID.String(),
		).Scan(&lastBalance); err != nil {
			t.Fatalf("reading the last ledger balance of %s: %v", entry.wallet.ID, err)
		}
		if stored.MinorUnits() != lastBalance {
			t.Errorf("wallet %s stored balance = %d, want the last ledger balance %d", entry.wallet.ID, stored.MinorUnits(), lastBalance)
		}
	}
}

// C182 - The integration suite starts and stops the Fx graph and observes all
// worker goroutines and resources terminate.
func TestFxLifecycle(t *testing.T) {
	recorder := &lifecycleRecorder{}
	application, err := app.New(testAppConfig(t), fx.Decorate(func(app.Recorder) app.Recorder { return recorder }))
	if err != nil {
		t.Fatalf("building the application graph: %v", err)
	}
	baseline := runtimeGoroutines()
	startCtx, cancelStart := context.WithTimeout(context.Background(), 20*time.Second)
	if err := application.Start(startCtx); err != nil {
		cancelStart()
		t.Fatalf("starting the application graph: %v", err)
	}
	cancelStart()
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 30*time.Second)
	if err := application.Stop(stopCtx); err != nil {
		cancelStop()
		t.Fatalf("stopping the application graph: %v", err)
	}
	cancelStop()

	for _, event := range []string{"http.stopped", "sqs.stopped", "outbox.stopped", "reference.stopped", "postgres.closed"} {
		if recorder.index(event) == -1 {
			t.Errorf("lifecycle event %s was not observed: %v", event, recorder.snapshot())
		}
	}
	settled := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if runtimeGoroutines() <= baseline+2 {
			settled = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !settled {
		t.Errorf("goroutines did not terminate: baseline %d, now %d", baseline, runtimeGoroutines())
	}
}

// runtimeGoroutines reports the live goroutine count.
func runtimeGoroutines() int {
	return runtime.NumGoroutine()
}
