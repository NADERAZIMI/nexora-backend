package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nexora-exchange/nexora/libs/go/observability"
	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

type httpClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *httpClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *httpClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

type httpHarness struct {
	server   *httptest.Server
	service  *identity.Service
	store    *identity.MemoryStore
	notifier *identity.CaptureNotifier
	clock    *httpClock
}

func newHTTPHarness(t *testing.T, ratePerMinute, burst int) *httpHarness {
	t.Helper()
	clock := &httpClock{at: time.Now().UTC()}
	store := identity.NewMemoryStore()
	notifier := &identity.CaptureNotifier{}
	hasher, err := identity.NewPasswordHasher(identity.Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}, []byte("http-test-password-pepper-0123456789"))
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}
	piiCipher, err := identity.NewAESGCMCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("pii cipher: %v", err)
	}
	secretCipher, err := identity.NewAESGCMCipher([]byte("abcdef0123456789abcdef0123456789"))
	if err != nil {
		t.Fatalf("secret cipher: %v", err)
	}
	_, private, err := identity.GenerateEd25519Key()
	if err != nil {
		t.Fatalf("jwt key: %v", err)
	}
	tokens, err := identity.NewTokenManager(private, "nexora-identity-test", "nexora-api-test", 15*time.Minute, 90*time.Second)
	if err != nil {
		t.Fatalf("tokens: %v", err)
	}
	tokens.SetClock(clock.Now)
	webAuthn, err := identity.NewWebAuthnManager(identity.WebAuthnConfig{
		RPDisplayName: "NEXORA Test", RPID: "localhost", RPOrigins: []string{"http://localhost:8082"}, Timeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("webauthn: %v", err)
	}
	cfg := identity.DefaultServiceConfig()
	cfg.ExposeDevTokens = true
	service, err := identity.NewService(store, tokens, hasher, piiCipher, secretCipher, webAuthn, notifier,
		[]byte("http-test-pii-pepper-0123456789abcdef"), []byte("http-test-ip-pepper-0123456789abcdef12"),
		cfg, identity.DefaultLockoutPolicy(), "nexora-identity-test")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	service.SetClock(clock.Now)
	providers, err := observability.New(context.Background(), "identity-service", "test", "")
	if err != nil {
		t.Fatalf("observability: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(service, cfg, logger, "test", false, providers, ratePerMinute, burst)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &httpHarness{server: server, service: service, store: store, notifier: notifier, clock: clock}
}

type httpResponse struct {
	status int
	body   []byte
	json   map[string]any
	header http.Header
}

func (h *httpHarness) do(t *testing.T, method, path string, payload any, headers map[string]string) httpResponse {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, h.server.URL+path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := h.server.Client().Do(request)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	result := httpResponse{status: response.StatusCode, body: raw, header: response.Header}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result.json)
	}
	return result
}

func (h *httpHarness) raw(t *testing.T, method, path, body string, headers map[string]string) httpResponse {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, h.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new raw request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := h.server.Client().Do(request)
	if err != nil {
		t.Fatalf("raw request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, _ := io.ReadAll(response.Body)
	result := httpResponse{status: response.StatusCode, body: raw, header: response.Header}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result.json)
	}
	return result
}

//nolint:unparam // the helper intentionally mirrors the registration API.
func (h *httpHarness) registerAndVerify(t *testing.T, email, password string) {
	t.Helper()
	registered := h.do(t, http.MethodPost, "/v1/auth/register", map[string]any{
		"email": email, "password": password, "countryCode": "GB",
	}, nil)
	if registered.status != http.StatusAccepted {
		t.Fatalf("register status %d: %s", registered.status, registered.body)
	}
	token, _ := registered.json["devVerificationToken"].(string)
	if token == "" {
		t.Fatalf("dev verification token missing: %s", registered.body)
	}
	verified := h.do(t, http.MethodPost, "/v1/auth/verify/email", map[string]any{"token": token}, nil)
	if verified.status != http.StatusNoContent {
		t.Fatalf("verify status %d: %s", verified.status, verified.body)
	}
}

