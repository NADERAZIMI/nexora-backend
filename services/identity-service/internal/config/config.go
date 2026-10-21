// Package config loads and validates identity-service configuration. Required
// security material fails closed outside explicitly ephemeral development mode.
package config

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	baseconfig "github.com/nexora-exchange/nexora/libs/go/config"
	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

// Config is the validated identity-service runtime configuration.
type Config struct {
	Base               baseconfig.Config
	DatabaseURL        string
	GRPCAddr           string
	DisableGRPC        bool
	GRPCTLS            *tls.Config
	AutoMigrate        bool
	TrustProxy         bool
	ExposeDevTokens    bool
	RateLimitPerMinute int
	RateLimitBurst     int
	MaxSessions        int
	MaxAPIKeys         int

	JWTPrivateKey ed25519.PrivateKey
	JWTIssuer     string
	JWTAudience   string
	AccessTTL     time.Duration
	RefreshTTL    time.Duration
	MFATTL        time.Duration
	StepUpTTL     time.Duration

	PasswordPepper []byte
	PIIPepper      []byte
	IPPepper       []byte
	PIICipherKey   []byte
	SecretKey      []byte
	Argon2         identity.Argon2Params
	WebAuthn       identity.WebAuthnConfig
	Service        identity.ServiceConfig
	Lockout        identity.LockoutPolicy
}

