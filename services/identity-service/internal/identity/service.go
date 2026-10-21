package identity

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ServiceConfig is the validated identity policy configuration.
type ServiceConfig struct {
	AccessTTL       time.Duration
	RefreshTTL      time.Duration
	MFATTL          time.Duration
	WebAuthnTTL     time.Duration
	MaxSessions     int
	MaxAPIKeys      int
	ExposeDevTokens bool
}

// DefaultServiceConfig returns approved B03 policy defaults.
func DefaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		AccessTTL: 15 * time.Minute, RefreshTTL: 30 * 24 * time.Hour,
		MFATTL: 5 * time.Minute, WebAuthnTTL: 2 * time.Minute,
		MaxSessions: 10, MaxAPIKeys: 10,
	}
}

// Validate bounds policy values.
func (c ServiceConfig) Validate() error {
	if c.AccessTTL < time.Minute || c.AccessTTL > time.Hour {
		return errors.New("access TTL must be between 1 minute and 1 hour")
	}
	if c.RefreshTTL < time.Hour || c.RefreshTTL > 90*24*time.Hour {
		return errors.New("refresh TTL must be between 1 hour and 90 days")
	}
	if c.MFATTL < time.Minute || c.MFATTL > 15*time.Minute {
		return errors.New("mfa TTL must be between 1 minute and 15 minutes")
	}
	if c.MaxSessions < 1 || c.MaxSessions > 100 {
		return errors.New("max sessions must be between 1 and 100")
	}
	if c.MaxAPIKeys < 1 || c.MaxAPIKeys > 100 {
		return errors.New("max api keys must be between 1 and 100")
	}
	return nil
}

// RequestMeta carries trusted request context. IP must come from the transport
// or a trusted proxy, never directly from an untrusted forwarding header.
type RequestMeta struct {
	IP                netip.Addr
	UserAgent         string
	RequestID         string
	DeviceFingerprint string
}

// Service implements identity workflows.
type Service struct {
	store        Store
	tokens       *TokenManager
	hasher       *PasswordHasher
	piiCipher    SecretCipher
	secretCipher SecretCipher
	webauthn     *WebAuthnManager
	notifier     Notifier
	piiPepper    []byte
	ipPepper     []byte
	now          func() time.Time
	cfg          ServiceConfig
	lockout      LockoutPolicy
	issuer       string
}

