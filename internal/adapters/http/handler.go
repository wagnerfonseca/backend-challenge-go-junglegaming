// Package http is the HTTP adapter: it decodes transport input, enforces the
// boundary policies and invokes the shared financial use case. It never
// orchestrates repositories.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http/middleware"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http/server"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/metrics"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// UseCases is the financial application surface consumed by this adapter.
type UseCases interface {
	OpenWallet(ctx context.Context, cmd application.OpenWalletCommand) (application.WalletView, error)
	SubmitWagerTransaction(ctx context.Context, cmd application.SubmitWagerCommand) (application.WagerResult, error)
	WalletByID(ctx context.Context, id financial.WalletID) (application.WalletView, error)
	LedgerPage(ctx context.Context, id financial.WalletID, cursor string, limit int) (application.LedgerPage, error)
	TransactionByID(ctx context.Context, id financial.TransactionID) (application.WagerResult, error)
	TransactionByProviderAndExternalID(ctx context.Context, providerID financial.ProviderID, externalID financial.ExternalID) (application.WagerResult, error)
	ReconcileWallet(ctx context.Context, id financial.WalletID) (application.ReconciliationReport, error)
}

// Readiness probes the runtime dependencies of this instance.
type Readiness interface {
	Ready(ctx context.Context) error
}

// Config wires the handler.
type Config struct {
	UseCases      UseCases
	Readiness     Readiness
	Authenticator middleware.Authenticator
	Metrics       *metrics.Metrics
	Registry      *metrics.Registry
	Logger        *slog.Logger
	Concurrency   int
	MaxBodyBytes  int64
}

// Handler serves the v1 routes over one shared use-case surface.
type Handler struct {
	useCases      UseCases
	readiness     Readiness
	authenticator middleware.Authenticator
	metrics       *metrics.Metrics
	registry      *metrics.Registry
	logger        *slog.Logger
	concurrency   int
	maxBodyBytes  int64
}

// NewHandler builds the HTTP handler.
func NewHandler(cfg Config) *Handler {
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 256
	}
	maxBody := cfg.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 1 << 20
	}
	registry := cfg.Registry
	if registry == nil {
		registry = metrics.NewRegistry()
	}
	bundle := cfg.Metrics
	if bundle == nil {
		bundle = metrics.New(registry)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		useCases:      cfg.UseCases,
		readiness:     cfg.Readiness,
		authenticator: cfg.Authenticator,
		metrics:       bundle,
		registry:      registry,
		logger:        logger,
		concurrency:   concurrency,
		maxBodyBytes:  maxBody,
	}
}

// Routes builds the v1 routing table.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	base := func(next http.Handler) http.Handler {
		return middleware.Correlation(middleware.Recover(h.logger)(next))
	}
	protected := func(scope string, next http.Handler) http.Handler {
		return middleware.Authenticate(h.authenticator)(middleware.RequireScope(scope)(next))
	}
	internal := func(scope string, next http.Handler) http.Handler {
		return middleware.Authenticate(h.authenticator)(middleware.RequireInternal(middleware.RequireScope(scope)(next)))
	}
	provider := func(scope string, next http.Handler) http.Handler {
		return middleware.Authenticate(h.authenticator)(middleware.RequireProvider(middleware.RequireScope(scope)(next)))
	}
	business := func(scope string, next http.Handler) http.Handler {
		return protected(scope,
			middleware.RateLimit(h.concurrency)(
				middleware.MaxBytes(h.maxBodyBytes)(
					middleware.RequireJSON(next))))
	}
	internalBusiness := func(scope string, next http.Handler) http.Handler {
		return internal(scope,
			middleware.RateLimit(h.concurrency)(
				middleware.MaxBytes(h.maxBodyBytes)(
					middleware.RequireJSON(next))))
	}
	mux.Handle("GET /health/live", base(http.HandlerFunc(h.handleLive)))
	mux.Handle("GET /health/ready", base(http.HandlerFunc(h.handleReady)))
	mux.Handle("GET /metrics", base(internal(middleware.ScopeMetricsRead, http.HandlerFunc(h.handleMetrics))))
	mux.Handle("POST /wallets", base(internalBusiness(middleware.ScopeWalletsWrite, http.HandlerFunc(h.handleOpenWallet))))
	mux.Handle("GET /wallets/{walletId}", base(internal(middleware.ScopeWalletsRead, http.HandlerFunc(h.handleWalletByID))))
	mux.Handle("GET /wallets/{walletId}/ledger", base(internal(middleware.ScopeWalletsRead, http.HandlerFunc(h.handleLedgerPage))))
	mux.Handle("POST /wagering/transactions", base(provider(middleware.ScopeWageringWrite,
		middleware.RateLimit(h.concurrency)(middleware.MaxBytes(h.maxBodyBytes)(middleware.RequireJSON(http.HandlerFunc(h.handleSubmitWager)))))))
	mux.Handle("GET /wagering/transactions/{transactionId}", base(business(middleware.ScopeWageringRead, http.HandlerFunc(h.handleTransactionByID))))
	mux.Handle("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", base(provider(middleware.ScopeWageringRead, http.HandlerFunc(h.handleProviderTransaction))))
	mux.Handle("POST /wallets/{walletId}/reconciliation", base(internal(middleware.ScopeReconciliationExecute,
		middleware.RateLimit(h.concurrency)(middleware.MaxBytes(h.maxBodyBytes)(http.HandlerFunc(h.handleReconcile))))))
	mux.Handle("/", base(http.HandlerFunc(h.handleNotFound)))
	return mux
}

