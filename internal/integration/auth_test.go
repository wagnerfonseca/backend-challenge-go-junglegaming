//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http/auth"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// C102 - A Keycloak token validates signature, issuer, audience, expiry, route
// scope and the provider_id claim.
func TestOIDCTokenValidation(t *testing.T) {
	runtime := newOIDCRuntime(t)
	view := runtime.harness.openWallet("100.00")
	command := runtime.harness.command(view, financial.KindBet, "10.00")
	token := keycloakToken(t, providerAAccount)

	// A valid token passes signature, issuer, audience, expiry, scope and
	// provider claim checks.
	status, body := runtime.request(http.MethodPost, "/wagering/transactions", wagerJSONOf(command),
		withBearer(token, map[string]string{"Idempotency-Key": command.IdempotencyKey.String()}))
	requireStatus(t, status, 200, body)
	persisted := runtime.harness.transactionByExternal("provider-a", command.ExternalTransactionID)
	if persisted.ProviderID == nil || *persisted.ProviderID != "provider-a" {
		t.Fatalf("persisted provider = %v, want provider-a", persisted.ProviderID)
	}

	// A tampered signature is rejected.
	status, body = runtime.request(http.MethodPost, "/wagering/transactions", wagerJSONOf(command),
		withBearer(tamperedToken(t, token), map[string]string{"Idempotency-Key": command.IdempotencyKey.String()}))
	requireStatus(t, status, 401, body)

	// An expired token is rejected.
	status, body = runtime.request(http.MethodPost, "/wagering/transactions", wagerJSONOf(command),
		withBearer(expiredToken(t), map[string]string{"Idempotency-Key": command.IdempotencyKey.String()}))
	requireStatus(t, status, 401, body)

	// A token issued for another audience is rejected.
	otherAudience, err := auth.NewOIDC(t.Context(), keycloakIssuer, "another-api")
	if err != nil {
		t.Fatalf("building an authenticator with another audience: %v", err)
	}
	t.Cleanup(otherAudience.Close)
	audienceRuntime := newHTTPRuntimeWith(t, runtime.harness, otherAudience, nil)
	status, body = audienceRuntime.request(http.MethodPost, "/wagering/transactions", wagerJSONOf(command),
		withBearer(token, map[string]string{"Idempotency-Key": command.IdempotencyKey.String()}))
	requireStatus(t, status, 401, body)

	// An issuer whose discovery document does not match is rejected at startup.
	if _, err := auth.NewOIDC(t.Context(), keycloakIssuer+"/unreachable", "wager-api"); err == nil {
		t.Error("an unreachable OIDC issuer built an authenticator")
	}
}

// C103 - Absent, invalid or expired credentials return 401 without financial
// effect or protected data.
func TestUnauthorizedNoData(t *testing.T) {
	runtime := newOIDCRuntime(t)
	view := runtime.harness.openWallet("100.00")
	command := runtime.harness.command(view, financial.KindBet, "10.00")
	transactionsBefore := runtime.harness.transactionsForWallet(view.ID)
	ledgerBefore := runtime.harness.ledgerForWallet(view.ID)

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{name: "absent", headers: map[string]string{}},
		{name: "invalid", headers: bearer("not-a-jwt")},
		{name: "expired", headers: bearer(expiredToken(t))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{"Idempotency-Key": command.IdempotencyKey.String()}
			for key, value := range tc.headers {
				headers[key] = value
			}
			status, body := runtime.request(http.MethodPost, "/wagering/transactions", wagerJSONOf(command), headers)
			requireStatus(t, status, 401, body)
			envelope := decodeError(t, body)
			if envelope.Error.Code != "UNAUTHORIZED" || envelope.Error.CorrelationID == "" {
				t.Fatalf("error = %+v, want UNAUTHORIZED with a correlation id", envelope.Error)
			}
			if strings.Contains(string(body), view.ID.String()) {
				t.Fatalf("401 response leaked wallet data: %s", string(body))
			}
		})
	}

	if got := runtime.harness.transactionsForWallet(view.ID); got != transactionsBefore {
		t.Errorf("transactions = %d, want %d (no financial effect)", got, transactionsBefore)
	}
	if got := runtime.harness.ledgerForWallet(view.ID); got != ledgerBefore {
		t.Errorf("ledger entries = %d, want %d (no financial effect)", got, ledgerBefore)
	}
	if got := runtime.harness.walletBalance(view.ID).MinorUnits(); got != 10000 {
		t.Errorf("balance = %d, want 10000", got)
	}
}

// C104 - The authenticated provider_id claim is the provider authority: a
// body that omits the provider is processed under the token identity.
func TestProviderAuthority(t *testing.T) {
	runtime := newOIDCRuntime(t)
	view := runtime.harness.openWallet("100.00")
	command := runtime.harness.command(view, financial.KindBet, "10.00")
	body := wagerJSONOf(command)
	body.ProviderID = ""
	token := keycloakToken(t, providerAAccount)

	status, response := runtime.request(http.MethodPost, "/wagering/transactions", body,
		withBearer(token, map[string]string{"Idempotency-Key": command.IdempotencyKey.String()}))
	requireStatus(t, status, 200, response)

	persisted := runtime.harness.transactionByExternal("provider-a", command.ExternalTransactionID)
	if persisted.ProviderID == nil || *persisted.ProviderID != "provider-a" {
		t.Fatalf("persisted provider = %v, want the authenticated provider-a", persisted.ProviderID)
	}
	result := decodeJSONBody[wagerResultJSON](t, response)
	if result.TransactionID == "" {
		t.Fatal("response carries no transaction id")
	}
}

