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
