//nolint:revive // MemoryStore methods intentionally mirror the Store interface.
package identity

import (
	"context"
	"encoding/hex"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryStore is an in-process Store used by unit and HTTP tests. It enforces
// the same security invariants as the PostgreSQL store, under a coarse lock.
type MemoryStore struct {
	mu sync.Mutex

	usersByID        map[uuid.UUID]*User
	userByEmail      map[string]uuid.UUID
	userByPhone      map[string]uuid.UUID
	credentials      map[uuid.UUID]*Credential
	credentialByUser map[uuid.UUID][]uuid.UUID
	sessions         map[uuid.UUID]*Session
	sessionByUser    map[uuid.UUID][]uuid.UUID
	refresh          map[string]*RefreshToken
	apiKeys          map[uuid.UUID]*APIKey
	apiKeyPrefix     map[string]uuid.UUID
	apiKeyByUser     map[uuid.UUID][]uuid.UUID
	nonces           map[string]time.Time
	verification     map[string]*VerificationToken
	mfa              map[string]*MFAChallenge
	webauthn         map[uuid.UUID]*WebAuthnChallenge
	attempts         map[string]*LoginAttempt
	audit            []*AuditEvent
	auditHead        []byte
	kyc              map[uuid.UUID]*KYCCase
	notifications    []*Notification
}

// NewMemoryStore constructs an empty memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		usersByID: make(map[uuid.UUID]*User), userByEmail: make(map[string]uuid.UUID),
		userByPhone: make(map[string]uuid.UUID), credentials: make(map[uuid.UUID]*Credential),
		credentialByUser: make(map[uuid.UUID][]uuid.UUID), sessions: make(map[uuid.UUID]*Session),
		sessionByUser: make(map[uuid.UUID][]uuid.UUID), refresh: make(map[string]*RefreshToken),
		apiKeys: make(map[uuid.UUID]*APIKey), apiKeyPrefix: make(map[string]uuid.UUID),
		apiKeyByUser: make(map[uuid.UUID][]uuid.UUID), nonces: make(map[string]time.Time),
		verification: make(map[string]*VerificationToken), mfa: make(map[string]*MFAChallenge),
		webauthn: make(map[uuid.UUID]*WebAuthnChallenge), attempts: make(map[string]*LoginAttempt),
		kyc: make(map[uuid.UUID]*KYCCase),
	}
}

func (m *MemoryStore) Ping(context.Context) error { return nil }
func (m *MemoryStore) Close()                     {}

func (m *MemoryStore) CreateUser(_ context.Context, user *User, password *Credential, verification *VerificationToken, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.userByEmail[hex.EncodeToString(user.EmailHash)]; exists {
		return ErrStoreConflict
	}
	if len(user.PhoneHash) > 0 {
		if _, exists := m.userByPhone[hex.EncodeToString(user.PhoneHash)]; exists {
			return ErrStoreConflict
		}
	}
	copyUser := cloneUser(user)
	if copyUser.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		copyUser.ID = id
	}
	m.usersByID[copyUser.ID] = copyUser
	m.userByEmail[hex.EncodeToString(copyUser.EmailHash)] = copyUser.ID
	if len(copyUser.PhoneHash) > 0 {
		m.userByPhone[hex.EncodeToString(copyUser.PhoneHash)] = copyUser.ID
	}
	if password != nil {
		password.UserID = copyUser.ID
		m.insertCredential(password)
	}
	if verification != nil {
		verification.UserID = copyUser.ID
		m.verification[hex.EncodeToString(verification.TokenHash)] = cloneVerification(verification)
	}
	m.appendAuditLocked(audit)
	*user = *cloneUser(copyUser)
	return nil
}

func (m *MemoryStore) GetUserByEmailHash(_ context.Context, hash []byte) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.userByEmail[hex.EncodeToString(hash)]
	if !ok {
		return nil, ErrStoreNotFound
	}
	return cloneUser(m.usersByID[id]), nil
}

func (m *MemoryStore) GetUserByPhoneHash(_ context.Context, hash []byte) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.userByPhone[hex.EncodeToString(hash)]
	if !ok {
		return nil, ErrStoreNotFound
	}
	return cloneUser(m.usersByID[id]), nil
}

func (m *MemoryStore) GetUserByID(_ context.Context, userID uuid.UUID) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.usersByID[userID]
	if !ok {
		return nil, ErrStoreNotFound
	}
	return cloneUser(user), nil
}

