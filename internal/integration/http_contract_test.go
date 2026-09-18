//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/app"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/config"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/health"
	httpadapter "github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http/middleware"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/logging"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/metrics"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/sqs"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/worker"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// C20 - An external command in a currency other than BRL returns 422 with
// UNSUPPORTED_CURRENCY over HTTP.
func TestUnsupportedCurrency(t *testing.T) {
	runtime := newHTTPRuntime(t)
	view := runtime.harness.openWallet("1000.00")
	command := runtime.harness.command(view, financial.KindBet, "25.00")
	command.Amount = mustMoney(t, "25.00", "USD")
	status, data := runtime.postWager(wagerJSONOf(command), command.IdempotencyKey.String())
	requireStatus(t, status, 422, data)
	if envelope := decodeError(t, data); envelope.Error.Code != "UNSUPPORTED_CURRENCY" {
		t.Errorf("code = %s, want UNSUPPORTED_CURRENCY", envelope.Error.Code)
	}
	if runtime.harness.transactionExists(command.ProviderID.String(), command.ExternalTransactionID) {
		t.Error("unsupported currency persisted a transaction")
	}
}

// C213 - While 256 business requests are active the next one receives 503 with
// Retry-After: 1.
func TestRateLimit256(t *testing.T) {
	blocking := &blockingUseCases{entered: make(chan struct{}, 512), release: make(chan struct{})}
	runtime := newHandlerRuntime(t, nil, blocking, allScopes("provider-a"), 256)
	defer close(blocking.release)

	var wait sync.WaitGroup
	for i := 0; i < 256; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			runtime.postWallet(openWalletJSON{PlayerID: financial.NewPlayerID().String(), InitialBalance: moneyJSON{Amount: "10.00", Currency: "BRL"}})
		}()
	}
	deadline := time.After(10 * time.Second)
	for i := 0; i < 256; i++ {
		select {
		case <-blocking.entered:
		case <-deadline:
			t.Fatalf("only %d of 256 requests entered the use case", i)
		}
	}
	status, data := runtime.postWallet(openWalletJSON{PlayerID: financial.NewPlayerID().String(), InitialBalance: moneyJSON{Amount: "10.00", Currency: "BRL"}})
	requireStatus(t, status, http.StatusServiceUnavailable, data)
	if got := runtime.retryAfter(); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	if envelope := decodeError(t, data); envelope.Error.Code != "SERVICE_UNAVAILABLE" {
		t.Errorf("code = %s, want SERVICE_UNAVAILABLE", envelope.Error.Code)
	}
}

// C223 - A body above 1 MiB returns 413 PAYLOAD_TOO_LARGE before decoding.
func TestPayloadTooLarge(t *testing.T) {
	runtime := newHTTPRuntime(t)
	oversized := `{"playerId":"` + strings.Repeat("a", 1<<20) + `"}`
	status, data := runtime.request(http.MethodPost, "/wallets", oversized, nil)
	requireStatus(t, status, http.StatusRequestEntityTooLarge, data)
	if envelope := decodeError(t, data); envelope.Error.Code != "PAYLOAD_TOO_LARGE" {
		t.Errorf("code = %s, want PAYLOAD_TOO_LARGE", envelope.Error.Code)
	}
}

// C227 - A content type other than application/json returns 415
// UNSUPPORTED_MEDIA_TYPE on both write routes.
func TestUnsupportedMediaType(t *testing.T) {
	runtime := newHTTPRuntime(t)
	for _, path := range []string{"/wallets", "/wagering/transactions"} {
		status, data := runtime.request(http.MethodPost, path, `{"playerId":"x"}`, map[string]string{"Content-Type": "text/plain"})
		requireStatus(t, status, http.StatusUnsupportedMediaType, data)
		if envelope := decodeError(t, data); envelope.Error.Code != "UNSUPPORTED_MEDIA_TYPE" {
			t.Errorf("%s code = %s, want UNSUPPORTED_MEDIA_TYPE", path, envelope.Error.Code)
		}
	}
}