func (h *Handler) handleLive(w http.ResponseWriter, _ *http.Request) {
	middleware.WriteJSON(w, http.StatusOK, healthResponse{Status: "live"})
}

func (h *Handler) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if h.readiness == nil || h.readiness.Ready(ctx) != nil {
		middleware.WriteJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "not_ready"})
		return
	}
	middleware.WriteJSON(w, http.StatusOK, healthResponse{Status: "ready"})
}

func (h *Handler) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = h.registry.Render(w)
}

func (h *Handler) handleNotFound(w http.ResponseWriter, r *http.Request) {
	middleware.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "route not found")
}

func (h *Handler) handleOpenWallet(w http.ResponseWriter, r *http.Request) {
	request, err := decodeJSON[openWalletRequest](r)
	if err != nil {
		h.writeDecodeError(w, r, err)
		return
	}
	playerID, err := financial.ParsePlayerID(request.PlayerID)
	if err != nil {
		middleware.WriteError(w, r, http.StatusUnprocessableEntity, "INVALID_REQUEST", "playerId is outside the documented format")
		return
	}
	initialBalance, err := request.InitialBalance.parse()
	if err != nil {
		middleware.WriteError(w, r, http.StatusUnprocessableEntity, "INVALID_MONEY", "initialBalance is outside the canonical money format")
		return
	}
	view, err := h.useCases.OpenWallet(r.Context(), application.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: initialBalance,
		CorrelationID:  middleware.CorrelationFrom(r.Context()),
	})
	if err != nil {
		h.writeApplicationError(w, r, err)
		return
	}
	middleware.WriteJSON(w, http.StatusCreated, newWalletResponse(view))
}

func (h *Handler) handleWalletByID(w http.ResponseWriter, r *http.Request) {
	walletID, err := financial.ParseWalletID(r.PathValue("walletId"))
	if err != nil {
		middleware.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "walletId is outside the documented format")
		return
	}
	view, err := h.useCases.WalletByID(r.Context(), walletID)
	if err != nil {
		h.writeApplicationError(w, r, err)
		return
	}
	middleware.WriteJSON(w, http.StatusOK, newWalletResponse(view))
}

func (h *Handler) handleLedgerPage(w http.ResponseWriter, r *http.Request) {
	walletID, err := financial.ParseWalletID(r.PathValue("walletId"))
	if err != nil {
		middleware.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "walletId is outside the documented format")
		return
	}
	limit := application.LedgerDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			middleware.WriteError(w, r, http.StatusBadRequest, string(application.CodeInvalidLimit), "limit must be an integer between 1 and 100")
			return
		}
		limit = parsed
	}
	page, err := h.useCases.LedgerPage(r.Context(), walletID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		h.writeApplicationError(w, r, err)
		return
	}
	middleware.WriteJSON(w, http.StatusOK, newLedgerPageResponse(page))
}

