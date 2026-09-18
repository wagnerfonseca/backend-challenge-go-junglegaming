// Command service is the runtime entry point. Signal handling is installed
// before the Fx graph starts, so SIGTERM always stops HTTP and workers before
// resources close. Startup failures exit non-zero.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/app"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/config"
)

func main() {
	cfg := config.Load()
	application, err := app.New(cfg)
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("invalid configuration", "error", err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startCtx, cancelStart := context.WithTimeout(context.Background(), 30*time.Second)
	if err := application.Start(startCtx); err != nil {
		cancelStart()
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("startup failed", "error", err.Error())
		os.Exit(1)
	}
	cancelStart()

	select {
	case <-ctx.Done():
	case <-application.Done():
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStop()
	if err := application.Stop(stopCtx); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("shutdown failed", "error", err.Error())
		os.Exit(1)
	}
}
