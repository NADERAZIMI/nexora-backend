package config

import (
	"strings"
	"testing"
	"time"
)

func baseEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("NEXORA_ENVIRONMENT", "development")
	t.Setenv("NEXORA_SERVICE_NAME", "reference-data-service")
	t.Setenv("NEXORA_REFERENCE_DATABASE_URL", "postgres://identity@localhost:5432/reference_test?sslmode=disable")
	t.Setenv("NEXORA_REFERENCE_INTERNAL_TOKEN", "")
	t.Setenv("NEXORA_REFERENCE_IDENTITY_CA_FILE", "")
	t.Setenv("NEXORA_REFERENCE_IDENTITY_CERT_FILE", "")
	t.Setenv("NEXORA_REFERENCE_IDENTITY_KEY_FILE", "")
	t.Setenv("NEXORA_REFERENCE_GRPC_TLS_CERT_FILE", "")
	t.Setenv("NEXORA_REFERENCE_GRPC_TLS_KEY_FILE", "")
	t.Setenv("NEXORA_REFERENCE_GRPC_CLIENT_CA_FILE", "")
	t.Setenv("NEXORA_REFERENCE_RELOAD_INTERVAL", "")
	t.Setenv("NEXORA_REFERENCE_ACTIVATION_INTERVAL", "")
	t.Setenv("NEXORA_REFERENCE_STALE_AFTER", "")
}

func TestLoadDevelopmentDefaults(t *testing.T) {
	baseEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.SeedFixtures || !cfg.AutoMigrate || cfg.InternalTokenRequired {
		t.Fatalf("unexpected development flags: seed=%t migrate=%t tokenRequired=%t",
			cfg.SeedFixtures, cfg.AutoMigrate, cfg.InternalTokenRequired)
	}
	if cfg.ReloadInterval != 5*time.Second || cfg.ActivationInterval != time.Second || cfg.StaleAfter != 24*time.Hour {
		t.Fatalf("unexpected intervals: reload=%s activation=%s stale=%s",
			cfg.ReloadInterval, cfg.ActivationInterval, cfg.StaleAfter)
	}
	if cfg.IdentityTLS != nil || cfg.GRPCTLS != nil {
		t.Fatal("development defaults unexpectedly require TLS material")
	}
	if strings.Contains(cfg.RedactedString(), "postgres://") {
		t.Fatal("redacted configuration leaked the database URL")
	}
}

func TestLoadProductionFailsClosedWithoutServiceToken(t *testing.T) {
	baseEnvironment(t)
	t.Setenv("NEXORA_ENVIRONMENT", "production")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "INTERNAL_TOKEN") {
		t.Fatalf("production without internal token was accepted: %v", err)
	}
}

func TestLoadProductionRequiresIdentityTLS(t *testing.T) {
	baseEnvironment(t)
	t.Setenv("NEXORA_ENVIRONMENT", "production")
	t.Setenv("NEXORA_REFERENCE_INTERNAL_TOKEN", strings.Repeat("s", 32))
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "mTLS") {
		t.Fatalf("production without identity mTLS was accepted: %v", err)
	}
}

func TestLoadRejectsShortServiceToken(t *testing.T) {
	baseEnvironment(t)
	t.Setenv("NEXORA_REFERENCE_INTERNAL_TOKEN", "short")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("short service token was accepted: %v", err)
	}
}

func TestLoadRejectsUnsafeIntervals(t *testing.T) {
	baseEnvironment(t)
	t.Setenv("NEXORA_REFERENCE_RELOAD_INTERVAL", "10ms")
	if _, err := Load(); err == nil {
		t.Fatal("unsafe reload interval was accepted")
	}
	t.Setenv("NEXORA_REFERENCE_RELOAD_INTERVAL", "")
	t.Setenv("NEXORA_REFERENCE_STALE_AFTER", "1s")
	if _, err := Load(); err == nil {
		t.Fatal("unsafe staleness bound was accepted")
	}
}
