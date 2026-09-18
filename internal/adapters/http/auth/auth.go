// Package auth is the HTTP authentication boundary. The Keycloak/OIDC
// validator lands with the authentication adapter; until then the composition
// root installs DenyAll so no request is silently trusted.
package auth

import (
	"context"
	"net/http"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http/middleware"
)

// DenyAll rejects every credential with 401.
type DenyAll struct{}

// Authenticate always fails closed.
func (DenyAll) Authenticate(context.Context, *http.Request) (middleware.Principal, error) {
	return middleware.Principal{}, middleware.ErrUnauthenticated
}
