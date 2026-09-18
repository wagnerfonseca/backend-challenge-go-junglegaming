//go:build integration

// Package integration proves the application obligations against a real
// PostgreSQL started with testcontainers-go. No database is mocked.
package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgmigrate "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tclocalstack "github.com/testcontainers/testcontainers-go/modules/localstack"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/failpoint"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/metrics"
	deadapter "github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/postgres"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/event"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

var (
	testDSN            string
	adminPool          *pgxpool.Pool
	localstackEndpoint string
)

func TestMain(m *testing.M) {
	// The Ryuk reaper hardcodes the Docker "bridge" network, which does not
	// exist on Podman-based daemons; this harness terminates its own containers.
	if _, set := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !set {
		_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("wager"),
		tcpostgres.WithUsername("admin"),
		tcpostgres.WithPassword("admin"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting postgres container: %v\n", err)
		os.Exit(1)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolving connection string: %v\n", err)
		os.Exit(1)
	}
	testDSN = dsn
	if err := applyMigrations(dsn); err != nil {
		fmt.Fprintf(os.Stderr, "applying migrations: %v\n", err)
		os.Exit(1)
	}
	adminPool, err = pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "opening admin pool: %v\n", err)
		os.Exit(1)
	}

	localstackContainer, err := tclocalstack.Run(ctx, "localstack/localstack:3.8",
		testcontainers.WithEnv(map[string]string{"SERVICES": "sqs"}),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting localstack container: %v\n", err)
		os.Exit(1)
	}
	host, err := localstackContainer.Host(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolving localstack host: %v\n", err)
		os.Exit(1)
	}
	mappedPort, err := localstackContainer.MappedPort(ctx, "4566/tcp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolving localstack port: %v\n", err)
		os.Exit(1)
	}
	localstackEndpoint = "http://" + host + ":" + mappedPort.Port()
	_ = os.Setenv("AWS_ACCESS_KEY_ID", "test")
	_ = os.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	_ = os.Setenv("AWS_REGION", "us-east-1")

	code := m.Run()
	adminPool.Close()
	_ = container.Terminate(ctx)
	_ = localstackContainer.Terminate(ctx)
	os.Exit(code)
}

func migrationsDir() string {
	path, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		panic(err)
	}
	return "file://" + path
}

func applyMigrations(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	driver, err := pgmigrate.WithInstance(db, &pgmigrate.Config{})
	if err != nil {
		return err
	}
	runner, err := migrate.NewWithDatabaseInstance(migrationsDir(), "postgres", driver)
	if err != nil {
		return err
	}
	if err := runner.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// testClock makes reference deadlines and retries deterministic.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delta)
}

// harness wires the application to the real database using the application
// database role, so grants, locks and constraints are exercised for real.
type harness struct {
	t          *testing.T
	service    *application.WagerService
	store      *deadapter.Store
	appPool    *pgxpool.Pool
	clock      *testClock
	metrics    *metrics.Metrics
	failpoints *failpoint.Set
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWithOptions(t)
}

// newHarnessWithOptions builds the harness with extra application options,
// such as a transaction-boundary failpoint.
func newHarnessWithOptions(t *testing.T, options ...application.Option) *harness {
	t.Helper()
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(testDSN)
	if err != nil {
		t.Fatalf("parsing test dsn: %v", err)
	}
	config.ConnConfig.User = "wager_app"
	config.ConnConfig.Password = "wager_app_local"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("opening application pool: %v", err)
	}
	t.Cleanup(pool.Close)
	clock := &testClock{now: time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)}
	store := deadapter.NewStore(pool)
	serviceOptions := append([]application.Option{application.WithReconciler(store)}, options...)
	return &harness{
		t:          t,
		service:    application.NewWagerService(store, clock, serviceOptions...),
		store:      store,
		appPool:    pool,
		clock:      clock,
		metrics:    metrics.New(metrics.NewRegistry()),
		failpoints: failpoint.New(nil),
	}
}