// NewService validates dependencies and returns a fail-closed service.
func NewService(store Store, tokens *TokenManager, hasher *PasswordHasher, piiCipher, secretCipher SecretCipher, webauthn *WebAuthnManager, notifier Notifier, piiPepper, ipPepper []byte, cfg ServiceConfig, lockout LockoutPolicy, issuer string) (*Service, error) {
	if store == nil || tokens == nil || hasher == nil || piiCipher == nil || secretCipher == nil {
		return nil, errors.New("identity service dependencies are incomplete")
	}
	if len(piiPepper) < 32 || len(ipPepper) < 32 {
		return nil, errors.New("identity hash peppers must be at least 32 bytes")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if issuer == "" {
		return nil, errors.New("identity issuer is required")
	}
	return &Service{
		store: store, tokens: tokens, hasher: hasher, piiCipher: piiCipher,
		secretCipher: secretCipher, webauthn: webauthn, notifier: notifier,
		piiPepper: append([]byte(nil), piiPepper...), ipPepper: append([]byte(nil), ipPepper...),
		now: time.Now, cfg: cfg, lockout: lockout, issuer: issuer,
	}, nil
}

// SetClock overrides time for deterministic tests.
func (s *Service) SetClock(now func() time.Time) {
	s.now = now
	s.tokens.SetClock(now)
}

// Store exposes the store for health checks and tests.
func (s *Service) Store() Store { return s.store }

// Hasher exposes the password hasher for bootstrap/admin operations.
func (s *Service) Hasher() *PasswordHasher { return s.hasher }

// TokenManager exposes the token manager for HTTP middleware.
func (s *Service) TokenManager() *TokenManager { return s.tokens }

// WebAuthn returns the WebAuthn manager.
func (s *Service) WebAuthn() *WebAuthnManager { return s.webauthn }

// RegisterInput carries untrusted registration fields.
type RegisterInput struct {
	Email             string
	Phone             string
	Password          string
	CountryCode       string
	DeviceFingerprint string
}

// RegisterResult deliberately never reveals whether an email already exists.
type RegisterResult struct {
	VerificationRequired bool
	DevVerificationToken string `json:"devVerificationToken,omitempty"`
	DevPhoneToken        string `json:"devPhoneToken,omitempty"`
}

// Register creates a pending user, Argon2id credential, and verification
// tokens. Duplicate emails return the same generic result. Notification
// delivery is queued to the configured notifier; the service never logs tokens.
func (s *Service) Register(ctx context.Context, input RegisterInput, meta RequestMeta) (*RegisterResult, error) {
	email, err := NormalizeEmail(input.Email)
	if err != nil {
		return nil, validationError("invalid email address")
	}
	phone := strings.TrimSpace(input.Phone)
	if phone != "" {
		phone, err = NormalizePhone(phone)
		if err != nil {
			return nil, validationError("invalid phone number")
		}
	}
	if err := ValidatePasswordPolicy(input.Password, email); err != nil {
		return nil, validationError(err.Error())
	}
	now := s.now().UTC()
	emailHash := s.hashPII("email", email)
	existing, lookupErr := s.store.GetUserByEmailHash(ctx, emailHash)
	if lookupErr == nil && existing != nil {
		// Equalize work for duplicate and new registrations.
		_, _ = s.hasher.Hash(input.Password)
		return &RegisterResult{VerificationRequired: true}, nil
	}
	if lookupErr != nil && !errors.Is(lookupErr, ErrStoreNotFound) {
		return nil, internalError(lookupErr)
	}
	if phone != "" {
		phoneHash := s.hashPII("phone", phone)
		if _, err := s.store.GetUserByPhoneHash(ctx, phoneHash); err == nil {
			_, _ = s.hasher.Hash(input.Password)
			return &RegisterResult{VerificationRequired: true}, nil
		} else if !errors.Is(err, ErrStoreNotFound) {
			return nil, internalError(err)
		}
	}
	passwordHash, err := s.hasher.Hash(input.Password)
	if err != nil {
		return nil, internalError(err)
	}
	emailCiphertext, err := s.piiCipher.Encrypt([]byte(email))
	if err != nil {
		return nil, internalError(err)
	}
	var phoneCiphertext, phoneHash []byte
	if phone != "" {
		phoneCiphertext, err = s.piiCipher.Encrypt([]byte(phone))
		if err != nil {
			return nil, internalError(err)
		}
		phoneHash = s.hashPII("phone", phone)
	}
	userID, err := uuid.NewV7()
	if err != nil {
		return nil, internalError(err)
	}
	country := strings.ToUpper(strings.TrimSpace(input.CountryCode))
	if country != "" && (len(country) != 2 || !isAlpha(country)) {
		return nil, validationError("invalid country code")
	}
	user := &User{
		ID: userID, EmailCiphertext: emailCiphertext, EmailHash: emailHash,
		PhoneCiphertext: phoneCiphertext, PhoneHash: phoneHash,
		PasswordHash: passwordHash, Status: UserPending, KYCLevel: 0,
		CreatedAt: now, UpdatedAt: now, Roles: []Role{RoleCustomer},
	}
	if country != "" {
		user.CountryCode = &country
	}
	passwordCredential := &Credential{
		UserID: userID, Kind: CredentialPassword, SecretCiphertext: []byte(passwordHash),
		Metadata: json.RawMessage(`{"scheme":"argon2id"}`), CreatedAt: now,
	}
	emailToken, err := s.newVerification(userID, "email", now.Add(24*time.Hour), now)
	if err != nil {
		return nil, internalError(err)
	}
	audit := NewAuditEvent("user", userID.String(), "auth.register", "user", userID.String(), "success",
		map[string]string{"email_verification": "pending", "phone_verification": boolString(phone != "")}, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.CreateUser(ctx, user, passwordCredential, emailToken.VerificationToken, audit); err != nil {
		if errors.Is(err, ErrStoreConflict) {
			return &RegisterResult{VerificationRequired: true}, nil
		}
		return nil, internalError(err)
	}
	result := &RegisterResult{VerificationRequired: true}
	emailTokenRaw := emailToken.plaintext
	_ = emailToken
	if s.notifier != nil {
		if err := s.notifier.Send(ctx, NotificationRequest{UserID: userID, Channel: "email", Kind: "email_verification", Destination: email, Token: emailTokenRaw}); err != nil {
			slog.Error("identity notification enqueue failed", "action", "auth.register", "error", err)
		}
	}
	if s.cfg.ExposeDevTokens {
		result.DevVerificationToken = emailTokenRaw
	}
	if phone != "" {
		phoneToken, err := s.newVerification(userID, "phone", now.Add(24*time.Hour), now)
		if err != nil {
			return nil, internalError(err)
		}
		phoneAudit := NewAuditEvent("user", userID.String(), "auth.phone_verification.request", "user", userID.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
		if err := s.store.CreateVerification(ctx, phoneToken.VerificationToken, phoneAudit); err != nil {
			return nil, internalError(err)
		}
		if s.notifier != nil {
			if err := s.notifier.Send(ctx, NotificationRequest{UserID: userID, Channel: "phone", Kind: "phone_verification", Destination: phone, Token: phoneToken.plaintext}); err != nil {
				slog.Error("identity notification enqueue failed", "action", "auth.phone_verification.request", "error", err)
			}
		}
		if s.cfg.ExposeDevTokens {
			result.DevPhoneToken = phoneToken.plaintext
		}
	}
	return result, nil
}

type verificationToken struct {
	*VerificationToken
	plaintext string
}

func (s *Service) newVerification(userID uuid.UUID, kind string, expires, now time.Time) (*verificationToken, error) {
	raw, err := RandomToken(32)
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	return &verificationToken{
		VerificationToken: &VerificationToken{
			ID: id, UserID: userID, Kind: kind, TokenHash: SHA256([]byte(raw)),
			ExpiresAt: expires.UTC(), CreatedAt: now.UTC(),
		},
		plaintext: raw,
	}, nil
}

// VerifyContact consumes a one-time email or phone verification token.
func (s *Service) VerifyContact(ctx context.Context, rawToken, kind string, meta RequestMeta) error {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "email" && kind != "phone" {
		return validationError("invalid verification kind")
	}
	if strings.TrimSpace(rawToken) == "" || len(rawToken) > 256 {
		return validationError("invalid verification token")
	}
	now := s.now().UTC()
	action := "auth.email.verify"
	if kind == "phone" {
		action = "auth.phone.verify"
	}
	audit := NewAuditEvent("user", "", action, "user", "", "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	_, err := s.store.VerifyContact(ctx, SHA256([]byte(rawToken)), kind, now, audit)
	if err != nil {
		if errors.Is(err, ErrStoreExpired) || errors.Is(err, ErrStoreConsumed) || errors.Is(err, ErrStoreNotFound) {
			return validationError("invalid or expired verification token")
		}
		return internalError(err)
	}
	return nil
}

// ResendVerification always returns the same generic outcome.
func (s *Service) ResendVerification(ctx context.Context, email, kind string, meta RequestMeta) error {
	email, err := NormalizeEmail(email)
	if err != nil {
		return nil
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "email" && kind != "phone" {
		return validationError("invalid verification kind")
	}
	user, err := s.store.GetUserByEmailHash(ctx, s.hashPII("email", email))
	if err != nil {
		return nil
	}
	now := s.now().UTC()
	if kind == "email" && user.EmailVerifiedAt != nil {
		return nil
	}
	if kind == "phone" && (len(user.PhoneHash) == 0 || user.PhoneVerifiedAt != nil) {
		return nil
	}
	token, err := s.newVerification(user.ID, kind, now.Add(24*time.Hour), now)
	if err != nil {
		return nil
	}
	audit := NewAuditEvent("user", user.ID.String(), "auth.verification.resend", "user", user.ID.String(), "success", map[string]string{"kind": kind}, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.CreateVerification(ctx, token.VerificationToken, audit); err != nil {
		return internalError(err)
	}
	destination := email
	if kind == "phone" {
		plain, err := s.piiCipher.Decrypt(user.PhoneCiphertext)
		if err != nil {
			return nil
		}
		destination = string(plain)
	}
	if s.notifier != nil {
		_ = s.notifier.Send(ctx, NotificationRequest{UserID: user.ID, Channel: kind, Kind: kind + "_verification", Destination: destination, Token: token.plaintext})
	}
	return nil
}

// LoginInput carries untrusted primary-authentication fields.
type LoginInput struct {
	Email             string
	Password          string
	TOTPCode          string
	RecoveryCode      string
	DeviceFingerprint string
}

// TokenPair is the only response that contains bearer credentials.
type TokenPair struct {
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	SessionID        uuid.UUID
	MFA              bool
}

// LoginResult represents either a completed login or an MFA challenge.
type LoginResult struct {
	MFARequired bool
	MFAToken    string
	Methods     []string
	Tokens      *TokenPair
}

// Login verifies a password and, if enrolled, a second factor.
func (s *Service) Login(ctx context.Context, input LoginInput, meta RequestMeta) (*LoginResult, error) {
	email, err := NormalizeEmail(input.Email)
	if err != nil {
		// Execute dummy work to reduce enumeration timing differences.
		s.hasher.VerifyDummy(input.Password)
		return nil, invalidCredentialsError()
	}
	if input.Password == "" || len(input.Password) > 128 {
		return nil, invalidCredentialsError()
	}
	now := s.now().UTC()
	emailHash := s.hashPII("email", email)
	ipHash := s.ipHash(meta)
	state, err := s.store.GetLoginState(ctx, emailHash, ipHash, now)
	if err != nil {
		return nil, internalError(err)
	}
	if state.Blocked(now) {
		return nil, lockedError(state.RetryAfter(now))
	}
	user, err := s.store.GetUserByEmailHash(ctx, emailHash)
	if err != nil {
		if !errors.Is(err, ErrStoreNotFound) {
			return nil, internalError(err)
		}
		s.hasher.VerifyDummy(input.Password)
		s.recordLoginFailure(ctx, emailHash, ipHash, meta, now, "unknown_user")
		return nil, invalidCredentialsError()
	}
	if user.Status == UserPending {
		return nil, NewAppError(403, CodeAuthEmailUnverified, "email verification required")
	}
	if user.Status == UserFrozen || user.Status == UserClosed {
		s.hasher.VerifyDummy(input.Password)
		s.recordLoginFailure(ctx, emailHash, ipHash, meta, now, "inactive_user")
		return nil, invalidCredentialsError()
	}
	if !s.hasher.Verify(user.PasswordHash, input.Password) {
		s.recordLoginFailure(ctx, emailHash, ipHash, meta, now, "bad_password")
		return nil, invalidCredentialsError()
	}
	if code := strings.TrimSpace(input.TOTPCode); code != "" || strings.TrimSpace(input.RecoveryCode) != "" {
		ok, appErr := s.verifySecondFactor(ctx, user, code, input.RecoveryCode, meta, now)
		if appErr != nil {
			if appErr.Code == CodeAuthMFAReplay {
				s.recordLoginFailure(ctx, user.EmailHash, ipHash, meta, now, "mfa_replay")
			}
			return nil, appErr
		}
		if !ok {
			s.recordLoginFailure(ctx, user.EmailHash, ipHash, meta, now, "bad_mfa")
			return nil, mfaInvalidError()
		}
		_ = s.store.ClearLoginFailures(ctx, user.EmailHash, ipHash)
		return s.completeLogin(ctx, user, meta, now, true, "auth.login.totp")
	}
	methods := s.mfaMethods(ctx, user)
	if len(methods) > 0 {
		raw, err := RandomToken(32)
		if err != nil {
			return nil, internalError(err)
		}
		challengeID, err := uuid.NewV7()
		if err != nil {
			return nil, internalError(err)
		}
		challenge := &MFAChallenge{
			ID: challengeID, UserID: user.ID, TokenHash: SHA256([]byte(raw)),
			Methods: methods, ExpiresAt: now.Add(s.cfg.MFATTL), CreatedAt: now,
		}
		if err := s.store.CreateMFAChallenge(ctx, challenge); err != nil {
			return nil, internalError(err)
		}
		_ = s.store.AppendAudit(ctx, NewAuditEvent("user", user.ID.String(), "auth.mfa.challenge", "user", user.ID.String(), "success", map[string]string{"methods": strings.Join(methods, ",")}, ipHash, meta.UserAgent, meta.RequestID, now))
		return &LoginResult{MFARequired: true, MFAToken: raw, Methods: methods}, nil
	}
	_ = s.store.ClearLoginFailures(ctx, user.EmailHash, ipHash)
	return s.completeLogin(ctx, user, meta, now, false, "auth.login.success")
}

// VerifyMFA completes a first-factor login using a TOTP or recovery code.
func (s *Service) VerifyMFA(ctx context.Context, mfaToken, totpCode, recoveryCode string, meta RequestMeta) (*LoginResult, error) {
	if strings.TrimSpace(mfaToken) == "" {
		return nil, mfaInvalidError()
	}
	now := s.now().UTC()
	challenge, err := s.store.ConsumeMFAChallenge(ctx, SHA256([]byte(mfaToken)), now)
	if err != nil {
		if errors.Is(err, ErrStoreExpired) || errors.Is(err, ErrStoreConsumed) || errors.Is(err, ErrStoreNotFound) {
			return nil, NewAppError(401, CodeAuthMFAInvalid, "multi-factor challenge expired or already used")
		}
		return nil, internalError(err)
	}
	user, err := s.store.GetUserByID(ctx, challenge.UserID)
	if err != nil {
		return nil, invalidCredentialsError()
	}
	if user.Status != UserActive {
		return nil, invalidCredentialsError()
	}
	ipHash := s.ipHash(meta)
	state, err := s.store.GetLoginState(ctx, user.EmailHash, ipHash, now)
	if err != nil {
		return nil, internalError(err)
	}
	if state.LockedUntil != nil {
		return nil, lockedError(state.RetryAfter(now))
	}
	ok, appErr := s.verifySecondFactor(ctx, user, totpCode, recoveryCode, meta, now)
	if appErr != nil {
		if appErr.Code == CodeAuthMFAReplay {
			s.recordLoginFailure(ctx, user.EmailHash, ipHash, meta, now, "mfa_replay")
		}
		return nil, appErr
	}
	if !ok {
		_ = s.store.AppendAudit(ctx, NewAuditEvent("user", user.ID.String(), "auth.mfa.verify", "user", user.ID.String(), "denied", nil, ipHash, meta.UserAgent, meta.RequestID, now))
		s.recordLoginFailure(ctx, user.EmailHash, ipHash, meta, now, "bad_mfa")
		return nil, mfaInvalidError()
	}
	_ = s.store.ClearLoginFailures(ctx, user.EmailHash, s.ipHash(meta))
	return s.completeLogin(ctx, user, meta, now, true, "auth.mfa.success")
}

func (s *Service) completeLogin(ctx context.Context, user *User, meta RequestMeta, now time.Time, mfa bool, action string) (*LoginResult, error) {
	sessionID, err := uuid.NewV7()
	if err != nil {
		return nil, internalError(err)
	}
	refreshRaw, err := RandomToken(32)
	if err != nil {
		return nil, internalError(err)
	}
	refreshID, err := uuid.NewV7()
	if err != nil {
		return nil, internalError(err)
	}
	refreshExpiry := now.Add(s.cfg.RefreshTTL)
	session := &Session{
		ID: sessionID, UserID: user.ID, DeviceFingerprint: cleanText(meta.DeviceFingerprint, 128),
		UserAgent: cleanText(meta.UserAgent, 512), IPHash: s.ipHash(meta), MFA: mfa,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: refreshExpiry,
	}
	refresh := &RefreshToken{ID: refreshID, TokenHash: SHA256([]byte(refreshRaw)), Status: RefreshActive, ExpiresAt: refreshExpiry, CreatedAt: now}
	audit := NewAuditEvent("user", user.ID.String(), action, "session", sessionID.String(), "success",
		map[string]string{"mfa": boolString(mfa)}, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.CreateSession(ctx, session, refresh, s.cfg.MaxSessions, now, audit); err != nil {
		if errors.Is(err, ErrStoreLimit) {
			return nil, NewAppError(429, CodeAuthRateLimited, "too many active sessions")
		}
		return nil, internalError(err)
	}
	access, accessExpiry, err := s.tokens.IssueAccess(user.ID, sessionID, user.Roles, mfa)
	if err != nil {
		return nil, internalError(err)
	}
	return &LoginResult{Tokens: &TokenPair{
		AccessToken: access, RefreshToken: refreshRaw, AccessExpiresAt: accessExpiry,
		RefreshExpiresAt: refreshExpiry, SessionID: sessionID, MFA: mfa,
	}}, nil
}

// Refresh rotates a refresh token and detects reuse of an already-rotated
// token. Reuse revokes the entire session family atomically in the store.
func (s *Service) Refresh(ctx context.Context, rawToken string, meta RequestMeta) (*TokenPair, error) {
	if strings.TrimSpace(rawToken) == "" {
		return nil, invalidTokenError()
	}
	now := s.now().UTC()
	newRaw, err := RandomToken(32)
	if err != nil {
		return nil, internalError(err)
	}
	newID, err := uuid.NewV7()
	if err != nil {
		return nil, internalError(err)
	}
	expiry := now.Add(s.cfg.RefreshTTL)
	newToken := &RefreshToken{ID: newID, TokenHash: SHA256([]byte(newRaw)), Status: RefreshActive, ExpiresAt: expiry, CreatedAt: now}
	successAudit := NewAuditEvent("session", "", "auth.refresh.rotate", "refresh_token", newID.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	reuseAudit := NewAuditEvent("session", "", "auth.refresh.reuse", "refresh_token", "", "denied", map[string]string{"action": "revoke_session_family"}, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	rotation, err := s.store.RotateRefresh(ctx, SHA256([]byte(rawToken)), newToken, expiry, now, successAudit, reuseAudit)
	if err != nil {
		switch {
		case errors.Is(err, ErrStoreRefreshReused):
			return nil, NewAppError(401, CodeAuthRefreshReused, "refresh token reuse detected; session revoked")
		case errors.Is(err, ErrStoreExpired), errors.Is(err, ErrStoreNotFound):
			return nil, invalidTokenError()
		default:
			return nil, internalError(err)
		}
	}
	access, accessExpiry, err := s.tokens.IssueAccess(rotation.User.ID, rotation.Session.ID, rotation.User.Roles, rotation.Session.MFA)
	if err != nil {
		return nil, internalError(err)
	}
	return &TokenPair{
		AccessToken: access, RefreshToken: newRaw, AccessExpiresAt: accessExpiry,
		RefreshExpiresAt: expiry, SessionID: rotation.Session.ID, MFA: rotation.Session.MFA,
	}, nil
}

// Logout revokes the current session and all of its refresh tokens.
func (s *Service) Logout(ctx context.Context, principal Principal, meta RequestMeta) error {
	if principal.Kind != "session" || principal.SessionID == uuid.Nil {
		return forbiddenError("session authentication required")
	}
	now := s.now().UTC()
	audit := NewAuditEvent("user", principal.UserID.String(), "auth.logout", "session", principal.SessionID.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.RevokeSession(ctx, principal.SessionID, &principal.UserID, now, audit); err != nil {
		if errors.Is(err, ErrStoreNotFound) {
			return sessionRevokedError()
		}
		return internalError(err)
	}
	return nil
}

// ResolveSession validates an access token and the authoritative session and
// user state. Revocation therefore takes effect on the next request.
func (s *Service) ResolveSession(ctx context.Context, accessToken string) (Principal, error) {
	claims, err := s.tokens.ParseAccess(accessToken)
	if err != nil {
		if errors.Is(err, ErrTokenExpired) {
			return Principal{}, tokenExpiredError()
		}
		return Principal{}, invalidTokenError()
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return Principal{}, invalidTokenError()
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		return Principal{}, invalidTokenError()
	}
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrStoreNotFound) {
			return Principal{}, sessionRevokedError()
		}
		return Principal{}, internalError(err)
	}
	now := s.now().UTC()
	if session.UserID != userID || session.RevokedAt != nil || !now.Before(session.ExpiresAt) {
		return Principal{}, sessionRevokedError()
	}
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil || user.Status != UserActive {
		return Principal{}, sessionRevokedError()
	}
	return Principal{
		UserID: userID, SessionID: sessionID, Roles: user.Roles, MFA: session.MFA, Kind: "session",
	}, nil
}

// ResolveStepUp validates a step-up token for a specific action and session.
func (s *Service) ResolveStepUp(raw string, action string, principal Principal) error {
	if raw == "" || principal.Kind != "session" {
		return stepUpRequiredError()
	}
	if _, err := s.tokens.ParseStepUp(raw, action, principal.UserID, principal.SessionID); err != nil {
		return stepUpRequiredError()
	}
	return nil
}

// IssueStepUp verifies a second factor and issues a 60-second action token.
func (s *Service) IssueStepUp(ctx context.Context, principal Principal, totpCode, recoveryCode, action string, meta RequestMeta) (string, time.Time, error) {
	if principal.Kind != "session" {
		return "", time.Time{}, stepUpRequiredError()
	}
	if strings.TrimSpace(action) == "" || len(action) > 128 {
		return "", time.Time{}, validationError("invalid step-up action")
	}
	user, err := s.store.GetUserByID(ctx, principal.UserID)
	if err != nil || user.Status != UserActive {
		return "", time.Time{}, sessionRevokedError()
	}
	now := s.now().UTC()
	ipHash := s.ipHash(meta)
	state, err := s.store.GetLoginState(ctx, user.EmailHash, ipHash, now)
	if err != nil {
		return "", time.Time{}, internalError(err)
	}
	if state.LockedUntil != nil {
		return "", time.Time{}, lockedError(state.RetryAfter(now))
	}
	ok, appErr := s.verifySecondFactor(ctx, user, totpCode, recoveryCode, meta, now)
	if appErr != nil {
		if appErr.Code == CodeAuthMFAReplay {
			s.recordLoginFailure(ctx, user.EmailHash, ipHash, meta, now, "mfa_replay")
		}
		return "", time.Time{}, appErr
	}
	if !ok {
		_ = s.store.AppendAudit(ctx, NewAuditEvent("user", principal.UserID.String(), "auth.stepup", "user", principal.UserID.String(), "denied", map[string]string{"action": action}, ipHash, meta.UserAgent, meta.RequestID, now))
		s.recordLoginFailure(ctx, user.EmailHash, ipHash, meta, now, "bad_mfa")
		return "", time.Time{}, mfaInvalidError()
	}
	_ = s.store.ClearLoginFailures(ctx, user.EmailHash, ipHash)
	raw, expires, err := s.tokens.IssueStepUp(principal.UserID, principal.SessionID, action)
	if err != nil {
		return "", time.Time{}, internalError(err)
	}
	_ = s.store.AppendAudit(ctx, NewAuditEvent("user", principal.UserID.String(), "auth.stepup", "user", principal.UserID.String(), "success", map[string]string{"action": action}, s.ipHash(meta), meta.UserAgent, meta.RequestID, now))
	return raw, expires, nil
}

// SessionView is a redacted session representation.
type SessionView struct {
	ID                uuid.UUID
	CreatedAt         time.Time
	LastSeenAt        time.Time
	ExpiresAt         time.Time
	DeviceFingerprint string
	UserAgent         string
	Current           bool
}

// ListSessions lists only the caller's sessions.
func (s *Service) ListSessions(ctx context.Context, principal Principal) ([]SessionView, error) {
	if principal.Kind != "session" {
		return nil, forbiddenError("session authentication required")
	}
	sessions, err := s.store.ListSessions(ctx, principal.UserID)
	if err != nil {
		return nil, internalError(err)
	}
	now := s.now().UTC()
	out := make([]SessionView, 0, len(sessions))
	for _, session := range sessions {
		if session.RevokedAt != nil || !now.Before(session.ExpiresAt) {
			continue
		}
		out = append(out, SessionView{
			ID: session.ID, CreatedAt: session.CreatedAt, LastSeenAt: session.LastSeenAt,
			ExpiresAt: session.ExpiresAt, DeviceFingerprint: session.DeviceFingerprint,
			UserAgent: session.UserAgent, Current: session.ID == principal.SessionID,
		})
	}
	return out, nil
}

// RevokeSession revokes one of the caller's own sessions.
func (s *Service) RevokeSession(ctx context.Context, principal Principal, sessionID uuid.UUID, meta RequestMeta) error {
	if principal.Kind != "session" || principal.UserID == uuid.Nil {
		return forbiddenError("session authentication required")
	}
	now := s.now().UTC()
	audit := NewAuditEvent("user", principal.UserID.String(), "auth.session.revoke", "session", sessionID.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.RevokeSession(ctx, sessionID, &principal.UserID, now, audit); err != nil {
		if errors.Is(err, ErrStoreNotFound) {
			return notFoundError()
		}
		return internalError(err)
	}
	return nil
}

// SecurityStatus summarizes the caller's security posture.
type SecurityStatus struct {
	UserID         uuid.UUID
	Status         UserStatus
	EmailVerified  bool
	PhoneVerified  bool
	MFAEnrolled    bool
	MFAMethods     []string
	ActiveSessions int
	ActiveAPIKeys  int
	Roles          []Role
	Permissions    []string
}

// SecurityStatus returns current-session security information.
func (s *Service) SecurityStatus(ctx context.Context, principal Principal) (*SecurityStatus, error) {
	if principal.Kind != "session" {
		return nil, forbiddenError("session authentication required")
	}
	user, err := s.store.GetUserByID(ctx, principal.UserID)
	if err != nil {
		return nil, sessionRevokedError()
	}
	now := s.now().UTC()
	sessions, _ := s.store.CountActiveSessions(ctx, principal.UserID, now)
	keys, _ := s.store.CountActiveAPIKeys(ctx, principal.UserID, now)
	return &SecurityStatus{
		UserID: user.ID, Status: user.Status, EmailVerified: user.EmailVerifiedAt != nil,
		PhoneVerified: user.PhoneVerifiedAt != nil, MFAEnrolled: user.MFAEnrolledAt != nil,
		MFAMethods: s.mfaMethods(ctx, user), ActiveSessions: sessions, ActiveAPIKeys: keys,
		Roles: user.Roles, Permissions: PermissionsFor(user.Roles),
	}, nil
}

// PermissionsView is the effective role and scope set for a principal.
type PermissionsView struct {
	UserID      uuid.UUID
	Roles       []Role
	Permissions []string
	Scopes      []string
	Kind        string
}

// Permissions returns the caller's effective RBAC permissions or API scopes.
func (s *Service) Permissions(principal Principal) PermissionsView {
	return PermissionsView{
		UserID: principal.UserID, Roles: principal.Roles,
		Permissions: PermissionsFor(principal.Roles), Scopes: principal.Scopes, Kind: principal.Kind,
	}
}

// TOTPEnrollment is returned only at enrollment time.
type TOTPEnrollment struct {
	CredentialID    uuid.UUID
	Secret          string
	ProvisioningURI string
	ExpiresAt       time.Time
}

// BeginTOTP creates an unverified TOTP credential.
func (s *Service) BeginTOTP(ctx context.Context, principal Principal, meta RequestMeta) (*TOTPEnrollment, error) {
	if principal.Kind != "session" {
		return nil, forbiddenError("session authentication required")
	}
	user, err := s.store.GetUserByID(ctx, principal.UserID)
	if err != nil {
		return nil, sessionRevokedError()
	}
	existing, err := s.store.GetCredentials(ctx, principal.UserID, CredentialTOTP)
	if err != nil {
		return nil, internalError(err)
	}
	for _, credential := range existing {
		if credential.VerifiedAt != nil {
			return nil, conflictError("TOTP is already enrolled")
		}
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		return nil, internalError(err)
	}
	encrypted, err := s.secretCipher.Encrypt([]byte(secret))
	if err != nil {
		return nil, internalError(err)
	}
	now := s.now().UTC()
	credentialID, err := uuid.NewV7()
	if err != nil {
		return nil, internalError(err)
	}
	credential := &Credential{
		ID: credentialID, UserID: principal.UserID, Kind: CredentialTOTP,
		SecretCiphertext: encrypted, Metadata: json.RawMessage(`{"verified":false}`), CreatedAt: now,
	}
	audit := NewAuditEvent("user", principal.UserID.String(), "mfa.totp.begin", "credential", credentialID.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.CreateCredential(ctx, credential, audit); err != nil {
		return nil, internalError(err)
	}
	account := user.ID.String()
	return &TOTPEnrollment{
		CredentialID: credentialID, Secret: secret,
		ProvisioningURI: TOTPProvisioningURI(s.issuer, account, secret),
		ExpiresAt:       now.Add(10 * time.Minute),
	}, nil
}

// ConfirmTOTP verifies the first code and marks the credential active.
func (s *Service) ConfirmTOTP(ctx context.Context, principal Principal, credentialID uuid.UUID, code string, meta RequestMeta) error {
	if principal.Kind != "session" {
		return forbiddenError("session authentication required")
	}
	credential, err := s.store.GetCredential(ctx, credentialID)
	if err != nil || credential.UserID != principal.UserID || credential.Kind != CredentialTOTP {
		return notFoundError()
	}
	if credential.VerifiedAt != nil {
		return conflictError("TOTP is already verified")
	}
	secret, err := s.secretCipher.Decrypt(credential.SecretCiphertext)
	if err != nil {
		return internalError(err)
	}
	now := s.now().UTC()
	step, ok, err := VerifyTOTP(string(secret), code, now, TOTPSkew, TOTPDigits, TOTPPeriod)
	if err != nil || !ok {
		return mfaInvalidError()
	}
	audit := NewAuditEvent("user", principal.UserID.String(), "mfa.totp.confirm", "credential", credential.ID.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	enrollAudit := NewAuditEvent("user", principal.UserID.String(), "mfa.enroll", "user", principal.UserID.String(), "success", map[string]string{"method": "totp"}, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.ConfirmTOTP(ctx, credential.ID, principal.UserID, step, now, audit, enrollAudit); err != nil {
		if errors.Is(err, ErrStoreConflict) {
			return mfaReplayError()
		}
		return internalError(err)
	}
	return nil
}

// RecoveryCodes is returned exactly once.
type RecoveryCodes struct {
	Codes []string
}

// GenerateRecoveryCodes replaces existing recovery codes. Callers must require
// step-up authentication.
func (s *Service) GenerateRecoveryCodes(ctx context.Context, principal Principal, meta RequestMeta) (*RecoveryCodes, error) {
	if principal.Kind != "session" {
		return nil, forbiddenError("session authentication required")
	}
	now := s.now().UTC()
	codes := make([]Credential, 0, 10)
	plain := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		raw, err := RandomToken(15)
		if err != nil {
			return nil, internalError(err)
		}
		code := formatRecoveryCode(raw)
		plain = append(plain, code)
		id, err := uuid.NewV7()
		if err != nil {
			return nil, internalError(err)
		}
		codes = append(codes, Credential{
			ID: id, UserID: principal.UserID, Kind: CredentialRecoveryCode,
			SecretCiphertext: SHA256([]byte(normalizeRecoveryCode(code))), Metadata: json.RawMessage(`{"single_use":true}`), CreatedAt: now,
		})
	}
	audit := NewAuditEvent("user", principal.UserID.String(), "mfa.recovery_codes.generate", "user", principal.UserID.String(), "success", map[string]string{"count": "10"}, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.CreateRecoveryCodes(ctx, principal.UserID, codes, now, audit); err != nil {
		return nil, internalError(err)
	}
	return &RecoveryCodes{Codes: plain}, nil
}

// WebAuthnOptions is the public ceremony payload plus an opaque ceremony ID.
type WebAuthnOptions struct {
	Options any
}

// BeginWebAuthnRegistration starts a registration ceremony.
func (s *Service) BeginWebAuthnRegistration(ctx context.Context, principal Principal, _ RequestMeta) (*WebAuthnOptions, error) {
	if s.webauthn == nil {
		return nil, NewAppError(503, CodeUnavailable, "WebAuthn is not configured")
	}
	if principal.Kind != "session" {
		return nil, forbiddenError("session authentication required")
	}
	user, err := s.store.GetUserByID(ctx, principal.UserID)
	if err != nil {
		return nil, sessionRevokedError()
	}
	credentials, err := s.store.GetCredentials(ctx, principal.UserID, CredentialWebAuthn)
	if err != nil {
		return nil, internalError(err)
	}
	options, sessionData, err := s.webauthn.BeginRegistration(user, credentials)
	if err != nil {
		return nil, NewAppError(400, CodeAuthWebAuthn, "WebAuthn registration could not be started")
	}
	now := s.now().UTC()
	challengeID, err := uuid.NewV7()
	if err != nil {
		return nil, internalError(err)
	}
	if err := s.store.CreateWebAuthnChallenge(ctx, &WebAuthnChallenge{
		ID: challengeID, UserID: principal.UserID, Kind: "registration",
		SessionData: sessionData, ExpiresAt: now.Add(s.cfg.WebAuthnTTL), CreatedAt: now,
	}); err != nil {
		return nil, internalError(err)
	}
	return &WebAuthnOptions{Options: options}, nil
}

// FinishWebAuthnRegistration validates and stores a WebAuthn credential.
func (s *Service) FinishWebAuthnRegistration(ctx context.Context, principal Principal, body []byte, meta RequestMeta) error {
	if s.webauthn == nil {
		return NewAppError(503, CodeUnavailable, "WebAuthn is not configured")
	}
	if principal.Kind != "session" {
		return forbiddenError("session authentication required")
	}
	now := s.now().UTC()
	challenge, err := s.store.LatestWebAuthnChallenge(ctx, principal.UserID, "registration", now)
	if err != nil {
		return NewAppError(400, CodeAuthWebAuthn, "WebAuthn challenge expired")
	}
	user, err := s.store.GetUserByID(ctx, principal.UserID)
	if err != nil {
		return sessionRevokedError()
	}
	credentials, err := s.store.GetCredentials(ctx, principal.UserID, CredentialWebAuthn)
	if err != nil {
		return internalError(err)
	}
	credential, err := s.webauthn.FinishRegistration(user, credentials, challenge.SessionData, body)
	if err != nil {
		_ = s.store.AppendAudit(ctx, NewAuditEvent("user", principal.UserID.String(), "mfa.webauthn.register", "user", principal.UserID.String(), "denied", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now))
		return NewAppError(400, CodeAuthWebAuthn, "WebAuthn registration verification failed")
	}
	metadata, err := json.Marshal(credential)
	if err != nil {
		return internalError(err)
	}
	credentialID, err := uuid.NewV7()
	if err != nil {
		return internalError(err)
	}
	audit := NewAuditEvent("user", principal.UserID.String(), "mfa.webauthn.register", "credential", credentialID.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	enrollAudit := NewAuditEvent("user", principal.UserID.String(), "mfa.enroll", "user", principal.UserID.String(), "success", map[string]string{"method": "webauthn"}, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.ConfirmWebAuthnRegistration(ctx, challenge.ID, principal.UserID, &Credential{
		ID: credentialID, UserID: principal.UserID, Kind: CredentialWebAuthn,
		SecretCiphertext: []byte{}, Metadata: metadata, CreatedAt: now, VerifiedAt: &now,
	}, now, audit, enrollAudit); err != nil {
		if errors.Is(err, ErrStoreConsumed) || errors.Is(err, ErrStoreExpired) || errors.Is(err, ErrStoreNotFound) {
			return NewAppError(400, CodeAuthWebAuthn, "WebAuthn challenge expired or already used")
		}
		return internalError(err)
	}
	return nil
}

// BeginWebAuthnLogin starts a passwordless assertion ceremony.
func (s *Service) BeginWebAuthnLogin(ctx context.Context, email string, _ RequestMeta) (*WebAuthnOptions, error) {
	if s.webauthn == nil {
		return nil, NewAppError(503, CodeUnavailable, "WebAuthn is not configured")
	}
	email, err := NormalizeEmail(email)
	if err != nil {
		return nil, invalidCredentialsError()
	}
	user, err := s.store.GetUserByEmailHash(ctx, s.hashPII("email", email))
	if err != nil || user.Status != UserActive {
		return nil, invalidCredentialsError()
	}
	credentials, err := s.store.GetCredentials(ctx, user.ID, CredentialWebAuthn)
	if err != nil || len(credentials) == 0 {
		return nil, invalidCredentialsError()
	}
	options, sessionData, err := s.webauthn.BeginLogin(user, credentials)
	if err != nil {
		return nil, NewAppError(400, CodeAuthWebAuthn, "WebAuthn login could not be started")
	}
	now := s.now().UTC()
	challengeID, err := uuid.NewV7()
	if err != nil {
		return nil, internalError(err)
	}
	if err := s.store.CreateWebAuthnChallenge(ctx, &WebAuthnChallenge{
		ID: challengeID, UserID: user.ID, Kind: "authentication",
		SessionData: sessionData, ExpiresAt: now.Add(s.cfg.WebAuthnTTL), CreatedAt: now,
	}); err != nil {
		return nil, internalError(err)
	}
	return &WebAuthnOptions{Options: options}, nil
}

// FinishWebAuthnLogin validates an assertion and creates a session.
func (s *Service) FinishWebAuthnLogin(ctx context.Context, email string, body []byte, meta RequestMeta) (*TokenPair, error) {
	if s.webauthn == nil {
		return nil, NewAppError(503, CodeUnavailable, "WebAuthn is not configured")
	}
	email, err := NormalizeEmail(email)
	if err != nil {
		return nil, invalidCredentialsError()
	}
	user, err := s.store.GetUserByEmailHash(ctx, s.hashPII("email", email))
	if err != nil || user.Status != UserActive {
		return nil, invalidCredentialsError()
	}
	now := s.now().UTC()
	challenge, err := s.store.LatestWebAuthnChallenge(ctx, user.ID, "authentication", now)
	if err != nil {
		return nil, NewAppError(400, CodeAuthWebAuthn, "WebAuthn challenge expired")
	}
	credentials, err := s.store.GetCredentials(ctx, user.ID, CredentialWebAuthn)
	if err != nil {
		return nil, internalError(err)
	}
	credential, err := s.webauthn.FinishLogin(user, credentials, challenge.SessionData, body)
	if err != nil {
		_ = s.store.AppendAudit(ctx, NewAuditEvent("user", user.ID.String(), "auth.webauthn.login", "user", user.ID.String(), "denied", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now))
		return nil, invalidCredentialsError()
	}
	// A non-increasing signature counter is the WebAuthn signal that the
	// credential private key may exist on a cloned authenticator. Fail closed
	// and leave the stored counter untouched instead of accepting either
	// copy.
	if credential.Authenticator.CloneWarning {
		_ = s.store.AppendAudit(ctx, NewAuditEvent("user", user.ID.String(), "auth.webauthn.clone_warning", "user", user.ID.String(), "denied", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now))
		return nil, NewAppError(http.StatusUnauthorized, CodeAuthWebAuthn, "WebAuthn authenticator counter regression detected")
	}
	// Persist the updated signature counter and consume the challenge atomically
	// so a cloned authenticator cannot replay an assertion.
	matched := -1
	for i := range credentials {
		var parsed struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(credentials[i].Metadata, &parsed) != nil || parsed.ID == "" {
			continue
		}
		if parsed.ID == base64.RawURLEncoding.EncodeToString(credential.ID) || parsed.ID == base64.StdEncoding.EncodeToString(credential.ID) {
			matched = i
			break
		}
	}
	if matched < 0 {
		return nil, NewAppError(400, CodeAuthWebAuthn, "WebAuthn credential could not be matched")
	}
	updatedMetadata, err := json.Marshal(credential)
	if err != nil {
		return nil, internalError(err)
	}
	counterAudit := NewAuditEvent("user", user.ID.String(), "auth.webauthn.counter", "credential", credentials[matched].ID.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.CompleteWebAuthnAssertion(ctx, challenge.ID, user.ID, credentials[matched].ID, updatedMetadata, now, counterAudit); err != nil {
		if errors.Is(err, ErrStoreConsumed) || errors.Is(err, ErrStoreExpired) || errors.Is(err, ErrStoreNotFound) {
			return nil, NewAppError(400, CodeAuthWebAuthn, "WebAuthn challenge expired or already used")
		}
		return nil, internalError(err)
	}
	result, err := s.completeLogin(ctx, user, meta, now, true, "auth.webauthn.login")
	if err != nil {
		return nil, err
	}
	return result.Tokens, nil
}

// Password reset request and confirmation.

// RequestPasswordReset always returns success for valid input.
func (s *Service) RequestPasswordReset(ctx context.Context, email string, meta RequestMeta) error {
	email, err := NormalizeEmail(email)
	if err != nil {
		return nil
	}
	user, err := s.store.GetUserByEmailHash(ctx, s.hashPII("email", email))
	if err != nil || user.Status == UserClosed {
		return nil
	}
	now := s.now().UTC()
	token, err := s.newVerification(user.ID, "password_reset", now.Add(15*time.Minute), now)
	if err != nil {
		return nil
	}
	audit := NewAuditEvent("user", user.ID.String(), "auth.password.reset.request", "user", user.ID.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.CreateVerification(ctx, token.VerificationToken, audit); err != nil {
		return nil
	}
	if s.notifier != nil {
		_ = s.notifier.Send(ctx, NotificationRequest{UserID: user.ID, Channel: "email", Kind: "password_reset", Destination: email, Token: token.plaintext})
	}
	return nil
}

// ConfirmPasswordReset consumes a reset token, updates the password, and
// revokes every session and API key.
func (s *Service) ConfirmPasswordReset(ctx context.Context, rawToken, newPassword string, meta RequestMeta) error {
	if err := ValidatePasswordPolicy(newPassword, ""); err != nil {
		return validationError(err.Error())
	}
	now := s.now().UTC()
	hash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return internalError(err)
	}
	audit := NewAuditEvent("user", "", "auth.password.reset", "user", "", "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	sessionAudit := NewAuditEvent("system", "", "auth.password.reset.revoke_sessions", "user", "", "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	keyAudit := NewAuditEvent("system", "", "auth.password.reset.revoke_api_keys", "user", "", "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	_, err = s.store.CompletePasswordReset(ctx, SHA256([]byte(rawToken)), hash, now, audit, sessionAudit, keyAudit)
	if err != nil {
		if errors.Is(err, ErrStoreExpired) || errors.Is(err, ErrStoreConsumed) || errors.Is(err, ErrStoreNotFound) {
			return validationError("invalid or expired reset token")
		}
		return internalError(err)
	}
	return nil
}

// API key management.

// CreateAPIKeyInput describes a requested API key and its restrictions.
type CreateAPIKeyInput struct {
	Label         string
	Scopes        []string
	IPAllowlist   []string
	Algorithm     string
	PublicKey     string
	ExpiresInDays int
}

// CreatedAPIKey includes the one-time secret for HMAC keys.
type CreatedAPIKey struct {
	ID          uuid.UUID
	Label       string
	KeyPrefix   string
	Algorithm   string
	Secret      string `json:"secret,omitempty"`
	Scopes      []string
	IPAllowlist []string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
}

// APIKeyView never contains secret material.
type APIKeyView struct {
	ID          uuid.UUID
	Label       string
	KeyPrefix   string
	Algorithm   string
	Scopes      []string
	IPAllowlist []string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
	Active      bool
}

// CreateAPIKey creates a scoped machine credential. The secret is returned
// exactly once and is never retrievable again.
func (s *Service) CreateAPIKey(ctx context.Context, principal Principal, input CreateAPIKeyInput, meta RequestMeta) (*CreatedAPIKey, error) {
	if principal.Kind != "session" {
		return nil, forbiddenError("session authentication required")
	}
	label := strings.TrimSpace(input.Label)
	if len(label) == 0 || len(label) > 64 {
		return nil, validationError("API key label must be between 1 and 64 characters")
	}
	scopes, err := ParseScopes(input.Scopes)
	if err != nil {
		return nil, validationError(err.Error())
	}
	prefixes, err := ParsePrefixes(input.IPAllowlist)
	if err != nil {
		return nil, validationError(err.Error())
	}
	algorithm := strings.ToLower(strings.TrimSpace(input.Algorithm))
	if algorithm == "" {
		algorithm = "hmac-sha256"
	}
	if algorithm != "hmac-sha256" && algorithm != "ed25519" {
		return nil, validationError("unsupported API key algorithm")
	}
	now := s.now().UTC()
	expiry := now.Add(90 * 24 * time.Hour)
	if input.ExpiresInDays > 0 {
		if input.ExpiresInDays > 365 {
			return nil, validationError("API key expiry cannot exceed 365 days")
		}
		expiry = now.Add(time.Duration(input.ExpiresInDays) * 24 * time.Hour)
	}
	keyID, err := uuid.NewV7()
	if err != nil {
		return nil, internalError(err)
	}
	prefix, err := RandomToken(9)
	if err != nil {
		return nil, internalError(err)
	}
	key := &APIKey{
		ID: keyID, UserID: principal.UserID, Label: label,
		KeyPrefix: prefix, Algorithm: algorithm, Scopes: scopes, IPAllowlist: prefixes,
		CreatedAt: now, ExpiresAt: &expiry,
	}
	created := &CreatedAPIKey{
		ID: keyID, Label: label, KeyPrefix: key.KeyPrefix, Algorithm: algorithm,
		Scopes: scopes, CreatedAt: now, ExpiresAt: &expiry,
	}
	for _, prefix := range prefixes {
		created.IPAllowlist = append(created.IPAllowlist, prefix.String())
	}
	switch algorithm {
	case "hmac-sha256":
		rawSecret, err := RandomToken(32)
		if err != nil {
			return nil, internalError(err)
		}
		encrypted, err := s.secretCipher.Encrypt([]byte(rawSecret))
		if err != nil {
			return nil, internalError(err)
		}
		key.SecretCiphertext = encrypted
		created.Secret = rawSecret
	case "ed25519":
		publicKey, err := decodePublicKey(input.PublicKey)
		if err != nil {
			return nil, validationError("invalid Ed25519 public key")
		}
		key.PublicKey = publicKey
	}
	audit := NewAuditEvent("user", principal.UserID.String(), "api_key.create", "api_key", keyID.String(), "success",
		map[string]string{"algorithm": algorithm, "scopes": strings.Join(scopes, ",")}, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.CreateAPIKey(ctx, key, s.cfg.MaxAPIKeys, now, audit); err != nil {
		if errors.Is(err, ErrStoreLimit) {
			return nil, conflictError("maximum active API keys reached")
		}
		return nil, internalError(err)
	}
	return created, nil
}

// ListAPIKeys lists caller-owned key metadata without secrets.
func (s *Service) ListAPIKeys(ctx context.Context, principal Principal) ([]APIKeyView, error) {
	if principal.Kind != "session" {
		return nil, forbiddenError("session authentication required")
	}
	keys, err := s.store.ListAPIKeys(ctx, principal.UserID)
	if err != nil {
		return nil, internalError(err)
	}
	now := s.now().UTC()
	out := make([]APIKeyView, 0, len(keys))
	for _, key := range keys {
		view := APIKeyView{
			ID: key.ID, Label: key.Label, KeyPrefix: key.KeyPrefix, Algorithm: key.Algorithm,
			Scopes: append([]string(nil), key.Scopes...), CreatedAt: key.CreatedAt,
			LastUsedAt: key.LastUsedAt, ExpiresAt: key.ExpiresAt, RevokedAt: key.RevokedAt,
			Active: key.Active(now),
		}
		for _, prefix := range key.IPAllowlist {
			view.IPAllowlist = append(view.IPAllowlist, prefix.String())
		}
		out = append(out, view)
	}
	return out, nil
}

// RevokeAPIKey revokes a caller-owned key.
func (s *Service) RevokeAPIKey(ctx context.Context, principal Principal, keyID uuid.UUID, meta RequestMeta) error {
	if principal.Kind != "session" {
		return forbiddenError("session authentication required")
	}
	now := s.now().UTC()
	audit := NewAuditEvent("user", principal.UserID.String(), "api_key.revoke", "api_key", keyID.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.RevokeAPIKey(ctx, keyID, &principal.UserID, now, audit); err != nil {
		if errors.Is(err, ErrStoreNotFound) {
			return notFoundError()
		}
		return internalError(err)
	}
	return nil
}

// APIKeyAuth is a signed-request authentication envelope.
type APIKeyAuth struct {
	KeyID     string
	Timestamp string
	Nonce     string
	Signature string
	Method    string
	Path      string
	Body      []byte
	IP        netip.Addr
}

// AuthenticateAPIKey verifies an HMAC or Ed25519 signed request, timestamp,
// nonce replay, key status, and IP allowlist. It never compares secrets with a
// variable-time operation.
func (s *Service) AuthenticateAPIKey(ctx context.Context, auth APIKeyAuth, meta RequestMeta) (Principal, error) {
	keyID, err := uuid.Parse(strings.TrimSpace(auth.KeyID))
	if err != nil {
		return Principal{}, invalidTokenError()
	}
	key, err := s.store.GetAPIKey(ctx, keyID)
	if err != nil {
		return Principal{}, invalidTokenError()
	}
	now := s.now().UTC()
	keyUser, err := s.store.GetUserByID(ctx, key.UserID)
	if err != nil || keyUser.Status != UserActive {
		return Principal{}, invalidTokenError()
	}
	if key.RevokedAt != nil {
		return Principal{}, NewAppError(401, CodeAuthKeyRevoked, "API key has been revoked")
	}
	if key.Expired(now) {
		return Principal{}, NewAppError(401, CodeAuthKeyExpired, "API key has expired")
	}
	addr := auth.IP
	if meta.IP.IsValid() {
		addr = meta.IP
	}
	if !addr.IsValid() || !key.IPAllowed(addr) {
		return Principal{}, NewAppError(403, CodeAuthIPNotAllowed, "source IP is not allowed for this API key")
	}
	timestamp, err := parseUnixTimestamp(auth.Timestamp)
	if err != nil || absDuration(now.Sub(timestamp)) > 5*time.Minute {
		return Principal{}, NewAppError(401, CodeAuthTimestamp, "request timestamp is outside the allowed window")
	}
	nonce := strings.TrimSpace(auth.Nonce)
	if len(nonce) < 16 || len(nonce) > 128 || !isASCIIAlnum(nonce) {
		return Principal{}, invalidTokenError()
	}
	if strings.TrimSpace(auth.Signature) == "" {
		return Principal{}, invalidTokenError()
	}
	canonical := auth.Method + "\n" + auth.Path + "\n" + auth.Timestamp + "\n" + nonce + "\n" + hex.EncodeToString(SHA256(auth.Body))
	switch key.Algorithm {
	case "hmac-sha256":
		secret, err := s.secretCipher.Decrypt(key.SecretCiphertext)
		if err != nil {
			return Principal{}, internalError(err)
		}
		mac := hmacSHA256(secret, []byte(canonical))
		signature, err := decodeSignature(auth.Signature)
		if err != nil || !SecureCompare(mac, signature) {
			return Principal{}, NewAppError(401, CodeAuthInvalidSignature, "invalid request signature")
		}
	case "ed25519":
		signature, err := decodeSignature(auth.Signature)
		if err != nil || len(key.PublicKey) != ed25519.PublicKeySize || !ed25519.Verify(ed25519.PublicKey(key.PublicKey), []byte(canonical), signature) {
			return Principal{}, NewAppError(401, CodeAuthInvalidSignature, "invalid request signature")
		}
	default:
		return Principal{}, invalidTokenError()
	}
	used, err := s.store.UseAPIKeyNonce(ctx, key.ID, nonce, now.Add(10*time.Minute), now)
	if err != nil {
		return Principal{}, internalError(err)
	}
	if !used {
		return Principal{}, NewAppError(401, CodeAuthInvalidSignature, "request nonce has already been used")
	}
	_ = s.store.TouchAPIKey(ctx, key.ID, now)
	return Principal{UserID: key.UserID, APIKeyID: key.ID, Scopes: append([]string(nil), key.Scopes...), Kind: "api_key"}, nil
}

// Admin and KYC operations.

// AdminUserView is a redacted administrative view.
type AdminUserView struct {
	ID            uuid.UUID
	Email         string
	Phone         string
	Status        UserStatus
	KYCLevel      int16
	Roles         []Role
	EmailVerified bool
	PhoneVerified bool
	MFAEnrolled   bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// AdminGetUser returns a masked administrative view of a user.
func (s *Service) AdminGetUser(ctx context.Context, actor Principal, target uuid.UUID) (*AdminUserView, error) {
	if !actor.HasPermission(PermAdminUserRead) {
		return nil, forbiddenError("insufficient permission")
	}
	user, err := s.store.GetUserByID(ctx, target)
	if err != nil {
		return nil, notFoundError()
	}
	email := s.maskEmail(user)
	phone := s.maskPhone(user)
	return &AdminUserView{
		ID: user.ID, Email: email, Phone: phone, Status: user.Status, KYCLevel: user.KYCLevel,
		Roles: user.Roles, EmailVerified: user.EmailVerifiedAt != nil, PhoneVerified: user.PhoneVerifiedAt != nil,
		MFAEnrolled: user.MFAEnrolledAt != nil, CreatedAt: user.CreatedAt, UpdatedAt: user.UpdatedAt,
	}, nil
}

// AdminSetStatus freezes or unfreezes an account and revokes live credentials
// when freezing.
func (s *Service) AdminSetStatus(ctx context.Context, actor Principal, target uuid.UUID, status UserStatus, meta RequestMeta) error {
	if !actor.HasPermission(PermAdminUserFreeze) {
		return forbiddenError("insufficient permission")
	}
	if status != UserActive && status != UserFrozen && status != UserClosed {
		return validationError("invalid account status")
	}
	now := s.now().UTC()
	if status != UserActive {
		targetUser, err := s.store.GetUserByID(ctx, target)
		if err != nil {
			return notFoundError()
		}
		if targetUser.HasRole(RoleAdmin) && status == UserFrozen {
			return forbiddenError("administrator accounts require a separate break-glass procedure")
		}
	}
	audit := NewAuditEvent("admin", actor.UserID.String(), "admin.user.status", "user", target.String(), "success", map[string]string{"status": string(status)}, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.SetUserStatus(ctx, target, status, now, audit); err != nil {
		if errors.Is(err, ErrStoreNotFound) {
			return notFoundError()
		}
		return internalError(err)
	}
	return nil
}

// AdminSetRoles replaces a user's roles with strict customer/admin separation.
func (s *Service) AdminSetRoles(ctx context.Context, actor Principal, target uuid.UUID, roles []Role, meta RequestMeta) error {
	if !actor.HasPermission(PermAdminRoleManage) {
		return forbiddenError("insufficient permission")
	}
	if len(roles) == 0 {
		return validationError("at least one role is required")
	}
	seen := map[Role]bool{}
	hasAdmin, hasCustomer := false, false
	for _, role := range roles {
		if !ValidRole(role) {
			return validationError("unknown role")
		}
		if seen[role] {
			return validationError("duplicate role")
		}
		seen[role] = true
		if role == RoleAdmin {
			hasAdmin = true
		}
		if role == RoleCustomer {
			hasCustomer = true
		}
	}
	if hasAdmin && hasCustomer {
		return validationError("admin and customer roles cannot be combined")
	}
	now := s.now().UTC()
	audit := NewAuditEvent("admin", actor.UserID.String(), "admin.user.roles", "user", target.String(), "success", map[string]string{"roles": joinRoles(roles)}, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.SetUserRoles(ctx, target, roles, actor.UserID, now, audit); err != nil {
		if errors.Is(err, ErrStoreNotFound) {
			return notFoundError()
		}
		return internalError(err)
	}
	return nil
}

// AdminRevokeSessions revokes all sessions for a target user.
func (s *Service) AdminRevokeSessions(ctx context.Context, actor Principal, target uuid.UUID, meta RequestMeta) (int, error) {
	if !actor.HasPermission(PermAdminSessionRevoke) {
		return 0, forbiddenError("insufficient permission")
	}
	now := s.now().UTC()
	audit := NewAuditEvent("admin", actor.UserID.String(), "admin.session.revoke_all", "user", target.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	count, err := s.store.RevokeAllSessions(ctx, target, nil, now, audit)
	if err != nil {
		return 0, internalError(err)
	}
	return count, nil
}

// AdminRevokeAPIKeys revokes all API keys for a target user.
func (s *Service) AdminRevokeAPIKeys(ctx context.Context, actor Principal, target uuid.UUID, meta RequestMeta) (int, error) {
	if !actor.HasPermission(PermAdminAPIKeyRevoke) {
		return 0, forbiddenError("insufficient permission")
	}
	now := s.now().UTC()
	audit := NewAuditEvent("admin", actor.UserID.String(), "admin.api_key.revoke_all", "user", target.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	count, err := s.store.RevokeAllAPIKeys(ctx, target, now, audit)
	if err != nil {
		return 0, internalError(err)
	}
	return count, nil
}

// AdminListAudit returns append-only audit events.
func (s *Service) AdminListAudit(ctx context.Context, actor Principal, filter AuditFilter) ([]AuditEvent, error) {
	if !actor.HasPermission(PermAdminAuditRead) {
		return nil, forbiddenError("insufficient permission")
	}
	events, err := s.store.ListAudit(ctx, filter)
	if err != nil {
		return nil, internalError(err)
	}
	return events, nil
}

// SubmitKYC records a provider case reference; it does not perform provider
// verification.
func (s *Service) SubmitKYC(ctx context.Context, principal Principal, providerRef string, level int16, meta RequestMeta) (*KYCCase, error) {
	if principal.Kind != "session" {
		return nil, forbiddenError("session authentication required")
	}
	if len(providerRef) > 128 {
		return nil, validationError("provider reference is too long")
	}
	if level < 0 || level > 5 {
		return nil, validationError("invalid KYC level")
	}
	now := s.now().UTC()
	id, err := uuid.NewV7()
	if err != nil {
		return nil, internalError(err)
	}
	kyc := &KYCCase{ID: id, UserID: principal.UserID, ProviderRef: providerRef, Status: KYCSubmitted, Level: level, SubmittedAt: now, UpdatedAt: now}
	audit := NewAuditEvent("user", principal.UserID.String(), "kyc.submit", "kyc_case", id.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.CreateKYCCase(ctx, kyc, audit); err != nil {
		return nil, internalError(err)
	}
	return kyc, nil
}

// ListOwnKYC lists caller-owned cases only.
func (s *Service) ListOwnKYC(ctx context.Context, principal Principal) ([]KYCCase, error) {
	if principal.Kind != "session" {
		return nil, forbiddenError("session authentication required")
	}
	cases, err := s.store.ListKYCCases(ctx, &principal.UserID, 50)
	if err != nil {
		return nil, internalError(err)
	}
	return cases, nil
}

// AdminListKYC lists cases for compliance/admin review.
func (s *Service) AdminListKYC(ctx context.Context, actor Principal, limit int) ([]KYCCase, error) {
	if !actor.HasPermission(PermAdminKYCRead) {
		return nil, forbiddenError("insufficient permission")
	}
	cases, err := s.store.ListKYCCases(ctx, nil, limit)
	if err != nil {
		return nil, internalError(err)
	}
	return cases, nil
}

// AdminDecideKYC records a compliance decision.
func (s *Service) AdminDecideKYC(ctx context.Context, actor Principal, caseID uuid.UUID, status KYCStatus, level int16, reason string, meta RequestMeta) error {
	if !actor.HasPermission(PermAdminKYCDecide) {
		return forbiddenError("insufficient permission")
	}
	if status != KYCApproved && status != KYCRejected && status != KYCInReview {
		return validationError("invalid KYC decision")
	}
	if len(reason) > 512 {
		return validationError("reason is too long")
	}
	now := s.now().UTC()
	audit := NewAuditEvent("admin", actor.UserID.String(), "admin.kyc.decision", "kyc_case", caseID.String(), "success", map[string]string{"status": string(status)}, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.DecideKYCCase(ctx, caseID, status, level, reason, actor.UserID, now, audit); err != nil {
		if errors.Is(err, ErrStoreNotFound) {
			return notFoundError()
		}
		return internalError(err)
	}
	return nil
}

// BootstrapAdmin creates or promotes an administrator for an out-of-band
// bootstrap procedure. It must only be invoked by an operator CLI.
func (s *Service) BootstrapAdmin(ctx context.Context, email, password string, meta RequestMeta) (*uuid.UUID, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return nil, validationError("invalid email")
	}
	if err := ValidatePasswordPolicy(password, email); err != nil {
		return nil, validationError(err.Error())
	}
	now := s.now().UTC()
	hash := s.hashPII("email", email)
	existing, lookupErr := s.store.GetUserByEmailHash(ctx, hash)
	if lookupErr == nil && existing != nil {
		roles := []Role{RoleAdmin}
		audit := NewAuditEvent("operator", "bootstrap", "admin.bootstrap.promote", "user", existing.ID.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
		if err := s.store.SetUserRoles(ctx, existing.ID, roles, existing.ID, now, audit); err != nil {
			return nil, internalError(err)
		}
		statusAudit := NewAuditEvent("operator", "bootstrap", "admin.bootstrap.activate", "user", existing.ID.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
		if err := s.store.SetUserStatus(ctx, existing.ID, UserActive, now, statusAudit); err != nil {
			return nil, internalError(err)
		}
		return &existing.ID, nil
	}
	if lookupErr != nil && !errors.Is(lookupErr, ErrStoreNotFound) {
		return nil, internalError(lookupErr)
	}
	passwordHash, err := s.hasher.Hash(password)
	if err != nil {
		return nil, internalError(err)
	}
	ciphertext, err := s.piiCipher.Encrypt([]byte(email))
	if err != nil {
		return nil, internalError(err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, internalError(err)
	}
	verified := now
	user := &User{
		ID: id, EmailCiphertext: ciphertext, EmailHash: hash, PasswordHash: passwordHash,
		Status: UserActive, KYCLevel: 0, EmailVerifiedAt: &verified,
		CreatedAt: now, UpdatedAt: now, Roles: []Role{RoleAdmin},
	}
	credential := &Credential{UserID: id, Kind: CredentialPassword, SecretCiphertext: []byte(passwordHash), CreatedAt: now}
	audit := NewAuditEvent("operator", "bootstrap", "admin.bootstrap", "user", id.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now)
	if err := s.store.CreateUser(ctx, user, credential, nil, audit); err != nil {
		return nil, internalError(err)
	}
	return &id, nil
}

// Helpers.

func (s *Service) verifySecondFactor(ctx context.Context, user *User, totpCode, recoveryCode string, meta RequestMeta, now time.Time) (bool, *AppError) {
	totpCode = strings.TrimSpace(totpCode)
	recoveryCode = strings.TrimSpace(recoveryCode)
	if totpCode == "" && recoveryCode == "" {
		return false, nil
	}
	if recoveryCode != "" {
		code := normalizeRecoveryCode(recoveryCode)
		used, err := s.store.ConsumeRecoveryCode(ctx, user.ID, SHA256([]byte(code)), now)
		if err != nil {
			return false, internalError(err)
		}
		if used {
			_ = s.store.AppendAudit(ctx, NewAuditEvent("user", user.ID.String(), "auth.recovery_code.use", "user", user.ID.String(), "success", nil, s.ipHash(meta), meta.UserAgent, meta.RequestID, now))
		}
		return used, nil
	}
	credentials, err := s.store.GetCredentials(ctx, user.ID, CredentialTOTP)
	if err != nil {
		return false, internalError(err)
	}
	for _, credential := range credentials {
		if credential.VerifiedAt == nil || credential.RevokedAt != nil {
			continue
		}
		secret, err := s.secretCipher.Decrypt(credential.SecretCiphertext)
		if err != nil {
			continue
		}
		step, ok, err := VerifyTOTP(string(secret), totpCode, now, TOTPSkew, TOTPDigits, TOTPPeriod)
		if err != nil || !ok {
			continue
		}
		consumed, err := s.store.MarkTOTPUsed(ctx, credential.ID, step, now)
		if err != nil {
			return false, internalError(err)
		}
		if !consumed {
			return false, mfaReplayError()
		}
		return true, nil
	}
	return false, nil
}

func (s *Service) mfaMethods(ctx context.Context, user *User) []string {
	if user == nil || user.MFAEnrolledAt == nil {
		return nil
	}
	var methods []string
	if credentials, err := s.store.GetCredentials(ctx, user.ID, CredentialTOTP); err == nil {
		for _, credential := range credentials {
			if credential.VerifiedAt != nil {
				methods = append(methods, "totp")
				break
			}
		}
	}
	if credentials, err := s.store.GetCredentials(ctx, user.ID, CredentialWebAuthn); err == nil && len(credentials) > 0 {
		methods = append(methods, "webauthn")
	}
	if credentials, err := s.store.GetCredentials(ctx, user.ID, CredentialRecoveryCode); err == nil && len(credentials) > 0 {
		methods = append(methods, "recovery_code")
	}
	sort.Strings(methods)
	return methods
}

func (s *Service) recordLoginFailure(ctx context.Context, subjectHash, ipHash []byte, meta RequestMeta, now time.Time, reason string) {
	audit := NewAuditEvent("anonymous", "", "auth.login.failure", "user", "", "denied", map[string]string{"reason": reason}, ipHash, meta.UserAgent, meta.RequestID, now)
	state, err := s.store.RecordLoginFailure(ctx, subjectHash, ipHash, s.lockout, now, audit)
	if err != nil {
		slog.Error("identity login failure accounting failed", "error", err)
		return
	}
	if state.LockedUntil != nil {
		slog.Warn("identity account temporarily locked", "retry_after", state.RetryAfter(now).String())
	}
}

func (s *Service) hashPII(domain, value string) []byte {
	return KeyedHash(s.piiPepper, domain, value)
}

func (s *Service) ipHash(meta RequestMeta) []byte {
	if !meta.IP.IsValid() {
		return KeyedHash(s.ipPepper, "ip", "unknown")
	}
	return KeyedHash(s.ipPepper, "ip", meta.IP.String())
}

func (s *Service) maskEmail(user *User) string {
	plain, err := s.piiCipher.Decrypt(user.EmailCiphertext)
	if err != nil {
		return "***"
	}
	email := string(plain)
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 || len(parts[0]) == 0 {
		return "***"
	}
	local := parts[0]
	if len(local) <= 2 {
		return "*@" + parts[1]
	}
	return local[:1] + strings.Repeat("*", len(local)-2) + local[len(local)-1:] + "@" + parts[1]
}

func (s *Service) maskPhone(user *User) string {
	if len(user.PhoneCiphertext) == 0 {
		return ""
	}
	plain, err := s.piiCipher.Decrypt(user.PhoneCiphertext)
	if err != nil {
		return "***"
	}
	phone := string(plain)
	if len(phone) <= 4 {
		return "***"
	}
	return strings.Repeat("*", len(phone)-4) + phone[len(phone)-4:]
}

// NormalizeEmail validates and canonicalizes an email address.
func NormalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 254 || strings.Count(value, "@") != 1 {
		return "", errors.New("invalid email")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value {
		return "", errors.New("invalid email")
	}
	parts := strings.SplitN(value, "@", 2)
	if len(parts[0]) == 0 || len(parts[0]) > 64 || len(parts[1]) == 0 || !strings.Contains(parts[1], ".") {
		return "", errors.New("invalid email")
	}
	return value, nil
}

// NormalizePhone validates E.164 format.
func NormalizePhone(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 8 || len(value) > 16 || value[0] != '+' {
		return "", errors.New("invalid phone")
	}
	for _, r := range value[1:] {
		if r < '0' || r > '9' {
			return "", errors.New("invalid phone")
		}
	}
	return value, nil
}

func decodePublicKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("public key is required")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(value)
	}
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("invalid Ed25519 public key")
	}
	allZero := true
	for _, b := range decoded {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return nil, errors.New("invalid Ed25519 public key")
	}
	return decoded, nil
}

func decodeSignature(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if decoded, err := hex.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(value)
}

func parseUnixTimestamp(value string) (time.Time, error) {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func formatRecoveryCode(raw string) string {
	normalized := normalizeRecoveryCode(raw)
	if len(normalized) > 16 {
		normalized = normalized[:16]
	}
	var out strings.Builder
	for i, r := range normalized {
		if i > 0 && i%4 == 0 {
			out.WriteByte('-')
		}
		out.WriteRune(r)
	}
	return out.String()
}

func normalizeRecoveryCode(value string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
}

func joinRoles(roles []Role) string {
	values := make([]string, 0, len(roles))
	for _, role := range roles {
		values = append(values, string(role))
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

func cleanText(value string, maxLength int) string {
	value = strings.TrimSpace(value)
	if len(value) > maxLength {
		value = value[:maxLength]
	}
	return value
}

func isAlpha(value string) bool {
	for _, r := range value {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

func isASCIIAlnum(value string) bool {
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func internalError(cause error) *AppError {
	return WrapAppError(500, CodeInternal, "internal server error", cause)
}