// C218 - Empty or out-of-format identities return 422 and persist nothing.
func TestInvalidIdentityRejection(t *testing.T) {
	runtime := newHTTPRuntime(t)
	view := runtime.harness.openWallet("100.00")
	cases := []struct {
		name   string
		mutate func(*wagerJSON)
	}{
		{"walletId out of format", func(body *wagerJSON) { body.WalletID = "not-a-uuid" }},
		{"playerId empty", func(body *wagerJSON) { body.PlayerID = "" }},
		{"externalTransactionId empty", func(body *wagerJSON) { body.ExternalTransactionID = "" }},
		{"roundId out of format", func(body *wagerJSON) { body.RoundID = "round with spaces" }},
		{"gameId empty", func(body *wagerJSON) { body.GameID = "" }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			command := runtime.harness.command(view, financial.KindBet, "10.00")
			body := wagerJSONOf(command)
			testCase.mutate(&body)
			status, data := runtime.postWager(body, "invalid-identity-"+newCorrelation())
			requireStatus(t, status, 422, data)
			envelope := decodeError(t, data)
			if envelope.Error.Code != "INVALID_REQUEST" && envelope.Error.Code != "INVALID_MONEY" {
				t.Errorf("code = %s, want a contract code", envelope.Error.Code)
			}
		})
	}
	if got := runtime.harness.transactionsForWallet(view.ID); got != 1 {
		t.Errorf("transactions = %d, want 1 (only the opening)", got)
	}
}

// C196 - A valid token lacking the exact route scope receives 403 before the
// use case is invoked.
func TestScopeEnforcement(t *testing.T) {
	spy := &blockingUseCases{entered: make(chan struct{}, 8), release: make(chan struct{})}
	defer close(spy.release)
	h := newHarness(t)
	runtime := newHandlerRuntime(t, h, spy, internalClient(middleware.ScopeWalletsRead), 8)
	status, data := runtime.postWallet(openWalletJSON{PlayerID: financial.NewPlayerID().String(), InitialBalance: moneyJSON{Amount: "10.00", Currency: "BRL"}})
	requireStatus(t, status, http.StatusForbidden, data)
	if envelope := decodeError(t, data); envelope.Error.Code != "FORBIDDEN" {
		t.Errorf("code = %s, want FORBIDDEN", envelope.Error.Code)
	}
	select {
	case <-spy.entered:
		t.Error("use case was invoked despite the missing scope")
	case <-time.After(100 * time.Millisecond):
	}

	// The provider token cannot submit wagers without wagering:write.
	runtime = newHandlerRuntime(t, h, spy, internalClient(middleware.ScopeWalletsWrite), 8)
	view := h.openWallet("100.00")
	command := h.command(view, financial.KindBet, "10.00")
	status, data = runtime.postWager(wagerJSONOf(command), command.IdempotencyKey.String())
	requireStatus(t, status, http.StatusForbidden, data)
}

// C164 - Logs never carry credentials, tokens, complete bodies or financial
// payloads.
func TestLogSanitization(t *testing.T) {
	runtime := newHTTPRuntime(t)
	const secret = "super-secret-token-value"
	view := runtime.harness.openWallet("100.00")
	command := runtime.harness.command(view, financial.KindBet, "25.00")
	body, _ := json.Marshal(wagerJSONOf(command))
	runtime.request(http.MethodPost, "/wagering/transactions", string(body), map[string]string{
		"Authorization":   "Bearer " + secret,
		"Idempotency-Key": command.IdempotencyKey.String(),
	})
	runtime.request(http.MethodPost, "/wagering/transactions", "{not-json", map[string]string{"Authorization": "Bearer " + secret, "Idempotency-Key": "key"})
	logs := runtime.logs.String()
	if strings.Contains(logs, secret) {
		t.Error("logs contain the access token")
	}
	if strings.Contains(logs, `"externalTransactionId"`) {
		t.Error("logs contain the complete financial payload")
	}
}

