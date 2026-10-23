// Package config loads and validates reference-data-service runtime settings.
package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	baseconfig "github.com/nexora-exchange/nexora/libs/go/config"
)

// Config is the validated reference-data-service configuration.
type Config struct {
	Base                  baseconfig.Config
	DatabaseURL           string
	GRPCAddr              string
	DisableGRPC           bool
	GRPCTLS               *tls.Config
	AutoMigrate           bool
	IdentityGRPCAddr      string
	IdentityTimeout       time.Duration
	IdentityTLS           *tls.Config
	InternalToken         string
	InternalTokenRequired bool
	ReloadInterval        time.Duration
	ActivationInterval    time.Duration
	StaleAfter            time.Duration
	SeedFixtures          bool
	RateLimitPerMinute    int
	RateLimitBurst        int
}

// Load reads NEXORA_REFERENCE_* environment variables and fails closed on
// unsafe values. Secrets are supplied only from the environment or a mounted
// path and are never logged.
func Load() (Config, error) {
	base, err := baseconfig.Load("reference-data-service")
	if err != nil {
		return Config{}, err
	}
	environment := strings.ToLower(base.Environment)
	cfg := Config{
		Base:                  base,
		GRPCAddr:              envDefault("NEXORA_REFERENCE_GRPC_ADDR", ":9091"),
		DisableGRPC:           envBool("NEXORA_REFERENCE_DISABLE_GRPC", false),
		IdentityGRPCAddr:      envDefault("NEXORA_REFERENCE_IDENTITY_GRPC_ADDR", "localhost:9090"),
		RateLimitPerMinute:    envInt("NEXORA_REFERENCE_RATE_LIMIT_PER_MINUTE", 120, 1, 10000),
		RateLimitBurst:        envInt("NEXORA_REFERENCE_RATE_LIMIT_BURST", 40, 1, 1000),
		SeedFixtures:          envBool("NEXORA_REFERENCE_SEED_FIXTURES", isLocalEnvironment(environment)),
		InternalTokenRequired: !isLocalEnvironment(environment),
	}
	cfg.DatabaseURL = strings.TrimSpace(os.Getenv("NEXORA_REFERENCE_DATABASE_URL"))
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("NEXORA_REFERENCE_DATABASE_URL is required")
	}
	cfg.AutoMigrate = envBool("NEXORA_REFERENCE_AUTO_MIGRATE", isLocalEnvironment(environment))
	cfg.IdentityTimeout, err = envDuration("NEXORA_REFERENCE_IDENTITY_TIMEOUT", 3*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.ReloadInterval, err = envDuration("NEXORA_REFERENCE_RELOAD_INTERVAL", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.ActivationInterval, err = envDuration("NEXORA_REFERENCE_ACTIVATION_INTERVAL", time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.StaleAfter, err = envDuration("NEXORA_REFERENCE_STALE_AFTER", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	if cfg.ReloadInterval < 250*time.Millisecond || cfg.ReloadInterval > time.Minute {
		return Config{}, errors.New("NEXORA_REFERENCE_RELOAD_INTERVAL must be between 250ms and 1m")
	}
	if cfg.ActivationInterval < 250*time.Millisecond || cfg.ActivationInterval > time.Minute {
		return Config{}, errors.New("NEXORA_REFERENCE_ACTIVATION_INTERVAL must be between 250ms and 1m")
	}
	if cfg.StaleAfter < time.Minute || cfg.StaleAfter > 30*24*time.Hour {
		return Config{}, errors.New("NEXORA_REFERENCE_STALE_AFTER must be between 1m and 720h")
	}
	cfg.InternalToken = strings.TrimSpace(os.Getenv("NEXORA_REFERENCE_INTERNAL_TOKEN"))
	if cfg.InternalToken == "" && cfg.InternalTokenRequired {
		return Config{}, errors.New("NEXORA_REFERENCE_INTERNAL_TOKEN is required outside development/test")
	}
	if cfg.InternalToken != "" && len(cfg.InternalToken) < 32 {
		return Config{}, errors.New("NEXORA_REFERENCE_INTERNAL_TOKEN must be at least 32 characters")
	}
	cfg.IdentityTLS, err = identityTLS(environment)
	if err != nil {
		return Config{}, err
	}
	cfg.GRPCTLS, err = serverTLS(environment, cfg.DisableGRPC)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func serverTLS(environment string, disabled bool) (*tls.Config, error) {
	if disabled {
		return nil, nil
	}
	certFile := strings.TrimSpace(os.Getenv("NEXORA_REFERENCE_GRPC_TLS_CERT_FILE"))
	keyFile := strings.TrimSpace(os.Getenv("NEXORA_REFERENCE_GRPC_TLS_KEY_FILE"))
	caFile := strings.TrimSpace(os.Getenv("NEXORA_REFERENCE_GRPC_CLIENT_CA_FILE"))
	required := !isLocalEnvironment(environment)
	if certFile == "" && keyFile == "" {
		if required {
			return nil, errors.New("gRPC TLS certificate and client CA are required outside development/test")
		}
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, errors.New("gRPC TLS certificate and key must both be configured")
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load reference-data gRPC certificate: %w", err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}
	if caFile != "" {
		caPEM, err := os.ReadFile(caFile) //nolint:gosec // operator-controlled TLS path
		if err != nil {
			return nil, fmt.Errorf("read reference-data client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("reference-data client CA contains no certificates")
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	} else if required {
		return nil, errors.New("reference-data gRPC client CA is required outside development/test")
	}
	return cfg, nil
}

func identityTLS(environment string) (*tls.Config, error) {
	caFile := strings.TrimSpace(os.Getenv("NEXORA_REFERENCE_IDENTITY_CA_FILE"))
	certFile := strings.TrimSpace(os.Getenv("NEXORA_REFERENCE_IDENTITY_CERT_FILE"))
	keyFile := strings.TrimSpace(os.Getenv("NEXORA_REFERENCE_IDENTITY_KEY_FILE"))
	required := !isLocalEnvironment(environment)
	if caFile == "" && certFile == "" && keyFile == "" {
		if required {
			return nil, errors.New("identity gRPC mTLS CA and client certificate are required outside development/test")
		}
		return nil, nil
	}
	if caFile == "" || certFile == "" || keyFile == "" {
		return nil, errors.New("identity gRPC CA, certificate, and key must all be configured")
	}
	caPEM, err := os.ReadFile(caFile) //nolint:gosec // operator-controlled TLS path
	if err != nil {
		return nil, fmt.Errorf("read identity CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("identity CA contains no certificates")
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load identity client certificate: %w", err)
	}
	return &tls.Config{
		RootCAs: pool, Certificates: []tls.Certificate{certificate},
		MinVersion: tls.VersionTLS13, ServerName: strings.TrimSpace(os.Getenv("NEXORA_REFERENCE_IDENTITY_TLS_SERVER_NAME")),
	}, nil
}

// RedactedString never includes secret material.
func (c Config) RedactedString() string {
	return fmt.Sprintf("reference-data-service{env=%s, db_configured=%t, grpc=%s, identity=%s, seed=%t}",
		c.Base.Environment, c.DatabaseURL != "", c.GRPCAddr, c.IdentityGRPCAddr, c.SeedFixtures)
}

func isLocalEnvironment(environment string) bool {
	return environment == "development" || environment == "dev" || environment == "test"
}

func envDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if value == "" {
		return fallback
	}
	return value == "true" || value == "1" || value == "yes"
}

func envInt(name string, fallback, minValue, maxValue int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minValue || parsed > maxValue {
		return fallback
	}
	return parsed
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}
