// Package logging builds the structured JSON logger used by every runtime
// component. Production logs never carry credentials, tokens, request bodies
// or complete financial payloads.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// New builds a JSON logger carrying the service and instance identity on every
// record.
func New(service, instance, level string, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(level)})
	return slog.New(handler).With(
		slog.String("service", service),
		slog.String("instance", instance),
	)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
