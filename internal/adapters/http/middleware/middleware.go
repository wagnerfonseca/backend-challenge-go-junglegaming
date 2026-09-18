// Package middleware holds the HTTP boundary policies: authentication,
// route scope enforcement, concurrency limiting, correlation and the common
// JSON error envelope.
package middleware

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
)

// Principal is the authenticated identity of one HTTP caller.
type Principal struct {
	Subject    string
	ProviderID string
	Scopes     []string
}

// HasScope reports whether the principal carries the exact route scope.
func (p Principal) HasScope(scope string) bool {
	for _, granted := range p.Scopes {
		if granted == scope {
			return true
		}
	}
	return false
}

// ErrUnauthenticated marks absent, invalid or expired credentials.
var ErrUnauthenticated = errors.New("middleware: unauthenticated")

// Authenticator validates one request credential. The OIDC implementation
// lands with the authentication adapter; tests inject controlled identities
// through the same boundary.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (Principal, error)
}

type principalKey struct{}

// PrincipalFrom returns the authenticated identity stored by Authenticate.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}

type correlationKey struct{}

// CorrelationFrom returns the correlation identity of the request.
func CorrelationFrom(ctx context.Context) string {
	correlation, _ := ctx.Value(correlationKey{}).(string)
	return correlation
}

// Correlation reads or creates the correlation identity and echoes it.
func Correlation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		correlation := r.Header.Get("X-Correlation-ID")
		if correlation == "" || len(correlation) > 128 {
			if id, err := uuid.NewV7(); err == nil {
				correlation = id.String()
			} else {
				correlation = uuid.NewString()
			}
		}
		w.Header().Set("X-Correlation-ID", correlation)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationKey{}, correlation)))
	})
}

// Authenticate rejects unauthenticated requests with 401 before any use case.
func Authenticate(authenticator Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := authenticator.Authenticate(r.Context(), r)
			if err != nil {
				status := http.StatusUnauthorized
				code := "UNAUTHORIZED"
				message := "authentication required"
				if errors.Is(err, ErrForbidden) {
					status = http.StatusForbidden
					code = "FORBIDDEN"
					message = "access denied"
				}
				WriteError(w, r, status, code, message)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
		})
	}
}

// ErrForbidden marks an authenticated caller rejected before the use case.
var ErrForbidden = errors.New("middleware: forbidden")

// RequireScope enforces one exact route scope before invoking the handler.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFrom(r.Context())
			if !ok || !principal.HasScope(scope) {
				WriteError(w, r, http.StatusForbidden, "FORBIDDEN", "token lacks the required scope")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimit admits at most limit concurrent business requests and rejects the
// next one with 503 and Retry-After: 1.
func RateLimit(limit int) func(http.Handler) http.Handler {
	semaphore := make(chan struct{}, limit)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
				next.ServeHTTP(w, r)
			default:
				w.Header().Set("Retry-After", "1")
				WriteError(w, r, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "instance concurrency limit reached")
			}
		})
	}
}

// Recover converts a panic into a 503 envelope without leaking internals.
func Recover(logger interface{ Error(string, ...any) }) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					if logger != nil {
						logger.Error("http handler panic", "correlationId", CorrelationFrom(r.Context()))
					}
					WriteError(w, r, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "service temporarily unavailable")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ErrorEnvelope is the common transport error body.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody is the classified error detail.
type ErrorBody struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlationId"`
}