func (m *MemoryStore) SetUserStatus(_ context.Context, userID uuid.UUID, status UserStatus, now time.Time, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.usersByID[userID]
	if !ok {
		return ErrStoreNotFound
	}
	user.Status = status
	user.UpdatedAt = now.UTC()
	user.Version++
	if status != UserActive {
		for _, id := range m.sessionByUser[userID] {
			m.revokeSessionLocked(id, now)
		}
		for _, id := range m.apiKeyByUser[userID] {
			key := m.apiKeys[id]
			if key.RevokedAt == nil {
				at := now.UTC()
				key.RevokedAt = &at
			}
		}
	}
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) SetEmailVerified(_ context.Context, userID uuid.UUID, now time.Time, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.usersByID[userID]
	if !ok {
		return ErrStoreNotFound
	}
	at := now.UTC()
	user.EmailVerifiedAt = &at
	if user.Status == UserPending {
		user.Status = UserActive
	}
	user.UpdatedAt = at
	user.Version++
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) SetPhoneVerified(_ context.Context, userID uuid.UUID, now time.Time, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.usersByID[userID]
	if !ok {
		return ErrStoreNotFound
	}
	at := now.UTC()
	user.PhoneVerifiedAt = &at
	user.UpdatedAt = at
	user.Version++
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) UpdatePassword(_ context.Context, userID uuid.UUID, passwordHash string, now time.Time, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.usersByID[userID]
	if !ok {
		return ErrStoreNotFound
	}
	user.PasswordHash = passwordHash
	user.UpdatedAt = now.UTC()
	user.Version++
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) SetMFAEnrolled(_ context.Context, userID uuid.UUID, now time.Time, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.usersByID[userID]
	if !ok {
		return ErrStoreNotFound
	}
	at := now.UTC()
	user.MFAEnrolledAt = &at
	user.UpdatedAt = at
	user.Version++
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) SetUserRoles(_ context.Context, userID uuid.UUID, roles []Role, _ uuid.UUID, now time.Time, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.usersByID[userID]
	if !ok {
		return ErrStoreNotFound
	}
	for _, role := range roles {
		if !ValidRole(role) {
			return ErrStoreConflict
		}
	}
	user.Roles = append([]Role(nil), roles...)
	user.UpdatedAt = now.UTC()
	user.Version++
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) CreateSession(_ context.Context, session *Session, refresh *RefreshToken, maxActive int, now time.Time, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	active := m.activeSessionsLocked(session.UserID, now)
	if maxActive > 0 && len(active) >= maxActive {
		sort.Slice(active, func(i, j int) bool { return active[i].CreatedAt.Before(active[j].CreatedAt) })
		remove := len(active) - maxActive + 1
		for i := 0; i < remove; i++ {
			m.revokeSessionLocked(active[i].ID, now)
			m.appendAuditLocked(NewAuditEvent("system", "", "session.auto_revoke", "session", active[i].ID.String(), "success", map[string]string{"reason": "max_active_sessions"}, nil, "", "", now))
		}
	}
	if session.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		session.ID = id
	}
	session.CreatedAt = now.UTC()
	session.LastSeenAt = now.UTC()
	m.sessions[session.ID] = cloneSession(session)
	m.sessionByUser[session.UserID] = append(m.sessionByUser[session.UserID], session.ID)
	if refresh != nil {
		if refresh.ID == uuid.Nil {
			id, err := uuid.NewV7()
			if err != nil {
				return err
			}
			refresh.ID = id
		}
		refresh.SessionID = session.ID
		refresh.Status = RefreshActive
		refresh.CreatedAt = now.UTC()
		m.refresh[hex.EncodeToString(refresh.TokenHash)] = cloneRefresh(refresh)
	}
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) GetSession(_ context.Context, sessionID uuid.UUID) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[sessionID]
	if !ok {
		return nil, ErrStoreNotFound
	}
	return cloneSession(session), nil
}

