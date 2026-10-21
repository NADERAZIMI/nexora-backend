package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

func clearNexoraEnv(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(name, "NEXORA_") {
			continue
		}
		old, existed := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
		nameCopy, oldCopy, existedCopy := name, old, existed
		t.Cleanup(func() {
			if existedCopy {
				_ = os.Setenv(nameCopy, oldCopy)
			} else {
				_ = os.Unsetenv(nameCopy)
			}
		})
	}
}

func developmentBaseline(t *testing.T) {
	t.Helper()
	clearNexoraEnv(t)
	t.Setenv("NEXORA_ENVIRONMENT", "development")
	t.Setenv("NEXORA_IDENTITY_DATABASE_URL", "postgres://identity:identity@localhost:5432/identity?sslmode=disable")
	t.Setenv("NEXORA_IDENTITY_ALLOW_EPHEMERAL_KEYS", "true")
	t.Setenv("NEXORA_IDENTITY_DISABLE_GRPC", "true")
}

func TestLoadDevelopmentEphemeralKeys(t *testing.T) {
	developmentBaseline(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.JWTPrivateKey) != ed25519.PrivateKeySize {
		t.Fatalf("unexpected JWT key length: %d", len(cfg.JWTPrivateKey))
	}
	if len(cfg.PasswordPepper) != 32 || len(cfg.PIIPepper) != 32 || len(cfg.IPPepper) != 32 ||
		len(cfg.PIICipherKey) != 32 || len(cfg.SecretKey) != 32 {
		t.Fatal("ephemeral secret lengths are invalid")
	}
	if cfg.AccessTTL != 15*time.Minute || cfg.RefreshTTL != 30*24*time.Hour || cfg.StepUpTTL != time.Minute {
		t.Fatalf("unexpected defaults: access=%s refresh=%s stepup=%s", cfg.AccessTTL, cfg.RefreshTTL, cfg.StepUpTTL)
	}
	if cfg.GRPCTLS != nil {
		t.Fatal("disabled gRPC must not configure TLS")
	}
	if cfg.WebAuthn.RPID != "localhost" || len(cfg.WebAuthn.RPOrigins) != 1 {
		t.Fatalf("unexpected WebAuthn defaults: %+v", cfg.WebAuthn)
	}
	redacted := cfg.RedactedString()
	for _, secret := range [][]byte{cfg.PasswordPepper, cfg.SecretKey, cfg.PIICipherKey} {
		if strings.Contains(redacted, string(secret)) {
			t.Fatal("redacted configuration leaked secret material")
		}
	}
}

func TestLoadFailsClosedWithoutRequiredSecrets(t *testing.T) {
	clearNexoraEnv(t)
	t.Setenv("NEXORA_ENVIRONMENT", "test")
	t.Setenv("NEXORA_IDENTITY_DATABASE_URL", "postgres://localhost/identity")
	t.Setenv("NEXORA_IDENTITY_DISABLE_GRPC", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PASSWORD_PEPPER") {
		t.Fatalf("expected missing password pepper error, got %v", err)
	}
}

func TestLoadRejectsEphemeralKeysOutsideDevelopment(t *testing.T) {
	clearNexoraEnv(t)
	t.Setenv("NEXORA_ENVIRONMENT", "production")
	t.Setenv("NEXORA_IDENTITY_DATABASE_URL", "postgres://localhost/identity")
	t.Setenv("NEXORA_IDENTITY_ALLOW_EPHEMERAL_KEYS", "true")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("expected ephemeral key rejection, got %v", err)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	clearNexoraEnv(t)
	t.Setenv("NEXORA_ENVIRONMENT", "development")
	t.Setenv("NEXORA_IDENTITY_ALLOW_EPHEMERAL_KEYS", "true")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("expected database URL error, got %v", err)
	}
}

func TestLoadRejectsInvalidDurations(t *testing.T) {
	developmentBaseline(t)
	t.Setenv("NEXORA_IDENTITY_ACCESS_TTL", "-1m")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ACCESS_TTL") {
		t.Fatalf("expected duration error, got %v", err)
	}
}

func TestLoadProductionRequiresTLSAndJWTKey(t *testing.T) {
	clearNexoraEnv(t)
	t.Setenv("NEXORA_ENVIRONMENT", "production")
	t.Setenv("NEXORA_IDENTITY_DATABASE_URL", "postgres://localhost/identity")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "gRPC TLS") {
		t.Fatalf("expected gRPC TLS requirement, got %v", err)
	}

	t.Setenv("NEXORA_IDENTITY_DISABLE_GRPC", "true")
	for _, name := range []string{
		"NEXORA_IDENTITY_PASSWORD_PEPPER",
		"NEXORA_IDENTITY_PII_PEPPER",
		"NEXORA_IDENTITY_IP_PEPPER",
		"NEXORA_IDENTITY_PII_KEY",
		"NEXORA_IDENTITY_SECRET_KEY",
	} {
		t.Setenv(name, base64.StdEncoding.EncodeToString(randomBytes(t, 32)))
	}
	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), "JWT Ed25519 private key") {
		t.Fatalf("expected JWT key requirement, got %v", err)
	}
}

