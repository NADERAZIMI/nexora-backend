package postgres

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

func integrationDatabase(t *testing.T) (*Store, fs.FS) {
	t.Helper()
	url := os.Getenv("NEXORA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("NEXORA_TEST_DATABASE_URL is not set; PostgreSQL integration test skipped")
	}
	migrations := os.DirFS("../../migrations")
	store, err := New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	t.Cleanup(store.Close)
	// Start from a known schema state. This is destructive and therefore only
	// runs against the dedicated test database selected by the environment.
	if err := Migrate(context.Background(), store.Pool(), migrations, "down"); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := Migrate(context.Background(), store.Pool(), migrations, "up"); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	t.Cleanup(func() {
		if err := Migrate(context.Background(), store.Pool(), migrations, "down"); err != nil {
			t.Errorf("cleanup schema: %v", err)
		}
	})
	return store, migrations
}

func randomHash(t *testing.T) []byte {
	t.Helper()
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("random hash: %v", err)
	}
	return value
}

func integrationUser(t *testing.T, store *Store, now time.Time, status identity.UserStatus) *identity.User {
	t.Helper()
	userID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	user := &identity.User{
		ID: userID, EmailCiphertext: []byte("ciphertext"), EmailHash: randomHash(t),
		PasswordHash: "$argon2id$v=19$m=8192,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		Status:       status, KYCLevel: 0, CreatedAt: now, UpdatedAt: now, Roles: []identity.Role{identity.RoleCustomer},
	}
	credential := &identity.Credential{
		UserID: userID, Kind: identity.CredentialPassword, SecretCiphertext: []byte(user.PasswordHash), CreatedAt: now,
	}
	verification := &identity.VerificationToken{
		ID: uuid.New(), UserID: userID, Kind: "email", TokenHash: randomHash(t),
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	audit := identity.NewAuditEvent("user", userID.String(), "auth.register", "user", userID.String(), "success", nil, nil, "integration", "req", now)
	if err := store.CreateUser(context.Background(), user, credential, verification, audit); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func integrationSession(t *testing.T, store *Store, user *identity.User, now time.Time) (*identity.Session, *identity.RefreshToken) {
	t.Helper()
	session := &identity.Session{
		ID: uuid.New(), UserID: user.ID, UserAgent: "integration", IPHash: randomHash(t),
		MFA: true, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	refresh := &identity.RefreshToken{
		ID: uuid.New(), TokenHash: randomHash(t), Status: identity.RefreshActive,
		ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now,
	}
	audit := identity.NewAuditEvent("user", user.ID.String(), "auth.login.success", "session", session.ID.String(), "success", nil, nil, "integration", "req", now)
	if err := store.CreateSession(context.Background(), session, refresh, 10, now, audit); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return session, refresh
}

func TestPostgresIdentityStoreIntegration(t *testing.T) {
	store, migrations := integrationDatabase(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	user := integrationUser(t, store, now, identity.UserPending)
	loaded, err := store.GetUserByEmailHash(ctx, user.EmailHash)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if loaded.ID != user.ID || loaded.Status != identity.UserPending || len(loaded.Roles) != 1 || loaded.Roles[0] != identity.RoleCustomer {
		t.Fatalf("loaded user mismatch: %+v", loaded)
	}
	if _, err := store.GetUserByID(ctx, uuid.New()); !errors.Is(err, identity.ErrStoreNotFound) {
		t.Fatalf("unknown user error: %v", err)
	}
	if err := store.SetEmailVerified(ctx, user.ID, now.Add(time.Minute), identity.NewAuditEvent("user", user.ID.String(), "auth.email.verify", "user", user.ID.String(), "success", nil, nil, "integration", "req", now)); err != nil {
		t.Fatalf("verify email: %v", err)
	}
	loaded, _ = store.GetUserByID(ctx, user.ID)
	if loaded.Status != identity.UserActive || loaded.EmailVerifiedAt == nil {
		t.Fatalf("email verification did not activate user: %+v", loaded)
	}

	session, refresh := integrationSession(t, store, loaded, now)
	loadedSession, err := store.GetSession(ctx, session.ID)
	if err != nil || loadedSession.UserID != user.ID || !loadedSession.MFA {
		t.Fatalf("get session: %+v %v", loadedSession, err)
	}
	active, err := store.CountActiveSessions(ctx, user.ID, now)
	if err != nil || active != 1 {
		t.Fatalf("active sessions: %d %v", active, err)
	}

	newToken := &identity.RefreshToken{
		ID: uuid.New(), TokenHash: randomHash(t), Status: identity.RefreshActive,
		ExpiresAt: now.Add(48 * time.Hour), CreatedAt: now,
	}
	rotation, err := store.RotateRefresh(ctx, refresh.TokenHash, newToken, now.Add(48*time.Hour), now,
		identity.NewAuditEvent("session", "", "auth.refresh.rotate", "refresh_token", newToken.ID.String(), "success", nil, nil, "integration", "req", now),
		identity.NewAuditEvent("session", "", "auth.refresh.reuse", "refresh_token", "", "denied", nil, nil, "integration", "req", now))
	if err != nil {
		t.Fatalf("rotate refresh: %v", err)
	}
	if rotation.Session.ID != session.ID || rotation.User.ID != user.ID || !rotation.Session.MFA {
		t.Fatalf("rotation mismatch: %+v", rotation)
	}
	if _, err := store.RotateRefresh(ctx, refresh.TokenHash, newToken, now.Add(48*time.Hour), now,
		identity.NewAuditEvent("session", "", "auth.refresh.rotate", "refresh_token", newToken.ID.String(), "success", nil, nil, "integration", "req", now),
		identity.NewAuditEvent("session", "", "auth.refresh.reuse", "refresh_token", "", "denied", nil, nil, "integration", "req", now)); !errors.Is(err, identity.ErrStoreRefreshReused) {
		t.Fatalf("expected reuse detection, got %v", err)
	}
	revokedSession, err := store.GetSession(ctx, session.ID)
	if err != nil || revokedSession.RevokedAt == nil {
		t.Fatalf("reuse did not revoke session: %+v %v", revokedSession, err)
	}
	if err := store.RevokeSession(ctx, uuid.New(), &user.ID, now, nil); !errors.Is(err, identity.ErrStoreNotFound) {
		t.Fatalf("unknown session revoke: %v", err)
	}

	// API keys, scopes, IP allowlists, and nonce replay protection.
	prefix := fmt.Sprintf("it%010d", now.UnixNano()%10000000000)
	expires := now.Add(24 * time.Hour)
	key := &identity.APIKey{
		ID: uuid.New(), UserID: user.ID, Label: "integration key", KeyPrefix: prefix,
		Algorithm: "hmac-sha256", SecretCiphertext: []byte("encrypted-secret"),
		Scopes: []string{"read"}, IPAllowlist: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
		CreatedAt: now, ExpiresAt: &expires,
	}
	if err := store.CreateAPIKey(ctx, key, 10, now, identity.NewAuditEvent("user", user.ID.String(), "api_key.create", "api_key", key.ID.String(), "success", nil, nil, "integration", "req", now)); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	loadedKey, err := store.GetAPIKey(ctx, key.ID)
	if err != nil || loadedKey.KeyPrefix != prefix || len(loadedKey.IPAllowlist) != 1 || !loadedKey.IPAllowlist[0].Contains(netip.MustParseAddr("192.0.2.42")) {
		t.Fatalf("loaded key mismatch: %+v %v", loadedKey, err)
	}
	if keys, err := store.ListAPIKeys(ctx, user.ID); err != nil || len(keys) != 1 {
		t.Fatalf("list api keys: %d %v", len(keys), err)
	}
	first, err := store.UseAPIKeyNonce(ctx, key.ID, "nonce-integration-0001", now.Add(10*time.Minute), now)
	if err != nil || !first {
		t.Fatalf("first nonce: %v %v", first, err)
	}
	replay, err := store.UseAPIKeyNonce(ctx, key.ID, "nonce-integration-0001", now.Add(10*time.Minute), now)
	if err != nil || replay {
		t.Fatalf("nonce replay accepted: %v %v", replay, err)
	}
	if err := store.TouchAPIKey(ctx, key.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("touch key: %v", err)
	}
	if err := store.RevokeAPIKey(ctx, key.ID, &user.ID, now.Add(time.Minute), identity.NewAuditEvent("user", user.ID.String(), "api_key.revoke", "api_key", key.ID.String(), "success", nil, nil, "integration", "req", now)); err != nil {
		t.Fatalf("revoke api key: %v", err)
	}
	if _, err := store.GetAPIKey(ctx, key.ID); err != nil {
		t.Fatalf("get revoked key: %v", err)
	}

	// Lockout accounting is persisted and visible to subsequent requests.
	subjectHash := randomHash(t)
	ipHash := randomHash(t)
	policy := identity.LockoutPolicy{BackoffAfter: 2, LockAfter: 3, BaseBackoff: time.Second, MaxBackoff: 2 * time.Second, LockDuration: time.Minute, MaxLockDuration: time.Minute}
	for i := 0; i < 3; i++ {
		state, err := store.RecordLoginFailure(ctx, subjectHash, ipHash, policy, now, identity.NewAuditEvent("anonymous", "", "auth.login.failure", "user", "", "denied", nil, ipHash, "integration", "req", now))
		if err != nil {
			t.Fatalf("record failure %d: %v", i, err)
		}
		if i == 2 && state.LockedUntil == nil {
			t.Fatalf("expected lock after third failure: %+v", state)
		}
	}
	state, err := store.GetLoginState(ctx, subjectHash, ipHash, now)
	if err != nil || !state.Blocked(now) {
		t.Fatalf("persisted lockout state: %+v %v", state, err)
	}
	if err := store.ClearLoginFailures(ctx, subjectHash, ipHash); err != nil {
		t.Fatalf("clear failures: %v", err)
	}
	var remaining int
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM identity.login_attempts WHERE subject_hash=$1`, subjectHash).Scan(&remaining); err != nil {
		t.Fatalf("count cleared subject rows: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("subject failures were not cleared: %d", remaining)
	}
	// The zero-subject IP bucket intentionally survives successful login so a
	// single valid account cannot reset credential-stuffing protection.
	ipState, err := store.GetLoginState(ctx, subjectHash, ipHash, now)
	if err != nil || !ipState.Blocked(now) {
		t.Fatalf("IP-level protection was not retained: %+v %v", ipState, err)
	}

	// Audit events are hash-chained and readable in reverse chronological order.
	events, err := store.ListAudit(ctx, identity.AuditFilter{Limit: 200})
	if err != nil || len(events) < 5 {
		t.Fatalf("list audit: %d %v", len(events), err)
	}
	hashes := make(map[string]bool, len(events))
	for _, event := range events {
		canonical, err := event.CanonicalAuditBytes()
		if err != nil {
			t.Fatalf("canonical audit: %v", err)
		}
		expected := identity.SHA256(append(append([]byte(nil), event.PrevHash...), canonical...))
		if !identity.SecureCompare(expected, event.EventHash) {
			t.Fatalf("audit hash mismatch for %s", event.Action)
		}
		hashes[string(event.EventHash)] = true
	}
	for _, event := range events {
		if len(event.PrevHash) == 0 {
			continue
		}
		if !hashes[string(event.PrevHash)] {
			t.Fatalf("audit chain reference missing for %s", event.Action)
		}
	}
	if _, err := store.Pool().Exec(ctx, `UPDATE identity.audit_events SET outcome='failure'`); err == nil {
		t.Fatal("audit table mutation was not rejected")
	}

	t.Run("concurrent_refresh_single_winner", func(t *testing.T) {
		session2, refresh2 := integrationSession(t, store, loaded, now.Add(2*time.Minute))
		const workers = 12
		var wg sync.WaitGroup
		results := make(chan error, workers)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				candidate := &identity.RefreshToken{ID: uuid.New(), TokenHash: randomHash(t), Status: identity.RefreshActive, ExpiresAt: now.Add(48 * time.Hour), CreatedAt: now}
				_, err := store.RotateRefresh(ctx, refresh2.TokenHash, candidate, now.Add(48*time.Hour), now.Add(2*time.Minute),
					identity.NewAuditEvent("session", "", "auth.refresh.rotate", "refresh_token", candidate.ID.String(), "success", nil, nil, "integration", fmt.Sprintf("req-%d", index), now),
					identity.NewAuditEvent("session", "", "auth.refresh.reuse", "refresh_token", "", "denied", nil, nil, "integration", fmt.Sprintf("req-%d", index), now))
				results <- err
			}(i)
		}
		wg.Wait()
		close(results)
		successes, reused := 0, 0
		for err := range results {
			if err == nil {
				successes++
			} else if errors.Is(err, identity.ErrStoreRefreshReused) {
				reused++
			}
		}
		if successes != 1 || reused != workers-1 {
			t.Fatalf("refresh race: successes=%d reused=%d", successes, reused)
		}
		final, err := store.GetSession(ctx, session2.ID)
		if err != nil || final.RevokedAt == nil {
			t.Fatalf("refresh race did not revoke family: %+v %v", final, err)
		}
	})

	t.Run("concurrent_nonce_single_winner", func(t *testing.T) {
		key2 := &identity.APIKey{
			ID: uuid.New(), UserID: user.ID, Label: "nonce race", KeyPrefix: fmt.Sprintf("nr%010d", now.UnixNano()%10000000000),
			Algorithm: "hmac-sha256", SecretCiphertext: []byte("encrypted"), Scopes: []string{"read"},
			CreatedAt: now, ExpiresAt: &expires,
		}
		if err := store.CreateAPIKey(ctx, key2, 10, now, nil); err != nil {
			t.Fatalf("create nonce key: %v", err)
		}
		const workers = 12
		var wg sync.WaitGroup
		results := make(chan bool, workers)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, err := store.UseAPIKeyNonce(ctx, key2.ID, "nonce-race-0000001", now.Add(10*time.Minute), now)
				if err != nil {
					results <- false
					return
				}
				results <- ok
			}()
		}
		wg.Wait()
		close(results)
		winners := 0
		for ok := range results {
			if ok {
				winners++
			}
		}
		if winners != 1 {
			t.Fatalf("nonce race winners=%d", winners)
		}
	})

	t.Run("concurrent_session_limit", func(t *testing.T) {
		const workers = 8
		const limit = 2
		// Revoke earlier sessions so the limit test starts clean.
		if _, err := store.RevokeAllSessions(ctx, user.ID, nil, now.Add(3*time.Minute), nil); err != nil {
			t.Fatalf("reset sessions: %v", err)
		}
		var wg sync.WaitGroup
		at := now.Add(3 * time.Minute)
		ipHash := randomHash(t)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			tokenHash := randomHash(t)
			go func(hash []byte) {
				defer wg.Done()
				session := &identity.Session{ID: uuid.New(), UserID: user.ID, UserAgent: "race", IPHash: ipHash, CreatedAt: at, LastSeenAt: at, ExpiresAt: at.Add(time.Hour)}
				refresh := &identity.RefreshToken{ID: uuid.New(), TokenHash: hash, Status: identity.RefreshActive, ExpiresAt: at.Add(time.Hour), CreatedAt: at}
				_ = store.CreateSession(ctx, session, refresh, limit, at, nil)
			}(tokenHash)
		}
		wg.Wait()
		active, err := store.CountActiveSessions(ctx, user.ID, at)
		if err != nil {
			t.Fatalf("count sessions: %v", err)
		}
		if active != limit {
			t.Fatalf("session limit not enforced exactly: %d != %d", active, limit)
		}
	})

	t.Run("webauthn_transactions", func(t *testing.T) {
		webauthnUser := integrationUser(t, store, now, identity.UserActive)
		registrationID := uuid.New()
		challenge := &identity.WebAuthnChallenge{
			ID: registrationID, UserID: webauthnUser.ID, Kind: "registration",
			SessionData: []byte(`{"challenge":"test"}`), ExpiresAt: now.Add(time.Minute), CreatedAt: now,
		}
		if err := store.CreateWebAuthnChallenge(ctx, challenge); err != nil {
			t.Fatalf("create registration challenge: %v", err)
		}
		credentialID := uuid.New()
		credential := &identity.Credential{
			ID: credentialID, UserID: webauthnUser.ID, Kind: identity.CredentialWebAuthn,
			SecretCiphertext: []byte{}, Metadata: []byte(`{"id":"dGVzdA"}`), CreatedAt: now, VerifiedAt: &now,
		}
		credentialAudit := identity.NewAuditEvent("user", webauthnUser.ID.String(), "mfa.webauthn.register", "credential", credentialID.String(), "success", nil, nil, "integration", "req", now)
		enrollAudit := identity.NewAuditEvent("user", webauthnUser.ID.String(), "mfa.enroll", "user", webauthnUser.ID.String(), "success", map[string]string{"method": "webauthn"}, nil, "integration", "req", now)
		if err := store.ConfirmWebAuthnRegistration(ctx, registrationID, webauthnUser.ID, credential, now, credentialAudit, enrollAudit); err != nil {
			t.Fatalf("confirm registration: %v", err)
		}
		loadedUser, err := store.GetUserByID(ctx, webauthnUser.ID)
		if err != nil || loadedUser.MFAEnrolledAt == nil {
			t.Fatalf("MFA not enrolled atomically: %+v %v", loadedUser, err)
		}
		credentials, err := store.GetCredentials(ctx, webauthnUser.ID, identity.CredentialWebAuthn)
		if err != nil || len(credentials) != 1 || credentials[0].ID != credentialID {
			t.Fatalf("registration credential mismatch: %+v %v", credentials, err)
		}
		if err := store.ConfirmWebAuthnRegistration(ctx, registrationID, webauthnUser.ID, &identity.Credential{
			ID: uuid.New(), UserID: webauthnUser.ID, Kind: identity.CredentialWebAuthn, Metadata: []byte(`{}`), CreatedAt: now,
		}, now, credentialAudit, enrollAudit); !errors.Is(err, identity.ErrStoreConsumed) {
			t.Fatalf("registration challenge reuse: %v", err)
		}

		authenticationID := uuid.New()
		if err := store.CreateWebAuthnChallenge(ctx, &identity.WebAuthnChallenge{
			ID: authenticationID, UserID: webauthnUser.ID, Kind: "authentication",
			SessionData: []byte(`{"challenge":"test"}`), ExpiresAt: now.Add(time.Minute), CreatedAt: now,
		}); err != nil {
			t.Fatalf("create authentication challenge: %v", err)
		}
		updatedMetadata := []byte(`{"id":"dGVzdA","signCount":7}`)
		counterAudit := identity.NewAuditEvent("user", webauthnUser.ID.String(), "auth.webauthn.counter", "credential", credentialID.String(), "success", nil, nil, "integration", "req", now)
		if err := store.CompleteWebAuthnAssertion(ctx, authenticationID, webauthnUser.ID, credentialID, updatedMetadata, now, counterAudit); err != nil {
			t.Fatalf("complete assertion: %v", err)
		}
		updated, err := store.GetCredential(ctx, credentialID)
		if err != nil || updated.LastUsedAt == nil {
			t.Fatalf("assertion update mismatch: %+v %v", updated, err)
		}
		var metadata map[string]any
		if err := json.Unmarshal(updated.Metadata, &metadata); err != nil || metadata["signCount"] != float64(7) {
			t.Fatalf("assertion counter not persisted: %s %v", updated.Metadata, err)
		}
		if err := store.CompleteWebAuthnAssertion(ctx, authenticationID, webauthnUser.ID, credentialID, updatedMetadata, now, counterAudit); !errors.Is(err, identity.ErrStoreConsumed) {
			t.Fatalf("assertion challenge reuse: %v", err)
		}
	})

	t.Run("audit_is_append_only", func(t *testing.T) {
		var eventID uuid.UUID
		if err := store.Pool().QueryRow(ctx, `SELECT event_id FROM identity.audit_events LIMIT 1`).Scan(&eventID); err != nil {
			t.Fatalf("load audit event: %v", err)
		}
		if _, err := store.Pool().Exec(ctx, `UPDATE identity.audit_events SET outcome='denied' WHERE event_id=$1`, eventID); err == nil {
			t.Fatal("audit event update was accepted")
		}
		if _, err := store.Pool().Exec(ctx, `DELETE FROM identity.audit_events WHERE event_id=$1`, eventID); err == nil {
			t.Fatal("audit event deletion was accepted")
		}
	})

	t.Run("migration_is_reversible", func(t *testing.T) {
		if err := Migrate(ctx, store.Pool(), migrations, "down"); err != nil {
			t.Fatalf("migrate down: %v", err)
		}
		var table *string
		if err := store.Pool().QueryRow(ctx, `SELECT to_regclass('identity.users')::text`).Scan(&table); err != nil {
			t.Fatalf("check dropped schema: %v", err)
		}
		if table != nil {
			t.Fatalf("identity schema survived down migration: %s", *table)
		}
		if err := Migrate(ctx, store.Pool(), migrations, "up"); err != nil {
			t.Fatalf("migrate up: %v", err)
		}
		if err := store.Ping(ctx); err != nil {
			t.Fatalf("ping after migration: %v", err)
		}
	})
}
