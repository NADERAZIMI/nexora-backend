// Package identity implements the NEXORA identity service domain: users,
// credentials, sessions, API keys, MFA, RBAC, audit, and account protection.
//
//nolint:revive
package identity

import (
	"encoding/json"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// UserStatus is the lifecycle state of a user account.
type UserStatus string

const (
	UserPending UserStatus = "pending"
	UserActive  UserStatus = "active"
	UserFrozen  UserStatus = "frozen"
	UserClosed  UserStatus = "closed"
)

// Role is a customer or administrative role. Roles are deny-by-default: a
// principal with no recognized role has no permissions.
type Role string

const (
	RoleCustomer     Role = "customer"
	RoleSupport      Role = "support"
	RoleRiskOperator Role = "risk_operator"
	RoleCompliance   Role = "compliance"
	RoleTreasury     Role = "treasury"
	RoleAdmin        Role = "admin"
	RoleAuditor      Role = "auditor"
)

var validRoles = map[Role]bool{
	RoleCustomer: true, RoleSupport: true, RoleRiskOperator: true,
	RoleCompliance: true, RoleTreasury: true, RoleAdmin: true, RoleAuditor: true,
}

// ParseRole validates a role name exactly.
func ParseRole(value string) (Role, error) {
	role := Role(strings.TrimSpace(value))
	if !validRoles[role] {
		return "", errors.New("unknown role")
	}
	return role, nil
}

// ValidRole reports whether role is a recognized role.
func ValidRole(role Role) bool { return validRoles[role] }

// CredentialKind identifies a credential type.
type CredentialKind string

const (
	CredentialPassword     CredentialKind = "password"
	CredentialTOTP         CredentialKind = "totp"
	CredentialWebAuthn     CredentialKind = "webauthn"
	CredentialRecoveryCode CredentialKind = "recovery_code"
)

// User is an identity record. Email and phone are encrypted at rest; EmailHash
// and PhoneHash are keyed hashes used for equality lookup without decryption.
type User struct {
	ID              uuid.UUID
	EmailCiphertext []byte
	EmailHash       []byte
	PhoneCiphertext []byte
	PhoneHash       []byte
	PasswordHash    string
	Status          UserStatus
	KYCLevel        int16
	CountryCode     *string
	EmailVerifiedAt *time.Time
	PhoneVerifiedAt *time.Time
	MFAEnrolledAt   *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Version         int64
	Roles           []Role
}

// HasRole reports whether the user holds role.
func (u *User) HasRole(role Role) bool {
	for _, candidate := range u.Roles {
		if candidate == role {
			return true
		}
	}
	return false
}

// Credential stores a password hash reference or a second-factor secret. TOTP
// secret material is encrypted with the service secret cipher.
type Credential struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	Kind             CredentialKind
	SecretCiphertext []byte
	Metadata         json.RawMessage
	CreatedAt        time.Time
	LastUsedAt       *time.Time
	VerifiedAt       *time.Time
	RevokedAt        *time.Time
	TOTPLastStep     *int64
}

// Session is an authenticated UI session. ExpiresAt is the absolute session
// expiry; individual refresh tokens carry their own expiry.
type Session struct {
	ID                uuid.UUID
	UserID            uuid.UUID
	DeviceFingerprint string
	UserAgent         string
	IPHash            []byte
	MFA               bool
	CreatedAt         time.Time
	LastSeenAt        time.Time
	ExpiresAt         time.Time
	RevokedAt         *time.Time
}

// RefreshTokenStatus tracks the state of a rotating refresh token.
type RefreshTokenStatus string

const (
	RefreshActive  RefreshTokenStatus = "active"
	RefreshRotated RefreshTokenStatus = "rotated"
	RefreshRevoked RefreshTokenStatus = "revoked"
)

// RefreshToken stores only a SHA-256 hash of a refresh token. Rotated rows are
// retained so that reuse can be detected and the whole session family revoked.
type RefreshToken struct {
	ID        uuid.UUID
	SessionID uuid.UUID
	TokenHash []byte
	Status    RefreshTokenStatus
	ExpiresAt time.Time
	CreatedAt time.Time
	RotatedAt *time.Time
}

// APIKey is a machine credential. SecretCiphertext is only present for HMAC
// keys; Ed25519 keys store PublicKey instead.
type APIKey struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	Label            string
	KeyPrefix        string
	Algorithm        string // hmac-sha256 or ed25519
	SecretCiphertext []byte
	PublicKey        []byte
	Scopes           []string
	IPAllowlist      []netip.Prefix
	CreatedAt        time.Time
	LastUsedAt       *time.Time
	ExpiresAt        *time.Time
	RevokedAt        *time.Time
}

