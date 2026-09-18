//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http/auth"
)

// keycloakAccount is one imported service account.
type keycloakAccount struct {
	ClientID string
	Secret   string
}

var (
	providerAAccount       = keycloakAccount{ClientID: "provider-a", Secret: "local-provider-a"}
	providerBAccount       = keycloakAccount{ClientID: "provider-b", Secret: "local-provider-b"}
	providerExpiringClient = keycloakAccount{ClientID: "provider-expiring", Secret: "local-provider-expiring"}
	internalAccount        = keycloakAccount{ClientID: "internal-service", Secret: "local-internal-service"}
)

// keycloakToken obtains a real client_credentials token from the imported
// realm. The request goes to the host-mapped issuer, so the token issuer is
// exactly the URL the in-process authenticator validates.
func keycloakToken(t *testing.T, account keycloakAccount) string {
	t.Helper()
	if keycloakIssuer == "" {
		t.Fatal("keycloak is not available")
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", account.ClientID)
	form.Set("client_secret", account.Secret)
	response, err := http.Post(
		keycloakIssuer+"/protocol/openid-connect/token",
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatalf("requesting a keycloak token for %s: %v", account.ClientID, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("keycloak token status for %s = %d", account.ClientID, response.StatusCode)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decoding the keycloak token for %s: %v", account.ClientID, err)
	}
	if payload.AccessToken == "" {
		t.Fatalf("keycloak returned an empty token for %s", account.ClientID)
	}
	return payload.AccessToken
}

// newOIDCRuntime builds the real HTTP adapter over the real application
// service and the real OIDC authenticator discovered from Keycloak.
func newOIDCRuntime(t *testing.T) *httpRuntime {
	t.Helper()
	authenticator, err := auth.NewOIDC(t.Context(), keycloakIssuer, "wager-api")
	if err != nil {
		t.Fatalf("building the OIDC authenticator: %v", err)
	}
	t.Cleanup(authenticator.Close)
	return newHTTPRuntimeWith(t, newHarness(t), authenticator, nil)
}

// bearer returns the authorization header map for one token.
func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// withBearer merges an authorization header into other headers.
func withBearer(token string, headers map[string]string) map[string]string {
	merged := map[string]string{"Authorization": "Bearer " + token}
	for key, value := range headers {
		merged[key] = value
	}
	return merged
}

// expiredToken obtains a token from the one-second-lifespan client and waits
// until Keycloak would reject it.
func expiredToken(t *testing.T) string {
	t.Helper()
	token := keycloakToken(t, providerExpiringClient)
	time.Sleep(2500 * time.Millisecond)
	return token
}

// tamperedToken flips one character of the signature of a valid token.
func tamperedToken(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token is not a three-part JWT: %q", token)
	}
	signature := []byte(parts[2])
	if signature[len(signature)-1] == 'A' {
		signature[len(signature)-1] = 'B'
	} else {
		signature[len(signature)-1] = 'A'
	}
	return parts[0] + "." + parts[1] + "." + string(signature)
}
