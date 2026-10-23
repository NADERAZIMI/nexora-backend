// Command reference-data-service implements B04 reference data, instrument
// registry, versioned configuration approval, and atomic snapshot activation.
//
// It is the authoritative configuration publisher. It does not execute trades,
// sign blockchain transactions, hold custody keys, or post ledger entries.
package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/nexora-exchange/nexora/libs/go/logging"
	"github.com/nexora-exchange/nexora/libs/go/observability"
	"github.com/nexora-exchange/nexora/libs/go/version"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/auth"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/config"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/grpcapi"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/httpapi"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/metrics"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/postgres"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/seed"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/snapshot"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid reference-data configuration", "error", err)
		return 1
	}
	logger := logging.New(cfg.Base.ServiceName, cfg.Base.Environment, cfg.Base.LogLevel, version.Version)
	slog.SetDefault(logger)
	logger.Info("reference-data configuration loaded", "config", cfg.RedactedString())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	providers, err := observability.New(ctx, cfg.Base.ServiceName, cfg.Base.Environment, cfg.Base.OTLPEndpoint)
	if err != nil {
		logger.Error("telemetry initialization failed", "error", err)
		return 1
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Base.ShutdownTimeout)
		defer cancel()
		_ = providers.Shutdown(shutdownCtx)
	}()

	store, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database connection failed", "error", err)
		return 1
	}
	defer store.Close()

	migrations, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		logger.Error("migration files unavailable", "error", err)
		return 1
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("NEXORA_REFERENCE_MIGRATION_MODE")))
	if mode == "down" {
		if cfg.Base.IsProduction() {
			logger.Error("destructive migration mode is forbidden in production")
			return 1
		}
		if err := postgres.Migrate(ctx, store.Pool(), migrations, "down"); err != nil {
			logger.Error("reference-data migration down failed", "error", err)
			return 1
		}
		logger.Info("reference-data migrations reversed")
		return 0
	}
	if cfg.AutoMigrate || mode == "up" {
		if err := postgres.Migrate(ctx, store.Pool(), migrations, "up"); err != nil {
			logger.Error("reference-data migration failed", "error", err)
			return 1
		}
		logger.Info("reference-data migrations applied")
	}

	metricSet := metrics.New(providers.Registry)
	service := reference.New(store, nil, metricSet, logger)

	if cfg.SeedFixtures {
		systemActor := reference.Principal{Roles: []string{"system:seed"}, RequestID: "bootstrap-seed"}
		if err := seed.Validate(); err != nil {
			logger.Error("reference-data seed fixture is invalid", "error", err)
			return 1
		}
		if err := service.Seed(ctx, seed.Document(), systemActor); err != nil {
			logger.Error("reference-data seed failed", "error", err)
			return 1
		}
		logger.Info("reference-data non-production fixtures ensured", "seed_actor", uuid.Nil.String())
	}

	identityClient, err := auth.NewIdentityClient(cfg.IdentityGRPCAddr, identityCredentials(cfg), cfg.IdentityTimeout)
	if err != nil {
		logger.Error("identity client initialization failed", "error", err)
		return 1
	}
	defer func() { _ = identityClient.Close() }()

	cache := snapshot.New(store, logger, nil, cfg.StaleAfter, metricSet)
	recoverReferenceData(ctx, service, cache, logger)

	httpServer := &http.Server{
		Addr:              cfg.Base.HTTPAddr,
		Handler:           httpapi.New(service, identityClient, cache, store, logger, version.Version, cfg.InternalToken, metricSet, providers),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}

	serverError := make(chan error, 2)
	go func() {
		logger.Info("reference-data HTTP service listening", "address", cfg.Base.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverError <- err
		}
	}()

	// Periodic consumer reload retains the last valid snapshot on any failure.
	go cache.Run(ctx, cfg.ReloadInterval)

	// Scheduled activation ticker: due activations are promoted atomically and
	// then installed by the local consumer.
	go func() {
		ticker := time.NewTicker(cfg.ActivationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				promoted, err := service.ActivateDue(ctx)
				if err != nil {
					logger.Error("scheduled activation failed", "error", err)
					metricSet.Incident("scheduled_activation_failed")
					continue
				}
				for _, activation := range promoted {
					reloadCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
					_, reloadErr := cache.Reload(reloadCtx, activation.ShardID)
					cancel()
					if reloadErr != nil {
						logger.Warn("consumer reload after scheduled activation failed",
							"shard_id", activation.ShardID, "version", activation.Version, "error", reloadErr)
					}
				}
			}
		}
	}()

	var grpcServer *grpc.Server
	var healthServer *health.Server
	if !cfg.DisableGRPC {
		options := []grpc.ServerOption{
			grpc.MaxRecvMsgSize(1 << 20),
			grpc.MaxSendMsgSize(32 << 20),
			grpc.ConnectionTimeout(10 * time.Second),
		}
		if cfg.GRPCTLS != nil {
			options = append(options, grpc.Creds(credentials.NewTLS(cfg.GRPCTLS)))
		} else {
			logger.Warn("reference-data gRPC is running without TLS; this is allowed only in development/test")
		}
		grpcServer = grpc.NewServer(options...)
		grpcapi.Register(grpcServer, grpcapi.New(store, cache, logger))
		healthServer = health.NewServer()
		healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
		healthpb.RegisterHealthServer(grpcServer, healthServer)
		listener, err := grpcapi.Listen(ctx, cfg.GRPCAddr)
		if err != nil {
			logger.Error("gRPC listen failed", "error", err)
			return 1
		}
		go func() {
			logger.Info("reference-data gRPC service listening", "address", cfg.GRPCAddr)
			if err := grpcServer.Serve(listener); err != nil {
				serverError <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serverError:
		logger.Error("reference-data server failed", "error", err)
		return 1
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Base.ShutdownTimeout)
	defer cancel()
	if healthServer != nil {
		healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	}
	if grpcServer != nil {
		grpcServer.GracefulStop()
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("reference-data HTTP shutdown failed", "error", err)
		return 1
	}
	logger.Info("reference-data service stopped")
	return 0
}

func recoverReferenceData(ctx context.Context, service *reference.Service, cache *snapshot.Cache, logger *slog.Logger) {
	if promoted, err := service.ActivateDue(ctx); err != nil {
		logger.Error("startup scheduled activation recovery failed", "error", err)
	} else {
		for _, activation := range promoted {
			logger.Info("recovered due activation", "shard_id", activation.ShardID, "version", activation.Version)
		}
	}
	shards, err := cacheReloadAll(ctx, cache)
	if err != nil {
		logger.Warn("startup snapshot recovery retained no configuration", "error", err)
		return
	}
	logger.Info("startup snapshot recovery complete", "shards", shards)
}

func cacheReloadAll(ctx context.Context, cache *snapshot.Cache) ([]string, error) {
	return cache.ReloadAll(ctx)
}

func identityCredentials(cfg config.Config) credentials.TransportCredentials {
	if cfg.IdentityTLS != nil {
		return credentials.NewTLS(cfg.IdentityTLS)
	}
	return nil
}