func (h *httpHarness) login(t *testing.T, email, password string) httpResponse {
	t.Helper()
	response := h.do(t, http.MethodPost, "/v1/auth/login", map[string]any{"email": email, "password": password}, nil)
	if response.status != http.StatusOK {
		t.Fatalf("login status %d: %s", response.status, response.body)
	}
	return response
}

func jsonString(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	value, ok := object[key].(string)
	if !ok {
		t.Fatalf("response field %q is not a string: %#v", key, object)
	}
	return value
}

func errorCode(t *testing.T, response httpResponse) string {
	t.Helper()
	errorObject, ok := response.json["error"].(map[string]any)
	if !ok {
		t.Fatalf("error envelope missing: status=%d body=%s", response.status, response.body)
	}
	return jsonString(t, errorObject, "code")
}

func bearer(access string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + access}
}

func TestHTTPRegistrationLoginSessionRefreshLogout(t *testing.T) {
	h := newHTTPHarness(t, 100000, 1000)
	const email = "http-user@example.com"
	const password = "Correct-Horse-Battery-9!"
	h.registerAndVerify(t, email, password)

	login := h.login(t, email, password)
	access := jsonString(t, login.json, "accessToken")
	refresh := jsonString(t, login.json, "refreshToken")
	sessionID := jsonString(t, login.json, "sessionId")
	if access == "" || refresh == "" || sessionID == "" {
		t.Fatalf("token response incomplete: %s", login.body)
	}
	if login.header.Get("Cache-Control") != "no-store" || login.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers missing: %v", login.header)
	}

	me := h.do(t, http.MethodGet, "/v1/me", nil, bearer(access))
	if me.status != http.StatusOK {
		t.Fatalf("me status %d: %s", me.status, me.body)
	}
	if me.json["kind"] != "session" || me.json["userId"] == "" {
		t.Fatalf("unexpected me response: %s", me.body)
	}
	current := h.do(t, http.MethodGet, "/v1/auth/session", nil, bearer(access))
	if current.status != http.StatusOK {
		t.Fatalf("session status %d: %s", current.status, current.body)
	}
	status := h.do(t, http.MethodGet, "/v1/auth/security-status", nil, bearer(access))
	if status.status != http.StatusOK {
		t.Fatalf("security status %d: %s", status.status, status.body)
	}
	sessions := h.do(t, http.MethodGet, "/v1/auth/sessions", nil, bearer(access))
	if sessions.status != http.StatusOK {
		t.Fatalf("sessions status %d: %s", sessions.status, sessions.body)
	}
	if list, ok := sessions.json["sessions"].([]any); !ok || len(list) != 1 {
		t.Fatalf("unexpected sessions: %s", sessions.body)
	}

	rotated := h.do(t, http.MethodPost, "/v1/auth/refresh", map[string]any{"refreshToken": refresh}, nil)
	if rotated.status != http.StatusOK {
		t.Fatalf("refresh status %d: %s", rotated.status, rotated.body)
	}
	newAccess := jsonString(t, rotated.json, "accessToken")

	reuse := h.do(t, http.MethodPost, "/v1/auth/refresh", map[string]any{"refreshToken": refresh}, nil)
	if reuse.status != http.StatusUnauthorized || errorCode(t, reuse) != identity.CodeAuthRefreshReused {
		t.Fatalf("refresh reuse response %d: %s", reuse.status, reuse.body)
	}
	revoked := h.do(t, http.MethodGet, "/v1/me", nil, bearer(newAccess))
	if revoked.status != http.StatusUnauthorized || errorCode(t, revoked) != identity.CodeAuthSessionRevoked {
		t.Fatalf("reuse did not revoke session: %d %s", revoked.status, revoked.body)
	}

	second := h.login(t, email, password)
	secondAccess := jsonString(t, second.json, "accessToken")
	logout := h.do(t, http.MethodPost, "/v1/auth/logout", nil, bearer(secondAccess))
	if logout.status != http.StatusNoContent {
		t.Fatalf("logout status %d: %s", logout.status, logout.body)
	}
	afterLogout := h.do(t, http.MethodGet, "/v1/me", nil, bearer(secondAccess))
	if afterLogout.status != http.StatusUnauthorized {
		t.Fatalf("access token accepted after logout: %d", afterLogout.status)
	}

	anonymous := h.do(t, http.MethodGet, "/v1/me", nil, nil)
	if anonymous.status != http.StatusUnauthorized || errorCode(t, anonymous) != identity.CodeAuthMissing {
		t.Fatalf("anonymous me response %d: %s", anonymous.status, anonymous.body)
	}
	invalid := h.do(t, http.MethodPost, "/v1/auth/login", map[string]any{"email": email, "password": "Wrong-Horse-Battery-9!"}, nil)
	if invalid.status != http.StatusUnauthorized || errorCode(t, invalid) != identity.CodeAuthInvalidCredentials {
		t.Fatalf("invalid login response %d: %s", invalid.status, invalid.body)
	}
	unknown := h.do(t, http.MethodPost, "/v1/auth/login", map[string]any{"email": "missing@example.com", "password": password}, nil)
	if unknown.status != http.StatusUnauthorized || errorCode(t, unknown) != identity.CodeAuthInvalidCredentials {
		t.Fatalf("unknown login response %d: %s", unknown.status, unknown.body)
	}
}