// C165 - The correlation identity crosses HTTP, SQS and the published event.
func TestCorrelationPropagation(t *testing.T) {
	runtime := newHTTPRuntime(t)
	view := runtime.harness.openWallet("1000.00")
	command := runtime.harness.command(view, financial.KindBet, "25.00")
	correlation := "corr-" + newCorrelation()
	headers := map[string]string{"Idempotency-Key": command.IdempotencyKey.String(), "X-Correlation-ID": correlation}
	body, _ := json.Marshal(wagerJSONOf(command))
	status, data := runtime.request(http.MethodPost, "/wagering/transactions", string(body), headers)
	requireStatus(t, status, 200, data)
	if got := runtime.correlation(); got != correlation {
		t.Errorf("X-Correlation-ID = %q, want %q", got, correlation)
	}
	if !hasProcessedEventWithCorrelation(runtime.harness, view.ID, correlation) {
		t.Errorf("no processed event carried correlation %q", correlation)
	}

	// SQS ingress: the envelope correlation reaches the published event.
	sqsCorrelation := "corr-sqs-" + newCorrelation()
	sqsView := runtime.harness.openWallet("1000.00")
	sqsCommand := runtime.harness.command(sqsView, financial.KindBet, "10.00")
	envelope := envelopeFor(t, sqsCommand, "message-"+newCorrelation())
	envelope.CorrelationID = sqsCorrelation
	broker := &sqsFake{}
	broker.enqueue(sqsMessage(envelope.MessageID, "sender-a", 1, envelopeJSON(t, envelope)))
	if err := newConsumer(runtime.harness, broker).PollOnce(context.Background()); err != nil {
		t.Fatalf("processing SQS delivery: %v", err)
	}
	if !hasProcessedEventWithCorrelation(runtime.harness, sqsView.ID, sqsCorrelation) {
		t.Errorf("no SQS event carried correlation %q", sqsCorrelation)
	}
}

func hasProcessedEventWithCorrelation(h *harness, walletID financial.WalletID, correlation string) bool {
	for _, row := range h.outboxEventsForWallet(walletID) {
		decoded := decodeOutbox(h.t, row)
		if decoded.EventType == "WagerTransactionProcessed" && decoded.CorrelationID == correlation {
			return true
		}
	}
	return false
}

// C166 - A permanent asynchronous failure produces FAILED, increments the
// failure metric and emits a structured audit log.
func TestFailureMetricAndAudit(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	refund := h.command(view, financial.KindRefund, "100.00")
	referenceExternal := newExternalID("bet")
	refund.ReferenceExternalID = referenceExternal
	pending := h.mustSubmit(refund)

	bet := h.command(view, financial.KindBet, "100.00")
	bet.ExternalTransactionID = referenceExternal
	h.mustSubmit(bet)
	if _, err := adminPool.Exec(h.ctx(),
		`UPDATE wager_transactions SET "digestVersion" = '' WHERE "providerId" = $1 AND "externalTransactionId" = $2`,
		bet.ProviderID.String(), referenceExternal.String()); err != nil {
		t.Fatalf("corrupting the reference row: %v", err)
	}

	logs := &safeBuffer{}
	registry := metrics.NewRegistry()
	bundle := metrics.New(registry)
	referenceWorker := worker.New(h.store, h.service,
		worker.WithMetrics(bundle),
		worker.WithLogger(logging.New("test", "i", "debug", logs)),
	)
	resolved, err := referenceWorker.ResolveOnce(h.ctx())
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("resolved = %d, want 1", resolved)
	}
	if state := h.transactionState(pending.TransactionID); state != financial.StateFailed {
		t.Errorf("state = %s, want FAILED", state)
	}
	if !strings.Contains(logs.String(), "failureCode") || !strings.Contains(logs.String(), pending.TransactionID.String()) {
		t.Errorf("audit log missing failure detail: %s", logs.String())
	}
	rendered := renderMetrics(t, registry)
	if !strings.Contains(rendered, `wager_transactions_total{kind="REFUND",status="FAILED",ingress="reference"} 1`) {
		t.Errorf("failure metric missing:\n%s", metricLine(rendered, "wager_transactions_total"))
	}
}

