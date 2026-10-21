//nolint:revive
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

func (s *Store) CreateCredential(ctx context.Context, credential *identity.Credential, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if credential.ID == uuid.Nil {
			credential.ID, _ = uuid.NewV7()
		}
		if credential.SecretCiphertext == nil {
			credential.SecretCiphertext = []byte{}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity.credentials
			(credential_id, user_id, kind, secret_ciphertext, metadata, totp_last_step, created_at, last_used_at, verified_at, revoked_at)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$10)`,
			credential.ID, credential.UserID, credential.Kind, credential.SecretCiphertext, string(jsonMetadata(credential.Metadata)),
			credential.TOTPLastStep, credential.CreatedAt, nullTime(credential.LastUsedAt), nullTime(credential.VerifiedAt), nullTime(credential.RevokedAt)); err != nil {
			return mapError(err)
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) GetCredentials(ctx context.Context, userID uuid.UUID, kind identity.CredentialKind) ([]identity.Credential, error) {
	rows, err := s.pool.Query(ctx, `SELECT credential_id, user_id, kind, secret_ciphertext, metadata, totp_last_step, created_at, last_used_at, verified_at, revoked_at
		FROM identity.credentials WHERE user_id=$1 AND kind=$2 AND revoked_at IS NULL ORDER BY created_at ASC`, userID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []identity.Credential
	for rows.Next() {
		credential, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *credential)
	}
	return out, rows.Err()
}

func (s *Store) GetCredential(ctx context.Context, credentialID uuid.UUID) (*identity.Credential, error) {
	row := s.pool.QueryRow(ctx, `SELECT credential_id, user_id, kind, secret_ciphertext, metadata, totp_last_step, created_at, last_used_at, verified_at, revoked_at
		FROM identity.credentials WHERE credential_id=$1`, credentialID)
	credential, err := scanCredential(row)
	if err != nil {
		return nil, mapError(err)
	}
	return credential, nil
}

func (s *Store) UpdateCredential(ctx context.Context, credential *identity.Credential, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE identity.credentials SET secret_ciphertext=$3, metadata=$4::jsonb,
			totp_last_step=$5, last_used_at=$6, verified_at=$7, revoked_at=$8
			WHERE credential_id=$1 AND user_id=$2`,
			credential.ID, credential.UserID, credential.SecretCiphertext, string(credential.Metadata),
			credential.TOTPLastStep, nullTime(credential.LastUsedAt), nullTime(credential.VerifiedAt), nullTime(credential.RevokedAt))
		if err != nil {
			return mapError(err)
		}
		if tag.RowsAffected() == 0 {
			return identity.ErrStoreNotFound
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) MarkTOTPUsed(ctx context.Context, credentialID uuid.UUID, step int64, now time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE identity.credentials SET totp_last_step=$2, last_used_at=$3
		WHERE credential_id=$1 AND kind='totp' AND revoked_at IS NULL
		AND (totp_last_step IS NULL OR totp_last_step < $2)`, credentialID, step, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ConfirmTOTP atomically verifies an enrollment credential, records the
// consumed time step, enables MFA on the account, and appends both audit
// events. A crash cannot leave a verified credential with MFA disabled.
func (s *Store) ConfirmTOTP(ctx context.Context, credentialID, userID uuid.UUID, step int64, now time.Time, confirmAudit, enrollAudit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE identity.credentials
			SET verified_at=$3, last_used_at=$3, totp_last_step=$4,
				metadata='{"verified":true}'::jsonb
			WHERE credential_id=$1 AND user_id=$2 AND kind='totp'
				AND verified_at IS NULL AND revoked_at IS NULL
				AND (totp_last_step IS NULL OR totp_last_step < $4)`,
			credentialID, userID, now, step)
		if err != nil {
			return mapError(err)
		}
		if tag.RowsAffected() != 1 {
			return identity.ErrStoreConflict
		}
		tag, err = tx.Exec(ctx, `UPDATE identity.users
			SET mfa_enrolled_at=COALESCE(mfa_enrolled_at,$2), updated_at=$2, version=version+1
			WHERE user_id=$1`, userID, now)
		if err != nil {
			return mapError(err)
		}
		if tag.RowsAffected() != 1 {
			return identity.ErrStoreNotFound
		}
		if err := appendAuditTx(ctx, tx, confirmAudit); err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, enrollAudit)
	})
}

