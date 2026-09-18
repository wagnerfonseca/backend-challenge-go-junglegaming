//go:build integration

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/app"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/config"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/sqs/consumer"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// C4 - Invalid or unavailable configuration, PostgreSQL, SQS or OIDC at
// startup exits non-zero before readiness becomes healthy.
func TestStartupFailure(t *testing.T) {
	// Invalid configuration is rejected when the graph is built.
	if _, err := app.New(config.Config{}); err == nil {
		t.Fatal("empty configuration built an application graph")
	}
	missingQueue := testAppConfig(t)
	missingQueue.SQS.IngressQueueURL = ""
	if _, err := app.New(missingQueue); err == nil {
		t.Fatal("configuration without an ingress queue built an application graph")
	}
	missingOIDC := testAppConfig(t)
	missingOIDC.OIDC = config.OIDC{}
	if _, err := app.New(missingOIDC); err == nil {
		t.Fatal("configuration without OIDC built an application graph")
	}

	// PostgreSQL unavailable fails startup.
	unreachableDatabase := testAppConfig(t)
	unreachableDatabase.Database.URL = "postgres://wager_app:wager_app_local@127.0.0.1:1/wager?sslmode=disable"
	startFails(t, unreachableDatabase, "PostgreSQL")

	// SQS unavailable fails startup.
	unreachableQueue := testAppConfig(t)
	unreachableQueue.SQS.Endpoint = "http://127.0.0.1:1"
	unreachableQueue.SQS.IngressQueueURL = "http://127.0.0.1:1/wager-transactions.fifo"
	unreachableQueue.SQS.EventQueueURL = "http://127.0.0.1:1/wager-events.fifo"
	startFails(t, unreachableQueue, "SQS")

	// OIDC discovery unavailable fails startup.
	unreachableOIDC := testAppConfig(t)
	unreachableOIDC.OIDC.IssuerURL = "http://127.0.0.1:1/realms/wager"
	startFails(t, unreachableOIDC, "OIDC")
}

func startFails(t *testing.T, cfg config.Config, dependency string) {
	t.Helper()
	application, err := app.New(cfg)
	if err != nil {
		t.Fatalf("building the graph with unreachable %s: %v", dependency, err)
	}
	startCtx, cancelStart := context.WithTimeout(context.Background(), 15*time.Second)
	err = application.Start(startCtx)
	cancelStart()
	if err == nil {
		stopCtx, cancelStop := context.WithTimeout(context.Background(), 15*time.Second)
		_ = application.Stop(stopCtx)
		cancelStop()
		t.Fatalf("startup succeeded with %s unavailable", dependency)
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 15*time.Second)
	_ = application.Stop(stopCtx)
	cancelStop()
}

// C6 - SIGTERM stops accepting HTTP and stops polling SQS before resource
// shutdown begins.
func TestSIGTERMOrdering(t *testing.T) {
	binary := buildServiceBinary(t)
	queues := provisionQueues(t)
	command := exec.Command(binary)
	command.Env = append(os.Environ(),
		"APP_ENV=integration",
		"DATABASE_URL="+testDSN,
		"SERVICE_NAME=wager-test",
		"INSTANCE_ID=sigterm-test",
		"LOG_LEVEL=debug",
		"HTTP_ADDR=127.0.0.1:0",
		"SQS_ENDPOINT="+localstackEndpoint,
		"SQS_INGRESS_QUEUE_URL="+queues.IngressURL,
		"SQS_EVENT_QUEUE_URL="+queues.EventURL,
		"OIDC_ISSUER_URL="+keycloakIssuer,
		"OIDC_AUDIENCE=wager-api",
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"AWS_REGION=us-east-1",
		"SQS_PROVIDER_SENDER_IDS=sender-a=provider-a",
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("piping stdout: %v", err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatalf("starting the service binary: %v", err)
	}
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	}()

	events := make(chan string, 256)
	sawListening := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				continue
			}
			message, _ := record["msg"].(string)
			if message == "" {
				continue
			}
			event, _ := record["event"].(string)
			events <- message
			if event != "" {
				events <- event
			}
			if message == "http server listening" {
				close(sawListening)
			}
		}
	}()

	select {
	case <-sawListening:
	case <-time.After(30 * time.Second):
		t.Fatal("service did not start listening")
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("service exit after SIGTERM: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("service did not stop within 30 seconds of SIGTERM")
	}
	close(events)
	var ordered []string
	for event := range events {
		ordered = append(ordered, event)
	}
	index := func(name string) int {
		for i, event := range ordered {
			if event == name {
				return i
			}
		}
		return -1
	}
	httpStopped := index("http server stopped")
	sqsStopped := index("sqs.stopped")
	postgresClosed := index("postgres pool closed")
	if httpStopped == -1 || sqsStopped == -1 || postgresClosed == -1 {
		t.Fatalf("shutdown did not stop every component in order: %v", ordered)
	}
	if !(httpStopped < postgresClosed && sqsStopped < postgresClosed) {
		t.Errorf("resources closed before HTTP and SQS stopped: %v", ordered)
	}
}