// C214 - A nonzero reconciliation difference increments
// wallet_reconciliation_divergences_total by exactly one.
func TestReconciliationMetric(t *testing.T) {
	runtime := newHTTPRuntime(t)
	view := runtime.harness.openWallet("1000.00")
	bet := runtime.harness.command(view, financial.KindBet, "25.00")
	status, data := runtime.postWager(wagerJSONOf(bet), bet.IdempotencyKey.String())
	requireStatus(t, status, 200, data)
	if _, err := adminPool.Exec(runtime.harness.ctx(), `UPDATE wallets SET "balance" = "balance" + 100 WHERE "id" = $1`, view.ID.String()); err != nil {
		t.Fatalf("forcing divergence: %v", err)
	}
	status, data = runtime.reconcile(view.ID.String())
	requireStatus(t, status, 200, data)
	report := decodeJSONBody[map[string]any](t, data)
	if consistent, _ := report["consistent"].(bool); consistent {
		t.Fatal("reconciliation reported consistent after a forced divergence")
	}
	rendered := renderMetrics(t, runtime.registry)
	if !strings.Contains(rendered, "wallet_reconciliation_divergences_total 1") {
		t.Errorf("metric not incremented exactly once:\n%s", metricLine(rendered, "wallet_reconciliation_divergences_total"))
	}
}