func TestHTTPRegistrationEnumerationAndJSONValidation(t *testing.T) {
	h := newHTTPHarness(t, 100000, 1000)
	email := "enum@example.com"
	password := "Correct-Horse-Battery-9!"
	h.registerAndVerify(t, email, password)

	duplicate := h.do(t, http.MethodPost, "/v1/auth/register", map[string]any{"email": strings.ToUpper(email), "password": password}, nil)
	if duplicate.status != http.StatusAccepted {
		t.Fatalf("duplicate status %d: %s", duplicate.status, duplicate.body)
	}
	if duplicate.json["verificationRequired"] != true {
		t.Fatalf("duplicate response leaked state: %s", duplicate.body)
	}
	if duplicate.json["devVerificationToken"] != "" && duplicate.json["devVerificationToken"] != nil {
		t.Fatalf("duplicate response returned a verification token: %s", duplicate.body)
	}

	malformed := h.raw(t, http.MethodPost, "/v1/auth/login", `{"email":"a@example.com","password":`, nil)
	if malformed.status != http.StatusBadRequest || errorCode(t, malformed) != identity.CodeValidation {
		t.Fatalf("malformed JSON response %d: %s", malformed.status, malformed.body)
	}
	unknownField := h.raw(t, http.MethodPost, "/v1/auth/login", `{"email":"a@example.com","password":"Correct-Horse-Battery-9!","admin":true}`, nil)
	if unknownField.status != http.StatusBadRequest || errorCode(t, unknownField) != identity.CodeValidation {
		t.Fatalf("unknown field response %d: %s", unknownField.status, unknownField.body)
	}
	trailing := h.raw(t, http.MethodPost, "/v1/auth/login", `{"email":"a@example.com","password":"Correct-Horse-Battery-9!"}{"x":1}`, nil)
	if trailing.status != http.StatusBadRequest {
		t.Fatalf("trailing JSON accepted: %d %s", trailing.status, trailing.body)
	}
	if strings.Contains(string(malformed.body), "goroutine") || strings.Contains(string(malformed.body), "sql") {
		t.Fatalf("internal details leaked: %s", malformed.body)
	}

	invalidPath := h.do(t, http.MethodDelete, "/v1/auth/sessions/not-a-uuid", nil, nil)
	if invalidPath.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated invalid path response %d", invalidPath.status)
	}
}

