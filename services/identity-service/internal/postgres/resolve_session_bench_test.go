package postgres

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

// BenchmarkIdentityServiceResolveSessionPostgres measures the authoritative
// per-request authorization path: JWT verification plus session, user, and role
// lookups from PostgreSQL. Run it only against the disposable test database:
//
//	NEXORA_TEST_DATABASE_URL=... go test -run '^$' -bench ResolveSession -benchtime 500x
//
// The benchmark is environment-specific and is not a production capacity claim.
func BenchmarkIdentityServiceResolveSessionPostgres(b *testing.B) {
	service, accessToken := benchmarkResolveSession(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := service.ResolveSession(ctx, accessToken); err != nil {
			b.Fatalf("resolve session: %v", err)
		}
	}
}

// BenchmarkIdentityServiceResolveSessionPostgresParallel measures the same path
// under concurrent load, exercising the pgx pool (default maximum 12
// connections) rather than a single serialized caller.
func BenchmarkIdentityServiceResolveSessionPostgresParallel(b *testing.B) {
	service, accessToken := benchmarkResolveSession(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := service.ResolveSession(ctx, accessToken); err != nil {
				b.Errorf("resolve session: %v", err)
				return
			}
		}
	})
}

func benchmarkResolveSession(b *testing.B) (*identity.Service, string) {
	b.Helper()
	store, _ := integrationDatabase(b)
	ctx := context.Background()

	pepper := randomHash(b)
	piiCipher, err := identity.NewAESGCMCipher(randomHash(b))
	if err != nil {
		b.Fatalf("pii cipher: %v", err)
	}
	secretCipher, err := identity.NewAESGCMCipher(randomHash(b))
	if err != nil {
		b.Fatalf("secret cipher: %v", err)
	}
	hasher, err := identity.NewPasswordHasher(identity.Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}, pepper)
	if err != nil {
		b.Fatalf("hasher: %v", err)
	}
	_, private, err := identity.GenerateEd25519Key()
	if err != nil {
		b.Fatalf("JWT key: %v", err)
	}
	tokens, err := identity.NewTokenManager(private, "nexora-identity-test", "nexora-api-test", 15*time.Minute, time.Minute)
	if err != nil {
		b.Fatalf("token manager: %v", err)
	}
	cfg := identity.DefaultServiceConfig()
	cfg.ExposeDevTokens = true
	service, err := identity.NewService(store, tokens, hasher, piiCipher, secretCipher, nil, &identity.CaptureNotifier{}, pepper, randomHash(b), cfg, identity.DefaultLockoutPolicy(), "NEXORA")
	if err != nil {
		b.Fatalf("service: %v", err)
	}
	meta := identity.RequestMeta{IP: netip.MustParseAddr("192.0.2.10"), UserAgent: "benchmark", RequestID: "req-bench"}
	const email = "resolve-session-bench@example.test"
	registered, err := service.Register(ctx, identity.RegisterInput{Email: email, Password: "CorrectHorseBatteryStaple9!", CountryCode: "GB"}, meta)
	if err != nil {
		b.Fatalf("register: %v", err)
	}
	if err := service.VerifyContact(ctx, registered.DevVerificationToken, "email", meta); err != nil {
		b.Fatalf("verify contact: %v", err)
	}
	login, err := service.Login(ctx, identity.LoginInput{Email: email, Password: "CorrectHorseBatteryStaple9!"}, meta)
	if err != nil {
		b.Fatalf("login: %v", err)
	}
	return service, login.Tokens.AccessToken
}