// runtimeMetrics returns the metric bundle of this harness.
func (h *harness) runtimeMetrics() *metrics.Metrics { return h.metrics }

func (h *harness) ctx() context.Context { return context.Background() }

func (h *harness) openWallet(balance string) application.WalletView {
	h.t.Helper()
	return h.openWalletFor(financial.NewPlayerID(), balance, "BRL")
}

func (h *harness) openWalletFor(playerID financial.PlayerID, balance, currency string) application.WalletView {
	h.t.Helper()
	view, err := h.service.OpenWallet(h.ctx(), application.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: mustMoney(h.t, balance, currency),
		CorrelationID:  newCorrelation(),
	})
	if err != nil {
		h.t.Fatalf("opening wallet: %v", err)
	}
	return view
}

func (h *harness) command(wallet application.WalletView, kind financial.Kind, amount string) application.SubmitWagerCommand {
	h.t.Helper()
	return application.SubmitWagerCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: newExternalID("ext"),
		IdempotencyKey:        financial.IdempotencyKey("key-" + uuid.NewString()),
		WalletID:              wallet.ID,
		PlayerID:              wallet.PlayerID,
		RoundID:               newExternalID("round"),
		GameID:                newExternalID("game"),
		Kind:                  kind,
		Amount:                mustMoney(h.t, amount, wallet.Currency),
		CorrelationID:         newCorrelation(),
	}
}

func (h *harness) submit(cmd application.SubmitWagerCommand) (application.WagerResult, error) {
	h.t.Helper()
	return h.service.SubmitWagerTransaction(h.ctx(), cmd)
}

func (h *harness) mustSubmit(cmd application.SubmitWagerCommand) application.WagerResult {
	h.t.Helper()
	result, err := h.submit(cmd)
	if err != nil {
		h.t.Fatalf("submitting %s: %v", cmd.Kind, err)
	}
	return result
}

func (h *harness) mustFailWith(cmd application.SubmitWagerCommand, code application.ErrorCode) {
	h.t.Helper()
	result, err := h.submit(cmd)
	if err == nil {
		h.t.Fatalf("submitting %s succeeded with %+v, want contract error %s", cmd.Kind, result, code)
	}
	got, ok := application.ErrorCodeOf(err)
	if !ok || got != code {
		h.t.Fatalf("submitting %s: error = %v, code = %s, want %s", cmd.Kind, err, got, code)
	}
}

func (h *harness) walletBalance(walletID financial.WalletID) money.Money {
	h.t.Helper()
	var balance int64
	var currency string
	err := adminPool.QueryRow(h.ctx(),
		`SELECT "balance", "currency" FROM wallets WHERE "id" = $1`, walletID.String(),
	).Scan(&balance, &currency)
	if err != nil {
		h.t.Fatalf("reading wallet balance: %v", err)
	}
	value, err := money.FromMinorUnits(balance, currency)
	if err != nil {
		h.t.Fatalf("building wallet balance: %v", err)
	}
	return value
}

func (h *harness) walletVersion(walletID financial.WalletID) int64 {
	h.t.Helper()
	var version int64
	err := adminPool.QueryRow(h.ctx(),
		`SELECT "version" FROM wallets WHERE "id" = $1`, walletID.String(),
	).Scan(&version)
	if err != nil {
		h.t.Fatalf("reading wallet version: %v", err)
	}
	return version
}

func (h *harness) transactionState(transactionID financial.TransactionID) financial.State {
	h.t.Helper()
	var state string
	err := adminPool.QueryRow(h.ctx(),
		`SELECT "state" FROM wager_transactions WHERE "id" = $1`, transactionID.String(),
	).Scan(&state)
	if err != nil {
		h.t.Fatalf("reading transaction state: %v", err)
	}
	return financial.State(state)
}

func (h *harness) countRows(query string, args ...any) int {
	h.t.Helper()
	var count int
	if err := adminPool.QueryRow(h.ctx(), query, args...).Scan(&count); err != nil {
		h.t.Fatalf("counting rows: %v", err)
	}
	return count
}