func (m *MemoryStore) ListSessions(_ context.Context, userID uuid.UUID) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := m.sessionByUser[userID]
	out := make([]Session, 0, len(ids))
	for _, id := range ids {
		out = append(out, *cloneSession(m.sessions[id]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) RevokeSession(_ context.Context, sessionID uuid.UUID, owner *uuid.UUID, now time.Time, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[sessionID]
	if !ok || (owner != nil && session.UserID != *owner) {
		return ErrStoreNotFound
	}
	if session.RevokedAt != nil {
		return nil
	}
	m.revokeSessionLocked(sessionID, now)
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) RevokeAllSessions(_ context.Context, userID uuid.UUID, except *uuid.UUID, now time.Time, audit *AuditEvent) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, session := range m.sessions {
		if session.UserID != userID || session.RevokedAt != nil {
			continue
		}
		if except != nil && session.ID == *except {
			continue
		}
		m.revokeSessionLocked(session.ID, now)
		count++
	}
	m.appendAuditLocked(audit)
	return count, nil
}

func (m *MemoryStore) RotateRefresh(_ context.Context, oldHash []byte, newToken *RefreshToken, sessionExpiry time.Time, now time.Time, successAudit, reuseAudit *AuditEvent) (*RefreshRotation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.refresh[hex.EncodeToString(oldHash)]
	if !ok {
		return nil, ErrStoreNotFound
	}
	session, ok := m.sessions[old.SessionID]
	if !ok {
		return nil, ErrStoreNotFound
	}
	if old.Status != RefreshActive {
		m.revokeSessionLocked(session.ID, now)
		m.appendAuditLocked(reuseAudit)
		return nil, ErrStoreRefreshReused
	}
	if !now.Before(old.ExpiresAt) || !now.Before(session.ExpiresAt) || session.RevokedAt != nil {
		return nil, ErrStoreExpired
	}
	user, ok := m.usersByID[session.UserID]
	if !ok || user.Status != UserActive {
		return nil, ErrStoreExpired
	}
	old.Status = RefreshRotated
	rotatedAt := now.UTC()
	old.RotatedAt = &rotatedAt
	if newToken.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		newToken.ID = id
	}
	newToken.SessionID = session.ID
	newToken.Status = RefreshActive
	newToken.CreatedAt = now.UTC()
	m.refresh[hex.EncodeToString(newToken.TokenHash)] = cloneRefresh(newToken)
	session.ExpiresAt = sessionExpiry.UTC()
	session.LastSeenAt = now.UTC()
	m.appendAuditLocked(successAudit)
	return &RefreshRotation{User: cloneUser(user), Session: cloneSession(session)}, nil
}

