//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	httpadapter "github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http/middleware"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/logging"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/metrics"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
)

// safeBuffer captures JSON logs for assertions.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// testAuthenticator serves controlled identities through the same boundary the
// OIDC adapter will implement.
type testAuthenticator struct {
	principal middleware.Principal
	err       error
}

// Authenticate returns the configured identity.
func (a testAuthenticator) Authenticate(context.Context, *http.Request) (middleware.Principal, error) {
	if a.err != nil {
		return middleware.Principal{}, a.err
	}
	return a.principal, nil
}

func allScopes(providerID string) testAuthenticator {
	return testAuthenticator{principal: middleware.Principal{
		Subject:    "test-client",
		ProviderID: providerID,
		Scopes: []string{
			middleware.ScopeWalletsWrite,
			middleware.ScopeWalletsRead,
			middleware.ScopeWageringWrite,
			middleware.ScopeWageringRead,
			middleware.ScopeReconciliationExecute,
			middleware.ScopeMetricsRead,
		},
	}}
}

func internalClient(scopes ...string) testAuthenticator {
	return testAuthenticator{principal: middleware.Principal{Subject: "internal-client", Scopes: scopes}}
}

func unauthenticated() testAuthenticator {
	return testAuthenticator{err: middleware.ErrUnauthenticated}
}

// httpRuntime runs the real HTTP adapter over the real application service and
// the real PostgreSQL container.
type httpRuntime struct {
	t               *testing.T
	harness         *harness
	handler         *httpadapter.Handler
	server          *httptest.Server
	registry        *metrics.Registry
	bundle          *metrics.Metrics
	logs            *safeBuffer
	mu              sync.Mutex
	lastRetryAfter  string
	lastCorrelation string
}

// retryAfter returns the Retry-After header of the most recent response.
func (r *httpRuntime) retryAfter() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastRetryAfter
}

// correlation returns the X-Correlation-ID header of the most recent response.
func (r *httpRuntime) correlation() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastCorrelation
}

func newHTTPRuntime(t *testing.T) *httpRuntime {
	t.Helper()
	return newHTTPRuntimeWith(t, newHarness(t), allScopes("provider-a"), nil)
}

// internalAuthenticator carries every internal scope and no provider
// identity, matching the internal Keycloak service account.
func internalAuthenticator() middleware.Authenticator {
	return internalClient(
		middleware.ScopeWalletsWrite,
		middleware.ScopeWalletsRead,
		middleware.ScopeReconciliationExecute,
		middleware.ScopeMetricsRead,
		middleware.ScopeWageringRead,
	)
}

// newInternalHTTPRuntime builds the real HTTP boundary over the real service
// with an internal client identity.
func newInternalHTTPRuntime(t *testing.T) *httpRuntime {
	t.Helper()
	return newHTTPRuntimeWith(t, newHarness(t), internalAuthenticator(), nil)
}

func newHTTPRuntimeWith(t *testing.T, h *harness, authenticator middleware.Authenticator, readiness httpadapter.Readiness) *httpRuntime {
	t.Helper()
	logs := &safeBuffer{}
	logger := logging.New("integration-test", "instance-1", "debug", logs)
	registry := metrics.NewRegistry()
	bundle := metrics.New(registry)
	handler := httpadapter.NewHandler(httpadapter.Config{
		UseCases:      h.service,
		Readiness:     readiness,
		Authenticator: authenticator,
		Metrics:       bundle,
		Registry:      registry,
		Logger:        logger,
		Concurrency:   256,
		MaxBodyBytes:  1 << 20,
	})
	server := httptest.NewServer(handler.Routes())
	t.Cleanup(server.Close)
	return &httpRuntime{t: t, harness: h, handler: handler, server: server, registry: registry, bundle: bundle, logs: logs}
}