func TestHTTPMFAStepUpAndAdminControls(t *testing.T) {
	h := newHTTPHarness(t, 100000, 1000)
	adminID, err := h.service.BootstrapAdmin(t.Context(), "admin@example.com", "Root-Correct-Horse-9!", identity.RequestMeta{})
	if err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	adminLogin := h.login(t, "admin@example.com", "Root-Correct-Horse-9!")
	adminAccess := jsonString(t, adminLogin.json, "accessToken")
	adminPrincipal, err := h.service.ResolveSession(t.Context(), adminAccess)
	if err != nil {
		t.Fatalf("admin principal: %v", err)
	}

	h.registerAndVerify(t, "customer@example.com", "Correct-Horse-Battery-9!")
	customerLogin := h.login(t, "customer@example.com", "Correct-Horse-Battery-9!")
	customerAccess := jsonString(t, customerLogin.json, "accessToken")
	customerPrincipal, err := h.service.ResolveSession(t.Context(), customerAccess)
	if err != nil {
		t.Fatalf("customer principal: %v", err)
	}

	customerView := h.do(t, http.MethodGet, "/v1/admin/users/"+adminID.String(), nil, bearer(customerAccess))
	if customerView.status != http.StatusForbidden || errorCode(t, customerView) != identity.CodeAuthForbidden {
		t.Fatalf("customer admin access response %d: %s", customerView.status, customerView.body)
	}
	adminView := h.do(t, http.MethodGet, "/v1/admin/users/"+adminID.String(), nil, bearer(adminAccess))
	if adminView.status != http.StatusOK {
		t.Fatalf("admin self read response %d: %s", adminView.status, adminView.body)
	}

	// Privileged mutation requires a step-up token. The admin first enrolls TOTP.
	enrollment, err := h.service.BeginTOTP(t.Context(), adminPrincipal, identity.RequestMeta{})
	if err != nil {
		t.Fatalf("begin totp: %v", err)
	}
	code, err := identity.TOTPCode(enrollment.Secret, h.clock.Now().Unix(), identity.TOTPDigits, identity.TOTPPeriod)
	if err != nil {
		t.Fatalf("totp code: %v", err)
	}
	if err := h.service.ConfirmTOTP(t.Context(), adminPrincipal, enrollment.CredentialID, code, identity.RequestMeta{}); err != nil {
		t.Fatalf("confirm totp: %v", err)
	}
	h.clock.Advance(30 * time.Second)
	stepCode, _ := identity.TOTPCode(enrollment.Secret, h.clock.Now().Unix(), identity.TOTPDigits, identity.TOTPPeriod)

	missingStepUp := h.do(t, http.MethodPost, "/v1/admin/users/"+adminID.String()+"/freeze", nil, bearer(adminAccess))
	if missingStepUp.status != http.StatusForbidden || errorCode(t, missingStepUp) != identity.CodeAuthStepUpRequired {
		t.Fatalf("missing step-up response %d: %s", missingStepUp.status, missingStepUp.body)
	}

	stepUp := h.do(t, http.MethodPost, "/v1/auth/step-up", map[string]any{
		"action": "admin:user:freeze", "totpCode": stepCode,
	}, bearer(adminAccess))
	if stepUp.status != http.StatusOK {
		t.Fatalf("step-up response %d: %s", stepUp.status, stepUp.body)
	}
	stepToken := jsonString(t, stepUp.json, "stepUpToken")
	payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(stepToken, ".")[1])
	if _, err := h.service.TokenManager().ParseStepUp(stepToken, "admin:user:freeze", adminPrincipal.UserID, adminPrincipal.SessionID); err != nil {
		t.Fatalf("issued step-up token did not parse: %v token=%s payload=%s", err, stepToken, payload)
	}
	if err := h.service.ResolveStepUp(stepToken, "admin:user:freeze", adminPrincipal); err != nil {
		t.Fatalf("issued step-up token did not resolve: %v (principal=%+v)", err, adminPrincipal)
	}

	// Freezing another admin requires a dedicated break-glass path; freezing a
	// customer should succeed and revoke the customer's live session.
	customerID := customerPrincipal.UserID
	frozen := h.do(t, http.MethodPost, "/v1/admin/users/"+customerID.String()+"/freeze", nil, map[string]string{
		"Authorization": "Bearer " + adminAccess, "X-Step-Up-Token": stepToken,
	})
	if frozen.status != http.StatusNoContent {
		t.Fatalf("freeze response %d: %s", frozen.status, frozen.body)
	}
	afterFreeze := h.do(t, http.MethodGet, "/v1/me", nil, bearer(customerAccess))
	if afterFreeze.status != http.StatusUnauthorized {
		t.Fatalf("customer token survived freeze: %d %s", afterFreeze.status, afterFreeze.body)
	}

	// A step-up token is action-scoped.
	wrongAction := h.do(t, http.MethodPost, "/v1/admin/users/"+customerID.String()+"/unfreeze", nil, map[string]string{
		"Authorization": "Bearer " + adminAccess, "X-Step-Up-Token": stepToken,
	})
	if wrongAction.status != http.StatusForbidden || errorCode(t, wrongAction) != identity.CodeAuthStepUpRequired {
		t.Fatalf("wrong-action step-up response %d: %s", wrongAction.status, wrongAction.body)
	}
}