func (m *MemoryStore) CreateCredential(_ context.Context, credential *Credential, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.usersByID[credential.UserID]; !ok {
		return ErrStoreNotFound
	}
	if credential.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		credential.ID = id
	}
	m.insertCredential(credential)
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) GetCredentials(_ context.Context, userID uuid.UUID, kind CredentialKind) ([]Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Credential
	for _, id := range m.credentialByUser[userID] {
		credential := m.credentials[id]
		if credential.Kind == kind && credential.RevokedAt == nil {
			out = append(out, *cloneCredential(credential))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) GetCredential(_ context.Context, credentialID uuid.UUID) (*Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	credential, ok := m.credentials[credentialID]
	if !ok {
		return nil, ErrStoreNotFound
	}
	return cloneCredential(credential), nil
}

func (m *MemoryStore) UpdateCredential(_ context.Context, credential *Credential, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.credentials[credential.ID]
	if !ok || existing.UserID != credential.UserID {
		return ErrStoreNotFound
	}
	*existing = *cloneCredential(credential)
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) MarkTOTPUsed(_ context.Context, credentialID uuid.UUID, step int64, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	credential, ok := m.credentials[credentialID]
	if !ok || credential.Kind != CredentialTOTP {
		return false, ErrStoreNotFound
	}
	if credential.TOTPLastStep != nil && *credential.TOTPLastStep >= step {
		return false, nil
	}
	credential.TOTPLastStep = &step
	at := now.UTC()
	credential.LastUsedAt = &at
	return true, nil
}

func (m *MemoryStore) ConfirmTOTP(_ context.Context, credentialID, userID uuid.UUID, step int64, now time.Time, confirmAudit, enrollAudit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	credential, ok := m.credentials[credentialID]
	if !ok || credential.UserID != userID || credential.Kind != CredentialTOTP || credential.RevokedAt != nil {
		return ErrStoreNotFound
	}
	if credential.VerifiedAt != nil || (credential.TOTPLastStep != nil && *credential.TOTPLastStep >= step) {
		return ErrStoreConflict
	}
	user, ok := m.usersByID[userID]
	if !ok {
		return ErrStoreNotFound
	}
	at := now.UTC()
	credential.VerifiedAt = &at
	credential.LastUsedAt = &at
	credential.TOTPLastStep = &step
	credential.Metadata = []byte(`{"verified":true}`)
	user.MFAEnrolledAt = &at
	user.UpdatedAt = at
	user.Version++
	m.appendAuditLocked(confirmAudit)
	m.appendAuditLocked(enrollAudit)
	return nil
}

func (m *MemoryStore) CreateRecoveryCodes(_ context.Context, userID uuid.UUID, codes []Credential, now time.Time, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.usersByID[userID]; !ok {
		return ErrStoreNotFound
	}
	for _, id := range m.credentialByUser[userID] {
		credential := m.credentials[id]
		if credential.Kind == CredentialRecoveryCode && credential.RevokedAt == nil {
			at := now.UTC()
			credential.RevokedAt = &at
		}
	}
	for i := range codes {
		codes[i].UserID = userID
		if codes[i].ID == uuid.Nil {
			id, err := uuid.NewV7()
			if err != nil {
				return err
			}
			codes[i].ID = id
		}
		m.insertCredential(&codes[i])
	}
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) ConsumeRecoveryCode(_ context.Context, userID uuid.UUID, tokenHash []byte, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.credentialByUser[userID] {
		credential := m.credentials[id]
		if credential.Kind != CredentialRecoveryCode || credential.RevokedAt != nil {
			continue
		}
		if SecureCompare(credential.SecretCiphertext, tokenHash) {
			at := now.UTC()
			credential.RevokedAt = &at
			credential.LastUsedAt = &at
			return true, nil
		}
	}
	return false, nil
}

func (m *MemoryStore) CreateVerification(_ context.Context, token *VerificationToken, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.usersByID[token.UserID]; !ok {
		return ErrStoreNotFound
	}
	if token.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		token.ID = id
	}
	m.verification[hex.EncodeToString(token.TokenHash)] = cloneVerification(token)
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) ConsumeVerification(_ context.Context, tokenHash []byte, kind string, now time.Time) (*VerificationToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	token, ok := m.verification[hex.EncodeToString(tokenHash)]
	if !ok || token.Kind != kind {
		return nil, ErrStoreNotFound
	}
	if token.ConsumedAt != nil {
		return nil, ErrStoreConsumed
	}
	if !now.Before(token.ExpiresAt) {
		return nil, ErrStoreExpired
	}
	at := now.UTC()
	token.ConsumedAt = &at
	return cloneVerification(token), nil
}

func (m *MemoryStore) VerifyContact(_ context.Context, tokenHash []byte, kind string, now time.Time, audit *AuditEvent) (*VerificationToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	token, ok := m.verification[hex.EncodeToString(tokenHash)]
	if !ok || token.Kind != kind {
		return nil, ErrStoreNotFound
	}
	if token.ConsumedAt != nil {
		return nil, ErrStoreConsumed
	}
	if !now.Before(token.ExpiresAt) {
		return nil, ErrStoreExpired
	}
	user, ok := m.usersByID[token.UserID]
	if !ok {
		return nil, ErrStoreNotFound
	}
	at := now.UTC()
	token.ConsumedAt = &at
	if kind == "email" {
		user.EmailVerifiedAt = &at
	} else {
		user.PhoneVerifiedAt = &at
	}
	if user.Status == UserPending {
		user.Status = UserActive
	}
	user.UpdatedAt = at
	user.Version++
	if audit != nil {
		audit.ActorType = "user"
		audit.ActorID = token.UserID.String()
		audit.TargetType = "user"
		audit.TargetID = token.UserID.String()
	}
	m.appendAuditLocked(audit)
	return cloneVerification(token), nil
}

func (m *MemoryStore) CompletePasswordReset(_ context.Context, tokenHash []byte, passwordHash string, now time.Time, passwordAudit, sessionAudit, keyAudit *AuditEvent) (*VerificationToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	token, ok := m.verification[hex.EncodeToString(tokenHash)]
	if !ok || token.Kind != "password_reset" {
		return nil, ErrStoreNotFound
	}
	if token.ConsumedAt != nil {
		return nil, ErrStoreConsumed
	}
	if !now.Before(token.ExpiresAt) {
		return nil, ErrStoreExpired
	}
	user, ok := m.usersByID[token.UserID]
	if !ok {
		return nil, ErrStoreNotFound
	}
	at := now.UTC()
	token.ConsumedAt = &at
	user.PasswordHash = passwordHash
	user.UpdatedAt = at
	user.Version++
	for _, id := range m.sessionByUser[user.ID] {
		m.revokeSessionLocked(id, at)
	}
	for _, id := range m.apiKeyByUser[user.ID] {
		key := m.apiKeys[id]
		if key.RevokedAt == nil {
			key.RevokedAt = &at
		}
	}
	userID := user.ID.String()
	if passwordAudit != nil {
		passwordAudit.ActorType = "user"
		passwordAudit.ActorID = userID
		passwordAudit.TargetType = "user"
		passwordAudit.TargetID = userID
	}
	if sessionAudit != nil {
		sessionAudit.TargetType = "user"
		sessionAudit.TargetID = userID
	}
	if keyAudit != nil {
		keyAudit.TargetType = "user"
		keyAudit.TargetID = userID
	}
	m.appendAuditLocked(passwordAudit)
	m.appendAuditLocked(sessionAudit)
	m.appendAuditLocked(keyAudit)
	return cloneVerification(token), nil
}

func (m *MemoryStore) CreateMFAChallenge(_ context.Context, challenge *MFAChallenge) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if challenge.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		challenge.ID = id
	}
	m.mfa[hex.EncodeToString(challenge.TokenHash)] = cloneMFA(challenge)
	return nil
}

func (m *MemoryStore) ConsumeMFAChallenge(_ context.Context, tokenHash []byte, now time.Time) (*MFAChallenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	challenge, ok := m.mfa[hex.EncodeToString(tokenHash)]
	if !ok {
		return nil, ErrStoreNotFound
	}
	if challenge.ConsumedAt != nil {
		return nil, ErrStoreConsumed
	}
	if !now.Before(challenge.ExpiresAt) {
		return nil, ErrStoreExpired
	}
	at := now.UTC()
	challenge.ConsumedAt = &at
	return cloneMFA(challenge), nil
}