func (s *Store) CreateRecoveryCodes(ctx context.Context, userID uuid.UUID, codes []identity.Credential, now time.Time, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE identity.credentials SET revoked_at=$2
			WHERE user_id=$1 AND kind='recovery_code' AND revoked_at IS NULL`, userID, now); err != nil {
			return err
		}
		for i := range codes {
			if codes[i].ID == uuid.Nil {
				codes[i].ID, _ = uuid.NewV7()
			}
			if _, err := tx.Exec(ctx, `INSERT INTO identity.credentials
				(credential_id, user_id, kind, secret_ciphertext, metadata, created_at)
				VALUES ($1,$2,$3,$4,$5::jsonb,$6)`,
				codes[i].ID, userID, codes[i].Kind, codes[i].SecretCiphertext, string(codes[i].Metadata), codes[i].CreatedAt); err != nil {
				return mapError(err)
			}
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) ConsumeRecoveryCode(ctx context.Context, userID uuid.UUID, tokenHash []byte, now time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE identity.credentials SET revoked_at=$3, last_used_at=$3
		WHERE user_id=$1 AND kind='recovery_code' AND revoked_at IS NULL AND secret_ciphertext=$2`, userID, tokenHash, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func scanCredential(row rowScanner) (*identity.Credential, error) {
	var credential identity.Credential
	var metadata []byte
	var lastStep pgtype.Int8
	var lastUsed, verified, revoked pgtype.Timestamptz
	err := row.Scan(&credential.ID, &credential.UserID, &credential.Kind, &credential.SecretCiphertext,
		&metadata, &lastStep, &credential.CreatedAt, &lastUsed, &verified, &revoked)
	if err != nil {
		return nil, err
	}
	credential.Metadata = metadata
	if lastStep.Valid {
		value := lastStep.Int64
		credential.TOTPLastStep = &value
	}
	credential.LastUsedAt = timePtr(lastUsed)
	credential.VerifiedAt = timePtr(verified)
	credential.RevokedAt = timePtr(revoked)
	return &credential, nil
}

func (s *Store) CreateVerification(ctx context.Context, token *identity.VerificationToken, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if token.ID == uuid.Nil {
			token.ID, _ = uuid.NewV7()
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity.verification_tokens
			(token_id, user_id, kind, token_hash, expires_at, created_at)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			token.ID, token.UserID, token.Kind, token.TokenHash, token.ExpiresAt, token.CreatedAt); err != nil {
			return mapError(err)
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) ConsumeVerification(ctx context.Context, tokenHash []byte, kind string, now time.Time) (*identity.VerificationToken, error) {
	row := s.pool.QueryRow(ctx, `UPDATE identity.verification_tokens SET consumed_at=$3
		WHERE token_hash=$1 AND kind=$2 AND consumed_at IS NULL AND expires_at>$3
		RETURNING token_id, user_id, kind, token_hash, expires_at, consumed_at, created_at`, tokenHash, kind, now)
	token, err := scanVerification(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var consumedAt pgtype.Timestamptz
			var expires time.Time
			lookupErr := s.pool.QueryRow(ctx, `SELECT consumed_at, expires_at FROM identity.verification_tokens WHERE token_hash=$1 AND kind=$2`, tokenHash, kind).Scan(&consumedAt, &expires)
			if lookupErr != nil {
				return nil, identity.ErrStoreNotFound
			}
			if consumedAt.Valid {
				return nil, identity.ErrStoreConsumed
			}
			return nil, identity.ErrStoreExpired
		}
		return nil, err
	}
	return token, nil
}

// VerifyContact consumes a contact-verification token and updates the user
// verification state in one transaction with its audit event.
func (s *Store) VerifyContact(ctx context.Context, tokenHash []byte, kind string, now time.Time, audit *identity.AuditEvent) (*identity.VerificationToken, error) {
	var token *identity.VerificationToken
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `UPDATE identity.verification_tokens SET consumed_at=$3
			WHERE token_hash=$1 AND kind=$2 AND consumed_at IS NULL AND expires_at>$3
			RETURNING token_id, user_id, kind, token_hash, expires_at, consumed_at, created_at`, tokenHash, kind, now)
		consumed, err := scanVerification(row)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			var consumedAt pgtype.Timestamptz
			var expires time.Time
			if lookupErr := tx.QueryRow(ctx, `SELECT consumed_at, expires_at FROM identity.verification_tokens WHERE token_hash=$1 AND kind=$2`, tokenHash, kind).Scan(&consumedAt, &expires); lookupErr != nil {
				return identity.ErrStoreNotFound
			}
			if consumedAt.Valid {
				return identity.ErrStoreConsumed
			}
			return identity.ErrStoreExpired
		}
		token = consumed
		var tag pgconn.CommandTag
		if kind == "email" {
			tag, err = tx.Exec(ctx, `UPDATE identity.users
				SET email_verified_at=$2, status=CASE WHEN status='pending' THEN 'active' ELSE status END, updated_at=$2, version=version+1
				WHERE user_id=$1`, token.UserID, now)
		} else {
			tag, err = tx.Exec(ctx, `UPDATE identity.users
				SET phone_verified_at=$2, status=CASE WHEN status='pending' THEN 'active' ELSE status END, updated_at=$2, version=version+1
				WHERE user_id=$1`, token.UserID, now)
		}
		if err != nil {
			return mapError(err)
		}
		if tag.RowsAffected() != 1 {
			return identity.ErrStoreNotFound
		}
		if audit != nil {
			audit.ActorType = "user"
			audit.ActorID = token.UserID.String()
			audit.TargetType = "user"
			audit.TargetID = token.UserID.String()
		}
		return appendAuditTx(ctx, tx, audit)
	})
	if err != nil {
		return nil, err
	}
	return token, nil
}