// Load reads NEXORA_* variables. Ephemeral keys are permitted only when
// NEXORA_IDENTITY_ALLOW_EPHEMERAL_KEYS=true and the environment is development
// or test. Staging and production require externally supplied key material.
func Load() (Config, error) {
	base, err := baseconfig.Load("identity-service")
	if err != nil {
		return Config{}, err
	}
	environment := strings.ToLower(base.Environment)
	ephemeral := envBool("NEXORA_IDENTITY_ALLOW_EPHEMERAL_KEYS", false)
	if ephemeral && environment != "development" && environment != "test" {
		return Config{}, errors.New("ephemeral identity keys are forbidden outside development/test")
	}
	cfg := Config{
		Base:               base,
		GRPCAddr:           envDefault("NEXORA_IDENTITY_GRPC_ADDR", ":9090"),
		DisableGRPC:        envBool("NEXORA_IDENTITY_DISABLE_GRPC", false),
		TrustProxy:         envBool("NEXORA_IDENTITY_TRUST_PROXY", false),
		ExposeDevTokens:    envBool("NEXORA_IDENTITY_EXPOSE_DEV_TOKENS", false),
		RateLimitPerMinute: envInt("NEXORA_IDENTITY_RATE_LIMIT_PER_MINUTE", 120, 1, 10000),
		RateLimitBurst:     envInt("NEXORA_IDENTITY_RATE_LIMIT_BURST", 40, 1, 1000),
		MaxSessions:        envInt("NEXORA_IDENTITY_MAX_SESSIONS", 10, 1, 100),
		MaxAPIKeys:         envInt("NEXORA_IDENTITY_MAX_API_KEYS", 10, 1, 100),
		JWTIssuer:          envDefault("NEXORA_IDENTITY_JWT_ISSUER", "nexora-identity"),
		JWTAudience:        envDefault("NEXORA_IDENTITY_JWT_AUDIENCE", "nexora-api"),
	}
	if cfg.ExposeDevTokens && environment != "development" && environment != "test" {
		return Config{}, errors.New("development token exposure is forbidden outside development/test")
	}
	cfg.DatabaseURL = strings.TrimSpace(os.Getenv("NEXORA_IDENTITY_DATABASE_URL"))
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("NEXORA_IDENTITY_DATABASE_URL is required")
	}
	cfg.GRPCTLS, err = grpcTLS(environment, cfg.DisableGRPC)
	if err != nil {
		return Config{}, err
	}
	cfg.AutoMigrate = envBool("NEXORA_IDENTITY_AUTO_MIGRATE", environment == "development" || environment == "test")

	cfg.AccessTTL, err = envDuration("NEXORA_IDENTITY_ACCESS_TTL", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	cfg.RefreshTTL, err = envDuration("NEXORA_IDENTITY_REFRESH_TTL", 30*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	cfg.MFATTL, err = envDuration("NEXORA_IDENTITY_MFA_TTL", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	cfg.StepUpTTL, err = envDuration("NEXORA_IDENTITY_STEPUP_TTL", 60*time.Second)
	if err != nil {
		return Config{}, err
	}

	cfg.PasswordPepper, err = secret("NEXORA_IDENTITY_PASSWORD_PEPPER", ephemeral)
	if err != nil {
		return Config{}, err
	}
	cfg.PIIPepper, err = secret("NEXORA_IDENTITY_PII_PEPPER", ephemeral)
	if err != nil {
		return Config{}, err
	}
	cfg.IPPepper, err = secret("NEXORA_IDENTITY_IP_PEPPER", ephemeral)
	if err != nil {
		return Config{}, err
	}
	cfg.PIICipherKey, err = secret("NEXORA_IDENTITY_PII_KEY", ephemeral)
	if err != nil {
		return Config{}, err
	}
	cfg.SecretKey, err = secret("NEXORA_IDENTITY_SECRET_KEY", ephemeral)
	if err != nil {
		return Config{}, err
	}
	cfg.JWTPrivateKey, err = jwtKey(ephemeral)
	if err != nil {
		return Config{}, err
	}
	cfg.Argon2 = identity.Argon2Params{
		MemoryKiB:   uint32(envInt("NEXORA_IDENTITY_ARGON2_MEMORY_KIB", 64*1024, 8*1024, 1024*1024)), //nolint:gosec // bounded by envInt
		Iterations:  uint32(envInt("NEXORA_IDENTITY_ARGON2_ITERATIONS", 3, 1, 10)),                   //nolint:gosec // bounded by envInt
		Parallelism: uint8(envInt("NEXORA_IDENTITY_ARGON2_PARALLELISM", 4, 1, 16)),                   //nolint:gosec // bounded by envInt
		SaltLength:  16, KeyLength: 32,
	}
	cfg.WebAuthn = identity.WebAuthnConfig{
		RPDisplayName: envDefault("NEXORA_IDENTITY_WEBAUTHN_RP_DISPLAY_NAME", "NEXORA"),
		RPID:          envDefault("NEXORA_IDENTITY_WEBAUTHN_RP_ID", ""),
		RPOrigins:     splitCSV(os.Getenv("NEXORA_IDENTITY_WEBAUTHN_RP_ORIGINS")),
		Timeout:       60 * time.Second,
	}
	if cfg.WebAuthn.RPID == "" || len(cfg.WebAuthn.RPOrigins) == 0 {
		if environment == "development" || environment == "test" {
			if cfg.WebAuthn.RPID == "" {
				cfg.WebAuthn.RPID = "localhost"
			}
			if len(cfg.WebAuthn.RPOrigins) == 0 {
				cfg.WebAuthn.RPOrigins = []string{"http://localhost:8082"}
			}
		} else {
			return Config{}, errors.New("WebAuthn RP ID and origins are required")
		}
	}
	cfg.Service = identity.ServiceConfig{
		AccessTTL: cfg.AccessTTL, RefreshTTL: cfg.RefreshTTL, MFATTL: cfg.MFATTL,
		WebAuthnTTL: 2 * time.Minute, MaxSessions: cfg.MaxSessions, MaxAPIKeys: cfg.MaxAPIKeys,
		ExposeDevTokens: cfg.ExposeDevTokens,
	}
	cfg.Lockout = identity.DefaultLockoutPolicy()
	return cfg, nil
}

func grpcTLS(environment string, disabled bool) (*tls.Config, error) {
	if disabled {
		return nil, nil
	}
	certFile := strings.TrimSpace(os.Getenv("NEXORA_IDENTITY_GRPC_TLS_CERT_FILE"))
	keyFile := strings.TrimSpace(os.Getenv("NEXORA_IDENTITY_GRPC_TLS_KEY_FILE"))
	caFile := strings.TrimSpace(os.Getenv("NEXORA_IDENTITY_GRPC_CLIENT_CA_FILE"))
	required := environment == "staging" || environment == "production" || environment == "prod"
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
		return nil, fmt.Errorf("load gRPC TLS certificate: %w", err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}
	if caFile != "" {
		pemData, err := os.ReadFile(caFile) //nolint:gosec // operator-controlled TLS path
		if err != nil {
			return nil, fmt.Errorf("read gRPC client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, errors.New("gRPC client CA contains no certificates")
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	} else if required {
		return nil, errors.New("gRPC client CA is required for mTLS outside development/test")
	}
	return cfg, nil
}

func jwtKey(ephemeral bool) (ed25519.PrivateKey, error) {
	if path := strings.TrimSpace(os.Getenv("NEXORA_IDENTITY_JWT_PRIVATE_KEY_FILE")); path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // operator-controlled JWT key path
		if err != nil {
			return nil, fmt.Errorf("read JWT private key: %w", err)
		}
		return identity.ParseEd25519PrivateKeyPEM(data)
	}
	if encoded := strings.TrimSpace(os.Getenv("NEXORA_IDENTITY_JWT_PRIVATE_KEY")); encoded != "" {
		decoded, err := identity.DecodeKey(encoded)
		if err != nil {
			return nil, errors.New("invalid JWT private key encoding")
		}
		key, err := identity.ParseEd25519PrivateKeyPEM(decoded)
		if err != nil {
			return nil, errors.New("invalid JWT private key")
		}
		return key, nil
	}
	if !ephemeral {
		return nil, errors.New("JWT Ed25519 private key is required")
	}
	_, private, err := identity.GenerateEd25519Key()
	return private, err
}

func secret(name string, ephemeral bool) ([]byte, error) {
	const minLength = 32
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		if !ephemeral {
			return nil, fmt.Errorf("%s is required", name)
		}
		raw, err := identity.RandomBytes(32)
		if err != nil {
			return nil, err
		}
		return raw, nil
	}
	decoded, err := identity.DecodeKey(value)
	if err != nil {
		return nil, fmt.Errorf("%s must be base64 encoded", name)
	}
	if len(decoded) < minLength {
		return nil, fmt.Errorf("%s must decode to at least %d bytes", name, minLength)
	}
	return decoded, nil
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

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// RedactedString never includes secret material.
func (c Config) RedactedString() string {
	return fmt.Sprintf("identity-service{env=%s, db_configured=%t, grpc=%s, webauthn_rp=%s}",
		c.Base.Environment, c.DatabaseURL != "", c.GRPCAddr, c.WebAuthn.RPID)
}

// Ensure the identity config package uses the base package intentionally.
var _ = baseconfig.Config{}