func (m *MemoryStore) CreateWebAuthnChallenge(_ context.Context, challenge *WebAuthnChallenge) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if challenge.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		challenge.ID = id
	}
	m.webauthn[challenge.ID] = &WebAuthnChallenge{
		ID: challenge.ID, UserID: challenge.UserID, Kind: challenge.Kind,
		SessionData: append([]byte(nil), challenge.SessionData...),
		ExpiresAt:   challenge.ExpiresAt, CreatedAt: challenge.CreatedAt,
	}
	return nil
}

func (m *MemoryStore) LatestWebAuthnChallenge(_ context.Context, userID uuid.UUID, kind string, now time.Time) (*WebAuthnChallenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *WebAuthnChallenge
	for _, challenge := range m.webauthn {
		if challenge.UserID != userID || challenge.Kind != kind || challenge.ConsumedAt != nil || !now.Before(challenge.ExpiresAt) {
			continue
		}
		if latest == nil || challenge.CreatedAt.After(latest.CreatedAt) {
			latest = challenge
		}
	}
	if latest == nil {
		return nil, ErrStoreNotFound
	}
	cloned := *latest
	cloned.SessionData = append([]byte(nil), latest.SessionData...)
	return &cloned, nil
}

func (m *MemoryStore) ConsumeWebAuthnChallenge(_ context.Context, challengeID uuid.UUID, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	challenge, ok := m.webauthn[challengeID]
	if !ok {
		return ErrStoreNotFound
	}
	if challenge.ConsumedAt != nil {
		return ErrStoreConsumed
	}
	at := now.UTC()
	challenge.ConsumedAt = &at
	return nil
}

func (m *MemoryStore) ConfirmWebAuthnRegistration(_ context.Context, challengeID, userID uuid.UUID, credential *Credential, now time.Time, credentialAudit, enrollAudit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	challenge, ok := m.webauthn[challengeID]
	if !ok || challenge.UserID != userID || challenge.Kind != "registration" {
		return ErrStoreNotFound
	}
	if challenge.ConsumedAt != nil {
		return ErrStoreConsumed
	}
	if !now.Before(challenge.ExpiresAt) {
		return ErrStoreExpired
	}
	user, ok := m.usersByID[userID]
	if !ok {
		return ErrStoreNotFound
	}
	at := now.UTC()
	challenge.ConsumedAt = &at
	credential.UserID = userID
	if credential.ID == uuid.Nil {
		credential.ID, _ = uuid.NewV7()
	}
	m.insertCredential(credential)
	user.MFAEnrolledAt = &at
	user.UpdatedAt = at
	user.Version++
	m.appendAuditLocked(credentialAudit)
	m.appendAuditLocked(enrollAudit)
	return nil
}

func (m *MemoryStore) CompleteWebAuthnAssertion(_ context.Context, challengeID, userID, credentialID uuid.UUID, metadata []byte, now time.Time, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	challenge, ok := m.webauthn[challengeID]
	if !ok || challenge.UserID != userID || challenge.Kind != "authentication" {
		return ErrStoreNotFound
	}
	if challenge.ConsumedAt != nil {
		return ErrStoreConsumed
	}
	if !now.Before(challenge.ExpiresAt) {
		return ErrStoreExpired
	}
	credential, ok := m.credentials[credentialID]
	if !ok || credential.UserID != userID || credential.Kind != CredentialWebAuthn || credential.RevokedAt != nil {
		return ErrStoreNotFound
	}
	at := now.UTC()
	challenge.ConsumedAt = &at
	credential.Metadata = append([]byte(nil), metadata...)
	credential.LastUsedAt = &at
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) CreateAPIKey(_ context.Context, key *APIKey, maxActive int, now time.Time, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.usersByID[key.UserID]; !ok {
		return ErrStoreNotFound
	}
	active := 0
	for _, id := range m.apiKeyByUser[key.UserID] {
		candidate := m.apiKeys[id]
		if candidate.Active(now) {
			active++
		}
	}
	if maxActive > 0 && active >= maxActive {
		return ErrStoreLimit
	}
	if _, exists := m.apiKeyPrefix[key.KeyPrefix]; exists {
		return ErrStoreConflict
	}
	if key.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		key.ID = id
	}
	m.apiKeys[key.ID] = cloneAPIKey(key)
	m.apiKeyPrefix[key.KeyPrefix] = key.ID
	m.apiKeyByUser[key.UserID] = append(m.apiKeyByUser[key.UserID], key.ID)
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) GetAPIKeyByPrefix(_ context.Context, prefix string) (*APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.apiKeyPrefix[prefix]
	if !ok {
		return nil, ErrStoreNotFound
	}
	return cloneAPIKey(m.apiKeys[id]), nil
}

