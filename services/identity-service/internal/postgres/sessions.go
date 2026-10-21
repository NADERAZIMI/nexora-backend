//nolint:revive
package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

func (s *Store) CreateSession(ctx context.Context, session *identity.Session, refresh *identity.RefreshToken, maxActive int, now time.Time, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT true FROM identity.users WHERE user_id=$1 FOR UPDATE`, session.UserID).Scan(&exists); err != nil {
			return mapError(err)
		}
		rows, err := tx.Query(ctx, `SELECT session_id FROM identity.sessions
			WHERE user_id=$1 AND revoked_at IS NULL AND expires_at > $2
			ORDER BY created_at ASC FOR UPDATE`, session.UserID, now)
		if err != nil {
			return err
		}
		var active []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			active = append(active, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if maxActive > 0 && len(active) >= maxActive {
			remove := len(active) - maxActive + 1
			for i := 0; i < remove; i++ {
				if _, err := tx.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$2 WHERE session_id=$1`, active[i], now); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE identity.refresh_tokens SET status='revoked' WHERE session_id=$1 AND status='active'`, active[i]); err != nil {
					return err
				}
				autoAudit := identity.NewAuditEvent("system", "", "session.auto_revoke", "session", active[i].String(), "success", map[string]string{"reason": "max_active_sessions"}, nil, "", "", now)
				if err := appendAuditTx(ctx, tx, autoAudit); err != nil {
					return err
				}
			}
		}
		if session.ID == uuid.Nil {
			session.ID, _ = uuid.NewV7()
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity.sessions
			(session_id, user_id, device_fingerprint, user_agent, ip_hash, mfa, created_at, last_seen_at, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			session.ID, session.UserID, session.DeviceFingerprint, session.UserAgent, session.IPHash, session.MFA,
			session.CreatedAt, session.LastSeenAt, session.ExpiresAt); err != nil {
			return mapError(err)
		}
		if refresh != nil {
			if refresh.ID == uuid.Nil {
				refresh.ID, _ = uuid.NewV7()
			}
			refresh.SessionID = session.ID
			if _, err := tx.Exec(ctx, `INSERT INTO identity.refresh_tokens
				(token_id, session_id, token_hash, status, expires_at, created_at)
				VALUES ($1,$2,$3,$4,$5,$6)`,
				refresh.ID, session.ID, refresh.TokenHash, refresh.Status, refresh.ExpiresAt, refresh.CreatedAt); err != nil {
				return mapError(err)
			}
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) GetSession(ctx context.Context, sessionID uuid.UUID) (*identity.Session, error) {
	row := s.pool.QueryRow(ctx, `SELECT session_id, user_id, device_fingerprint, user_agent, ip_hash, mfa, created_at, last_seen_at, expires_at, revoked_at
		FROM identity.sessions WHERE session_id=$1`, sessionID)
	session, err := scanSession(row)
	if err != nil {
		return nil, mapError(err)
	}
	return session, nil
}

func (s *Store) ListSessions(ctx context.Context, userID uuid.UUID) ([]identity.Session, error) {
	rows, err := s.pool.Query(ctx, `SELECT session_id, user_id, device_fingerprint, user_agent, ip_hash, mfa, created_at, last_seen_at, expires_at, revoked_at
		FROM identity.sessions WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []identity.Session
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *session)
	}
	return out, rows.Err()
}

func (s *Store) RevokeSession(ctx context.Context, sessionID uuid.UUID, owner *uuid.UUID, now time.Time, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var userID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT user_id FROM identity.sessions WHERE session_id=$1 FOR UPDATE`, sessionID).Scan(&userID); err != nil {
			return mapError(err)
		}
		if owner != nil && userID != *owner {
			return identity.ErrStoreNotFound
		}
		if _, err := tx.Exec(ctx, `UPDATE identity.sessions SET revoked_at=COALESCE(revoked_at,$2) WHERE session_id=$1`, sessionID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE identity.refresh_tokens SET status='revoked' WHERE session_id=$1 AND status='active'`, sessionID); err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) RevokeAllSessions(ctx context.Context, userID uuid.UUID, except *uuid.UUID, now time.Time, audit *identity.AuditEvent) (int, error) {
	count := 0
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var rows pgx.Rows
		var err error
		if except != nil {
			rows, err = tx.Query(ctx, `UPDATE identity.sessions SET revoked_at=$2
				WHERE user_id=$1 AND revoked_at IS NULL AND session_id<>$3 RETURNING session_id`, userID, now, *except)
		} else {
			rows, err = tx.Query(ctx, `UPDATE identity.sessions SET revoked_at=$2
				WHERE user_id=$1 AND revoked_at IS NULL RETURNING session_id`, userID, now)
		}
		if err != nil {
			return err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		count = len(ids)
		for _, id := range ids {
			if _, err := tx.Exec(ctx, `UPDATE identity.refresh_tokens SET status='revoked' WHERE session_id=$1 AND status='active'`, id); err != nil {
				return err
			}
		}
		return appendAuditTx(ctx, tx, audit)
	})
	return count, err
}

