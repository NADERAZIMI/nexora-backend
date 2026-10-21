package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Store errors are translated into safe API errors by the service.
var (
	ErrStoreNotFound      = errors.New("store: not found")
	ErrStoreConflict      = errors.New("store: conflict")
	ErrStoreExpired       = errors.New("store: expired")
	ErrStoreConsumed      = errors.New("store: already consumed")
	ErrStoreRefreshReused = errors.New("store: refresh token reuse")
	ErrStoreLimit         = errors.New("store: limit reached")
)

// LockoutPolicy configures exponential backoff and temporary lockout.
type LockoutPolicy struct {
	BackoffAfter    int
	LockAfter       int
	BaseBackoff     time.Duration
	MaxBackoff      time.Duration
	LockDuration    time.Duration
	MaxLockDuration time.Duration
}

// DefaultLockoutPolicy returns the approved B03 policy: backoff after 5
// failures, temporary lockout after 20, capped at 60 minutes.
func DefaultLockoutPolicy() LockoutPolicy {
	return LockoutPolicy{
		BackoffAfter: 5, LockAfter: 20,
		BaseBackoff: time.Second, MaxBackoff: 60 * time.Second,
		LockDuration: 15 * time.Minute, MaxLockDuration: 60 * time.Minute,
	}
}

// RefreshRotation is the result of an atomic refresh rotation.
type RefreshRotation struct {
	User    *User
	Session *Session
}

// AuditFilter selects audit events for administrative reads.
type AuditFilter struct {
	ActorID  string
	Action   string
	TargetID string
	Limit    int
}