func (h *harness) transactionsForWallet(walletID financial.WalletID) int {
	h.t.Helper()
	return h.countRows(`SELECT count(*) FROM wager_transactions WHERE "walletId" = $1`, walletID.String())
}

func (h *harness) ledgerForWallet(walletID financial.WalletID) int {
	h.t.Helper()
	return h.countRows(`SELECT count(*) FROM wallet_ledger_entries WHERE "walletId" = $1`, walletID.String())
}

func (h *harness) outboxForWallet(walletID financial.WalletID) int {
	h.t.Helper()
	return h.countRows(`SELECT count(*) FROM outbox_events WHERE "aggregateId" = $1`, walletID.String())
}

func (h *harness) outboxEventsForWallet(walletID financial.WalletID) []outboxRow {
	h.t.Helper()
	rows, err := adminPool.Query(h.ctx(),
		`SELECT "eventId", "eventType", "aggregateId", "version", "payload"
		FROM outbox_events WHERE "aggregateId" = $1 ORDER BY "createdAt", "eventId"`,
		walletID.String(),
	)
	if err != nil {
		h.t.Fatalf("reading outbox events: %v", err)
	}
	defer rows.Close()
	var events []outboxRow
	for rows.Next() {
		var row outboxRow
		if err := rows.Scan(&row.EventID, &row.EventType, &row.AggregateID, &row.Version, &row.Payload); err != nil {
			h.t.Fatalf("scanning outbox event: %v", err)
		}
		events = append(events, row)
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("iterating outbox events: %v", err)
	}
	return events
}

func (h *harness) outboxCountOfType(walletID financial.WalletID, eventType string) int {
	h.t.Helper()
	return h.countRows(
		`SELECT count(*) FROM outbox_events WHERE "aggregateId" = $1 AND "eventType" = $2`,
		walletID.String(), eventType,
	)
}

func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	value, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatalf("parsing money %s %s: %v", amount, currency, err)
	}
	return value
}

// outboxRow is one raw outbox snapshot.
type outboxRow struct {
	EventID     string
	EventType   string
	AggregateID string
	Version     int
	Payload     []byte
}

// decodedEvent exposes the envelope and keeps typed data for assertions.
type decodedEvent struct {
	EventID       string          `json:"eventId"`
	EventType     string          `json:"eventType"`
	AggregateID   string          `json:"aggregateId"`
	CorrelationID string          `json:"correlationId"`
	CausationID   string          `json:"causationId"`
	OccurredAt    string          `json:"occurredAt"`
	Version       int             `json:"version"`
	Data          json.RawMessage `json:"data"`
}

func decodeOutbox(t *testing.T, row outboxRow) decodedEvent {
	t.Helper()
	var decoded decodedEvent
	if err := json.Unmarshal(row.Payload, &decoded); err != nil {
		t.Fatalf("decoding outbox payload %s: %v", row.EventID, err)
	}
	return decoded
}

func decodeProcessedData(t *testing.T, envelope decodedEvent) event.ProcessedData {
	t.Helper()
	var data event.ProcessedData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decoding processed data: %v", err)
	}
	return data
}

func decodeRejectedData(t *testing.T, envelope decodedEvent) event.RejectedData {
	t.Helper()
	var data event.RejectedData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decoding rejected data: %v", err)
	}
	return data
}

func decodeBalanceChangedData(t *testing.T, envelope decodedEvent) event.BalanceChangedData {
	t.Helper()
	var data event.BalanceChangedData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decoding balance changed data: %v", err)
	}
	return data
}

func decodePendingReferenceData(t *testing.T, envelope decodedEvent) event.PendingReferenceData {
	t.Helper()
	var data event.PendingReferenceData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decoding pending reference data: %v", err)
	}
	return data
}

func newExternalID(prefix string) financial.ExternalID {
	return financial.ExternalID(prefix + "-" + uuid.NewString())
}

func newCorrelation() string { return uuid.NewString() }
