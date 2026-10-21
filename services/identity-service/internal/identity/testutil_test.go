package identity

import (
	"encoding/base64"
	"encoding/hex"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type testClock struct {
	mu sync.Mutex
	at time.Time
}

func newTestClock() *testClock {
	return &testClock{at: time.Now().UTC()}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

type testHarness struct {
	service  *Service
	store    *MemoryStore
	notifier *CaptureNotifier
	clock    *testClock
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	clock := newTestClock()
	store := NewMemoryStore()
	notifier := &CaptureNotifier{}
	params := Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	pepper := []byte("test-password-pepper-0123456789abcdef")
	hasher, err := NewPasswordHasher(params, pepper)
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}
	piiCipher, err := NewAESGCMCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("pii cipher: %v", err)
	}
	secretCipher, err := NewAESGCMCipher([]byte("abcdef0123456789abcdef0123456789"))
	if err != nil {
		t.Fatalf("secret cipher: %v", err)
	}
	_, private, err := GenerateEd25519Key()
	if err != nil {
		t.Fatalf("jwt key: %v", err)
	}
	tokens, err := NewTokenManager(private, "nexora-identity-test", "nexora-api-test", 15*time.Minute, 60*time.Second)
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}
	tokens.SetClock(clock.Now)
	webAuthn, err := NewWebAuthnManager(WebAuthnConfig{
		RPDisplayName: "NEXORA Test", RPID: "localhost",
		RPOrigins: []string{"http://localhost:8082"}, Timeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("webauthn: %v", err)
	}
	cfg := DefaultServiceConfig()
	cfg.MaxSessions = 5
	cfg.MaxAPIKeys = 5
	cfg.ExposeDevTokens = true
	lockout := DefaultLockoutPolicy()
	service, err := NewService(store, tokens, hasher, piiCipher, secretCipher, webAuthn, notifier,
		[]byte("test-pii-pepper-0123456789abcdef0123"), []byte("test-ip-pepper-0123456789abcdef01234"),
		cfg, lockout, "nexora-identity-test")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	service.SetClock(clock.Now)
	return &testHarness{service: service, store: store, notifier: notifier, clock: clock}
}

//nolint:unparam // the helper intentionally mirrors the registration API.
func (h *testHarness) registerVerified(t *testing.T, email, password string) *User {
	t.Helper()
	result, err := h.service.Register(t.Context(), RegisterInput{
		Email: email, Password: password, CountryCode: "GB",
	}, RequestMeta{IP: mustAddr("192.0.2.10"), UserAgent: "service-test", RequestID: "req-register"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if result.DevVerificationToken == "" {
		t.Fatalf("expected development verification token")
	}
	if err := h.service.VerifyContact(t.Context(), result.DevVerificationToken, "email", RequestMeta{IP: mustAddr("192.0.2.10")}); err != nil {
		t.Fatalf("verify email: %v", err)
	}
	user, err := h.store.GetUserByEmailHash(t.Context(), KeyedHash(h.service.piiPepper, "email", email))
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	return user
}

func (h *testHarness) login(t *testing.T, email, password string) *LoginResult {
	t.Helper()
	result, err := h.service.Login(t.Context(), LoginInput{Email: email, Password: password},
		RequestMeta{IP: mustAddr("192.0.2.10"), UserAgent: "service-test", RequestID: "req-login"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return result
}

func mustAddr(value string) (addr netipAddr) {
	parsed, err := parseAddr(value)
	if err != nil {
		panic(err)
	}
	return parsed
}

// Small aliases keep test call sites readable without importing net/netip.
type netipAddr = netip.Addr

func parseAddr(value string) (netip.Addr, error) {
	return netip.ParseAddr(value)
}

func authMeta(ip string) RequestMeta {
	return RequestMeta{IP: mustAddr(ip), UserAgent: "service-test", RequestID: "req-api"}
}

//nolint:unparam // test helper keeps the full HMAC request shape.
func signHMAC(secret, method, path, timestamp, nonce string, body []byte) string {
	canonical := method + "\n" + path + "\n" + timestamp + "\n" + nonce + "\n" + hex.EncodeToString(SHA256(body))
	return base64.RawURLEncoding.EncodeToString(hmacSHA256([]byte(secret), []byte(canonical)))
}