func TestHTTPAPIKeySignatureScopeIPAndReplay(t *testing.T) {
	h := newHTTPHarness(t, 100000, 1000)
	h.registerAndVerify(t, "keys-http@example.com", "Correct-Horse-Battery-9!")
	login := h.login(t, "keys-http@example.com", "Correct-Horse-Battery-9!")
	access := jsonString(t, login.json, "accessToken")
	principal, err := h.service.ResolveSession(t.Context(), access)
	if err != nil {
		t.Fatalf("principal: %v", err)
	}
	readKey, err := h.service.CreateAPIKey(t.Context(), principal, identity.CreateAPIKeyInput{Label: "http-read", Scopes: []string{"read"}}, identity.RequestMeta{})
	if err != nil {
		t.Fatalf("create read key: %v", err)
	}
	tradeKey, err := h.service.CreateAPIKey(t.Context(), principal, identity.CreateAPIKeyInput{Label: "http-trade", Scopes: []string{"trade"}}, identity.RequestMeta{})
	if err != nil {
		t.Fatalf("create trade key: %v", err)
	}
	ipKey, err := h.service.CreateAPIKey(t.Context(), principal, identity.CreateAPIKeyInput{
		Label: "http-ip", Scopes: []string{"read"}, IPAllowlist: []string{"192.0.2.0/24"},
	}, identity.RequestMeta{})
	if err != nil {
		t.Fatalf("create ip key: %v", err)
	}

	timestamp := strconv.FormatInt(h.clock.Now().Unix(), 10)
	nonce := "nonce0123456789abcdef01"
	signature := httpSign(readKey.Secret, "GET", "/v1/me", timestamp, nonce, nil)
	signed := h.do(t, http.MethodGet, "/v1/me", nil, map[string]string{
		"X-Nexora-Key-Id": readKey.ID.String(), "X-Nexora-Timestamp": timestamp,
		"X-Nexora-Nonce": nonce, "X-Nexora-Signature": signature,
	})
	if signed.status != http.StatusOK || signed.json["kind"] != "api_key" {
		t.Fatalf("signed request response %d: %s", signed.status, signed.body)
	}
	replay := h.do(t, http.MethodGet, "/v1/me", nil, map[string]string{
		"X-Nexora-Key-Id": readKey.ID.String(), "X-Nexora-Timestamp": timestamp,
		"X-Nexora-Nonce": nonce, "X-Nexora-Signature": signature,
	})
	if replay.status != http.StatusUnauthorized || errorCode(t, replay) != identity.CodeAuthInvalidSignature {
		t.Fatalf("replay response %d: %s", replay.status, replay.body)
	}

	tradeNonce := "nonce0123456789abcdef02"
	tradeSig := httpSign(tradeKey.Secret, "GET", "/v1/me", timestamp, tradeNonce, nil)
	scope := h.do(t, http.MethodGet, "/v1/me", nil, map[string]string{
		"X-Nexora-Key-Id": tradeKey.ID.String(), "X-Nexora-Timestamp": timestamp,
		"X-Nexora-Nonce": tradeNonce, "X-Nexora-Signature": tradeSig,
	})
	if scope.status != http.StatusForbidden || errorCode(t, scope) != identity.CodeAuthScope {
		t.Fatalf("scope response %d: %s", scope.status, scope.body)
	}

	ipNonce := "nonce0123456789abcdef03"
	ipSig := httpSign(ipKey.Secret, "GET", "/v1/me", timestamp, ipNonce, nil)
	ipDenied := h.do(t, http.MethodGet, "/v1/me", nil, map[string]string{
		"X-Nexora-Key-Id": ipKey.ID.String(), "X-Nexora-Timestamp": timestamp,
		"X-Nexora-Nonce": ipNonce, "X-Nexora-Signature": ipSig,
	})
	if ipDenied.status != http.StatusForbidden || errorCode(t, ipDenied) != identity.CodeAuthIPNotAllowed {
		t.Fatalf("IP allowlist response %d: %s", ipDenied.status, ipDenied.body)
	}

	// A bad API key signature must never fall back to a valid bearer token.
	fallback := h.do(t, http.MethodGet, "/v1/me", nil, map[string]string{
		"Authorization":   "Bearer " + access,
		"X-Nexora-Key-Id": readKey.ID.String(), "X-Nexora-Timestamp": timestamp,
		"X-Nexora-Nonce": "nonce0123456789abcdef04", "X-Nexora-Signature": "not-a-signature",
	})
	if fallback.status != http.StatusUnauthorized {
		t.Fatalf("bad API key fell back to bearer: %d %s", fallback.status, fallback.body)
	}
}