func (h *Handler) handleProviderTransaction(w http.ResponseWriter, r *http.Request) {
	principal, _ := middleware.PrincipalFrom(r.Context())
	pathProvider, err := financial.ParseProviderID(r.PathValue("providerId"))
	if err != nil {
		middleware.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "providerId is outside the documented format")
		return
	}
	if pathProvider.String() != principal.ProviderID {
		middleware.WriteError(w, r, http.StatusForbidden, "FORBIDDEN", "providerId does not match the authenticated provider")
		return
	}
	externalID, err := financial.ParseExternalID("externalTransactionId", r.PathValue("externalTransactionId"))
	if err != nil {
		middleware.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "externalTransactionId is outside the documented format")
		return
	}
	result, err := h.useCases.TransactionByProviderAndExternalID(r.Context(), pathProvider, externalID)
	if err != nil {
		h.writeApplicationError(w, r, err)
		return
	}
	middleware.WriteJSON(w, http.StatusOK, newTransactionResponse(result))
}

func (h *Handler) handleSubmitWager(w http.ResponseWriter, r *http.Request) {
	principal, ok := middleware.PrincipalFrom(r.Context())
	if !ok || principal.ProviderID == "" {
		middleware.WriteError(w, r, http.StatusForbidden, "FORBIDDEN", "a provider identity is required")
		return
	}
	request, err := decodeJSON[wagerRequest](r)
	if err != nil {
		h.writeDecodeError(w, r, err)
		return
	}
	if request.ProviderID != "" && request.ProviderID != principal.ProviderID {
		middleware.WriteError(w, r, http.StatusForbidden, "FORBIDDEN", "providerId does not match the authenticated provider")
		return
	}
	idempotencyKey, err := financial.ParseIdempotencyKey(r.Header.Get("Idempotency-Key"))
	if err != nil {
		if r.Header.Get("Idempotency-Key") == "" {
			middleware.WriteError(w, r, http.StatusBadRequest, string(application.CodeIdempotencyKeyRequired), "Idempotency-Key is required")
			return
		}
		middleware.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Idempotency-Key is outside the documented format")
		return
	}
	providerID, err := financial.ParseProviderID(principal.ProviderID)
	if err != nil {
		middleware.WriteError(w, r, http.StatusUnprocessableEntity, "INVALID_REQUEST", "providerId is outside the documented format")
		return
	}
	externalID, err := financial.ParseExternalID("externalTransactionId", request.ExternalTransactionID)
	if err != nil {
		middleware.WriteError(w, r, http.StatusUnprocessableEntity, "INVALID_REQUEST", "externalTransactionId is outside the documented format")
		return
	}
	walletID, err := financial.ParseWalletID(request.WalletID)
	if err != nil {
		middleware.WriteError(w, r, http.StatusUnprocessableEntity, "INVALID_REQUEST", "walletId is outside the documented format")
		return
	}
	playerID, err := financial.ParsePlayerID(request.PlayerID)
	if err != nil {
		middleware.WriteError(w, r, http.StatusUnprocessableEntity, "INVALID_REQUEST", "playerId is outside the documented format")
		return
	}
	roundID, err := financial.ParseExternalID("roundId", request.RoundID)
	if err != nil {
		middleware.WriteError(w, r, http.StatusUnprocessableEntity, "INVALID_REQUEST", "roundId is outside the documented format")
		return
	}
	gameID, err := financial.ParseExternalID("gameId", request.GameID)
	if err != nil {
		middleware.WriteError(w, r, http.StatusUnprocessableEntity, "INVALID_REQUEST", "gameId is outside the documented format")
		return
	}
	var reference financial.ExternalID
	if request.ReferenceExternalTransactionID != "" {
		reference, err = financial.ParseExternalID("referenceExternalTransactionId", request.ReferenceExternalTransactionID)
		if err != nil {
			middleware.WriteError(w, r, http.StatusUnprocessableEntity, "INVALID_REQUEST", "referenceExternalTransactionId is outside the documented format")
			return
		}
	}
	amount, err := request.Money.parse()
	if err != nil {
		middleware.WriteError(w, r, http.StatusUnprocessableEntity, "INVALID_MONEY", "money is outside the canonical money format")
		return
	}
	started := time.Now()
	result, err := h.useCases.SubmitWagerTransaction(r.Context(), application.SubmitWagerCommand{
		ProviderID:            providerID,
		ExternalTransactionID: externalID,
		IdempotencyKey:        idempotencyKey,
		WalletID:              walletID,
		PlayerID:              playerID,
		RoundID:               roundID,
		GameID:                gameID,
		Kind:                  financial.Kind(request.Kind),
		Amount:                amount,
		ReferenceExternalID:   reference,
		CorrelationID:         middleware.CorrelationFrom(r.Context()),
	})
	if err != nil {
		h.writeApplicationError(w, r, err)
		return
	}
	h.observeTransaction(result, "http", time.Since(started))
	status := http.StatusOK
	if result.State == financial.StatePendingReference {
		status = http.StatusAccepted
	}
	middleware.WriteJSON(w, status, newTransactionResponse(result))
}

