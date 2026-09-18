// Package auth is the HTTP authentication boundary. The OIDC adapter
// validates Keycloak access tokens against the discovered signing keys and
// turns the trusted claims into the principal consumed by route policies.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http/middleware"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// discoveryTimeout bounds the OIDC discovery document fetch at startup.
const discoveryTimeout = 10 * time.Second

// discovery is the subset of the OpenID Connect discovery document used here.
type discovery struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// OIDC validates Keycloak access tokens. It is built once at startup: an
// unavailable or inconsistent discovery document fails composition before the
// service can become ready.
type OIDC struct {
	issuer   string
	audience string
	keys     keyfunc.Keyfunc
	cancel   context.CancelFunc
}

// NewOIDC discovers the issuer, verifies the discovery document is consistent
// with the configured issuer and starts the JWKS refresh loop.
func NewOIDC(ctx context.Context, issuerURL, audience string) (*OIDC, error) {
	issuer := strings.TrimSuffix(issuerURL, "/")
	if issuer == "" {
		return nil, errors.New("auth: OIDC issuer is required")
	}
	if audience == "" {
		return nil, errors.New("auth: OIDC audience is required")
	}
	document, err := fetchDiscovery(ctx, issuer)
	if err != nil {
		return nil, err
	}
	if strings.TrimSuffix(document.Issuer, "/") != issuer {
		return nil, fmt.Errorf("auth: discovery issuer %q does not match configured issuer %q", document.Issuer, issuer)
	}
	if document.JWKSURI == "" {
		return nil, errors.New("auth: discovery document carries no jwks_uri")
	}
	refreshCtx, cancel := context.WithCancel(context.Background())
	keys, err := keyfunc.NewDefaultCtx(refreshCtx, []string{document.JWKSURI})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("auth: building the JWKS key set: %w", err)
	}
	return &OIDC{issuer: issuer, audience: audience, keys: keys, cancel: cancel}, nil
}

// Close stops the JWKS refresh loop.
func (a *OIDC) Close() {
	if a.cancel != nil {
		a.cancel()
	}
}

// Authenticate validates the bearer token signature, issuer, audience and
// expiry, then derives the principal from the trusted claims. Any failure is
// an unauthenticated request: no claim is trusted before verification.
func (a *OIDC) Authenticate(ctx context.Context, r *http.Request) (middleware.Principal, error) {
	raw, ok := bearerToken(r)
	if !ok {
		return middleware.Principal{}, middleware.ErrUnauthenticated
	}
	token, err := jwt.Parse(raw, a.keys.KeyfuncCtx(ctx),
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(a.issuer),
		jwt.WithAudience(a.audience),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !token.Valid {
		return middleware.Principal{}, middleware.ErrUnauthenticated
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return middleware.Principal{}, middleware.ErrUnauthenticated
	}
	providerID := claimString(claims, "provider_id")
	if providerID != "" {
		if _, err := financial.ParseProviderID(providerID); err != nil {
			return middleware.Principal{}, middleware.ErrUnauthenticated
		}
	}
	return middleware.Principal{
		Subject:    claimString(claims, "sub"),
		ProviderID: providerID,
		Scopes:     strings.Fields(claimString(claims, "scope")),
	}, nil
}

func fetchDiscovery(ctx context.Context, issuer string) (discovery, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return discovery{}, fmt.Errorf("auth: building the discovery request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return discovery{}, fmt.Errorf("auth: fetching the discovery document: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return discovery{}, fmt.Errorf("auth: discovery document returned status %d", response.StatusCode)
	}
	var document discovery
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		return discovery{}, fmt.Errorf("auth: decoding the discovery document: %w", err)
	}
	return document, nil
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	prefix := "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

func claimString(claims jwt.MapClaims, name string) string {
	value, ok := claims[name].(string)
	if !ok {
		return ""
	}
	return value
}
