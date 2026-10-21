package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func requireAppCode(t *testing.T, err error, code string) *AppError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", code)
	}
	appErr, ok := IsAppError(err)
	if !ok {
		t.Fatalf("expected application error %s, got %T: %v", code, err, err)
	}
	if appErr.Code != code {
		t.Fatalf("expected error %s, got %s (%v)", code, appErr.Code, err)
	}
	return appErr
}

func TestRegistrationVerificationAndUserEnumeration(t *testing.T) {
	h := newTestHarness(t)
	email := "alice@example.com"
	const password = "Correct-Horse-Battery-9!"

	result, err := h.service.Register(t.Context(), RegisterInput{Email: email, Password: password, CountryCode: "GB"}, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !result.VerificationRequired || result.DevVerificationToken == "" {
		t.Fatalf("unexpected registration result: %+v", result)
	}
	user, err := h.store.GetUserByEmailHash(t.Context(), KeyedHash(h.service.piiPepper, "email", email))
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.Status != UserPending {
		t.Fatalf("new user status = %s, want pending", user.Status)
	}
	if strings.Contains(user.PasswordHash, password) || user.PasswordHash == "" {
		t.Fatal("password was stored in plaintext")
	}
	if len(user.EmailCiphertext) == 0 || strings.Contains(string(user.EmailCiphertext), email) {
		t.Fatal("email was stored without encryption")
	}

	duplicate, err := h.service.Register(t.Context(), RegisterInput{Email: strings.ToUpper(email), Password: password}, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("duplicate registration: %v", err)
	}
	if !duplicate.VerificationRequired || duplicate.DevVerificationToken != "" {
		t.Fatalf("duplicate registration leaked account state: %+v", duplicate)
	}

	if _, err := h.service.Login(t.Context(), LoginInput{Email: email, Password: password}, authMeta("192.0.2.10")); err == nil {
		t.Fatal("pending account logged in before verification")
	} else {
		_ = requireAppCode(t, err, CodeAuthEmailUnverified)
	}
	if err := h.service.VerifyContact(t.Context(), "not-the-token", "email", authMeta("192.0.2.10")); err == nil {
		t.Fatal("invalid verification token accepted")
	}
	if err := h.service.VerifyContact(t.Context(), result.DevVerificationToken, "email", authMeta("192.0.2.10")); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := h.service.VerifyContact(t.Context(), result.DevVerificationToken, "email", authMeta("192.0.2.10")); err == nil {
		t.Fatal("verification token was reusable")
	}

	unknownErr := func() error {
		_, err := h.service.Login(t.Context(), LoginInput{Email: "nobody@example.com", Password: password}, authMeta("192.0.2.10"))
		return err
	}()
	wrongErr := func() error {
		_, err := h.service.Login(t.Context(), LoginInput{Email: email, Password: "Wrong-Horse-Battery-9!"}, authMeta("192.0.2.10"))
		return err
	}()
	_ = requireAppCode(t, unknownErr, CodeAuthInvalidCredentials)
	_ = requireAppCode(t, wrongErr, CodeAuthInvalidCredentials)
	if unknownErr.Error() != wrongErr.Error() {
		t.Fatalf("user enumeration through error messages: %q != %q", unknownErr, wrongErr)
	}

	login := h.login(t, email, password)
	if login.Tokens == nil || login.Tokens.AccessToken == "" || login.Tokens.RefreshToken == "" {
		t.Fatalf("login did not return tokens: %+v", login)
	}
	principal, err := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	if principal.UserID != user.ID || principal.Kind != "session" || !principal.HasRole(RoleCustomer) {
		t.Fatalf("unexpected principal: %+v", principal)
	}
	sessions, err := h.service.ListSessions(t.Context(), principal)
	if err != nil || len(sessions) != 1 || !sessions[0].Current {
		t.Fatalf("list sessions: %+v %v", sessions, err)
	}
}

func TestRefreshRotationReuseDetectionAndLogout(t *testing.T) {
	h := newTestHarness(t)
	user := h.registerVerified(t, "rotate@example.com", "Correct-Horse-Battery-9!")
	login := h.login(t, "rotate@example.com", "Correct-Horse-Battery-9!")

	rotated, err := h.service.Refresh(t.Context(), login.Tokens.RefreshToken, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if rotated.SessionID != login.Tokens.SessionID || rotated.RefreshToken == login.Tokens.RefreshToken {
		t.Fatal("refresh did not rotate within the same session")
	}
	if _, err := h.service.ResolveSession(t.Context(), rotated.AccessToken); err != nil {
		t.Fatalf("rotated access token rejected: %v", err)
	}

	_, err = h.service.Refresh(t.Context(), login.Tokens.RefreshToken, authMeta("192.0.2.10"))
	_ = requireAppCode(t, err, CodeAuthRefreshReused)
	_, err = h.service.ResolveSession(t.Context(), rotated.AccessToken)
	_ = requireAppCode(t, err, CodeAuthSessionRevoked)
	if _, err := h.service.Refresh(t.Context(), rotated.RefreshToken, authMeta("192.0.2.10")); err == nil {
		t.Fatal("refresh token in a revoked family was accepted")
	}

	// A fresh login can be explicitly revoked.
	second := h.login(t, "rotate@example.com", "Correct-Horse-Battery-9!")
	principal, err := h.service.ResolveSession(t.Context(), second.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("resolve second session: %v", err)
	}
	if err := h.service.Logout(t.Context(), principal, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := h.service.ResolveSession(t.Context(), second.Tokens.AccessToken); err == nil {
		t.Fatal("access token accepted after logout")
	}
	if principal.UserID != user.ID {
		t.Fatal("wrong principal user")
	}
}

func TestAccountLockoutExponentialBackoffAndRecovery(t *testing.T) {
	h := newTestHarness(t)
	h.service.lockout = LockoutPolicy{
		BackoffAfter: 2, LockAfter: 4,
		BaseBackoff: time.Second, MaxBackoff: 4 * time.Second,
		LockDuration: 30 * time.Second, MaxLockDuration: time.Minute,
	}
	h.registerVerified(t, "lockout@example.com", "Correct-Horse-Battery-9!")

	for i := 0; i < 2; i++ {
		_, err := h.service.Login(t.Context(), LoginInput{Email: "lockout@example.com", Password: "Wrong-Horse-Battery-9!"}, authMeta("192.0.2.10"))
		_ = requireAppCode(t, err, CodeAuthInvalidCredentials)
	}
	// The third attempt is denied by exponential backoff before password work.
	appErr := requireAppCode(t, func() error {
		_, err := h.service.Login(t.Context(), LoginInput{Email: "lockout@example.com", Password: "Correct-Horse-Battery-9!"}, authMeta("192.0.2.10"))
		return err
	}(), CodeAuthAccountLocked)
	if appErr.RetryAfter <= 0 {
		t.Fatal("backoff response did not include retry-after")
	}
	h.clock.Advance(2 * time.Second)
	_, err := h.service.Login(t.Context(), LoginInput{Email: "lockout@example.com", Password: "Wrong-Horse-Battery-9!"}, authMeta("192.0.2.10"))
	_ = requireAppCode(t, err, CodeAuthInvalidCredentials)
	h.clock.Advance(3 * time.Second)
	_, err = h.service.Login(t.Context(), LoginInput{Email: "lockout@example.com", Password: "Wrong-Horse-Battery-9!"}, authMeta("192.0.2.10"))
	_ = requireAppCode(t, err, CodeAuthInvalidCredentials)
	appErr = requireAppCode(t, func() error {
		_, err := h.service.Login(t.Context(), LoginInput{Email: "lockout@example.com", Password: "Correct-Horse-Battery-9!"}, authMeta("192.0.2.10"))
		return err
	}(), CodeAuthAccountLocked)
	if appErr.RetryAfter <= 0 {
		t.Fatal("lockout response did not include retry-after")
	}
	h.clock.Advance(31 * time.Second)
	if _, err := h.service.Login(t.Context(), LoginInput{Email: "lockout@example.com", Password: "Correct-Horse-Battery-9!"}, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("login after lockout expiry failed: %v", err)
	}
}

func TestTOTPEnrollmentMFAAndReplay(t *testing.T) {
	h := newTestHarness(t)
	h.registerVerified(t, "mfa@example.com", "Correct-Horse-Battery-9!")
	login := h.login(t, "mfa@example.com", "Correct-Horse-Battery-9!")
	principal, err := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	enrollment, err := h.service.BeginTOTP(t.Context(), principal, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin totp: %v", err)
	}
	if enrollment.Secret == "" || !strings.HasPrefix(enrollment.ProvisioningURI, "otpauth://totp/") {
		t.Fatalf("bad enrollment: %+v", enrollment)
	}
	code, err := TOTPCode(enrollment.Secret, h.clock.Now().Unix(), TOTPDigits, TOTPPeriod)
	if err != nil {
		t.Fatalf("totp code: %v", err)
	}
	if err := h.service.ConfirmTOTP(t.Context(), principal, enrollment.CredentialID, code, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("confirm totp: %v", err)
	}
	if err := h.service.ConfirmTOTP(t.Context(), principal, enrollment.CredentialID, code, authMeta("192.0.2.10")); err == nil {
		t.Fatal("confirmed TOTP credential was confirmed twice")
	}

	challenge := h.login(t, "mfa@example.com", "Correct-Horse-Battery-9!")
	if !challenge.MFARequired || challenge.MFAToken == "" || challenge.Tokens != nil {
		t.Fatalf("expected MFA challenge, got %+v", challenge)
	}
	hasTOTP := false
	for _, method := range challenge.Methods {
		if method == "totp" {
			hasTOTP = true
		}
	}
	if !hasTOTP {
		t.Fatalf("TOTP method missing: %v", challenge.Methods)
	}
	h.clock.Advance(30 * time.Second)
	code, _ = TOTPCode(enrollment.Secret, h.clock.Now().Unix(), TOTPDigits, TOTPPeriod)
	completed, err := h.service.VerifyMFA(t.Context(), challenge.MFAToken, code, "", authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("verify mfa: %v", err)
	}
	if completed.Tokens == nil || !completed.Tokens.MFA {
		t.Fatalf("MFA login did not produce an MFA session: %+v", completed)
	}
	if _, err := h.service.VerifyMFA(t.Context(), challenge.MFAToken, code, "", authMeta("192.0.2.10")); err == nil {
		t.Fatal("MFA challenge was reusable")
	}

	replay := h.login(t, "mfa@example.com", "Correct-Horse-Battery-9!")
	_, err = h.service.VerifyMFA(t.Context(), replay.MFAToken, code, "", authMeta("192.0.2.10"))
	_ = requireAppCode(t, err, CodeAuthMFAReplay)
}

func TestMFAFailuresHonorBackoffAndLockout(t *testing.T) {
	h := newTestHarness(t)
	h.service.lockout = LockoutPolicy{
		BackoffAfter: 1, LockAfter: 2,
		BaseBackoff: time.Second, MaxBackoff: 4 * time.Second,
		LockDuration: 30 * time.Second, MaxLockDuration: time.Minute,
	}
	h.registerVerified(t, "mfa-lock@example.com", "Correct-Horse-Battery-9!")
	login := h.login(t, "mfa-lock@example.com", "Correct-Horse-Battery-9!")
	principal, _ := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	enrollment, err := h.service.BeginTOTP(t.Context(), principal, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin totp: %v", err)
	}
	code, _ := TOTPCode(enrollment.Secret, h.clock.Now().Unix(), TOTPDigits, TOTPPeriod)
	if err := h.service.ConfirmTOTP(t.Context(), principal, enrollment.CredentialID, code, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("confirm totp: %v", err)
	}
	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}
	challenge := h.login(t, "mfa-lock@example.com", "Correct-Horse-Battery-9!")
	if _, err := h.service.VerifyMFA(t.Context(), challenge.MFAToken, wrong, "", authMeta("192.0.2.10")); err == nil {
		t.Fatal("wrong TOTP was accepted")
	}
	_, err = h.service.Login(t.Context(), LoginInput{Email: "mfa-lock@example.com", Password: "Correct-Horse-Battery-9!"}, authMeta("192.0.2.10"))
	_ = requireAppCode(t, err, CodeAuthAccountLocked)

	h.clock.Advance(2 * time.Second)
	challenge = h.login(t, "mfa-lock@example.com", "Correct-Horse-Battery-9!")
	if _, err := h.service.VerifyMFA(t.Context(), challenge.MFAToken, wrong, "", authMeta("192.0.2.10")); err == nil {
		t.Fatal("wrong TOTP was accepted")
	}
	_, err = h.service.Login(t.Context(), LoginInput{Email: "mfa-lock@example.com", Password: "Correct-Horse-Battery-9!"}, authMeta("192.0.2.10"))
	_ = requireAppCode(t, err, CodeAuthAccountLocked)
}

func TestRecoveryCodesAreSingleUseAndRevocable(t *testing.T) {
	h := newTestHarness(t)
	h.registerVerified(t, "recovery@example.com", "Correct-Horse-Battery-9!")
	login := h.login(t, "recovery@example.com", "Correct-Horse-Battery-9!")
	principal, _ := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	enrollment, err := h.service.BeginTOTP(t.Context(), principal, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	code, _ := TOTPCode(enrollment.Secret, h.clock.Now().Unix(), TOTPDigits, TOTPPeriod)
	if err := h.service.ConfirmTOTP(t.Context(), principal, enrollment.CredentialID, code, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	recovery, err := h.service.GenerateRecoveryCodes(t.Context(), principal, authMeta("192.0.2.10"))
	if err != nil || len(recovery.Codes) != 10 {
		t.Fatalf("recovery codes: %+v %v", recovery, err)
	}
	challenge := h.login(t, "recovery@example.com", "Correct-Horse-Battery-9!")
	completed, err := h.service.VerifyMFA(t.Context(), challenge.MFAToken, "", strings.ToLower(recovery.Codes[0]), authMeta("192.0.2.10"))
	if err != nil || completed.Tokens == nil {
		t.Fatalf("recovery code login failed: %+v %v", completed, err)
	}
	second := h.login(t, "recovery@example.com", "Correct-Horse-Battery-9!")
	if _, err := h.service.VerifyMFA(t.Context(), second.MFAToken, "", recovery.Codes[0], authMeta("192.0.2.10")); err == nil {
		t.Fatal("recovery code was reusable")
	}
}

func TestAPIKeyHMACEd25519ScopesIPAndNonceReplay(t *testing.T) {
	h := newTestHarness(t)
	h.registerVerified(t, "keys@example.com", "Correct-Horse-Battery-9!")
	login := h.login(t, "keys@example.com", "Correct-Horse-Battery-9!")
	principal, _ := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)

	hmacKey, err := h.service.CreateAPIKey(t.Context(), principal, CreateAPIKeyInput{
		Label: "hmac-key", Scopes: []string{"read"}, IPAllowlist: []string{"192.0.2.0/24"}, ExpiresInDays: 2,
	}, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("create hmac key: %v", err)
	}
	if hmacKey.Secret == "" || hmacKey.KeyPrefix == "" {
		t.Fatal("one-time HMAC secret missing")
	}
	timestamp := strconvFormat(h.clock.Now().Unix())
	nonce := "abcdef0123456789abcdef01"
	signature := signHMAC(hmacKey.Secret, "GET", "/v1/me", timestamp, nonce, nil)
	keyPrincipal, err := h.service.AuthenticateAPIKey(t.Context(), APIKeyAuth{
		KeyID: hmacKey.ID.String(), Timestamp: timestamp, Nonce: nonce, Signature: signature,
		Method: "GET", Path: "/v1/me",
	}, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("authenticate hmac key: %v", err)
	}
	if !keyPrincipal.HasScope("read") || keyPrincipal.HasScope("trade") || keyPrincipal.HasPermission(PermAdminUserRead) {
		t.Fatalf("API key scope escalation: %+v", keyPrincipal)
	}
	if _, err := h.service.AuthenticateAPIKey(t.Context(), APIKeyAuth{
		KeyID: hmacKey.ID.String(), Timestamp: timestamp, Nonce: nonce, Signature: signature,
		Method: "GET", Path: "/v1/me",
	}, authMeta("192.0.2.10")); err == nil {
		t.Fatal("replayed nonce accepted")
	}
	if _, err := h.service.AuthenticateAPIKey(t.Context(), APIKeyAuth{
		KeyID: hmacKey.ID.String(), Timestamp: timestamp, Nonce: "abcdef0123456789abcdef02",
		Signature: signHMAC(hmacKey.Secret, "GET", "/v1/me", timestamp, "abcdef0123456789abcdef02", nil),
		Method:    "GET", Path: "/v1/me",
	}, authMeta("198.51.100.5")); err == nil {
		t.Fatal("API key accepted outside its IP allowlist")
	}
	badPathNonce := "abcdef0123456789abcdef03"
	if _, err := h.service.AuthenticateAPIKey(t.Context(), APIKeyAuth{
		KeyID: hmacKey.ID.String(), Timestamp: timestamp, Nonce: badPathNonce,
		Signature: signHMAC(hmacKey.Secret, "GET", "/v1/me", timestamp, badPathNonce, nil),
		Method:    "GET", Path: "/v1/admin/users",
	}, authMeta("192.0.2.10")); err == nil {
		t.Fatal("signature for a different path accepted")
	}
	if _, err := h.service.AuthenticateAPIKey(t.Context(), APIKeyAuth{
		KeyID: hmacKey.ID.String(), Timestamp: strconvFormat(h.clock.Now().Add(-10 * time.Minute).Unix()),
		Nonce: "abcdef0123456789abcdef04", Signature: signature, Method: "GET", Path: "/v1/me",
	}, authMeta("192.0.2.10")); err == nil {
		t.Fatal("stale API key timestamp accepted")
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519: %v", err)
	}
	edKey, err := h.service.CreateAPIKey(t.Context(), principal, CreateAPIKeyInput{
		Label: "ed25519-key", Scopes: []string{"trade"}, Algorithm: "ed25519",
		PublicKey: base64.StdEncoding.EncodeToString(public), ExpiresInDays: 2,
	}, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("create ed25519 key: %v", err)
	}
	edNonce := "abcdef0123456789abcdef05"
	edCanonical := "GET\n/v1/orders\n" + timestamp + "\n" + edNonce + "\n" + hexEncode(SHA256(nil))
	edSig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(edCanonical)))
	if _, err := h.service.AuthenticateAPIKey(t.Context(), APIKeyAuth{
		KeyID: edKey.ID.String(), Timestamp: timestamp, Nonce: edNonce, Signature: edSig,
		Method: "GET", Path: "/v1/orders",
	}, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("authenticate ed25519 key: %v", err)
	}

	keys, err := h.service.ListAPIKeys(t.Context(), principal)
	if err != nil || len(keys) != 2 {
		t.Fatalf("list keys: %+v %v", keys, err)
	}
	for _, key := range keys {
		if key.Active != true {
			t.Fatalf("active key marked inactive: %+v", key)
		}
	}
	if err := h.service.RevokeAPIKey(t.Context(), principal, hmacKey.ID, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("revoke key: %v", err)
	}
	if _, err := h.service.AuthenticateAPIKey(t.Context(), APIKeyAuth{
		KeyID: hmacKey.ID.String(), Timestamp: timestamp, Nonce: "abcdef0123456789abcdef06", Signature: signature,
		Method: "GET", Path: "/v1/me",
	}, authMeta("192.0.2.10")); err == nil {
		t.Fatal("revoked API key accepted")
	}
}

func TestWebAuthnValidationFailsClosed(t *testing.T) {
	if _, err := NewWebAuthnManager(WebAuthnConfig{}); err == nil {
		t.Fatal("WebAuthn manager accepted an empty RP configuration")
	}
	if _, err := NewWebAuthnManager(WebAuthnConfig{RPID: "example.com", RPOrigins: []string{"https://example.com"}}); err != nil {
		t.Fatalf("valid WebAuthn config rejected: %v", err)
	}
	h := newTestHarness(t)
	h.registerVerified(t, "webauthn@example.com", "Correct-Horse-Battery-9!")
	login := h.login(t, "webauthn@example.com", "Correct-Horse-Battery-9!")
	principal, _ := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	options, err := h.service.BeginWebAuthnRegistration(t.Context(), principal, authMeta("192.0.2.10"))
	if err != nil || options.Options == nil {
		t.Fatalf("begin webauthn registration: %+v %v", options, err)
	}
	err = h.service.FinishWebAuthnRegistration(t.Context(), principal, []byte(`{"id":"invalid","type":"public-key"}`), authMeta("192.0.2.10"))
	_ = requireAppCode(t, err, CodeAuthWebAuthn)
	if _, err := h.service.BeginWebAuthnLogin(t.Context(), "webauthn@example.com", authMeta("192.0.2.10")); err == nil {
		t.Fatal("WebAuthn login started without a registered credential")
	}
}

func TestPasswordResetRevokesSessionsAndAPIKeys(t *testing.T) {
	h := newTestHarness(t)
	h.registerVerified(t, "reset@example.com", "Correct-Horse-Battery-9!")
	login := h.login(t, "reset@example.com", "Correct-Horse-Battery-9!")
	principal, _ := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	if _, err := h.service.CreateAPIKey(t.Context(), principal, CreateAPIKeyInput{Label: "reset-key", Scopes: []string{"read"}}, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("create key: %v", err)
	}
	if err := h.service.RequestPasswordReset(t.Context(), "reset@example.com", authMeta("192.0.2.10")); err != nil {
		t.Fatalf("request reset: %v", err)
	}
	notification, ok := h.notifier.Find("password_reset")
	if !ok || notification.Token == "" {
		t.Fatalf("password reset notification missing: %+v", h.notifier.Requests)
	}
	if err := h.service.RequestPasswordReset(t.Context(), "nobody@example.com", authMeta("192.0.2.10")); err != nil {
		t.Fatalf("unknown reset leaked an error: %v", err)
	}
	if err := h.service.ConfirmPasswordReset(t.Context(), notification.Token, "New-Correct-Horse-42!", authMeta("192.0.2.10")); err != nil {
		t.Fatalf("confirm reset: %v", err)
	}
	if _, err := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken); err == nil {
		t.Fatal("access token survived password reset")
	}
	if _, err := h.service.Login(t.Context(), LoginInput{Email: "reset@example.com", Password: "Correct-Horse-Battery-9!"}, authMeta("192.0.2.10")); err == nil {
		t.Fatal("old password accepted after reset")
	}
	if _, err := h.service.Login(t.Context(), LoginInput{Email: "reset@example.com", Password: "New-Correct-Horse-42!"}, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
}

func TestAdminRBACAndSessionRevocation(t *testing.T) {
	h := newTestHarness(t)
	adminID, err := h.service.BootstrapAdmin(t.Context(), "admin@example.com", "Root-Correct-Horse-9!", authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	adminLogin := h.login(t, "admin@example.com", "Root-Correct-Horse-9!")
	adminPrincipal, err := h.service.ResolveSession(t.Context(), adminLogin.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("admin session: %v", err)
	}
	if adminPrincipal.UserID != *adminID || !adminPrincipal.HasRole(RoleAdmin) {
		t.Fatalf("bootstrap admin principal: %+v", adminPrincipal)
	}
	target := h.registerVerified(t, "target@example.com", "Correct-Horse-Battery-9!")
	targetLogin := h.login(t, "target@example.com", "Correct-Horse-Battery-9!")
	targetPrincipal, _ := h.service.ResolveSession(t.Context(), targetLogin.Tokens.AccessToken)

	if _, err := h.service.AdminGetUser(t.Context(), targetPrincipal, target.ID); err == nil {
		t.Fatal("customer accessed admin user read")
	} else {
		_ = requireAppCode(t, err, CodeAuthForbidden)
	}
	view, err := h.service.AdminGetUser(t.Context(), adminPrincipal, target.ID)
	if err != nil {
		t.Fatalf("admin get user: %v", err)
	}
	if view.ID != target.ID || strings.Contains(view.Email, "@") && strings.Contains(view.Email, "target") {
		t.Fatalf("admin view leaked full email: %+v", view)
	}
	if err := h.service.AdminSetRoles(t.Context(), adminPrincipal, target.ID, []Role{RoleAdmin, RoleCustomer}, authMeta("192.0.2.10")); err == nil {
		t.Fatal("admin and customer roles were combined")
	}
	if err := h.service.AdminSetStatus(t.Context(), adminPrincipal, target.ID, UserFrozen, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("freeze target: %v", err)
	}
	if _, err := h.service.ResolveSession(t.Context(), targetLogin.Tokens.AccessToken); err == nil {
		t.Fatal("freezing an account did not revoke its access token")
	}
	if _, err := h.service.Login(t.Context(), LoginInput{Email: "target@example.com", Password: "Correct-Horse-Battery-9!"}, authMeta("192.0.2.10")); err == nil {
		t.Fatal("frozen account logged in")
	}
	if _, err := h.service.AdminRevokeSessions(t.Context(), targetPrincipal, adminPrincipal.UserID, authMeta("192.0.2.10")); err == nil {
		t.Fatal("customer revoked another user's sessions")
	}
	events, err := h.service.AdminListAudit(t.Context(), adminPrincipal, AuditFilter{Limit: 50})
	if err != nil || len(events) == 0 {
		t.Fatalf("admin audit: %v %v", len(events), err)
	}
}

func TestSessionOwnershipAndIDOR(t *testing.T) {
	h := newTestHarness(t)
	h.registerVerified(t, "one@example.com", "Correct-Horse-Battery-9!")
	h.registerVerified(t, "two@example.com", "Correct-Horse-Battery-9!")
	one := h.login(t, "one@example.com", "Correct-Horse-Battery-9!")
	two := h.login(t, "two@example.com", "Correct-Horse-Battery-9!")
	onePrincipal, _ := h.service.ResolveSession(t.Context(), one.Tokens.AccessToken)
	twoPrincipal, _ := h.service.ResolveSession(t.Context(), two.Tokens.AccessToken)

	if err := h.service.RevokeSession(t.Context(), onePrincipal, two.Tokens.SessionID, authMeta("192.0.2.10")); err == nil {
		t.Fatal("IDOR: user revoked another user's session")
	}
	if _, err := h.service.ResolveSession(t.Context(), two.Tokens.AccessToken); err != nil {
		t.Fatalf("victim session was revoked: %v", err)
	}
	twoSessions, _ := h.service.ListSessions(t.Context(), twoPrincipal)
	if len(twoSessions) != 1 || twoSessions[0].ID != two.Tokens.SessionID {
		t.Fatalf("cross-account session listing: %+v", twoSessions)
	}
	if err := h.service.RevokeSession(t.Context(), twoPrincipal, two.Tokens.SessionID, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("own session revoke: %v", err)
	}
	if _, err := h.service.ResolveSession(t.Context(), two.Tokens.AccessToken); err == nil {
		t.Fatal("own revoked session still valid")
	}
}

func TestAuditHashChainDetectsTampering(t *testing.T) {
	h := newTestHarness(t)
	h.registerVerified(t, "audit@example.com", "Correct-Horse-Battery-9!")
	h.login(t, "audit@example.com", "Correct-Horse-Battery-9!")
	events, err := h.store.ListAudit(t.Context(), AuditFilter{Limit: 100})
	if err != nil || len(events) < 2 {
		t.Fatalf("audit events: %v %v", len(events), err)
	}
	// ListAudit returns newest first.
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		canonical, err := event.CanonicalAuditBytes()
		if err != nil {
			t.Fatalf("canonical: %v", err)
		}
		expected := SHA256(append(append([]byte(nil), event.PrevHash...), canonical...))
		if !SecureCompare(expected, event.EventHash) {
			t.Fatalf("audit event %d hash mismatch: action=%s id=%s prev=%x hash=%x expected=%x", i, event.Action, event.ID, event.PrevHash, event.EventHash, expected)
		}
		if i < len(events)-1 && !SecureCompare(event.PrevHash, events[i+1].EventHash) {
			t.Fatalf("audit chain broken at %d: action=%s prev=%x olderAction=%s olderHash=%x", i, event.Action, event.PrevHash, events[i+1].Action, events[i+1].EventHash)
		}
	}
}

func TestConcurrentRefreshRotationHasSingleWinner(t *testing.T) {
	h := newTestHarness(t)
	h.registerVerified(t, "race@example.com", "Correct-Horse-Battery-9!")
	login := h.login(t, "race@example.com", "Correct-Horse-Battery-9!")

	const attempts = 20
	var wg sync.WaitGroup
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.service.Refresh(t.Context(), login.Tokens.RefreshToken, authMeta("192.0.2.10"))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes, reused := 0, 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if appErr, ok := IsAppError(err); ok && appErr.Code == CodeAuthRefreshReused {
			reused++
		}
	}
	if successes != 1 {
		t.Fatalf("refresh race produced %d successes, want exactly 1", successes)
	}
	if reused != attempts-1 {
		t.Fatalf("refresh race produced %d reuse detections, want %d", reused, attempts-1)
	}
	if _, err := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken); err == nil {
		t.Fatal("session survived concurrent refresh reuse")
	}
}