// Expired reports whether the key is expired at now.
func (k *APIKey) Expired(now time.Time) bool {
	return k.ExpiresAt != nil && !now.Before(*k.ExpiresAt)
}

// Active reports whether the key can authenticate at now.
func (k *APIKey) Active(now time.Time) bool {
	return k.RevokedAt == nil && !k.Expired(now)
}

// HasScope reports whether the key holds scope.
func (k *APIKey) HasScope(scope string) bool {
	for _, candidate := range k.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

// IPAllowed reports whether addr is permitted by the key allowlist. An empty
// allowlist is unrestricted.
func (k *APIKey) IPAllowed(addr netip.Addr) bool {
	if len(k.IPAllowlist) == 0 {
		return true
	}
	for _, prefix := range k.IPAllowlist {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// AuditEvent is an append-only, hash-chained security event.
type AuditEvent struct {
	ID         uuid.UUID
	OccurredAt time.Time
	ActorType  string
	ActorID    string
	Action     string
	TargetType string
	TargetID   string
	Outcome    string
	IPHash     []byte
	UserAgent  string
	RequestID  string
	Metadata   map[string]string
	PrevHash   []byte
	EventHash  []byte
}

// VerificationToken is a one-time email, phone, or password-reset token. Only
// its hash is stored.
type VerificationToken struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Kind       string // email, phone, password_reset
	TokenHash  []byte
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

// MFAChallenge binds a first-factor login to a short-lived second-factor step.
type MFAChallenge struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	TokenHash  []byte
	Methods    []string
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

// WebAuthnChallenge stores server-side ceremony state. It is never exposed to
// the client beyond the options document.
type WebAuthnChallenge struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	Kind        string // registration or authentication
	SessionData json.RawMessage
	ExpiresAt   time.Time
	ConsumedAt  *time.Time
	CreatedAt   time.Time
}

// LoginAttempt is a distributed account/IP protection counter.
type LoginAttempt struct {
	SubjectHash  []byte
	IPHash       []byte
	Failures     int
	FirstFailure time.Time
	LastFailure  time.Time
	BackoffUntil *time.Time
	LockedUntil  *time.Time
}

// LockState is the effective protection state for a login attempt.
type LockState struct {
	Failures        int
	BackoffUntil    *time.Time
	LockedUntil     *time.Time
	CaptchaRequired bool
}

// Blocked reports whether authentication is currently blocked.
func (s LockState) Blocked(now time.Time) bool {
	return (s.LockedUntil != nil && now.Before(*s.LockedUntil)) ||
		(s.BackoffUntil != nil && now.Before(*s.BackoffUntil))
}

// RetryAfter returns the remaining block duration, rounded up to seconds.
func (s LockState) RetryAfter(now time.Time) time.Duration {
	var until time.Time
	if s.LockedUntil != nil && s.LockedUntil.After(until) {
		until = *s.LockedUntil
	}
	if s.BackoffUntil != nil && s.BackoffUntil.After(until) {
		until = *s.BackoffUntil
	}
	if !until.After(now) {
		return 0
	}
	d := until.Sub(now)
	if rem := d % time.Second; rem != 0 {
		d += time.Second - rem
	}
	return d
}

// ComputeLockState derives backoff and lockout deadlines from a failure count.
func ComputeLockState(failures int, policy LockoutPolicy, now time.Time) LockState {
	state := LockState{Failures: failures, CaptchaRequired: failures >= 10}
	if failures >= policy.LockAfter {
		exponent := (failures - policy.LockAfter) / 5
		if exponent > 4 {
			exponent = 4
		}
		duration := policy.LockDuration
		for i := 0; i < exponent; i++ {
			duration *= 2
		}
		if duration > policy.MaxLockDuration {
			duration = policy.MaxLockDuration
		}
		until := now.Add(duration).UTC()
		state.LockedUntil = &until
		state.BackoffUntil = &until
	} else if failures >= policy.BackoffAfter {
		exponent := failures - policy.BackoffAfter
		if exponent > 8 {
			exponent = 8
		}
		duration := policy.BaseBackoff * time.Duration(1<<exponent)
		if duration > policy.MaxBackoff {
			duration = policy.MaxBackoff
		}
		until := now.Add(duration).UTC()
		state.BackoffUntil = &until
	}
	return state
}

// StrongerLockState combines two protection states conservatively.
func StrongerLockState(a, b LockState) LockState {
	out := a
	if b.Failures > out.Failures {
		out.Failures = b.Failures
	}
	out.CaptchaRequired = a.CaptchaRequired || b.CaptchaRequired
	if b.LockedUntil != nil && (out.LockedUntil == nil || b.LockedUntil.After(*out.LockedUntil)) {
		out.LockedUntil = b.LockedUntil
	}
	if b.BackoffUntil != nil && (out.BackoffUntil == nil || b.BackoffUntil.After(*out.BackoffUntil)) {
		out.BackoffUntil = b.BackoffUntil
	}
	return out
}

// KYCStatus is the state of a KYC case reference.
type KYCStatus string

const (
	KYCSubmitted KYCStatus = "submitted"
	KYCInReview  KYCStatus = "in_review"
	KYCApproved  KYCStatus = "approved"
	KYCRejected  KYCStatus = "rejected"
)

// KYCCase records a KYC provider case reference and decision history.
type KYCCase struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	ProviderRef string
	Status      KYCStatus
	Level       int16
	Reason      string
	SubmittedAt time.Time
	UpdatedAt   time.Time
	DecidedAt   *time.Time
	DecidedBy   *uuid.UUID
}

// Notification is an encrypted outbox item for a future notification worker.
// Destination and payload are encrypted by the service; the identity service
// never performs network delivery itself.
type Notification struct {
	ID                    uuid.UUID
	UserID                uuid.UUID
	Channel               string
	Kind                  string
	DestinationCiphertext []byte
	PayloadCiphertext     []byte
	CreatedAt             time.Time
}

// Principal is an authenticated caller. Exactly one of SessionID or APIKeyID
// is set. Permissions are derived from roles for sessions, from scopes for keys.
type Principal struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
	APIKeyID  uuid.UUID
	Roles     []Role
	Scopes    []string
	MFA       bool
	Kind      string // session or api_key
}

