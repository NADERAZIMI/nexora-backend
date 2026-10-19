// Package config loads and validates service configuration from the
// environment. Services fail fast on invalid values.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddr        = ":8080"
	defaultOTLPEndpoint    = "http://localhost:4317"
	defaultShutdownTimeout = 10 * time.Second
	minShutdownTimeout     = time.Second
	maxShutdownTimeout     = 60 * time.Second
)

// Config is the validated runtime configuration shared by Go services.
type Config struct {
	ServiceName     string
	Environment     string
	HTTPAddr        string
	OTLPEndpoint    string
	LogLevel        string
	ShutdownTimeout time.Duration
}

// Load reads NEXORA_* environment variables, applies development defaults,
// and validates the result.
func Load(defaultService string) (Config, error) {
	cfg := Config{
		ServiceName:     valueOrDefault("NEXORA_SERVICE_NAME", defaultService),
		Environment:     strings.ToLower(valueOrDefault("NEXORA_ENVIRONMENT", "development")),
		HTTPAddr:        valueOrDefault("NEXORA_HTTP_ADDR", defaultHTTPAddr),
		OTLPEndpoint:    defaultOTLPEndpoint,
		LogLevel:        strings.ToLower(valueOrDefault("NEXORA_LOG_LEVEL", "info")),
		ShutdownTimeout: defaultShutdownTimeout,
	}

	// An explicitly empty OTLP endpoint disables trace export.
	if endpoint, ok := os.LookupEnv("NEXORA_OTEL_EXPORTER_OTLP_ENDPOINT"); ok {
		cfg.OTLPEndpoint = strings.TrimSpace(endpoint)
	}

	switch cfg.Environment {
	case "development", "dev", "test", "staging", "production", "prod":
	default:
		return Config{}, fmt.Errorf("NEXORA_ENVIRONMENT=%q: expected development, test, staging, or production", cfg.Environment)
	}

	switch cfg.LogLevel {
	case "trace", "debug", "info", "warn", "error", "off":
	default:
		return Config{}, fmt.Errorf("NEXORA_LOG_LEVEL=%q: expected trace, debug, info, warn, error, or off", cfg.LogLevel)
	}

	if _, _, err := net.SplitHostPort(cfg.HTTPAddr); err != nil {
		return Config{}, fmt.Errorf("NEXORA_HTTP_ADDR=%q: %w", cfg.HTTPAddr, err)
	}

	if raw := strings.TrimSpace(os.Getenv("NEXORA_SHUTDOWN_TIMEOUT_MS")); raw != "" {
		ms, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("NEXORA_SHUTDOWN_TIMEOUT_MS=%q: expected integer milliseconds", raw)
		}
		if ms < uint64(minShutdownTimeout/time.Millisecond) || ms > uint64(maxShutdownTimeout/time.Millisecond) {
			return Config{}, fmt.Errorf("NEXORA_SHUTDOWN_TIMEOUT_MS=%d: must be between 1000 and 60000", ms)
		}
		cfg.ShutdownTimeout = time.Duration(ms) * time.Millisecond
	}

	if cfg.ServiceName == "" {
		return Config{}, fmt.Errorf("NEXORA_SERVICE_NAME must not be empty")
	}

	return cfg, nil
}

// IsProduction reports whether the service is running in production.
func (c Config) IsProduction() bool {
	return c.Environment == "production" || c.Environment == "prod"
}

func valueOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