// C215 - A nonzero reconciliation difference writes one JSON log with
// walletId, storedBalance, calculatedBalance and difference.
func TestReconciliationLog(t *testing.T) {
	runtime := newHTTPRuntime(t)
	view := runtime.harness.openWallet("1000.00")
	if _, err := adminPool.Exec(runtime.harness.ctx(), `UPDATE wallets SET "balance" = "balance" + 250 WHERE "id" = $1`, view.ID.String()); err != nil {
		t.Fatalf("forcing divergence: %v", err)
	}
	status, data := runtime.reconcile(view.ID.String())
	requireStatus(t, status, 200, data)

	var divergence map[string]any
	for _, line := range strings.Split(strings.TrimSpace(runtime.logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if record["msg"] == "wallet reconciliation divergence detected" {
			divergence = record
		}
	}
	if divergence == nil {
		t.Fatalf("no divergence log record found in %s", runtime.logs.String())
	}
	for _, field := range []string{"walletId", "storedBalance", "calculatedBalance", "difference"} {
		if _, ok := divergence[field]; !ok {
			t.Errorf("divergence log is missing %s: %v", field, divergence)
		}
	}
	if divergence["walletId"] != view.ID.String() {
		t.Errorf("walletId = %v, want %s", divergence["walletId"], view.ID)
	}
}

// C160 - /health/ready returns 200 {"status":"ready"} while PostgreSQL and SQS
// answer within the 2-second budget, and 503 {"status":"not_ready"} otherwise.
func TestHealthReady(t *testing.T) {
	queues := provisionQueues(t)
	client := sqsClient(t)
	ready := health.New(
		func(ctx context.Context) error { return adminPool.Ping(ctx) },
		func(ctx context.Context) error { return sqs.Ping(ctx, client, queues.IngressURL) },
	)
	runtime := newHTTPRuntimeWith(t, newHarness(t), allScopes("provider-a"), ready)
	status, data := runtime.request(http.MethodGet, "/health/ready", nil, nil)
	requireStatus(t, status, 200, data)
	if got := decodeJSONBody[healthJSON](t, data).Status; got != "ready" {
		t.Errorf("status = %s, want ready", got)
	}

	unready := health.New(func(context.Context) error { return fmt.Errorf("postgres offline") })
	runtime = newHTTPRuntimeWith(t, newHarness(t), allScopes("provider-a"), unready)
	status, data = runtime.request(http.MethodGet, "/health/ready", nil, nil)
	requireStatus(t, status, 503, data)
	if got := decodeJSONBody[healthJSON](t, data).Status; got != "not_ready" {
		t.Errorf("status = %s, want not_ready", got)
	}
}

type healthJSON struct {
	Status string `json:"status"`
}

// C226 - The HTTP Idempotency-Key is stored byte for byte.
func TestIdempotencyKeyExactValue(t *testing.T) {
	runtime := newHTTPRuntime(t)
	view := runtime.harness.openWallet("100.00")
	command := runtime.harness.command(view, financial.KindBet, "10.00")
	key := "ExactKey!#$%&'()*+,./:;<=>?@[]^_`{|}~123"
	status, data := runtime.postWager(wagerJSONOf(command), key)
	requireStatus(t, status, 200, data)
	record := runtime.harness.transactionByExternal(command.ProviderID.String(), command.ExternalTransactionID)
	if record.IdempotencyKey == nil || *record.IdempotencyKey != key {
		t.Errorf("persisted key = %v, want the exact supplied key", record.IdempotencyKey)
	}
}

// C217 - Production configuration enabling an integration failpoint rejects
// startup.
func TestFailpointRejection(t *testing.T) {
	t.Setenv("APP_ENV", string(config.EnvironmentProduction))
	t.Setenv("FAILPOINTS", "http.after_commit")
	t.Setenv("DATABASE_URL", testDSN)
	t.Setenv("SQS_INGRESS_QUEUE_URL", "http://localhost/ingress.fifo")
	t.Setenv("SQS_EVENT_QUEUE_URL", "http://localhost/events.fifo")
	cfg := config.Load()
	if err := cfg.Validate(); err == nil {
		t.Fatal("production configuration accepted an integration failpoint")
	}
	if _, err := app.New(cfg); err == nil {
		t.Fatal("application graph accepted a production failpoint configuration")
	}
	if cfg.FailpointsEnabled() {
		t.Error("failpoints reported enabled outside integration configuration")
	}
}

// C221 - A reference external ID known only under another provider is treated
// as absent and reveals no referenced transaction field.
func TestCrossProviderReferenceHidden(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	bet := h.command(view, financial.KindBet, "100.00")
	bet.ProviderID = "provider-a"
	h.mustSubmit(bet)

	refund := h.command(view, financial.KindRefund, "100.00")
	refund.ProviderID = "provider-b"
	refund.ReferenceExternalID = bet.ExternalTransactionID
	refund.RoundID = bet.RoundID
	result := h.mustSubmit(refund)
	if result.State != financial.StatePendingReference {
		t.Fatalf("state = %s, want PENDING_REFERENCE (foreign reference hidden)", result.State)
	}
	record := h.transactionByID(result.TransactionID)
	if record.ReferenceTransactionID != nil {
		t.Error("foreign reference identity was persisted")
	}
}

// C222 - An internal OPENING persists the wallet, player, currency, amount,
// state and timestamps and stores no external metadata.
func TestInternalOpeningSchema(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	record := h.countRows(`SELECT count(*) FROM wager_transactions WHERE "walletId" = $1 AND "origin" = 'INTERNAL' AND "kind" = 'OPENING' AND "state" = 'PROCESSED' AND "observedBalance" IS NOT NULL AND "createdAt" IS NOT NULL AND "updatedAt" IS NOT NULL`, view.ID.String())
	if record != 1 {
		t.Fatalf("opening rows with required fields = %d, want 1", record)
	}
	if got := h.countRows(`SELECT count(*) FROM wager_transactions WHERE "walletId" = $1 AND ("providerId" IS NOT NULL OR "externalTransactionId" IS NOT NULL OR "idempotencyKey" IS NOT NULL OR "digest" IS NOT NULL OR "roundId" IS NOT NULL OR "gameId" IS NOT NULL OR "referenceExternalTransactionId" IS NOT NULL)`, view.ID.String()); got != 0 {
		t.Errorf("opening persisted external metadata in %d rows", got)
	}
}

// C231 - A REJECTED wager transaction persists no ledger entry.
func TestRejectedNoLedger(t *testing.T) {
	runtime := newHTTPRuntime(t)
	view := runtime.harness.openWallet("100.00")
	command := runtime.harness.command(view, financial.KindBet, "150.00")
	status, data := runtime.postWager(wagerJSONOf(command), command.IdempotencyKey.String())
	requireStatus(t, status, 200, data)
	result := decodeJSONBody[wagerResultJSON](t, data)
	if result.Status != "REJECTED" || result.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Fatalf("result = %s/%s, want REJECTED/INSUFFICIENT_FUNDS", result.Status, result.FailureCode)
	}
	if got := runtime.harness.countRows(`SELECT count(*) FROM wallet_ledger_entries WHERE "transactionId" = $1`, result.TransactionID); got != 0 {
		t.Errorf("rejected transaction persisted %d ledger entries", got)
	}
}