// HasRole reports whether the principal holds role.
func (p Principal) HasRole(role Role) bool {
	for _, candidate := range p.Roles {
		if candidate == role {
			return true
		}
	}
	return false
}

// HasPermission applies deny-by-default RBAC.
func (p Principal) HasPermission(permission string) bool {
	for _, role := range p.Roles {
		if roleHasPermission(role, permission) {
			return true
		}
	}
	return false
}

// HasScope applies API-key scope checks. Session principals are not scope
// limited; they use RBAC instead.
func (p Principal) HasScope(scope string) bool {
	if p.Kind != "api_key" {
		return false
	}
	for _, candidate := range p.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

// CanonicalAuditBytes returns the deterministic bytes hashed into EventHash.
func (e AuditEvent) CanonicalAuditBytes() ([]byte, error) {
	meta := make(map[string]string, len(e.Metadata))
	for k, v := range e.Metadata {
		meta[k] = v
	}
	payload := struct {
		ID         string            `json:"id"`
		At         string            `json:"at"`
		ActorType  string            `json:"actor_type"`
		ActorID    string            `json:"actor_id"`
		Action     string            `json:"action"`
		TargetType string            `json:"target_type"`
		TargetID   string            `json:"target_id"`
		Outcome    string            `json:"outcome"`
		IPHash     string            `json:"ip_hash"`
		UserAgent  string            `json:"user_agent"`
		RequestID  string            `json:"request_id"`
		Metadata   map[string]string `json:"metadata"`
	}{
		ID: e.ID.String(), At: e.OccurredAt.UTC().Format(time.RFC3339Nano),
		ActorType: e.ActorType, ActorID: e.ActorID, Action: e.Action,
		TargetType: e.TargetType, TargetID: e.TargetID, Outcome: e.Outcome,
		IPHash: hexBytes(e.IPHash), UserAgent: e.UserAgent,
		RequestID: e.RequestID, Metadata: meta,
	}
	return json.Marshal(payload)
}

func hexBytes(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(value)*2)
	for _, b := range value {
		out = append(out, digits[b>>4], digits[b&0x0f])
	}
	return string(out)
}

// ParseScopes validates and normalizes API key scopes. Admin is never a
// customer API key scope.
func ParseScopes(values []string) ([]string, error) {
	allowed := map[string]bool{"read": true, "trade": true, "transfer": true, "withdraw": true}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		scope := strings.TrimSpace(value)
		if !allowed[scope] {
			return nil, errors.New("invalid scope")
		}
		if !seen[scope] {
			seen[scope] = true
			out = append(out, scope)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("at least one scope is required")
	}
	return out, nil
}

// ParsePrefixes parses and canonicalizes an IP allowlist.
func ParsePrefixes(values []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			addr, addrErr := netip.ParseAddr(value)
			if addrErr != nil {
				return nil, errors.New("invalid IP allowlist entry")
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		prefix = prefix.Masked()
		if prefix.Bits() == 0 {
			return nil, errors.New("IP allowlist must not allow the entire address space")
		}
		if !seen[prefix.String()] {
			seen[prefix.String()] = true
			out = append(out, prefix)
		}
	}
	return out, nil
}

// VersionString renders a user version safely (used in admin responses).
func VersionString(v int64) string { return strconv.FormatInt(v, 10) }
