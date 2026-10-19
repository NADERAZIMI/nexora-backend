// Package server builds the hello-service HTTP handler.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/nexora-exchange/nexora/libs/go/config"
	"github.com/nexora-exchange/nexora/libs/go/httpx"
	"github.com/nexora-exchange/nexora/libs/go/observability"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// New builds the hello-service HTTP handler.
func New(cfg config.Config, logger *slog.Logger, providers *observability.Providers, version string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "ok",
			"service": cfg.ServiceName,
			"version": version,
		})
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "ready",
			"service": cfg.ServiceName,
			"version": version,
		})
	})

	mux.Handle("GET /metrics", promhttp.HandlerFor(providers.Registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))

	mux.HandleFunc("GET /v1/hello", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"message":    "nexora backend baseline",
			"service":    cfg.ServiceName,
			"request_id": httpx.RequestIDFromContext(r.Context()),
		})
	})

	metrics := httpx.NewMetrics(providers.Registry)
	handler := httpx.Chain(mux,
		httpx.RequestID,
		httpx.AccessLog(logger),
		metrics.Middleware(cfg.ServiceName),
		httpx.Recover(logger),
	)

	return otelhttp.NewHandler(handler, "http.server")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