// C7 - In-flight SQS work that cannot finish within 30 seconds of SIGTERM
// releases its message visibility for redelivery.
func TestSIGTERMSOFSQS(t *testing.T) {
	if consumer.ShutdownGrace != 30*time.Second {
		t.Fatalf("shutdown grace = %s, want 30s", consumer.ShutdownGrace)
	}
	h := newHarness(t)
	view := h.openWallet("100.00")
	command := h.command(view, financial.KindBet, "10.00")
	envelope := envelopeFor(t, command, "message-"+newCorrelation())
	stubborn := &stubbornUseCase{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(stubborn.release)
	broker := &sqsFake{}
	broker.enqueue(sqsMessage(envelope.MessageID, "sender-a", 1, envelopeJSON(t, envelope)))
	graceful := consumer.New(consumer.Config{
		QueueURL:     "http://fake/wager-transactions.fifo",
		ConsumerName: "wager-transactions",
		ProviderForSender: func(string) (string, bool) {
			return "provider-a", true
		},
		ShutdownGrace: 200 * time.Millisecond,
	}, broker, stubborn)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- graceful.Run(ctx) }()
	<-stubborn.entered
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("consumer shutdown: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("consumer did not stop within the 30-second shutdown grace")
	}
	if handles := broker.deletedHandles(); len(handles) != 0 {
		t.Errorf("unfinished work was acknowledged: %v", handles)
	}
	released := false
	for _, change := range broker.visibilityChanges() {
		if change.Timeout == 0 {
			released = true
		}
	}
	if !released {
		t.Errorf("unfinished work did not release visibility: %v", broker.visibilityChanges())
	}
}

// C8 - Shutdown closes PostgreSQL and SQS only after HTTP and all workers
// stopped.
func TestShutdownOrder(t *testing.T) {
	recorder := &lifecycleRecorder{}
	application, err := app.New(testAppConfig(t), fx.Decorate(func(app.Recorder) app.Recorder { return recorder }))
	if err != nil {
		t.Fatalf("building the application graph: %v", err)
	}
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

	events := recorder.snapshot()
	index := func(name string) int {
		for i, event := range events {
			if event == name {
				return i
			}
		}
		return -1
	}
	postgres := index("postgres.closed")
	if postgres == -1 {
		t.Fatalf("PostgreSQL close was not observed: %v", events)
	}
	for _, event := range []string{"http.stopped", "sqs.stopped", "outbox.stopped", "reference.stopped"} {
		position := index(event)
		if position == -1 {
			t.Errorf("stop event %s missing: %v", event, events)
			continue
		}
		if position > postgres {
			t.Errorf("%s happened after PostgreSQL closed: %v", event, events)
		}
	}
	if postgres != len(events)-1 {
		t.Errorf("PostgreSQL was not the last resource closed: %v", events)
	}
}

// stubbornUseCase ignores cancellation, modelling work that cannot finish
// within the shutdown grace.
type stubbornUseCase struct {
	entered chan struct{}
	release chan struct{}
}

func (s *stubbornUseCase) SubmitWagerFromInbox(context.Context, application.SubmitWagerCommand, application.InboxDelivery) (application.WagerResult, bool, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return application.WagerResult{}, false, nil
}

// testAppConfig builds a runnable configuration over the test infrastructure.
func testAppConfig(t *testing.T) config.Config {
	t.Helper()
	queues := provisionQueues(t)
	return config.Config{
		Environment: config.EnvironmentIntegration,
		ServiceName: "wager-test",
		InstanceID:  "instance-test",
		LogLevel:    "debug",
		Database:    config.Database{URL: applicationDSN(t)},
		HTTP:        config.HTTP{Addr: "127.0.0.1:0", ConcurrencyLimit: 16, MaxBodyBytes: 1 << 20},
		OIDC:        config.OIDC{IssuerURL: keycloakIssuer, Audience: "wager-api"},
		SQS: config.SQS{
			Region:          "us-east-1",
			Endpoint:        localstackEndpoint,
			IngressQueueURL: queues.IngressURL,
			EventQueueURL:   queues.EventURL,
			ConsumerName:    "wager-transactions",
		},
		Providers: map[string]string{"sender-a": "provider-a"},
	}
}

// applicationDSN rewrites the admin DSN to the application role.
func applicationDSN(t *testing.T) string {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(testDSN)
	if err != nil {
		t.Fatalf("parsing test dsn: %v", err)
	}
	cfg.ConnConfig.User = "wager_app"
	cfg.ConnConfig.Password = "wager_app_local"
	return cfg.ConnConfig.ConnString()
}

var (
	serviceBinaryOnce sync.Once
	serviceBinaryPath string
	serviceBinaryErr  error
)

// buildServiceBinary compiles cmd/service once per test process.
func buildServiceBinary(t *testing.T) string {
	t.Helper()
	serviceBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wager-service")
		if err != nil {
			serviceBinaryErr = err
			return
		}
		serviceBinaryPath = filepath.Join(dir, "service")
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			serviceBinaryErr = err
			return
		}
		build := exec.Command("go", "build", "-o", serviceBinaryPath, "./cmd/service")
		build.Dir = root
		if output, err := build.CombinedOutput(); err != nil {
			serviceBinaryErr = fmt.Errorf("building cmd/service: %v: %s", err, strings.TrimSpace(string(output)))
		}
	})
	if serviceBinaryErr != nil {
		t.Fatalf("building the service binary: %v", serviceBinaryErr)
	}
	return serviceBinaryPath
}