// blockingUseCases blocks every invocation until released, so concurrency
// admission can be observed deterministically.
type blockingUseCases struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingUseCases) enter() {
	b.entered <- struct{}{}
	<-b.release
}

func (b *blockingUseCases) OpenWallet(context.Context, application.OpenWalletCommand) (application.WalletView, error) {
	b.enter()
	return application.WalletView{}, nil
}

func (b *blockingUseCases) SubmitWagerTransaction(context.Context, application.SubmitWagerCommand) (application.WagerResult, error) {
	b.enter()
	return application.WagerResult{}, nil
}

func (b *blockingUseCases) TransactionByID(context.Context, financial.TransactionID) (application.WagerResult, error) {
	b.enter()
	return application.WagerResult{}, nil
}

func (b *blockingUseCases) ReconcileWallet(context.Context, financial.WalletID) (application.ReconciliationReport, error) {
	b.enter()
	return application.ReconciliationReport{}, nil
}

// newHandlerRuntime builds the real HTTP handler over arbitrary use cases for
// boundary policy tests.
func newHandlerRuntime(t *testing.T, h *harness, useCases httpadapter.UseCases, authenticator middleware.Authenticator, concurrency int) *httpRuntime {
	t.Helper()
	logs := &safeBuffer{}
	registry := metrics.NewRegistry()
	handler := httpadapter.NewHandler(httpadapter.Config{
		UseCases:      useCases,
		Authenticator: authenticator,
		Metrics:       metrics.New(registry),
		Registry:      registry,
		Logger:        logging.New("integration-test", "instance-1", "debug", logs),
		Concurrency:   concurrency,
		MaxBodyBytes:  1 << 20,
	})
	server := httptest.NewServer(handler.Routes())
	t.Cleanup(server.Close)
	return &httpRuntime{t: t, harness: h, handler: handler, server: server, registry: registry, logs: logs}
}

func renderMetrics(t *testing.T, registry *metrics.Registry) string {
	t.Helper()
	var builder strings.Builder
	if err := registry.Render(&builder); err != nil {
		t.Fatalf("rendering metrics: %v", err)
	}
	return builder.String()
}

func metricLine(rendered, name string) string {
	var lines []string
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, name) {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func provisionQueues(t *testing.T) sqs.Queues {
	t.Helper()
	client := sqsClient(t)
	queues, err := sqs.Provision(context.Background(), client)
	if err != nil {
		t.Fatalf("provisioning queues: %v", err)
	}
	return queues
}

// provisionIsolatedQueues creates a uniquely named queue set so redrive tests
// never share dead-letter state with each other.
func provisionIsolatedQueues(t *testing.T) (sqs.Queues, *awssqs.Client) {
	t.Helper()
	client := sqsClient(t)
	suffix := strings.ToLower(strings.ReplaceAll(newCorrelation(), "-", ""))[:12]
	queues, err := sqs.ProvisionNamed(context.Background(), client,
		"wager-transactions-"+suffix+".fifo",
		"wager-transactions-dlq-"+suffix+".fifo",
		"wager-events-"+suffix+".fifo",
	)
	if err != nil {
		t.Fatalf("provisioning isolated queues: %v", err)
	}
	return queues, client
}
