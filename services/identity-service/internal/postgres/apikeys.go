//nolint:revive
package postgres

import (
	"context"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

func (s *Store) CreateAPIKey(ctx context.Context, key *identity.APIKey, maxActive int, now time.Time, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT true FROM identity.users WHERE user_id=$1 FOR UPDATE`, key.UserID).Scan(&exists); err != nil {
			return mapError(err)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM identity.api_keys
			WHERE user_id=$1 AND revoked_at IS NULL AND expires_at>$2`, key.UserID, now).Scan(&count); err != nil {
			return err
		}
		if maxActive > 0 && count >= maxActive {
			return identity.ErrStoreLimit
		}
		if key.ID == uuid.Nil {
			key.ID, _ = uuid.NewV7()
		}
		scopes := key.Scopes
		if scopes == nil {
			scopes = []string{}
		}
		allowlist := key.IPAllowlist
		if allowlist == nil {
			allowlist = []netip.Prefix{}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity.api_keys
			(key_id, user_id, label, key_prefix, algorithm, secret_ciphertext, public_key, scopes, ip_allowlist, created_at, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			key.ID, key.UserID, key.Label, key.KeyPrefix, key.Algorithm, nullBytes(key.SecretCiphertext), nullBytes(key.PublicKey),
			scopes, pgtype.FlatArray[netip.Prefix](allowlist), key.CreatedAt, nullTime(key.ExpiresAt)); err != nil {
			return mapError(err)
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) GetAPIKeyByPrefix(ctx context.Context, prefix string) (*identity.APIKey, error) {
	return s.getAPIKey(ctx, `SELECT `+apiKeyColumns+` FROM identity.api_keys WHERE key_prefix=$1`, prefix)
}

func (s *Store) GetAPIKey(ctx context.Context, keyID uuid.UUID) (*identity.APIKey, error) {
	return s.getAPIKey(ctx, `SELECT `+apiKeyColumns+` FROM identity.api_keys WHERE key_id=$1`, keyID)
}

const apiKeyColumns = `key_id, user_id, label, key_prefix, algorithm, secret_ciphertext, public_key, scopes, ip_allowlist, created_at, last_used_at, expires_at, revoked_at` //nolint:gosec // column list, not a credential

func (s *Store) getAPIKey(ctx context.Context, query string, args ...any) (*identity.APIKey, error) {
	row := s.pool.QueryRow(ctx, query, args...)
	key, err := scanAPIKey(row)
	if err != nil {
		return nil, mapError(err)
	}
	return key, nil
}

func (s *Store) ListAPIKeys(ctx context.Context, userID uuid.UUID) ([]identity.APIKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+apiKeyColumns+` FROM identity.api_keys WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []identity.APIKey
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *key)
	}
	return out, rows.Err()
}

func (s *Store) RevokeAPIKey(ctx context.Context, keyID uuid.UUID, owner *uuid.UUID, now time.Time, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var userID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT user_id FROM identity.api_keys WHERE key_id=$1 FOR UPDATE`, keyID).Scan(&userID); err != nil {
			return mapError(err)
		}
		if owner != nil && userID != *owner {
			return identity.ErrStoreNotFound
		}
		if _, err := tx.Exec(ctx, `UPDATE identity.api_keys SET revoked_at=COALESCE(revoked_at,$2) WHERE key_id=$1`, keyID, now); err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) RevokeAllAPIKeys(ctx context.Context, userID uuid.UUID, now time.Time, audit *identity.AuditEvent) (int, error) {
	count := 0
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE identity.api_keys SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL`, userID, now)
		if err != nil {
			return err
		}
		count = int(tag.RowsAffected())
		return appendAuditTx(ctx, tx, audit)
	})
	return count, err
}

func (s *Store) TouchAPIKey(ctx context.Context, keyID uuid.UUID, now time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE identity.api_keys SET last_used_at=$2 WHERE key_id=$1`, keyID, now)
	return err
}

func (s *Store) UseAPIKeyNonce(ctx context.Context, keyID uuid.UUID, nonce string, expiresAt time.Time, now time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `DELETE FROM identity.api_key_nonces WHERE key_id=$1 AND expires_at<=$2`, keyID, now); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO identity.api_key_nonces (key_id, nonce, expires_at)
		VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, keyID, nonce, expiresAt)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) CountActiveAPIKeys(ctx context.Context, userID uuid.UUID, now time.Time) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM identity.api_keys
		WHERE user_id=$1 AND revoked_at IS NULL AND expires_at>$2`, userID, now).Scan(&count)
	return count, err
}

func scanAPIKey(row rowScanner) (*identity.APIKey, error) {
	var key identity.APIKey
	var secret, public []byte
	var allowlist pgtype.FlatArray[netip.Prefix]
	var lastUsed, expires, revoked pgtype.Timestamptz
	err := row.Scan(&key.ID, &key.UserID, &key.Label, &key.KeyPrefix, &key.Algorithm,
		&secret, &public, &key.Scopes, &allowlist, &key.CreatedAt, &lastUsed, &expires, &revoked)
	if err != nil {
		return nil, err
	}
	key.SecretCiphertext = secret
	key.PublicKey = public
	key.IPAllowlist = append([]netip.Prefix(nil), allowlist...)
	key.LastUsedAt = timePtr(lastUsed)
	key.ExpiresAt = timePtr(expires)
	key.RevokedAt = timePtr(revoked)
	return &key, nil
}