func (m *MemoryStore) GetAPIKey(_ context.Context, keyID uuid.UUID) (*APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key, ok := m.apiKeys[keyID]
	if !ok {
		return nil, ErrStoreNotFound
	}
	return cloneAPIKey(key), nil
}

func (m *MemoryStore) ListAPIKeys(_ context.Context, userID uuid.UUID) ([]APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]APIKey, 0, len(m.apiKeyByUser[userID]))
	for _, id := range m.apiKeyByUser[userID] {
		out = append(out, *cloneAPIKey(m.apiKeys[id]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) RevokeAPIKey(_ context.Context, keyID uuid.UUID, owner *uuid.UUID, now time.Time, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key, ok := m.apiKeys[keyID]
	if !ok || (owner != nil && key.UserID != *owner) {
		return ErrStoreNotFound
	}
	if key.RevokedAt != nil {
		return nil
	}
	at := now.UTC()
	key.RevokedAt = &at
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) RevokeAllAPIKeys(_ context.Context, userID uuid.UUID, now time.Time, audit *AuditEvent) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, id := range m.apiKeyByUser[userID] {
		key := m.apiKeys[id]
		if key.RevokedAt == nil {
			at := now.UTC()
			key.RevokedAt = &at
			count++
		}
	}
	m.appendAuditLocked(audit)
	return count, nil
}

func (m *MemoryStore) TouchAPIKey(_ context.Context, keyID uuid.UUID, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key, ok := m.apiKeys[keyID]
	if !ok {
		return ErrStoreNotFound
	}
	at := now.UTC()
	key.LastUsedAt = &at
	return nil
}

func (m *MemoryStore) UseAPIKeyNonce(_ context.Context, keyID uuid.UUID, nonce string, expiresAt time.Time, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, expiry := range m.nonces {
		if !now.Before(expiry) {
			delete(m.nonces, key)
		}
	}
	key := keyID.String() + "|" + nonce
	if _, exists := m.nonces[key]; exists {
		return false, nil
	}
	m.nonces[key] = expiresAt.UTC()
	return true, nil
}

func (m *MemoryStore) AppendAudit(_ context.Context, event *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendAuditLocked(event)
	return nil
}

func (m *MemoryStore) ListAudit(_ context.Context, filter AuditFilter) ([]AuditEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out := make([]AuditEvent, 0, limit)
	for i := len(m.audit) - 1; i >= 0 && len(out) < limit; i-- {
		event := m.audit[i]
		if filter.ActorID != "" && event.ActorID != filter.ActorID {
			continue
		}
		if filter.Action != "" && event.Action != filter.Action {
			continue
		}
		if filter.TargetID != "" && event.TargetID != filter.TargetID {
			continue
		}
		out = append(out, *cloneAudit(event))
	}
	return out, nil
}

func (m *MemoryStore) GetLoginState(_ context.Context, subjectHash, ipHash []byte, now time.Time) (LockState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loginStateLocked(subjectHash, ipHash, now), nil
}

func (m *MemoryStore) RecordLoginFailure(_ context.Context, subjectHash, ipHash []byte, policy LockoutPolicy, now time.Time, audit *AuditEvent) (LockState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	subject := m.recordFailureLocked(subjectHash, ipHash, policy, now)
	ip := m.recordFailureLocked(make([]byte, 32), ipHash, policy, now)
	m.appendAuditLocked(audit)
	return strongerLockState(subject, ip), nil
}

func (m *MemoryStore) ClearLoginFailures(_ context.Context, subjectHash, _ []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, attempt := range m.attempts {
		if len(attempt.SubjectHash) == len(subjectHash) && SecureCompare(attempt.SubjectHash, subjectHash) {
			delete(m.attempts, key)
		}
	}
	return nil
}

func (m *MemoryStore) CreateKYCCase(_ context.Context, kyc *KYCCase, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.usersByID[kyc.UserID]; !ok {
		return ErrStoreNotFound
	}
	if kyc.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		kyc.ID = id
	}
	m.kyc[kyc.ID] = cloneKYC(kyc)
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) GetKYCCase(_ context.Context, caseID uuid.UUID, owner *uuid.UUID) (*KYCCase, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kyc, ok := m.kyc[caseID]
	if !ok || (owner != nil && kyc.UserID != *owner) {
		return nil, ErrStoreNotFound
	}
	return cloneKYC(kyc), nil
}

