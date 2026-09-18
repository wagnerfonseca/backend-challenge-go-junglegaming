// Package server owns the HTTP server lifecycle and its literal limits.
package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// Server timeouts fixed by the approved plan.
const (
	ReadHeaderTimeout = 5 * time.Second
	ReadTimeout       = 10 * time.Second
	WriteTimeout      = 35 * time.Second
	IdleTimeout       = 60 * time.Second
)

// ReconciliationTimeout bounds one reconciliation request.
const ReconciliationTimeout = 30 * time.Second

// New builds the HTTP server with the approved timeout contract.
func New(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
	}
}

// Listen binds the server address so startup failures surface synchronously.
func Listen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

// Serve serves until the context is canceled, then shuts the server down
// gracefully within its shutdown budget.
func Serve(ctx context.Context, srv *http.Server, listener net.Listener) error {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(listener) }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
