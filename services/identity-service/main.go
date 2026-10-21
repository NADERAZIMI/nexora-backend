// Command identity-service implements the B03 NEXORA identity and
// authentication service. It is the only component that verifies passwords,
// issues sessions, and manages MFA credentials.
package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nexora-exchange/nexora/libs/go/logging"
	"github.com/nexora-exchange/nexora/libs/go/observability"
	"github.com/nexora-exchange/nexora/libs/go/version"
	"github.com/nexora-exchange/nexora/services/identity-service/internal/config"
	"github.com/nexora-exchange/nexora/services/identity-service/internal/grpcapi"
	"github.com/nexora-exchange/nexora/services/identity-service/internal/httpapi"
	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
	"github.com/nexora-exchange/nexora/services/identity-service/internal/postgres"
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
		slog.Error("invalid identity configuration", "error", err)
		return 1
	}
	logger := logging.New(cfg.Base.ServiceName, cfg.Base.Environment, cfg.Base.LogLevel, version.Version)
	slog.SetDefault(logger)
	logger.Info("identity configuration loaded", "config", cfg.RedactedString())

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
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("NEXORA_IDENTITY_MIGRATION_MODE")))
	if mode == "down" {
		if cfg.Base.Environment == "production" || cfg.Base.Environment == "prod" {
			logger.Error("destructive migration mode is forbidden in production")
			return 1
		}
		if err := postgres.Migrate(ctx, store.Pool(), migrations, "down"); err != nil {
			logger.Error("identity migration down failed", "error", err)
			return 1
		}
		logger.Info("identity migrations reversed")
		return 0
	}
	if cfg.AutoMigrate || mode == "up" {
		if err := postgres.Migrate(ctx, store.Pool(), migrations, "up"); err != nil {
			logger.Error("identity migration failed", "error", err)
			return 1
		}
		logger.Info("identity migrations applied")
	}

	hasher, err := identity.NewPasswordHasher(cfg.Argon2, cfg.PasswordPepper)
	if err != nil {
		logger.Error("password hasher initialization failed", "error", err)
		return 1
	}
	piiCipher, err := identity.NewAESGCMCipher(cfg.PIICipherKey)
	if err != nil {
		logger.Error("PII cipher initialization failed", "error", err)
		return 1
	}
	secretCipher, err := identity.NewAESGCMCipher(cfg.SecretKey)
	if err != nil {
		logger.Error("secret cipher initialization failed", "error", err)
		return 1
	}
	tokens, err := identity.NewTokenManager(cfg.JWTPrivateKey, cfg.JWTIssuer, cfg.JWTAudience, cfg.AccessTTL, cfg.StepUpTTL)
	if err != nil {
		logger.Error("token manager initialization failed", "error", err)
		return 1
	}
	webAuthn, err := identity.NewWebAuthnManager(cfg.WebAuthn)
	if err != nil {
		logger.Error("WebAuthn initialization failed", "error", err)
		return 1
	}
	notifier, err := identity.NewOutboxNotifier(store, piiCipher, secretCipher)
	if err != nil {
		logger.Error("notifier initialization failed", "error", err)
		return 1
	}
	service, err := identity.NewService(store, tokens, hasher, piiCipher, secretCipher, webAuthn, notifier,
		cfg.PIIPepper, cfg.IPPepper, cfg.Service, cfg.Lockout, cfg.JWTIssuer)
	if err != nil {
		logger.Error("identity service initialization failed", "error", err)
		return 1
	}

	if email := strings.TrimSpace(os.Getenv("NEXORA_IDENTITY_BOOTSTRAP_ADMIN_EMAIL")); email != "" {
		password := os.Getenv("NEXORA_IDENTITY_BOOTSTRAP_ADMIN_PASSWORD")
		if password == "" {
			logger.Error("bootstrap admin password is required when email is set")
			return 1
		}
		id, err := service.BootstrapAdmin(ctx, email, password, identity.RequestMeta{RequestID: "bootstrap"})
		if err != nil {
			logger.Error("bootstrap admin failed", "error", err)
			return 1
		}
		logger.Info("bootstrap administrator ensured", "user_id", id.String())
		return 0
	}

	httpServer := &http.Server{
		Addr:              cfg.Base.HTTPAddr,
		Handler:           httpapi.New(service, cfg.Service, logger, version.Version, cfg.TrustProxy, providers, cfg.RateLimitPerMinute, cfg.RateLimitBurst),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}

	serverError := make(chan error, 2)
	go func() {
		logger.Info("identity HTTP service listening", "address", cfg.Base.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverError <- err
		}
	}()

	var grpcServer *grpc.Server
	var healthServer *health.Server
	if !cfg.DisableGRPC {
		options := []grpc.ServerOption{
			grpc.MaxRecvMsgSize(1 << 20),
			grpc.ConnectionTimeout(10 * time.Second),
		}
		if cfg.GRPCTLS != nil {
			options = append(options, grpc.Creds(credentials.NewTLS(cfg.GRPCTLS)))
		} else {
			logger.Warn("identity gRPC is running without TLS; this is allowed only in development/test")
		}
		grpcServer = grpc.NewServer(options...)
		grpcapi.Register(grpcServer, grpcapi.New(service, logger))
		healthServer = health.NewServer()
		healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
		healthpb.RegisterHealthServer(grpcServer, healthServer)
		var listenConfig net.ListenConfig
		listener, err := listenConfig.Listen(ctx, "tcp", cfg.GRPCAddr)
		if err != nil {
			logger.Error("gRPC listen failed", "error", err)
			return 1
		}
		go func() {
			logger.Info("identity gRPC service listening", "address", cfg.GRPCAddr)
			if err := grpcServer.Serve(listener); err != nil {
				serverError <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serverError:
		logger.Error("identity server failed", "error", err)
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
		logger.Error("identity HTTP shutdown failed", "error", err)
		return 1
	}
	logger.Info("identity service stopped")
	return 0
}