// Store is the authoritative identity persistence boundary. Methods that
// change security state take audit events and must commit the state change and
// audit event atomically.
type Store interface {
	Ping(ctx context.Context) error
	Close()

	CreateUser(ctx context.Context, user *User, passwordCredential *Credential, verification *VerificationToken, audit *AuditEvent) error
	GetUserByEmailHash(ctx context.Context, emailHash []byte) (*User, error)
	GetUserByPhoneHash(ctx context.Context, phoneHash []byte) (*User, error)
	GetUserByID(ctx context.Context, userID uuid.UUID) (*User, error)
	SetUserStatus(ctx context.Context, userID uuid.UUID, status UserStatus, now time.Time, audit *AuditEvent) error
	SetEmailVerified(ctx context.Context, userID uuid.UUID, now time.Time, audit *AuditEvent) error
	SetPhoneVerified(ctx context.Context, userID uuid.UUID, now time.Time, audit *AuditEvent) error
	UpdatePassword(ctx context.Context, userID uuid.UUID, passwordHash string, now time.Time, audit *AuditEvent) error
	SetMFAEnrolled(ctx context.Context, userID uuid.UUID, now time.Time, audit *AuditEvent) error
	SetUserRoles(ctx context.Context, userID uuid.UUID, roles []Role, grantedBy uuid.UUID, now time.Time, audit *AuditEvent) error

	CreateSession(ctx context.Context, session *Session, refresh *RefreshToken, maxActive int, now time.Time, audit *AuditEvent) error
	GetSession(ctx context.Context, sessionID uuid.UUID) (*Session, error)
	ListSessions(ctx context.Context, userID uuid.UUID) ([]Session, error)
	RevokeSession(ctx context.Context, sessionID uuid.UUID, owner *uuid.UUID, now time.Time, audit *AuditEvent) error
	RevokeAllSessions(ctx context.Context, userID uuid.UUID, except *uuid.UUID, now time.Time, audit *AuditEvent) (int, error)
	RotateRefresh(ctx context.Context, oldHash []byte, newToken *RefreshToken, sessionExpiry time.Time, now time.Time, successAudit, reuseAudit *AuditEvent) (*RefreshRotation, error)

	CreateCredential(ctx context.Context, credential *Credential, audit *AuditEvent) error
	GetCredentials(ctx context.Context, userID uuid.UUID, kind CredentialKind) ([]Credential, error)
	GetCredential(ctx context.Context, credentialID uuid.UUID) (*Credential, error)
	UpdateCredential(ctx context.Context, credential *Credential, audit *AuditEvent) error
	MarkTOTPUsed(ctx context.Context, credentialID uuid.UUID, step int64, now time.Time) (bool, error)
	ConfirmTOTP(ctx context.Context, credentialID, userID uuid.UUID, step int64, now time.Time, confirmAudit, enrollAudit *AuditEvent) error
	CreateRecoveryCodes(ctx context.Context, userID uuid.UUID, codes []Credential, now time.Time, audit *AuditEvent) error
	ConsumeRecoveryCode(ctx context.Context, userID uuid.UUID, tokenHash []byte, now time.Time) (bool, error)

	CreateVerification(ctx context.Context, token *VerificationToken, audit *AuditEvent) error
	ConsumeVerification(ctx context.Context, tokenHash []byte, kind string, now time.Time) (*VerificationToken, error)
	VerifyContact(ctx context.Context, tokenHash []byte, kind string, now time.Time, audit *AuditEvent) (*VerificationToken, error)
	CompletePasswordReset(ctx context.Context, tokenHash []byte, passwordHash string, now time.Time, passwordAudit, sessionAudit, keyAudit *AuditEvent) (*VerificationToken, error)
	CreateMFAChallenge(ctx context.Context, challenge *MFAChallenge) error
	ConsumeMFAChallenge(ctx context.Context, tokenHash []byte, now time.Time) (*MFAChallenge, error)
	CreateWebAuthnChallenge(ctx context.Context, challenge *WebAuthnChallenge) error
	LatestWebAuthnChallenge(ctx context.Context, userID uuid.UUID, kind string, now time.Time) (*WebAuthnChallenge, error)
	ConsumeWebAuthnChallenge(ctx context.Context, challengeID uuid.UUID, now time.Time) error
	ConfirmWebAuthnRegistration(ctx context.Context, challengeID, userID uuid.UUID, credential *Credential, now time.Time, credentialAudit, enrollAudit *AuditEvent) error
	CompleteWebAuthnAssertion(ctx context.Context, challengeID, userID, credentialID uuid.UUID, metadata []byte, now time.Time, audit *AuditEvent) error

	CreateAPIKey(ctx context.Context, key *APIKey, maxActive int, now time.Time, audit *AuditEvent) error
	GetAPIKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error)
	GetAPIKey(ctx context.Context, keyID uuid.UUID) (*APIKey, error)
	ListAPIKeys(ctx context.Context, userID uuid.UUID) ([]APIKey, error)
	RevokeAPIKey(ctx context.Context, keyID uuid.UUID, owner *uuid.UUID, now time.Time, audit *AuditEvent) error
	RevokeAllAPIKeys(ctx context.Context, userID uuid.UUID, now time.Time, audit *AuditEvent) (int, error)
	TouchAPIKey(ctx context.Context, keyID uuid.UUID, now time.Time) error
	UseAPIKeyNonce(ctx context.Context, keyID uuid.UUID, nonce string, expiresAt time.Time, now time.Time) (bool, error)

	AppendAudit(ctx context.Context, event *AuditEvent) error
	ListAudit(ctx context.Context, filter AuditFilter) ([]AuditEvent, error)

	GetLoginState(ctx context.Context, subjectHash, ipHash []byte, now time.Time) (LockState, error)
	RecordLoginFailure(ctx context.Context, subjectHash, ipHash []byte, policy LockoutPolicy, now time.Time, audit *AuditEvent) (LockState, error)
	ClearLoginFailures(ctx context.Context, subjectHash, ipHash []byte) error

	CreateKYCCase(ctx context.Context, kyc *KYCCase, audit *AuditEvent) error
	GetKYCCase(ctx context.Context, caseID uuid.UUID, owner *uuid.UUID) (*KYCCase, error)
	ListKYCCases(ctx context.Context, owner *uuid.UUID, limit int) ([]KYCCase, error)
	DecideKYCCase(ctx context.Context, caseID uuid.UUID, status KYCStatus, level int16, reason string, decidedBy uuid.UUID, now time.Time, audit *AuditEvent) error
	EnqueueNotification(ctx context.Context, notification *Notification) error

	CountActiveSessions(ctx context.Context, userID uuid.UUID, now time.Time) (int, error)
	CountActiveAPIKeys(ctx context.Context, userID uuid.UUID, now time.Time) (int, error)
}

// NewAuditEvent constructs an audit event. The store assigns ID, timestamp,
// and hash-chain fields atomically.
func NewAuditEvent(actorType, actorID, action, targetType, targetID, outcome string, metadata map[string]string, ipHash []byte, userAgent, requestID string, now time.Time) *AuditEvent {
	if metadata == nil {
		metadata = map[string]string{}
	}
	return &AuditEvent{
		ID: uuid.Nil, OccurredAt: now.UTC(), ActorType: actorType, ActorID: actorID,
		Action: action, TargetType: targetType, TargetID: targetID, Outcome: outcome,
		IPHash: append([]byte(nil), ipHash...), UserAgent: userAgent,
		RequestID: requestID, Metadata: metadata,
	}
}
