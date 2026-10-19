package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("NEXORA_SERVICE_NAME", "")
	t.Setenv("NEXORA_ENVIRONMENT", "")
	t.Setenv("NEXORA_HTTP_ADDR", "")
	t.Setenv("NEXORA_OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("NEXORA_LOG_LEVEL", "")
	t.Setenv("NEXORA_SHUTDOWN_TIMEOUT_MS", "")

	cfg, err := Load("hello-service")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ServiceName != "hello-service" {
		t.Fatalf("ServiceName = %q", cfg.ServiceName)
	}
	if cfg.Environment != "development" {
		t.Fatalf("Environment = %q", cfg.Environment)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("ShutdownTimeout = %v", cfg.ShutdownTimeout)
	}
}

func TestLoadOverridesAndTrims(t *testing.T) {
	t.Setenv("NEXORA_SERVICE_NAME", "  edge-gateway  ")
	t.Setenv("NEXORA_ENVIRONMENT", "STAGING")
	t.Setenv("NEXORA_HTTP_ADDR", "127.0.0.1:9000")
	t.Setenv("NEXORA_LOG_LEVEL", "DEBUG")
	t.Setenv("NEXORA_SHUTDOWN_TIMEOUT_MS", "2500")

	cfg, err := Load("unused")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ServiceName != "edge-gateway" {
		t.Fatalf("ServiceName = %q", cfg.ServiceName)
	}
	if cfg.Environment != "staging" {
		t.Fatalf("Environment = %q", cfg.Environment)
	}
	if cfg.HTTPAddr != "127.0.0.1:9000" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("LogLevel = %q", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 2500*time.Millisecond {
		t.Fatalf("ShutdownTimeout = %v", cfg.ShutdownTimeout)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		key   string
		value string
		want  string
	}{
		{"NEXORA_ENVIRONMENT", "qa", "NEXORA_ENVIRONMENT"},
		{"NEXORA_LOG_LEVEL", "verbose", "NEXORA_LOG_LEVEL"},
		{"NEXORA_HTTP_ADDR", "localhost", "NEXORA_HTTP_ADDR"},
		{"NEXORA_SHUTDOWN_TIMEOUT_MS", "10", "NEXORA_SHUTDOWN_TIMEOUT_MS"},
		{"NEXORA_SHUTDOWN_TIMEOUT_MS", "60001", "NEXORA_SHUTDOWN_TIMEOUT_MS"},
		{"NEXORA_SHUTDOWN_TIMEOUT_MS", "soon", "NEXORA_SHUTDOWN_TIMEOUT_MS"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			_, err := Load("hello-service")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}
