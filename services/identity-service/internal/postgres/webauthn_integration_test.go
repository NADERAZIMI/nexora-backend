package postgres

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/google/uuid"

	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
	"github.com/nexora-exchange/nexora/services/identity-service/internal/webauthntest"
)

// TestIdentityServicePostgresWebAuthn exercises real P-256/ES256 WebAuthn
// ceremonies through the service and the PostgreSQL store. Unlike the memory
// tests this proves challenge single-use, counter persistence, and MFA
// enrollment survive real transactions.
func TestIdentityServicePostgresWebAuthn(t *testing.T) {
	store, _ := integrationDatabase(t)
	ctx := context.Background()

	pepper := randomHash(t)
	piiCipher, err := identity.NewAESGCMCipher(randomHash(t))
	if err != nil {
		t.Fatalf("pii cipher: %v", err)
	}
	secretCipher, err := identity.NewAESGCMCipher(randomHash(t))
	if err != nil {
		t.Fatalf("secret cipher: %v", err)
	}
	hasher, err := identity.NewPasswordHasher(identity.Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}, pepper)
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}
	_, private, err := identity.GenerateEd25519Key()
	if err != nil {
		t.Fatalf("JWT key: %v", err)
	}
	tokens, err := identity.NewTokenManager(private, "nexora-identity-test", "nexora-api-test", 15*time.Minute, time.Minute)
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}
	webAuthn, err := identity.NewWebAuthnManager(identity.WebAuthnConfig{
		RPDisplayName: "NEXORA Test", RPID: "localhost",
		RPOrigins: []string{"http://localhost:8082"}, Timeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("webauthn manager: %v", err)
	}
	notifier := &identity.CaptureNotifier{}
	cfg := identity.DefaultServiceConfig()
	cfg.ExposeDevTokens = true
	service, err := identity.NewService(store, tokens, hasher, piiCipher, secretCipher, webAuthn, notifier, pepper, randomHash(t), cfg, identity.DefaultLockoutPolicy(), "NEXORA")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	now := time.Now().UTC()
	service.SetClock(func() time.Time { return now })
	meta := identity.RequestMeta{IP: netip.MustParseAddr("192.0.2.10"), UserAgent: "webauthn-integration", RequestID: "req-webauthn"}

	const email = "webauthn-integration@example.test"
	registered, err := service.Register(ctx, identity.RegisterInput{Email: email, Password: "CorrectHorseBatteryStaple9!", CountryCode: "GB"}, meta)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := service.VerifyContact(ctx, registered.DevVerificationToken, "email", meta); err != nil {
		t.Fatalf("verify contact: %v", err)
	}
	passwordLogin, err := service.Login(ctx, identity.LoginInput{Email: email, Password: "CorrectHorseBatteryStaple9!"}, meta)
	if err != nil {
		t.Fatalf("password login: %v", err)
	}
	principal, err := service.ResolveSession(ctx, passwordLogin.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}

	authenticator, err := webauthntest.New(webauthntest.Options{RPID: "localhost", Origin: "http://localhost:8082"})
	if err != nil {
		t.Fatalf("software authenticator: %v", err)
	}

	// Registration: real attestation verified by go-webauthn and persisted.
	creationOptions, err := service.BeginWebAuthnRegistration(ctx, principal, meta)
	if err != nil {
		t.Fatalf("begin registration: %v", err)
	}
	registrationBody, err := authenticator.Create(creationChallenge(t, creationOptions), principal.UserID[:])
	if err != nil {
		t.Fatalf("create registration: %v", err)
	}
	if err := service.FinishWebAuthnRegistration(ctx, principal, registrationBody, meta); err != nil {
		t.Fatalf("finish registration: %v", err)
	}
	credentials, err := store.GetCredentials(ctx, principal.UserID, identity.CredentialWebAuthn)
	if err != nil || len(credentials) != 1 {
		t.Fatalf("persisted credentials: %d %v", len(credentials), err)
	}
	var stored struct {
		ID            string `json:"id"`
		PublicKey     []byte `json:"publicKey"`
		Authenticator struct {
			SignCount uint32 `json:"signCount"`
		} `json:"authenticator"`
	}
	if err := json.Unmarshal(credentials[0].Metadata, &stored); err != nil {
		t.Fatalf("decode credential metadata: %v", err)
	}
	storedID, err := base64.StdEncoding.DecodeString(stored.ID)
	if err != nil {
		storedID, err = base64.RawURLEncoding.DecodeString(stored.ID)
	}
	if err != nil || !bytes.Equal(storedID, authenticator.CredentialID()) || len(stored.PublicKey) == 0 {
		t.Fatalf("persisted credential mismatch: id=%q publicKey=%d", stored.ID, len(stored.PublicKey))
	}
	enrolled, err := store.GetUserByID(ctx, principal.UserID)
	if err != nil || enrolled.MFAEnrolledAt == nil {
		t.Fatalf("MFA not enrolled: %+v %v", enrolled, err)
	}
	registrationAudits, err := store.ListAudit(ctx, identity.AuditFilter{Action: "mfa.webauthn.register", Limit: 10})
	if err != nil || len(registrationAudits) == 0 {
		t.Fatalf("registration audit missing: %d %v", len(registrationAudits), err)
	}
	enrollAudits, err := store.ListAudit(ctx, identity.AuditFilter{Action: "mfa.enroll", Limit: 10})
	if err != nil || len(enrollAudits) == 0 {
		t.Fatalf("MFA enrollment audit missing: %d %v", len(enrollAudits), err)
	}

	// Passwordless assertion through PostgreSQL.
	assertionOptions, err := service.BeginWebAuthnLogin(ctx, email, meta)
	if err != nil {
		t.Fatalf("begin assertion: %v", err)
	}
	assertionBody, err := authenticator.Assert(assertionChallenge(t, assertionOptions), principal.UserID[:])
	if err != nil {
		t.Fatalf("assert: %v", err)
	}
	webauthnLogin, err := service.FinishWebAuthnLogin(ctx, email, assertionBody, meta)
	if err != nil {
		t.Fatalf("finish assertion: %v", err)
	}
	if webauthnLogin == nil || webauthnLogin.AccessToken == "" {
		t.Fatalf("assertion login issued no tokens: %+v", webauthnLogin)
	}
	assertionPrincipal, err := service.ResolveSession(ctx, webauthnLogin.AccessToken)
	if err != nil || assertionPrincipal.UserID != principal.UserID {
		t.Fatalf("resolve assertion session: %+v %v", assertionPrincipal, err)
	}
	if got := postgresSignCount(t, store, principal.UserID); got != authenticator.SignCount() || got == 0 {
		t.Fatalf("counter not persisted: got %d want %d", got, authenticator.SignCount())
	}
	if _, err := service.FinishWebAuthnLogin(ctx, email, assertionBody, meta); err == nil {
		t.Fatal("replayed assertion was accepted")
	} else if appErr, ok := identity.IsAppError(err); !ok || appErr.Code != identity.CodeAuthWebAuthn {
		t.Fatalf("replayed assertion returned unexpected error: %v", err)
	}
	if got := postgresSignCount(t, store, principal.UserID); got != authenticator.SignCount() {
		t.Fatalf("replay changed counter: got %d", got)
	}

	// Wrong origin is rejected, and the unconsumed challenge remains usable.
	wrongOptions, err := service.BeginWebAuthnLogin(ctx, email, meta)
	if err != nil {
		t.Fatalf("begin wrong-origin assertion: %v", err)
	}
	wrongChallenge := assertionChallenge(t, wrongOptions)
	wrongOrigin, err := authenticator.Assert(wrongChallenge, principal.UserID[:], webauthntest.WithOrigin("https://evil.example"))
	if err != nil {
		t.Fatalf("wrong-origin assert: %v", err)
	}
	if _, err := service.FinishWebAuthnLogin(ctx, email, wrongOrigin, meta); err == nil {
		t.Fatal("wrong origin was accepted")
	}
	recoveredBody, err := authenticator.Assert(wrongChallenge, principal.UserID[:])
	if err != nil {
		t.Fatalf("recovered assert: %v", err)
	}
	if _, err := service.FinishWebAuthnLogin(ctx, email, recoveredBody, meta); err != nil {
		t.Fatalf("recovered assertion: %v", err)
	}

	t.Run("concurrent_challenge_single_winner", func(t *testing.T) {
		raceOptions, err := service.BeginWebAuthnLogin(ctx, email, meta)
		if err != nil {
			t.Fatalf("begin race assertion: %v", err)
		}
		raceBody, err := authenticator.Assert(assertionChallenge(t, raceOptions), principal.UserID[:])
		if err != nil {
			t.Fatalf("race assert: %v", err)
		}
		const workers = 8
		results := make(chan error, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(_ int) {
				defer wg.Done()
				_, err := service.FinishWebAuthnLogin(ctx, email, raceBody,
					identity.RequestMeta{IP: netip.MustParseAddr("192.0.2.10"), UserAgent: "race", RequestID: "req-race"})
				results <- err
			}(i)
		}
		wg.Wait()
		close(results)
		successes := 0
		for err := range results {
			if err == nil {
				successes++
			}
		}
		if successes != 1 {
			t.Fatalf("concurrent assertion winners=%d", successes)
		}
		if got := postgresSignCount(t, store, principal.UserID); got != authenticator.SignCount() {
			t.Fatalf("race corrupted counter: got %d want %d", got, authenticator.SignCount())
		}
	})

	t.Run("expired_ceremony", func(t *testing.T) {
		expiryOptions, err := service.BeginWebAuthnLogin(ctx, email, meta)
		if err != nil {
			t.Fatalf("begin expiry assertion: %v", err)
		}
		expiryBody, err := authenticator.Assert(assertionChallenge(t, expiryOptions), principal.UserID[:])
		if err != nil {
			t.Fatalf("expiry assert: %v", err)
		}
		now = now.Add(cfg.WebAuthnTTL + time.Minute)
		defer func() { now = now.Add(-(cfg.WebAuthnTTL + time.Minute)) }()
		if _, err := service.FinishWebAuthnLogin(ctx, email, expiryBody, meta); err == nil {
			t.Fatal("expired challenge was accepted")
		}
	})

	t.Run("revoked_credential", func(t *testing.T) {
		revoked := credentials[0]
		revokedAt := now
		revoked.RevokedAt = &revokedAt
		if err := store.UpdateCredential(ctx, &revoked, nil); err != nil {
			t.Fatalf("revoke credential: %v", err)
		}
		if _, err := service.BeginWebAuthnLogin(ctx, email, meta); err == nil {
			t.Fatal("revoked credential started a ceremony")
		}
	})
}