// C109 - A provider using wagering scopes submits and reads only transactions
// matching its provider_id.
func TestProviderScopeIsolation(t *testing.T) {
	runtime := newOIDCRuntime(t)
	view := runtime.harness.openWallet("100.00")
	command := runtime.harness.command(view, financial.KindBet, "10.00")
	providerA := keycloakToken(t, providerAAccount)
	providerB := keycloakToken(t, providerBAccount)

	status, body := runtime.request(http.MethodPost, "/wagering/transactions", wagerJSONOf(command),
		withBearer(providerA, map[string]string{"Idempotency-Key": command.IdempotencyKey.String()}))
	requireStatus(t, status, 200, body)
	result := decodeJSONBody[wagerResultJSON](t, body)

	// The owning provider reads its own transaction.
	status, body = runtime.request(http.MethodGet, "/wagering/transactions/"+result.TransactionID, nil, bearer(providerA))
	requireStatus(t, status, 200, body)

	// Another provider sees no transaction fields.
	status, body = runtime.request(http.MethodGet, "/wagering/transactions/"+result.TransactionID, nil, bearer(providerB))
	requireStatus(t, status, 404, body)
	if strings.Contains(string(body), result.TransactionID) {
		t.Fatalf("cross-provider response leaked the transaction: %s", string(body))
	}
}

// C105 - A provider supplying a different provider in body or path returns 403
// without processing or revealing the other provider's data.
func TestProviderIdMismatch(t *testing.T) {
	runtime := newOIDCRuntime(t)
	view := runtime.harness.openWallet("100.00")
	command := runtime.harness.command(view, financial.KindBet, "10.00")
	providerA := keycloakToken(t, providerAAccount)

	body := wagerJSONOf(command)
	body.ProviderID = "provider-b"
	status, response := runtime.request(http.MethodPost, "/wagering/transactions", body,
		withBearer(providerA, map[string]string{"Idempotency-Key": command.IdempotencyKey.String()}))
	requireStatus(t, status, 403, response)
	if runtime.harness.transactionExists("provider-a", command.ExternalTransactionID) {
		t.Error("mismatched provider in the body persisted a transaction")
	}

	status, response = runtime.request(http.MethodGet,
		"/providers/provider-b/wagering/transactions/"+command.ExternalTransactionID.String(), nil, bearer(providerA))
	requireStatus(t, status, 403, response)
	if runtime.harness.transactionExists("provider-b", command.ExternalTransactionID) {
		t.Error("mismatched provider in the path revealed provider-b data")
	}
}

// C106 - A provider querying another provider's transaction by internal or
// external identity returns 404 with no transaction fields.
func TestCrossProviderQuery(t *testing.T) {
	runtime := newOIDCRuntime(t)
	view := runtime.harness.openWallet("100.00")
	command := runtime.harness.command(view, financial.KindBet, "10.00")
	providerA := keycloakToken(t, providerAAccount)
	providerB := keycloakToken(t, providerBAccount)

	status, body := runtime.request(http.MethodPost, "/wagering/transactions", wagerJSONOf(command),
		withBearer(providerA, map[string]string{"Idempotency-Key": command.IdempotencyKey.String()}))
	requireStatus(t, status, 200, body)
	result := decodeJSONBody[wagerResultJSON](t, body)

	status, body = runtime.request(http.MethodGet, "/wagering/transactions/"+result.TransactionID, nil, bearer(providerB))
	requireStatus(t, status, 404, body)
	if strings.Contains(string(body), result.TransactionID) || strings.Contains(string(body), "provider-a") {
		t.Fatalf("internal-id lookup leaked the transaction: %s", string(body))
	}

	status, body = runtime.request(http.MethodGet,
		"/providers/provider-b/wagering/transactions/"+command.ExternalTransactionID.String(), nil, bearer(providerB))
	requireStatus(t, status, 404, body)
	if strings.Contains(string(body), result.TransactionID) || strings.Contains(string(body), "provider-a") {
		t.Fatalf("external-id lookup leaked the transaction: %s", string(body))
	}
}