func (m *MemoryStore) ListKYCCases(_ context.Context, owner *uuid.UUID, limit int) ([]KYCCase, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := make([]KYCCase, 0, limit)
	for _, kyc := range m.kyc {
		if owner != nil && kyc.UserID != *owner {
			continue
		}
		out = append(out, *cloneKYC(kyc))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubmittedAt.After(out[j].SubmittedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemoryStore) DecideKYCCase(_ context.Context, caseID uuid.UUID, status KYCStatus, level int16, reason string, decidedBy uuid.UUID, now time.Time, audit *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kyc, ok := m.kyc[caseID]
	if !ok {
		return ErrStoreNotFound
	}
	at := now.UTC()
	kyc.Status = status
	kyc.Level = level
	kyc.Reason = reason
	kyc.DecidedAt = &at
	kyc.DecidedBy = &decidedBy
	kyc.UpdatedAt = at
	m.appendAuditLocked(audit)
	return nil
}

func (m *MemoryStore) EnqueueNotification(_ context.Context, notification *Notification) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if notification.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		notification.ID = id
	}
	copy := *notification
	copy.DestinationCiphertext = append([]byte(nil), notification.DestinationCiphertext...)
	copy.PayloadCiphertext = append([]byte(nil), notification.PayloadCiphertext...)
	m.notifications = append(m.notifications, &copy)
	return nil
}

func (m *MemoryStore) CountActiveSessions(_ context.Context, userID uuid.UUID, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.activeSessionsLocked(userID, now)), nil
}

func (m *MemoryStore) CountActiveAPIKeys(_ context.Context, userID uuid.UUID, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, id := range m.apiKeyByUser[userID] {
		if m.apiKeys[id].Active(now) {
			count++
		}
	}
	return count, nil
}

// Helpers.

func (m *MemoryStore) insertCredential(credential *Credential) {
	if credential.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err == nil {
			credential.ID = id
		}
	}
	m.credentials[credential.ID] = cloneCredential(credential)
	m.credentialByUser[credential.UserID] = append(m.credentialByUser[credential.UserID], credential.ID)
}

func (m *MemoryStore) activeSessionsLocked(userID uuid.UUID, now time.Time) []*Session {
	var out []*Session
	for _, id := range m.sessionByUser[userID] {
		session := m.sessions[id]
		if session.RevokedAt == nil && now.Before(session.ExpiresAt) {
			out = append(out, session)
		}
	}
	return out
}

func (m *MemoryStore) revokeSessionLocked(sessionID uuid.UUID, now time.Time) {
	session, ok := m.sessions[sessionID]
	if !ok || session.RevokedAt != nil {
		return
	}
	at := now.UTC()
	session.RevokedAt = &at
	for _, token := range m.refresh {
		if token.SessionID == sessionID && token.Status == RefreshActive {
			token.Status = RefreshRevoked
		}
	}
}

func (m *MemoryStore) appendAuditLocked(event *AuditEvent) {
	if event == nil {
		return
	}
	copy := cloneAudit(event)
	if copy.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err == nil {
			copy.ID = id
		}
	}
	if copy.OccurredAt.IsZero() {
		copy.OccurredAt = time.Now().UTC()
	}
	copy.PrevHash = append([]byte(nil), m.auditHead...)
	canonical, err := copy.CanonicalAuditBytes()
	if err != nil {
		return
	}
	hash := SHA256(append(append([]byte(nil), copy.PrevHash...), canonical...))
	copy.EventHash = hash
	m.auditHead = hash
	m.audit = append(m.audit, copy)
	*event = *cloneAudit(copy)
}

func (m *MemoryStore) loginStateLocked(subjectHash, ipHash []byte, now time.Time) LockState {
	subject := m.attempts[hex.EncodeToString(subjectHash)+"|"+hex.EncodeToString(ipHash)]
	ip := m.attempts[hex.EncodeToString(make([]byte, 32))+"|"+hex.EncodeToString(ipHash)]
	var states []LockState
	for _, attempt := range []*LoginAttempt{subject, ip} {
		if attempt == nil {
			continue
		}
		if now.Sub(attempt.LastFailure) > 24*time.Hour {
			continue
		}
		states = append(states, LockState{
			Failures: attempt.Failures, BackoffUntil: cloneTime(attempt.BackoffUntil),
			LockedUntil: cloneTime(attempt.LockedUntil), CaptchaRequired: attempt.Failures >= 10,
		})
	}
	if len(states) == 0 {
		return LockState{}
	}
	out := states[0]
	for _, state := range states[1:] {
		out = strongerLockState(out, state)
	}
	return out
}

func (m *MemoryStore) recordFailureLocked(subjectHash, ipHash []byte, policy LockoutPolicy, now time.Time) LockState {
	key := hex.EncodeToString(subjectHash) + "|" + hex.EncodeToString(ipHash)
	attempt, ok := m.attempts[key]
	if !ok {
		attempt = &LoginAttempt{SubjectHash: append([]byte(nil), subjectHash...), IPHash: append([]byte(nil), ipHash...), FirstFailure: now.UTC()}
		m.attempts[key] = attempt
	}
	if now.Sub(attempt.LastFailure) > 24*time.Hour {
		attempt.Failures = 0
		attempt.FirstFailure = now.UTC()
	}
	attempt.Failures++
	attempt.LastFailure = now.UTC()
	state := stateForAttempt(attempt, policy, now)
	attempt.BackoffUntil = state.BackoffUntil
	attempt.LockedUntil = state.LockedUntil
	return state
}