func scanVerification(row rowScanner) (*identity.VerificationToken, error) {
	var token identity.VerificationToken
	var consumed pgtype.Timestamptz
	err := row.Scan(&token.ID, &token.UserID, &token.Kind, &token.TokenHash, &token.ExpiresAt, &consumed, &token.CreatedAt)
	if err != nil {
		return nil, err
	}
	token.ConsumedAt = timePtr(consumed)
	return &token, nil
}

func (s *Store) CreateMFAChallenge(ctx context.Context, challenge *identity.MFAChallenge) error {
	if challenge.ID == uuid.Nil {
		challenge.ID, _ = uuid.NewV7()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO identity.mfa_challenges
		(challenge_id, user_id, token_hash, methods, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		challenge.ID, challenge.UserID, challenge.TokenHash, challenge.Methods, challenge.ExpiresAt, challenge.CreatedAt)
	return mapError(err)
}

func (s *Store) ConsumeMFAChallenge(ctx context.Context, tokenHash []byte, now time.Time) (*identity.MFAChallenge, error) {
	row := s.pool.QueryRow(ctx, `UPDATE identity.mfa_challenges SET consumed_at=$2
		WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>$2
		RETURNING challenge_id, user_id, token_hash, methods, expires_at, consumed_at, created_at`, tokenHash, now)
	var challenge identity.MFAChallenge
	var consumed pgtype.Timestamptz
	err := row.Scan(&challenge.ID, &challenge.UserID, &challenge.TokenHash, &challenge.Methods, &challenge.ExpiresAt, &consumed, &challenge.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var consumedAt pgtype.Timestamptz
			if lookupErr := s.pool.QueryRow(ctx, `SELECT consumed_at FROM identity.mfa_challenges WHERE token_hash=$1`, tokenHash).Scan(&consumedAt); lookupErr != nil {
				return nil, identity.ErrStoreNotFound
			}
			if consumedAt.Valid {
				return nil, identity.ErrStoreConsumed
			}
			return nil, identity.ErrStoreExpired
		}
		return nil, err
	}
	challenge.ConsumedAt = timePtr(consumed)
	return &challenge, nil
}