func TestConcurrentAPIKeyNonceHasSingleWinner(t *testing.T) {
	h := newTestHarness(t)
	h.registerVerified(t, "nonce@example.com", "Correct-Horse-Battery-9!")
	login := h.login(t, "nonce@example.com", "Correct-Horse-Battery-9!")
	principal, _ := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	key, err := h.service.CreateAPIKey(t.Context(), principal, CreateAPIKeyInput{Label: "race", Scopes: []string{"read"}}, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	timestamp := strconvFormat(h.clock.Now().Unix())
	nonce := "abcdef0123456789abcdef99"
	signature := signHMAC(key.Secret, "GET", "/v1/me", timestamp, nonce, nil)

	const attempts = 20
	var wg sync.WaitGroup
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.service.AuthenticateAPIKey(t.Context(), APIKeyAuth{
				KeyID: key.ID.String(), Timestamp: timestamp, Nonce: nonce, Signature: signature,
				Method: "GET", Path: "/v1/me",
			}, authMeta("192.0.2.10"))
			results <- err
		}()
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
		t.Fatalf("nonce race produced %d successes, want exactly 1", successes)
	}
}

func TestInvalidSecurityConfigurationFailsClosed(t *testing.T) {
	_, private, _ := GenerateEd25519Key()
	if _, err := NewTokenManager(private, "", "audience", time.Minute, time.Minute); err == nil {
		t.Fatal("empty JWT issuer accepted")
	}
	if _, err := NewTokenManager(private, "issuer", "", time.Minute, time.Minute); err == nil {
		t.Fatal("empty JWT audience accepted")
	}
	if _, err := NewTokenManager(private, "issuer", "audience", 10*time.Second, time.Minute); err == nil {
		t.Fatal("unsafe short access TTL accepted")
	}
	if _, err := NewTokenManager(make(ed25519.PrivateKey, 3), "issuer", "audience", time.Minute, time.Minute); err == nil {
		t.Fatal("malformed Ed25519 key accepted")
	}
	if _, err := NewAESGCMCipher(make([]byte, 16)); err == nil {
		t.Fatal("short AES key accepted")
	}
	cfg := DefaultServiceConfig()
	cfg.MaxSessions = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("zero max sessions accepted")
	}
}