func (r *httpRuntime) request(method, path string, body any, headers map[string]string) (int, []byte) {
	r.t.Helper()
	var reader io.Reader
	switch typed := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			r.t.Fatalf("encoding request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, r.server.URL+path, reader)
	if err != nil {
		r.t.Fatalf("building request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatalf("performing request: %v", err)
	}
	defer resp.Body.Close()
	r.mu.Lock()
	r.lastRetryAfter = resp.Header.Get("Retry-After")
	r.lastCorrelation = resp.Header.Get("X-Correlation-ID")
	r.mu.Unlock()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		r.t.Fatalf("reading response: %v", err)
	}
	return resp.StatusCode, data
}

type moneyJSON struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type openWalletJSON struct {
	PlayerID       string    `json:"playerId"`
	InitialBalance moneyJSON `json:"initialBalance"`
}

type wagerJSON struct {
	ProviderID                     string    `json:"providerId"`
	ExternalTransactionID          string    `json:"externalTransactionId"`
	PlayerID                       string    `json:"playerId"`
	WalletID                       string    `json:"walletId"`
	RoundID                        string    `json:"roundId"`
	GameID                         string    `json:"gameId"`
	Kind                           string    `json:"kind"`
	Money                          moneyJSON `json:"money"`
	ReferenceExternalTransactionID string    `json:"referenceExternalTransactionId,omitempty"`
}

type walletJSON struct {
	ID        string    `json:"id"`
	PlayerID  string    `json:"playerId"`
	Balance   moneyJSON `json:"balance"`
	Version   int64     `json:"version"`
	CreatedAt string    `json:"createdAt"`
	UpdatedAt string    `json:"updatedAt"`
}

type wagerResultJSON struct {
	TransactionID     string     `json:"transactionId"`
	Kind              string     `json:"kind"`
	Status            string     `json:"status"`
	Balance           *moneyJSON `json:"balance"`
	FailureCode       string     `json:"failureCode"`
	ReferenceDeadline *string    `json:"referenceDeadline"`
	IdempotentReplay  bool       `json:"idempotentReplay"`
}

type errorJSON struct {
	Error struct {
		Code          string `json:"code"`
		Message       string `json:"message"`
		CorrelationID string `json:"correlationId"`
	} `json:"error"`
}

func wagerJSONOf(cmd application.SubmitWagerCommand) wagerJSON {
	return wagerJSON{
		ProviderID:                     cmd.ProviderID.String(),
		ExternalTransactionID:          cmd.ExternalTransactionID.String(),
		PlayerID:                       cmd.PlayerID.String(),
		WalletID:                       cmd.WalletID.String(),
		RoundID:                        cmd.RoundID.String(),
		GameID:                         cmd.GameID.String(),
		Kind:                           string(cmd.Kind),
		Money:                          moneyJSON{Amount: cmd.Amount.String(), Currency: cmd.Amount.Currency()},
		ReferenceExternalTransactionID: cmd.ReferenceExternalID.String(),
	}
}

func (r *httpRuntime) postWager(body wagerJSON, idempotencyKey string) (int, []byte) {
	headers := map[string]string{}
	if idempotencyKey != "" {
		headers["Idempotency-Key"] = idempotencyKey
	}
	return r.request(http.MethodPost, "/wagering/transactions", body, headers)
}

func (r *httpRuntime) postWallet(body openWalletJSON) (int, []byte) {
	return r.request(http.MethodPost, "/wallets", body, nil)
}

func (r *httpRuntime) getTransaction(id string) (int, []byte) {
	return r.request(http.MethodGet, "/wagering/transactions/"+id, nil, nil)
}

func (r *httpRuntime) reconcile(walletID string) (int, []byte) {
	return r.request(http.MethodPost, "/wallets/"+walletID+"/reconciliation", nil, nil)
}

func decodeJSONBody[T any](t *testing.T, data []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decoding response %s: %v", string(data), err)
	}
	return value
}

func decodeError(t *testing.T, data []byte) errorJSON {
	t.Helper()
	return decodeJSONBody[errorJSON](t, data)
}

// requireStatus fails when the response status differs.
func requireStatus(t *testing.T, got, want int, data []byte) {
	t.Helper()
	if got != want {
		t.Fatalf("status = %d, want %d (body %s)", got, want, string(data))
	}
}

// lifecycleRecorder records ordered lifecycle transitions for ordering proofs.
type lifecycleRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *lifecycleRecorder) Record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *lifecycleRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *lifecycleRecorder) index(event string) int {
	for i, candidate := range r.snapshot() {
		if candidate == event {
			return i
		}
	}
	return -1
}
