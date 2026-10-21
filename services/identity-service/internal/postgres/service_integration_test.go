package postgres

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

func TestIdentityServicePostgresEndToEnd(t *testing.T) {
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
	notifier := &identity.CaptureNotifier{}
	cfg := identity.DefaultServiceConfig()
	cfg.ExposeDevTokens = true
	service, err := identity.NewService(store, tokens, hasher, piiCipher, secretCipher, nil, notifier, pepper, randomHash(t), cfg, identity.DefaultLockoutPolicy(), "NEXORA")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	meta := identity.RequestMeta{IP: netip.MustParseAddr("192.0.2.10"), UserAgent: "integration-test", RequestID: "req-integration"}

	registered, err := service.Register(ctx, identity.RegisterInput{Email: "integration@example.test", Password: "CorrectHorseBatteryStaple9!", CountryCode: "GB"}, meta)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !registered.VerificationRequired || registered.DevVerificationToken == "" {
		t.Fatalf("verification token missing: %+v", registered)
	}
	if err := service.VerifyContact(ctx, registered.DevVerificationToken, "email", meta); err != nil {
		t.Fatalf("verify contact: %v", err)
	}
	if err := service.VerifyContact(ctx, registered.DevVerificationToken, "email", meta); err == nil {
		t.Fatal("verification token was accepted twice")
	}

	login, err := service.Login(ctx, identity.LoginInput{Email: "integration@example.test", Password: "CorrectHorseBatteryStaple9!"}, meta)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if login.Tokens == nil || login.Tokens.AccessToken == "" || login.Tokens.RefreshToken == "" {
		t.Fatalf("login tokens missing: %+v", login)
	}
	principal, err := service.ResolveSession(ctx, login.Tokens.AccessToken)
	if err != nil || principal.UserID.String() == "" || principal.SessionID != login.Tokens.SessionID {
		t.Fatalf("resolve session: %+v %v", principal, err)
	}

	rotated, err := service.Refresh(ctx, login.Tokens.RefreshToken, meta)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if rotated.RefreshToken == login.Tokens.RefreshToken || rotated.AccessToken == login.Tokens.AccessToken {
		t.Fatal("refresh did not rotate tokens")
	}
	if _, err := service.Refresh(ctx, login.Tokens.RefreshToken, meta); err == nil {
		t.Fatal("refresh token reuse was accepted")
	}
	if _, err := service.ResolveSession(ctx, rotated.AccessToken); err == nil {
		t.Fatal("session remained usable after refresh-token reuse detection")
	}

	secondLogin, err := service.Login(ctx, identity.LoginInput{Email: "integration@example.test", Password: "CorrectHorseBatteryStaple9!"}, meta)
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	secondPrincipal, err := service.ResolveSession(ctx, secondLogin.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if err := service.Logout(ctx, secondPrincipal, meta); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := service.ResolveSession(ctx, secondLogin.Tokens.AccessToken); err == nil {
		t.Fatal("access token remained usable after logout")
	}

	thirdLogin, err := service.Login(ctx, identity.LoginInput{Email: "integration@example.test", Password: "CorrectHorseBatteryStaple9!"}, meta)
	if err != nil {
		t.Fatalf("third login: %v", err)
	}
	if err := service.RequestPasswordReset(ctx, "integration@example.test", meta); err != nil {
		t.Fatalf("request reset: %v", err)
	}
	reset, ok := notifier.Find("password_reset")
	if !ok || reset.Token == "" {
		t.Fatal("password reset notification was not queued")
	}
	if err := service.ConfirmPasswordReset(ctx, reset.Token, "NewCorrectHorseBattery9!", meta); err != nil {
		t.Fatalf("confirm reset: %v", err)
	}
	if _, err := service.ResolveSession(ctx, thirdLogin.Tokens.AccessToken); err == nil {
		t.Fatal("session survived password reset")
	}
	if _, err := service.Login(ctx, identity.LoginInput{Email: "integration@example.test", Password: "CorrectHorseBatteryStaple9!"}, meta); err == nil {
		t.Fatal("old password remained valid after reset")
	}
	if _, err := service.Login(ctx, identity.LoginInput{Email: "integration@example.test", Password: "NewCorrectHorseBattery9!"}, meta); err != nil {
		t.Fatalf("new password login: %v", err)
	}

	events, err := store.ListAudit(ctx, identity.AuditFilter{Action: "auth.password.reset", Limit: 10})
	if err != nil || len(events) == 0 {
		t.Fatalf("password reset audit missing: %d %v", len(events), err)
	}
}