func TestHTTPIDORAndSessionOwnership(t *testing.T) {
	h := newHTTPHarness(t, 100000, 1000)
	h.registerAndVerify(t, "idor-one@example.com", "Correct-Horse-Battery-9!")
	h.registerAndVerify(t, "idor-two@example.com", "Correct-Horse-Battery-9!")
	one := h.login(t, "idor-one@example.com", "Correct-Horse-Battery-9!")
	two := h.login(t, "idor-two@example.com", "Correct-Horse-Battery-9!")
	oneAccess := jsonString(t, one.json, "accessToken")
	twoAccess := jsonString(t, two.json, "accessToken")
	twoSession := jsonString(t, two.json, "sessionId")

	attempt := h.do(t, http.MethodDelete, "/v1/auth/sessions/"+twoSession, nil, bearer(oneAccess))
	if attempt.status != http.StatusNotFound {
		t.Fatalf("cross-account session revoke response %d: %s", attempt.status, attempt.body)
	}
	victim := h.do(t, http.MethodGet, "/v1/me", nil, bearer(twoAccess))
	if victim.status != http.StatusOK {
		t.Fatalf("victim session was revoked: %d %s", victim.status, victim.body)
	}
	own := h.do(t, http.MethodDelete, "/v1/auth/sessions/"+jsonString(t, one.json, "sessionId"), nil, bearer(oneAccess))
	if own.status != http.StatusNoContent {
		t.Fatalf("own session revoke response %d: %s", own.status, own.body)
	}
	if after := h.do(t, http.MethodGet, "/v1/me", nil, bearer(oneAccess)); after.status != http.StatusUnauthorized {
		t.Fatalf("own revoked session still valid: %d", after.status)
	}
}