func creationChallenge(t *testing.T, options *identity.WebAuthnOptions) string {
	t.Helper()
	creation, ok := options.Options.(*protocol.CredentialCreation)
	if !ok {
		t.Fatalf("unexpected creation options type %T", options.Options)
	}
	return creation.Response.Challenge.String()
}

func assertionChallenge(t *testing.T, options *identity.WebAuthnOptions) string {
	t.Helper()
	assertion, ok := options.Options.(*protocol.CredentialAssertion)
	if !ok {
		t.Fatalf("unexpected assertion options type %T", options.Options)
	}
	return assertion.Response.Challenge.String()
}

func postgresSignCount(t *testing.T, store *Store, userID uuid.UUID) uint32 {
	t.Helper()
	credentials, err := store.GetCredentials(context.Background(), userID, identity.CredentialWebAuthn)
	if err != nil || len(credentials) != 1 {
		t.Fatalf("load credentials: %d %v", len(credentials), err)
	}
	var stored struct {
		Authenticator struct {
			SignCount uint32 `json:"signCount"`
		} `json:"authenticator"`
	}
	if err := json.Unmarshal(credentials[0].Metadata, &stored); err != nil {
		t.Fatalf("decode credential: %v", err)
	}
	return stored.Authenticator.SignCount
}