// RotateRefresh locks the refresh token row and its session, rotates atomically,
// and revokes the family on reuse. The reuse path commits its revocation.
func (s *Store) RotateRefresh(ctx context.Context, oldHash []byte, newToken *identity.RefreshToken, sessionExpiry time.Time, now time.Time, successAudit, reuseAudit *identity.AuditEvent) (*identity.RefreshRotation, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	rollback := func() { _ = tx.Rollback(context.Background()) }
	var (
		tokenID, sessionID, userID uuid.UUID
		status                     identity.RefreshTokenStatus
		tokenExpiry, sessExpiry    time.Time
		revokedAt                  pgtype.Timestamptz
		mfa                        bool
		userStatus                 identity.UserStatus
	)
	err = tx.QueryRow(ctx, `SELECT t.token_id, t.session_id, t.status, t.expires_at, s.user_id, s.expires_at, s.revoked_at, s.mfa, u.status
		FROM identity.refresh_tokens t
		JOIN identity.sessions s ON s.session_id=t.session_id
		JOIN identity.users u ON u.user_id=s.user_id
		WHERE t.token_hash=$1
		FOR UPDATE OF t, s`, oldHash).Scan(&tokenID, &sessionID, &status, &tokenExpiry, &userID, &sessExpiry, &revokedAt, &mfa, &userStatus)
	if err != nil {
		rollback()
		return nil, mapError(err)
	}
	if status != identity.RefreshActive {
		if _, err := tx.Exec(ctx, `UPDATE identity.sessions SET revoked_at=COALESCE(revoked_at,$2) WHERE session_id=$1`, sessionID, now); err != nil {
			rollback()
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE identity.refresh_tokens SET status='revoked' WHERE session_id=$1 AND status='active'`, sessionID); err != nil {
			rollback()
			return nil, err
		}
		if err := appendAuditTx(ctx, tx, reuseAudit); err != nil {
			rollback()
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, identity.ErrStoreRefreshReused
	}
	if !now.Before(tokenExpiry) || !now.Before(sessExpiry) || revokedAt.Valid || userStatus != identity.UserActive {
		rollback()
		return nil, identity.ErrStoreExpired
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.refresh_tokens SET status='rotated', rotated_at=$2 WHERE token_id=$1`, tokenID, now); err != nil {
		rollback()
		return nil, err
	}
	if newToken.ID == uuid.Nil {
		newToken.ID, _ = uuid.NewV7()
	}
	if _, err := tx.Exec(ctx, `INSERT INTO identity.refresh_tokens
		(token_id, session_id, token_hash, status, expires_at, created_at)
		VALUES ($1,$2,$3,'active',$4,$5)`, newToken.ID, sessionID, newToken.TokenHash, newToken.ExpiresAt, newToken.CreatedAt); err != nil {
		rollback()
		return nil, mapError(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.sessions SET expires_at=$2, last_seen_at=$3 WHERE session_id=$1`, sessionID, sessionExpiry, now); err != nil {
		rollback()
		return nil, err
	}
	if err := appendAuditTx(ctx, tx, successAudit); err != nil {
		rollback()
		return nil, err
	}
	roles, err := loadRolesTx(ctx, tx, userID)
	if err != nil {
		rollback()
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &identity.RefreshRotation{
		User: &identity.User{ID: userID, Status: userStatus, Roles: roles},
		Session: &identity.Session{
			ID: sessionID, UserID: userID, MFA: mfa, ExpiresAt: sessionExpiry, LastSeenAt: now,
		},
	}, nil
}

func (s *Store) CountActiveSessions(ctx context.Context, userID uuid.UUID, now time.Time) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM identity.sessions WHERE user_id=$1 AND revoked_at IS NULL AND expires_at>$2`, userID, now).Scan(&count)
	return count, err
}

func scanSession(row rowScanner) (*identity.Session, error) {
	var session identity.Session
	var revoked pgtype.Timestamptz
	err := row.Scan(&session.ID, &session.UserID, &session.DeviceFingerprint, &session.UserAgent,
		&session.IPHash, &session.MFA, &session.CreatedAt, &session.LastSeenAt, &session.ExpiresAt, &revoked)
	if err != nil {
		return nil, err
	}
	session.RevokedAt = timePtr(revoked)
	return &session, nil
}

func loadRolesTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) ([]identity.Role, error) {
	rows, err := tx.Query(ctx, `SELECT role FROM identity.user_roles WHERE user_id=$1 ORDER BY role`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []identity.Role
	for rows.Next() {
		var role identity.Role
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}