func stateForAttempt(attempt *LoginAttempt, policy LockoutPolicy, now time.Time) LockState {
	return ComputeLockState(attempt.Failures, policy, now)
}

func strongerLockState(a, b LockState) LockState {
	return StrongerLockState(a, b)
}

// Clone helpers keep caller mutations out of store state.

func cloneUser(user *User) *User {
	if user == nil {
		return nil
	}
	copy := *user
	copy.EmailCiphertext = append([]byte(nil), user.EmailCiphertext...)
	copy.EmailHash = append([]byte(nil), user.EmailHash...)
	copy.PhoneCiphertext = append([]byte(nil), user.PhoneCiphertext...)
	copy.PhoneHash = append([]byte(nil), user.PhoneHash...)
	copy.Roles = append([]Role(nil), user.Roles...)
	copy.CountryCode = cloneString(user.CountryCode)
	copy.EmailVerifiedAt = cloneTime(user.EmailVerifiedAt)
	copy.PhoneVerifiedAt = cloneTime(user.PhoneVerifiedAt)
	copy.MFAEnrolledAt = cloneTime(user.MFAEnrolledAt)
	return &copy
}

func cloneCredential(c *Credential) *Credential {
	if c == nil {
		return nil
	}
	copy := *c
	copy.SecretCiphertext = append([]byte(nil), c.SecretCiphertext...)
	copy.Metadata = append([]byte(nil), c.Metadata...)
	copy.LastUsedAt = cloneTime(c.LastUsedAt)
	copy.VerifiedAt = cloneTime(c.VerifiedAt)
	copy.RevokedAt = cloneTime(c.RevokedAt)
	if c.TOTPLastStep != nil {
		value := *c.TOTPLastStep
		copy.TOTPLastStep = &value
	}
	return &copy
}

func cloneSession(s *Session) *Session {
	if s == nil {
		return nil
	}
	copy := *s
	copy.IPHash = append([]byte(nil), s.IPHash...)
	copy.RevokedAt = cloneTime(s.RevokedAt)
	return &copy
}

func cloneRefresh(r *RefreshToken) *RefreshToken {
	if r == nil {
		return nil
	}
	copy := *r
	copy.TokenHash = append([]byte(nil), r.TokenHash...)
	copy.RotatedAt = cloneTime(r.RotatedAt)
	return &copy
}

func cloneAPIKey(k *APIKey) *APIKey {
	if k == nil {
		return nil
	}
	copy := *k
	copy.SecretCiphertext = append([]byte(nil), k.SecretCiphertext...)
	copy.PublicKey = append([]byte(nil), k.PublicKey...)
	copy.Scopes = append([]string(nil), k.Scopes...)
	copy.IPAllowlist = append([]netip.Prefix(nil), k.IPAllowlist...)
	copy.LastUsedAt = cloneTime(k.LastUsedAt)
	copy.ExpiresAt = cloneTime(k.ExpiresAt)
	copy.RevokedAt = cloneTime(k.RevokedAt)
	return &copy
}

func cloneAudit(e *AuditEvent) *AuditEvent {
	if e == nil {
		return nil
	}
	copy := *e
	copy.IPHash = append([]byte(nil), e.IPHash...)
	copy.PrevHash = append([]byte(nil), e.PrevHash...)
	copy.EventHash = append([]byte(nil), e.EventHash...)
	copy.Metadata = make(map[string]string, len(e.Metadata))
	for k, v := range e.Metadata {
		copy.Metadata[k] = v
	}
	return &copy
}

func cloneVerification(v *VerificationToken) *VerificationToken {
	if v == nil {
		return nil
	}
	copy := *v
	copy.TokenHash = append([]byte(nil), v.TokenHash...)
	copy.ConsumedAt = cloneTime(v.ConsumedAt)
	return &copy
}

func cloneMFA(c *MFAChallenge) *MFAChallenge {
	if c == nil {
		return nil
	}
	copy := *c
	copy.TokenHash = append([]byte(nil), c.TokenHash...)
	copy.Methods = append([]string(nil), c.Methods...)
	copy.ConsumedAt = cloneTime(c.ConsumedAt)
	return &copy
}

func cloneKYC(k *KYCCase) *KYCCase {
	if k == nil {
		return nil
	}
	copy := *k
	copy.DecidedAt = cloneTime(k.DecidedAt)
	if k.DecidedBy != nil {
		id := *k.DecidedBy
		copy.DecidedBy = &id
	}
	return &copy
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
