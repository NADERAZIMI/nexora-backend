// Command hello-service is the B01 reference Go service. It exposes health,
// readiness, metrics, and a trivial hello endpoint. It implements no trading
// functionality.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nexora-exchange/nexora/libs/go/config"
	"github.com/nexora-exchange/nexora/libs/go/logging"
	"github.com/nexora-exchange/nexora/libs/go/observability"
	"github.com/nexora-exchange/nexora/libs/go/version"
	"github.com/nexora-exchange/nexora/services/hello-service/internal/server"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load("hello-service")
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		return 1
	}

	logger := logging.New(cfg.ServiceName, cfg.Environment, cfg.LogLevel, version.Version)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	providers, err := observability.New(ctx, cfg.ServiceName, cfg.Environment, cfg.OTLPEndpoint)
	if err != nil {
		logger.Error("telemetry initialization failed", "error", err)
		return 1
	}

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           server.New(cfg, logger, providers, version.Version),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverError := make(chan error, 1)
	go func() {
		logger.Info("service listening",
			"address", cfg.HTTPAddr,
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverError <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serverError:
		logger.Error("http server failed", "error", err)
		return 1
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		return 1
	}
	if err := providers.Shutdown(shutdownCtx); err != nil {
		logger.Warn("telemetry shutdown failed", "error", err)
	}

	logger.Info("service stopped")
	return 0
}
