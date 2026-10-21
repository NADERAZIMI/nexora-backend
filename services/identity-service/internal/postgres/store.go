// Package postgres is the authoritative PostgreSQL 17 persistence adapter for
// the identity service.
//
//nolint:revive
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

// Store implements identity.Store against PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// New opens and verifies a PostgreSQL pool.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.MaxConns < 4 || cfg.MaxConns > 32 {
		cfg.MaxConns = 12
	}
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// NewFromPool wraps an existing pool (used by tests).
func NewFromPool(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the pool for migration and health checks.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
func (s *Store) Close()                         { s.pool.Close() }

func withTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.ErrStoreNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return identity.ErrStoreConflict
		case "23503":
			return identity.ErrStoreNotFound
		}
	}
	return err
}

// CreateUser inserts a pending user, password credential, initial verification
// token, roles, and audit event in one transaction.
func (s *Store) CreateUser(ctx context.Context, user *identity.User, password *identity.Credential, verification *identity.VerificationToken, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO identity.users
			(user_id, email_ciphertext, email_hash, phone_ciphertext, phone_hash, password_hash, status, kyc_level, country_code, email_verified_at, phone_verified_at, mfa_enrolled_at, created_at, updated_at, version)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			user.ID, user.EmailCiphertext, user.EmailHash, nullBytes(user.PhoneCiphertext), nullBytes(user.PhoneHash),
			user.PasswordHash, user.Status, user.KYCLevel, nullString(user.CountryCode),
			nullTime(user.EmailVerifiedAt), nullTime(user.PhoneVerifiedAt), nullTime(user.MFAEnrolledAt),
			user.CreatedAt, user.UpdatedAt, user.Version)
		if err != nil {
			return mapError(err)
		}
		if password != nil {
			if password.ID == uuid.Nil {
				password.ID, _ = uuid.NewV7()
			}
			if _, err := tx.Exec(ctx, `INSERT INTO identity.credentials
				(credential_id, user_id, kind, secret_ciphertext, metadata, created_at)
				VALUES ($1,$2,$3,$4,$5,$6)`,
				password.ID, user.ID, password.Kind, password.SecretCiphertext, jsonMetadata(password.Metadata), password.CreatedAt); err != nil {
				return mapError(err)
			}
		}
		if verification != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO identity.verification_tokens
				(token_id, user_id, kind, token_hash, expires_at, created_at)
				VALUES ($1,$2,$3,$4,$5,$6)`,
				verification.ID, user.ID, verification.Kind, verification.TokenHash, verification.ExpiresAt, verification.CreatedAt); err != nil {
				return mapError(err)
			}
		}
		for _, role := range user.Roles {
			if _, err := tx.Exec(ctx, `INSERT INTO identity.user_roles (user_id, role, granted_at) VALUES ($1,$2,$3)`, user.ID, role, user.CreatedAt); err != nil {
				return mapError(err)
			}
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) GetUserByEmailHash(ctx context.Context, hash []byte) (*identity.User, error) {
	return s.getUser(ctx, `SELECT `+userColumns+` FROM identity.users WHERE email_hash=$1`, hash)
}

func (s *Store) GetUserByPhoneHash(ctx context.Context, hash []byte) (*identity.User, error) {
	return s.getUser(ctx, `SELECT `+userColumns+` FROM identity.users WHERE phone_hash=$1`, hash)
}

func (s *Store) GetUserByID(ctx context.Context, userID uuid.UUID) (*identity.User, error) {
	return s.getUser(ctx, `SELECT `+userColumns+` FROM identity.users WHERE user_id=$1`, userID)
}

const userColumns = `user_id, email_ciphertext, email_hash, phone_ciphertext, phone_hash, password_hash,
status, kyc_level, country_code, email_verified_at, phone_verified_at, mfa_enrolled_at, created_at, updated_at, version`

func (s *Store) getUser(ctx context.Context, query string, args ...any) (*identity.User, error) {
	row := s.pool.QueryRow(ctx, query, args...)
	user, err := scanUser(row)
	if err != nil {
		return nil, mapError(err)
	}
	roles, err := s.loadRoles(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	user.Roles = roles
	return user, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (*identity.User, error) {
	var user identity.User
	var phoneCipher, phoneHash []byte
	var country pgtype.Text
	var emailVerified, phoneVerified, mfaEnrolled pgtype.Timestamptz
	err := row.Scan(&user.ID, &user.EmailCiphertext, &user.EmailHash, &phoneCipher, &phoneHash,
		&user.PasswordHash, &user.Status, &user.KYCLevel, &country,
		&emailVerified, &phoneVerified, &mfaEnrolled, &user.CreatedAt, &user.UpdatedAt, &user.Version)
	if err != nil {
		return nil, err
	}
	user.PhoneCiphertext = phoneCipher
	user.PhoneHash = phoneHash
	if country.Valid {
		value := country.String
		user.CountryCode = &value
	}
	user.EmailVerifiedAt = timePtr(emailVerified)
	user.PhoneVerifiedAt = timePtr(phoneVerified)
	user.MFAEnrolledAt = timePtr(mfaEnrolled)
	return &user, nil
}

func (s *Store) loadRoles(ctx context.Context, userID uuid.UUID) ([]identity.Role, error) {
	rows, err := s.pool.Query(ctx, `SELECT role FROM identity.user_roles WHERE user_id=$1 ORDER BY role`, userID)
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

func (s *Store) SetUserStatus(ctx context.Context, userID uuid.UUID, status identity.UserStatus, now time.Time, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE identity.users SET status=$2, updated_at=$3, version=version+1 WHERE user_id=$1`, userID, status, now)
		if err != nil {
			return mapError(err)
		}
		if tag.RowsAffected() == 0 {
			return identity.ErrStoreNotFound
		}
		if status != identity.UserActive {
			rows, err := tx.Query(ctx, `UPDATE identity.sessions SET revoked_at=$2
				WHERE user_id=$1 AND revoked_at IS NULL RETURNING session_id`, userID, now)
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
			if err := rows.Err(); err != nil {
				return err
			}
			for _, id := range ids {
				if _, err := tx.Exec(ctx, `UPDATE identity.refresh_tokens SET status='revoked' WHERE session_id=$1 AND status='active'`, id); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE identity.api_keys SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL`, userID, now); err != nil {
				return err
			}
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) SetEmailVerified(ctx context.Context, userID uuid.UUID, now time.Time, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE identity.users
			SET email_verified_at=$2, status=CASE WHEN status='pending' THEN 'active' ELSE status END, updated_at=$2, version=version+1
			WHERE user_id=$1`, userID, now)
		if err != nil {
			return mapError(err)
		}
		if tag.RowsAffected() == 0 {
			return identity.ErrStoreNotFound
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) SetPhoneVerified(ctx context.Context, userID uuid.UUID, now time.Time, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE identity.users SET phone_verified_at=$2, updated_at=$2, version=version+1 WHERE user_id=$1`, userID, now)
		if err != nil {
			return mapError(err)
		}
		if tag.RowsAffected() == 0 {
			return identity.ErrStoreNotFound
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) UpdatePassword(ctx context.Context, userID uuid.UUID, passwordHash string, now time.Time, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE identity.users SET password_hash=$2, updated_at=$3, version=version+1 WHERE user_id=$1`, userID, passwordHash, now)
		if err != nil {
			return mapError(err)
		}
		if tag.RowsAffected() == 0 {
			return identity.ErrStoreNotFound
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) SetMFAEnrolled(ctx context.Context, userID uuid.UUID, now time.Time, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE identity.users SET mfa_enrolled_at=COALESCE(mfa_enrolled_at,$2), updated_at=$2, version=version+1 WHERE user_id=$1`, userID, now)
		if err != nil {
			return mapError(err)
		}
		if tag.RowsAffected() == 0 {
			return identity.ErrStoreNotFound
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) SetUserRoles(ctx context.Context, userID uuid.UUID, roles []identity.Role, grantedBy uuid.UUID, now time.Time, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT true FROM identity.users WHERE user_id=$1 FOR UPDATE`, userID).Scan(&exists); err != nil {
			return mapError(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM identity.user_roles WHERE user_id=$1`, userID); err != nil {
			return mapError(err)
		}
		for _, role := range roles {
			if _, err := tx.Exec(ctx, `INSERT INTO identity.user_roles (user_id, role, granted_by, granted_at) VALUES ($1,$2,$3,$4)`, userID, role, grantedBy, now); err != nil {
				return mapError(err)
			}
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

// appendAuditTx appends an audit event while serializing the hash chain through
// the audit_heads singleton row.
func appendAuditTx(ctx context.Context, tx pgx.Tx, event *identity.AuditEvent) error {
	if event == nil {
		return nil
	}
	if event.ID == uuid.Nil {
		event.ID, _ = uuid.NewV7()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	var prev []byte
	if err := tx.QueryRow(ctx, `SELECT last_hash FROM identity.audit_heads WHERE id=1 FOR UPDATE`).Scan(&prev); err != nil {
		return err
	}
	event.PrevHash = append([]byte(nil), prev...)
	canonical, err := event.CanonicalAuditBytes()
	if err != nil {
		return err
	}
	event.EventHash = identity.SHA256(append(append([]byte(nil), prev...), canonical...))
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO identity.audit_events
		(event_id, occurred_at, actor_type, actor_id, action, target_type, target_id, outcome, ip_hash, user_agent, request_id, metadata, prev_hash, event_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		event.ID, event.OccurredAt, event.ActorType, event.ActorID, event.Action,
		event.TargetType, event.TargetID, event.Outcome, nullBytes(event.IPHash),
		event.UserAgent, event.RequestID, metadata, nullBytes(event.PrevHash), event.EventHash); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE identity.audit_heads SET last_hash=$1, last_event_id=$2, updated_at=$3 WHERE id=1`, event.EventHash, event.ID, event.OccurredAt)
	return err
}

// AppendAudit appends a standalone audit event.
func (s *Store) AppendAudit(ctx context.Context, event *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error { return appendAuditTx(ctx, tx, event) })
}

