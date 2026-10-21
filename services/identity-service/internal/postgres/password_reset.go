//nolint:revive
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

// CompletePasswordReset consumes the reset token, replaces the password hash,
// revokes all sessions and API keys, and appends the three audit events in one
// database transaction. A partial failure cannot leave a reset token consumed
// while existing credentials remain usable.
func (s *Store) CompletePasswordReset(ctx context.Context, tokenHash []byte, passwordHash string, now time.Time, passwordAudit, sessionAudit, keyAudit *identity.AuditEvent) (*identity.VerificationToken, error) {
	var token *identity.VerificationToken
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `UPDATE identity.verification_tokens SET consumed_at=$2
			WHERE token_hash=$1 AND kind='password_reset' AND consumed_at IS NULL AND expires_at>$2
			RETURNING token_id, user_id, kind, token_hash, expires_at, consumed_at, created_at`, tokenHash, now)
		consumed, err := scanVerification(row)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			var consumedAt pgtype.Timestamptz
			var expires time.Time
			if lookupErr := tx.QueryRow(ctx, `SELECT consumed_at, expires_at FROM identity.verification_tokens WHERE token_hash=$1 AND kind='password_reset'`, tokenHash).Scan(&consumedAt, &expires); lookupErr != nil {
				return identity.ErrStoreNotFound
			}
			if consumedAt.Valid {
				return identity.ErrStoreConsumed
			}
			return identity.ErrStoreExpired
		}
		token = consumed
		tag, err := tx.Exec(ctx, `UPDATE identity.users SET password_hash=$2, updated_at=$3, version=version+1 WHERE user_id=$1`, token.UserID, passwordHash, now)
		if err != nil {
			return mapError(err)
		}
		if tag.RowsAffected() != 1 {
			return identity.ErrStoreNotFound
		}
		rows, err := tx.Query(ctx, `UPDATE identity.sessions SET revoked_at=$2
			WHERE user_id=$1 AND revoked_at IS NULL RETURNING session_id`, token.UserID, now)
		if err != nil {
			return err
		}
		var sessionIDs []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			sessionIDs = append(sessionIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range sessionIDs {
			if _, err := tx.Exec(ctx, `UPDATE identity.refresh_tokens SET status='revoked' WHERE session_id=$1 AND status='active'`, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE identity.api_keys SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL`, token.UserID, now); err != nil {
			return err
		}
		userID := token.UserID.String()
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
		for _, audit := range []*identity.AuditEvent{passwordAudit, sessionAudit, keyAudit} {
			if err := appendAuditTx(ctx, tx, audit); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return token, nil
}