func (s *Store) CreateWebAuthnChallenge(ctx context.Context, challenge *identity.WebAuthnChallenge) error {
	if challenge.ID == uuid.Nil {
		challenge.ID, _ = uuid.NewV7()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO identity.webauthn_challenges
		(challenge_id, user_id, kind, session_data, expires_at, created_at)
		VALUES ($1,$2,$3,$4::jsonb,$5,$6)`,
		challenge.ID, challenge.UserID, challenge.Kind, string(challenge.SessionData), challenge.ExpiresAt, challenge.CreatedAt)
	return mapError(err)
}

func (s *Store) LatestWebAuthnChallenge(ctx context.Context, userID uuid.UUID, kind string, now time.Time) (*identity.WebAuthnChallenge, error) {
	row := s.pool.QueryRow(ctx, `SELECT challenge_id, user_id, kind, session_data, expires_at, consumed_at, created_at
		FROM identity.webauthn_challenges
		WHERE user_id=$1 AND kind=$2 AND consumed_at IS NULL AND expires_at>$3
		ORDER BY created_at DESC LIMIT 1`, userID, kind, now)
	var challenge identity.WebAuthnChallenge
	var data []byte
	var consumed pgtype.Timestamptz
	err := row.Scan(&challenge.ID, &challenge.UserID, &challenge.Kind, &data, &challenge.ExpiresAt, &consumed, &challenge.CreatedAt)
	if err != nil {
		return nil, mapError(err)
	}
	challenge.SessionData = data
	challenge.ConsumedAt = timePtr(consumed)
	return &challenge, nil
}

func (s *Store) ConsumeWebAuthnChallenge(ctx context.Context, challengeID uuid.UUID, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE identity.webauthn_challenges SET consumed_at=$2 WHERE challenge_id=$1 AND consumed_at IS NULL`, challengeID, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return identity.ErrStoreConsumed
	}
	return nil
}

func consumeWebAuthnChallengeTx(ctx context.Context, tx pgx.Tx, challengeID, userID uuid.UUID, kind string, now time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE identity.webauthn_challenges SET consumed_at=$4
		WHERE challenge_id=$1 AND user_id=$2 AND kind=$3 AND consumed_at IS NULL AND expires_at>$4`,
		challengeID, userID, kind, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return identity.ErrStoreConsumed
	}
	return nil
}

// ConfirmWebAuthnRegistration consumes the ceremony, stores the verified
// credential, enables MFA, and writes both audit events atomically.
func (s *Store) ConfirmWebAuthnRegistration(ctx context.Context, challengeID, userID uuid.UUID, credential *identity.Credential, now time.Time, credentialAudit, enrollAudit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := consumeWebAuthnChallengeTx(ctx, tx, challengeID, userID, "registration", now); err != nil {
			return err
		}
		if credential.ID == uuid.Nil {
			credential.ID, _ = uuid.NewV7()
		}
		if credential.SecretCiphertext == nil {
			credential.SecretCiphertext = []byte{}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity.credentials
			(credential_id, user_id, kind, secret_ciphertext, metadata, totp_last_step, created_at, last_used_at, verified_at, revoked_at)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$10)`,
			credential.ID, credential.UserID, credential.Kind, credential.SecretCiphertext, string(jsonMetadata(credential.Metadata)),
			credential.TOTPLastStep, credential.CreatedAt, nullTime(credential.LastUsedAt), nullTime(credential.VerifiedAt), nullTime(credential.RevokedAt)); err != nil {
			return mapError(err)
		}
		tag, err := tx.Exec(ctx, `UPDATE identity.users
			SET mfa_enrolled_at=COALESCE(mfa_enrolled_at,$2), updated_at=$2, version=version+1
			WHERE user_id=$1`, userID, now)
		if err != nil {
			return mapError(err)
		}
		if tag.RowsAffected() != 1 {
			return identity.ErrStoreNotFound
		}
		if err := appendAuditTx(ctx, tx, credentialAudit); err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, enrollAudit)
	})
}

// CompleteWebAuthnAssertion consumes the authentication challenge and updates
// the authenticator signature counter in the same transaction.
func (s *Store) CompleteWebAuthnAssertion(ctx context.Context, challengeID, userID, credentialID uuid.UUID, metadata []byte, now time.Time, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := consumeWebAuthnChallengeTx(ctx, tx, challengeID, userID, "authentication", now); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE identity.credentials
			SET metadata=$3::jsonb, last_used_at=$4
			WHERE credential_id=$1 AND user_id=$2 AND kind='webauthn' AND revoked_at IS NULL`,
			credentialID, userID, string(jsonMetadata(metadata)), now)
		if err != nil {
			return mapError(err)
		}
		if tag.RowsAffected() != 1 {
			return identity.ErrStoreNotFound
		}
		return appendAuditTx(ctx, tx, audit)
	})
}