// C107 - A provider credential calling a wallet, ledger, reconciliation,
// metrics or internal OPENING operation returns 403.
func TestProviderForbiddenRoutes(t *testing.T) {
	runtime := newOIDCRuntime(t)
	view := runtime.harness.openWallet("100.00")
	openingID := runtime.harness.openingTransactionID(view.ID)
	providerA := keycloakToken(t, providerAAccount)

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{name: "open wallet", method: http.MethodPost, path: "/wallets"},
		{name: "wallet get", method: http.MethodGet, path: "/wallets/" + view.ID.String()},
		{name: "ledger", method: http.MethodGet, path: "/wallets/" + view.ID.String() + "/ledger"},
		{name: "reconciliation", method: http.MethodPost, path: "/wallets/" + view.ID.String() + "/reconciliation"},
		{name: "metrics", method: http.MethodGet, path: "/metrics"},
		{name: "internal opening", method: http.MethodGet, path: "/wagering/transactions/" + openingID.String()},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var body any
			if testCase.method == http.MethodPost && testCase.path == "/wallets" {
				body = openWalletJSON{PlayerID: financial.NewPlayerID().String(), InitialBalance: moneyJSON{Amount: "0.00", Currency: "BRL"}}
			}
			status, response := runtime.request(testCase.method, testCase.path, body, bearer(providerA))
			requireStatus(t, status, 403, response)
			envelope := decodeError(t, response)
			if envelope.Error.Code != "FORBIDDEN" {
				t.Errorf("code = %s, want FORBIDDEN", envelope.Error.Code)
			}
		})
	}
}

// C108 - The internal client accesses internal routes with the exact required
// scope and no provider_id claim.
func TestInternalAccessNoProvider(t *testing.T) {
	runtime := newOIDCRuntime(t)
	view := runtime.harness.openWallet("100.00")
	openingID := runtime.harness.openingTransactionID(view.ID)
	token := keycloakToken(t, internalAccount)

	status, body := runtime.request(http.MethodGet, "/wallets/"+view.ID.String(), nil, bearer(token))
	requireStatus(t, status, 200, body)

	status, body = runtime.request(http.MethodGet, "/wallets/"+view.ID.String()+"/ledger", nil, bearer(token))
	requireStatus(t, status, 200, body)

	status, body = runtime.request(http.MethodPost, "/wallets/"+view.ID.String()+"/reconciliation", nil, bearer(token))
	requireStatus(t, status, 200, body)

	status, body = runtime.request(http.MethodGet, "/metrics", nil, bearer(token))
	requireStatus(t, status, 200, body)

	status, body = runtime.request(http.MethodGet, "/wagering/transactions/"+openingID.String(), nil, bearer(token))
	requireStatus(t, status, 200, body)
}

// C171 - Against Keycloak, the suite proves absent, invalid and expired
// credentials, provider isolation and internal-scope restrictions.
func TestAuthIntegration(t *testing.T) {
	runtime := newOIDCRuntime(t)
	view := runtime.harness.openWallet("100.00")
	command := runtime.harness.command(view, financial.KindBet, "10.00")
	providerA := keycloakToken(t, providerAAccount)
	providerB := keycloakToken(t, providerBAccount)
	internal := keycloakToken(t, internalAccount)
	openingID := runtime.harness.openingTransactionID(view.ID)

	t.Run("absent credentials", func(t *testing.T) {
		status, body := runtime.request(http.MethodGet, "/wallets/"+view.ID.String(), nil, nil)
		requireStatus(t, status, 401, body)
	})
	t.Run("invalid credentials", func(t *testing.T) {
		status, body := runtime.request(http.MethodGet, "/wallets/"+view.ID.String(), nil, bearer(tamperedToken(t, providerA)))
		requireStatus(t, status, 401, body)
	})
	t.Run("expired credentials", func(t *testing.T) {
		status, body := runtime.request(http.MethodGet, "/wallets/"+view.ID.String(), nil, bearer(expiredToken(t)))
		requireStatus(t, status, 401, body)
	})
	t.Run("provider isolation", func(t *testing.T) {
		status, body := runtime.request(http.MethodPost, "/wagering/transactions", wagerJSONOf(command),
			withBearer(providerA, map[string]string{"Idempotency-Key": command.IdempotencyKey.String()}))
		requireStatus(t, status, 200, body)
		result := decodeJSONBody[wagerResultJSON](t, body)

		status, body = runtime.request(http.MethodGet, "/wagering/transactions/"+result.TransactionID, nil, bearer(providerB))
		requireStatus(t, status, 404, body)

		status, body = runtime.request(http.MethodGet,
			"/providers/provider-b/wagering/transactions/"+command.ExternalTransactionID.String(), nil, bearer(providerB))
		requireStatus(t, status, 404, body)

		status, body = runtime.request(http.MethodGet, "/wagering/transactions/"+openingID.String(), nil, bearer(providerA))
		requireStatus(t, status, 403, body)
	})
	t.Run("internal scope restrictions", func(t *testing.T) {
		status, body := runtime.request(http.MethodGet, "/wallets/"+view.ID.String(), nil, bearer(providerA))
		requireStatus(t, status, 403, body)

		status, body = runtime.request(http.MethodGet, "/metrics", nil, bearer(providerA))
		requireStatus(t, status, 403, body)

		status, body = runtime.request(http.MethodGet,
			"/providers/provider-a/wagering/transactions/"+command.ExternalTransactionID.String(), nil, bearer(internal))
		requireStatus(t, status, 403, body)

		status, body = runtime.request(http.MethodGet, "/wallets/"+view.ID.String(), nil, bearer(internal))
		requireStatus(t, status, 200, body)
	})
}