// Migrate applies or reverses embedded SQL migrations under an advisory lock.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, direction string) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('nexora_identity_migrations'))`); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('nexora_identity_migrations'))`)
	}()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_migrations (
		version text PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	files, err := migrationFiles(fsys, direction)
	if err != nil {
		return err
	}
	applied := map[string]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM public.schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return err
		}
		applied[version] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if direction == "down" {
		for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
			files[i], files[j] = files[j], files[i]
		}
	}
	for _, file := range files {
		version := strings.SplitN(file.name, "_", 2)[0]
		if direction == "up" && applied[version] {
			continue
		}
		if direction == "down" && !applied[version] {
			continue
		}
		sqlBytes, err := fs.ReadFile(fsys, file.name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", file.name, err)
		}
		if direction == "up" {
			if _, err := tx.Exec(ctx, `INSERT INTO public.schema_migrations (version, name) VALUES ($1,$2)`, version, file.name); err != nil {
				_ = tx.Rollback(ctx)
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `DELETE FROM public.schema_migrations WHERE version=$1`, version); err != nil {
				_ = tx.Rollback(ctx)
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

type migrationFile struct{ name string }

func migrationFiles(fsys fs.FS, direction string) ([]migrationFile, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	suffix := "." + direction + ".sql"
	var out []migrationFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		out = append(out, migrationFile{name: entry.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	if len(out) == 0 {
		return nil, errors.New("no migrations found")
	}
	return out, nil
}

// Helpers.

func nullBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func nullString(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	return *value
}

func nullTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func jsonMetadata(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return []byte(raw)
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	at := value.Time
	return &at
}