func TestLoadProductionWithExternalMaterial(t *testing.T) {
	clearNexoraEnv(t)
	t.Setenv("NEXORA_ENVIRONMENT", "production")
	t.Setenv("NEXORA_IDENTITY_DATABASE_URL", "postgres://localhost/identity")
	t.Setenv("NEXORA_IDENTITY_DISABLE_GRPC", "true")
	_, private, err := identity.GenerateEd25519Key()
	if err != nil {
		t.Fatalf("generate JWT key: %v", err)
	}
	pemData, err := identity.MarshalEd25519PrivateKeyPEM(private)
	if err != nil {
		t.Fatalf("marshal JWT key: %v", err)
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "jwt.pem")
	if err := os.WriteFile(keyPath, pemData, 0o600); err != nil {
		t.Fatalf("write JWT key: %v", err)
	}
	t.Setenv("NEXORA_IDENTITY_JWT_PRIVATE_KEY_FILE", keyPath)
	for _, name := range []string{
		"NEXORA_IDENTITY_PASSWORD_PEPPER",
		"NEXORA_IDENTITY_PII_PEPPER",
		"NEXORA_IDENTITY_IP_PEPPER",
		"NEXORA_IDENTITY_PII_KEY",
		"NEXORA_IDENTITY_SECRET_KEY",
	} {
		encoded := base64.StdEncoding.EncodeToString(randomBytes(t, 32))
		t.Setenv(name, encoded)
	}
	t.Setenv("NEXORA_IDENTITY_WEBAUTHN_RP_ID", "identity.example.test")
	t.Setenv("NEXORA_IDENTITY_WEBAUTHN_RP_ORIGINS", "https://identity.example.test")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load production config: %v", err)
	}
	if !cfg.JWTPrivateKey.Equal(private) {
		t.Fatal("loaded JWT key does not match external key material")
	}
	if cfg.Base.Environment != "production" {
		t.Fatalf("environment: %s", cfg.Base.Environment)
	}
}

func TestLoadProductionMutualTLS(t *testing.T) {
	clearNexoraEnv(t)
	t.Setenv("NEXORA_ENVIRONMENT", "production")
	t.Setenv("NEXORA_IDENTITY_DATABASE_URL", "postgres://localhost/identity")
	dir := t.TempDir()
	certFile, keyFile, caFile := writeTestCertificate(t, dir)
	t.Setenv("NEXORA_IDENTITY_GRPC_TLS_CERT_FILE", certFile)
	t.Setenv("NEXORA_IDENTITY_GRPC_TLS_KEY_FILE", keyFile)
	t.Setenv("NEXORA_IDENTITY_GRPC_CLIENT_CA_FILE", caFile)
	_, private, err := identity.GenerateEd25519Key()
	if err != nil {
		t.Fatalf("generate JWT key: %v", err)
	}
	pemData, err := identity.MarshalEd25519PrivateKeyPEM(private)
	if err != nil {
		t.Fatalf("marshal JWT key: %v", err)
	}
	keyPath := filepath.Join(dir, "jwt.pem")
	if err := os.WriteFile(keyPath, pemData, 0o600); err != nil {
		t.Fatalf("write JWT key: %v", err)
	}
	t.Setenv("NEXORA_IDENTITY_JWT_PRIVATE_KEY_FILE", keyPath)
	for _, name := range []string{
		"NEXORA_IDENTITY_PASSWORD_PEPPER",
		"NEXORA_IDENTITY_PII_PEPPER",
		"NEXORA_IDENTITY_IP_PEPPER",
		"NEXORA_IDENTITY_PII_KEY",
		"NEXORA_IDENTITY_SECRET_KEY",
	} {
		encoded := base64.StdEncoding.EncodeToString(randomBytes(t, 32))
		t.Setenv(name, encoded)
	}
	t.Setenv("NEXORA_IDENTITY_WEBAUTHN_RP_ID", "identity.example.test")
	t.Setenv("NEXORA_IDENTITY_WEBAUTHN_RP_ORIGINS", "https://identity.example.test")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load mTLS config: %v", err)
	}
	if cfg.GRPCTLS == nil || cfg.GRPCTLS.MinVersion != tls.VersionTLS13 || cfg.GRPCTLS.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("unexpected gRPC TLS configuration: %+v", cfg.GRPCTLS)
	}
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	out := make([]byte, n)
	if _, err := rand.Read(out); err != nil {
		t.Fatalf("random bytes: %v", err)
	}
	return out
}

func writeTestCertificate(t *testing.T, dir string) (string, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate TLS key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "identity.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:              []string{"identity.test"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create TLS certificate: %v", err)
	}
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	caFile := filepath.Join(dir, "ca.crt")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatalf("write CA: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile, caFile
}