func TestUnknownAndExpiredVerificationTokensFailGenerically(t *testing.T) {
	h := newTestHarness(t)
	token := strings.Repeat("a", 43)
	if err := h.service.VerifyContact(t.Context(), token, "email", authMeta("192.0.2.10")); err == nil {
		t.Fatal("unknown verification token accepted")
	}
	if err := h.service.VerifyContact(t.Context(), token, "sms", authMeta("192.0.2.10")); err == nil {
		t.Fatal("invalid verification kind accepted")
	}
	if err := h.service.VerifyContact(t.Context(), strings.Repeat("b", 5000), "email", authMeta("192.0.2.10")); err == nil {
		t.Fatal("oversized verification token accepted")
	}
}

func TestRecoveryCodeNormalization(t *testing.T) {
	h := newTestHarness(t)
	h.registerVerified(t, "normalize@example.com", "Correct-Horse-Battery-9!")
	login := h.login(t, "normalize@example.com", "Correct-Horse-Battery-9!")
	principal, _ := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	enroll, _ := h.service.BeginTOTP(t.Context(), principal, authMeta("192.0.2.10"))
	code, _ := TOTPCode(enroll.Secret, h.clock.Now().Unix(), TOTPDigits, TOTPPeriod)
	if err := h.service.ConfirmTOTP(t.Context(), principal, enroll.CredentialID, code, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	codes, _ := h.service.GenerateRecoveryCodes(t.Context(), principal, authMeta("192.0.2.10"))
	plain := strings.ReplaceAll(strings.ToLower(codes.Codes[3]), "-", " ")
	plain = strings.ReplaceAll(plain, " ", "-")
	challenge := h.login(t, "normalize@example.com", "Correct-Horse-Battery-9!")
	if _, err := h.service.VerifyMFA(t.Context(), challenge.MFAToken, "", plain, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("normalized recovery code rejected: %v", err)
	}
}

func strconvFormat(value int64) string {
	return strconv.FormatInt(value, 10)
}

func hexEncode(value []byte) string {
	return hex.EncodeToString(value)
}