func TestHTTPPasswordResetAndBearerRevocation(t *testing.T) {
	h := newHTTPHarness(t, 100000, 1000)
	h.registerAndVerify(t, "http-reset@example.com", "Correct-Horse-Battery-9!")
	login := h.login(t, "http-reset@example.com", "Correct-Horse-Battery-9!")
	access := jsonString(t, login.json, "accessToken")

	request := h.do(t, http.MethodPost, "/v1/auth/password/reset/request", map[string]any{"email": "http-reset@example.com"}, nil)
	if request.status != http.StatusAccepted {
		t.Fatalf("reset request status %d: %s", request.status, request.body)
	}
	unknown := h.do(t, http.MethodPost, "/v1/auth/password/reset/request", map[string]any{"email": "nobody-http@example.com"}, nil)
	if unknown.status != http.StatusAccepted {
		t.Fatalf("unknown reset request status %d: %s", unknown.status, unknown.body)
	}
	notification, ok := h.notifier.Find("password_reset")
	if !ok || notification.Token == "" {
		t.Fatalf("reset notification missing")
	}
	confirm := h.do(t, http.MethodPost, "/v1/auth/password/reset/confirm", map[string]any{
		"token": notification.Token, "newPassword": "New-Correct-Horse-42!",
	}, nil)
	if confirm.status != http.StatusNoContent {
		t.Fatalf("reset confirm status %d: %s", confirm.status, confirm.body)
	}
	if after := h.do(t, http.MethodGet, "/v1/me", nil, bearer(access)); after.status != http.StatusUnauthorized {
		t.Fatalf("session survived reset: %d", after.status)
	}
	old := h.do(t, http.MethodPost, "/v1/auth/login", map[string]any{"email": "http-reset@example.com", "password": "Correct-Horse-Battery-9!"}, nil)
	if old.status != http.StatusUnauthorized {
		t.Fatalf("old password accepted after reset: %d", old.status)
	}
	h.login(t, "http-reset@example.com", "New-Correct-Horse-42!")
}

func TestHTTPRateLimitAndHealth(t *testing.T) {
	h := newHTTPHarness(t, 1, 1)
	first := h.do(t, http.MethodGet, "/healthz", nil, nil)
	if first.status != http.StatusOK {
		t.Fatalf("health status %d", first.status)
	}
	second := h.do(t, http.MethodGet, "/healthz", nil, nil)
	if second.status != http.StatusTooManyRequests || errorCode(t, second) != identity.CodeAuthRateLimited {
		t.Fatalf("rate limit response %d: %s", second.status, second.body)
	}
	if second.header.Get("Retry-After") == "" {
		t.Fatal("rate limit did not include Retry-After")
	}
	ready := h.do(t, http.MethodGet, "/healthz", nil, nil)
	if ready.status != http.StatusTooManyRequests {
		t.Fatalf("limiter did not hold")
	}
}

func TestHTTPJWKSAndOpenIDConfiguration(t *testing.T) {
	h := newHTTPHarness(t, 100000, 1000)
	jwks := h.do(t, http.MethodGet, "/v1/.well-known/jwks.json", nil, nil)
	if jwks.status != http.StatusOK {
		t.Fatalf("jwks status %d", jwks.status)
	}
	keys, ok := jwks.json["keys"].([]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("unexpected JWKS: %s", jwks.body)
	}
	key := keys[0].(map[string]any)
	if key["kty"] != "OKP" || key["crv"] != "Ed25519" || key["alg"] != "EdDSA" {
		t.Fatalf("unexpected JWK: %s", jwks.body)
	}
	if _, ok := key["d"]; ok {
		t.Fatal("JWKS leaked private key material")
	}
	configuration := h.do(t, http.MethodGet, "/v1/.well-known/openid-configuration", nil, nil)
	if configuration.status != http.StatusOK || configuration.json["issuer"] != "nexora-identity" {
		t.Fatalf("openid configuration: %d %s", configuration.status, configuration.body)
	}
}

func httpSign(secret, method, path, timestamp, nonce string, body []byte) string {
	canonical := method + "\n" + path + "\n" + timestamp + "\n" + nonce + "\n" + hex.EncodeToString(httpSHA256(body))
	return base64.RawURLEncoding.EncodeToString(httpHMAC([]byte(secret), []byte(canonical)))
}

func httpSHA256(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}

func httpHMAC(secret, message []byte) []byte {
	hash := hmac.New(sha256.New, secret)
	_, _ = hash.Write(message)
	return hash.Sum(nil)
}
