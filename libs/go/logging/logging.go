// Package logging builds the canonical structured logger for Go services.
package logging

import (
	"log/slog"
	"os"
)

// New returns a JSON logger with stable service metadata.
func New(service, environment, level, version string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(level),
	})
	return slog.New(handler).With(
		"service", service,
		"environment", environment,
		"version", version,
	)
}

func parseLevel(level string) slog.Level {
	switch level {
	case "trace", "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "off":
		return slog.Level(100)
	default:
		return slog.LevelInfo
	}
}