func (h *Handler) handleTransactionByID(w http.ResponseWriter, r *http.Request) {
	transactionID, err := financial.ParseTransactionID(r.PathValue("transactionId"))
	if err != nil {
		middleware.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "transactionId is outside the documented format")
		return
	}
	result, err := h.useCases.TransactionByID(r.Context(), transactionID)
	if err != nil {
		h.writeApplicationError(w, r, err)
		return
	}
	principal, _ := middleware.PrincipalFrom(r.Context())
	if principal.ProviderID != "" {
		// A provider sees only its own external transactions. Internal
		// OPENING transactions are internal operations and never visible to
		// a provider; another provider's transaction is reported as absent.
		if result.Origin == financial.OriginInternal {
			middleware.WriteError(w, r, http.StatusForbidden, "FORBIDDEN", "internal transactions are not visible to providers")
			return
		}
		if result.ProviderID.String() != principal.ProviderID {
			middleware.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "transaction not found")
			return
		}
	}
	middleware.WriteJSON(w, http.StatusOK, newTransactionResponse(result))
}

func (h *Handler) handleReconcile(w http.ResponseWriter, r *http.Request) {
	walletID, err := financial.ParseWalletID(r.PathValue("walletId"))
	if err != nil {
		middleware.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "walletId is outside the documented format")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), server.ReconciliationTimeout)
	defer cancel()
	report, err := h.useCases.ReconcileWallet(ctx, walletID)
	if err != nil {
		h.writeApplicationError(w, r, err)
		return
	}
	if !report.Consistent {
		h.metrics.WalletReconciliationDivergencesTotal.Inc()
		h.logger.InfoContext(r.Context(), "wallet reconciliation divergence detected",
			"correlationId", middleware.CorrelationFrom(r.Context()),
			"walletId", report.WalletID.String(),
			"storedBalance", report.StoredBalance.String(),
			"calculatedBalance", report.CalculatedBalance.String(),
			"difference", report.Difference.String(),
		)
	}
	middleware.WriteJSON(w, http.StatusOK, newReconciliationResponse(report))
}

func (h *Handler) observeTransaction(result application.WagerResult, ingress string, elapsed time.Duration) {
	h.metrics.WagerTransactionsTotal.Inc(string(result.Kind), string(result.State), ingress)
	if result.IdempotentReplay {
		h.metrics.WagerIdempotencyDuplicatesTotal.Inc(ingress)
	}
	h.metrics.WagerProcessingDurationSeconds.Observe(elapsed.Seconds(), ingress, string(result.Kind), string(result.State))
}

func (h *Handler) writeDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		middleware.WriteError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "request body exceeds 1 MiB")
		return
	}
	middleware.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is not valid JSON")
}

func (h *Handler) writeApplicationError(w http.ResponseWriter, r *http.Request, err error) {
	code, ok := application.ErrorCodeOf(err)
	if !ok {
		middleware.WriteError(w, r, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "service temporarily unavailable")
		return
	}
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, application.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, application.ErrConflict):
		status = http.StatusConflict
	case code == application.CodeIdempotencyKeyRequired:
		status = http.StatusBadRequest
	case code == application.CodeInvalidCursor || code == application.CodeInvalidLimit:
		status = http.StatusBadRequest
	case errors.Is(err, application.ErrContract):
		status = http.StatusUnprocessableEntity
	case errors.Is(err, application.ErrTransient):
		status = http.StatusServiceUnavailable
	}
	message := "service temporarily unavailable"
	if status != http.StatusServiceUnavailable {
		message = string(code)
		var appErr *application.Error
		if errors.As(err, &appErr) {
			message = appErr.Message
		}
	}
	middleware.WriteError(w, r, status, string(code), message)
}

func decodeJSON[T any](r *http.Request) (T, error) {
	var value T
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return value, errors.New("request body contains more than one JSON value")
		}
		return value, err
	}
	return value, nil
}
