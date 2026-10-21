//nolint:revive
package postgres

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

func (s *Store) GetLoginState(ctx context.Context, subjectHash, ipHash []byte, now time.Time) (identity.LockState, error) {
	zero := make([]byte, 32)
	rows, err := s.pool.Query(ctx, `SELECT subject_hash, failures, last_failure_at, backoff_until, locked_until
		FROM identity.login_attempts
		WHERE (subject_hash=$1 AND ip_hash=$2) OR (subject_hash=$3 AND ip_hash=$2)`, subjectHash, ipHash, zero)
	if err != nil {
		return identity.LockState{}, err
	}
	defer rows.Close()
	var state identity.LockState
	for rows.Next() {
		var subject []byte
		var failures int
		var lastFailure time.Time
		var backoff, locked pgtype.Timestamptz
		if err := rows.Scan(&subject, &failures, &lastFailure, &backoff, &locked); err != nil {
			return identity.LockState{}, err
		}
		if now.Sub(lastFailure) > 24*time.Hour {
			continue
		}
		candidate := identity.LockState{Failures: failures, CaptchaRequired: failures >= 10, BackoffUntil: timePtr(backoff), LockedUntil: timePtr(locked)}
		state = identity.StrongerLockState(state, candidate)
	}
	return state, rows.Err()
}

func (s *Store) RecordLoginFailure(ctx context.Context, subjectHash, ipHash []byte, policy identity.LockoutPolicy, now time.Time, audit *identity.AuditEvent) (identity.LockState, error) {
	var result identity.LockState
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		subjectState, err := recordFailureTx(ctx, tx, subjectHash, ipHash, policy, now)
		if err != nil {
			return err
		}
		ipState, err := recordFailureTx(ctx, tx, make([]byte, 32), ipHash, policy, now)
		if err != nil {
			return err
		}
		result = identity.StrongerLockState(subjectState, ipState)
		return appendAuditTx(ctx, tx, audit)
	})
	return result, err
}

func recordFailureTx(ctx context.Context, tx pgx.Tx, subjectHash, ipHash []byte, policy identity.LockoutPolicy, now time.Time) (identity.LockState, error) {
	var failures int
	err := tx.QueryRow(ctx, `INSERT INTO identity.login_attempts
		(subject_hash, ip_hash, failures, first_failure_at, last_failure_at)
		VALUES ($1,$2,1,$3,$3)
		ON CONFLICT (subject_hash, ip_hash) DO UPDATE SET
			failures = CASE WHEN login_attempts.last_failure_at < $4 THEN 1 ELSE login_attempts.failures + 1 END,
			first_failure_at = CASE WHEN login_attempts.last_failure_at < $4 THEN $3 ELSE login_attempts.first_failure_at END,
			last_failure_at = $3,
			backoff_until = NULL,
			locked_until = NULL
		RETURNING failures`, subjectHash, ipHash, now, now.Add(-24*time.Hour)).Scan(&failures)
	if err != nil {
		return identity.LockState{}, err
	}
	state := identity.ComputeLockState(failures, policy, now)
	if _, err := tx.Exec(ctx, `UPDATE identity.login_attempts SET backoff_until=$3, locked_until=$4
		WHERE subject_hash=$1 AND ip_hash=$2`, subjectHash, ipHash, state.BackoffUntil, state.LockedUntil); err != nil {
		return identity.LockState{}, err
	}
	return state, nil
}

func (s *Store) ClearLoginFailures(ctx context.Context, subjectHash, _ []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM identity.login_attempts WHERE subject_hash=$1`, subjectHash)
	return err
}

func (s *Store) ListAudit(ctx context.Context, filter identity.AuditFilter) ([]identity.AuditEvent, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT event_id, occurred_at, actor_type, actor_id, action, target_type, target_id, outcome,
		ip_hash, user_agent, request_id, metadata, prev_hash, event_hash
		FROM identity.audit_events WHERE 1=1`
	args := []any{}
	if filter.ActorID != "" {
		args = append(args, filter.ActorID)
		query += ` AND actor_id=$` + itoa(len(args))
	}
	if filter.Action != "" {
		args = append(args, filter.Action)
		query += ` AND action=$` + itoa(len(args))
	}
	if filter.TargetID != "" {
		args = append(args, filter.TargetID)
		query += ` AND target_id=$` + itoa(len(args))
	}
	args = append(args, limit)
	query += ` ORDER BY occurred_at DESC LIMIT $` + itoa(len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []identity.AuditEvent
	for rows.Next() {
		var event identity.AuditEvent
		var ipHash, prevHash, metadata []byte
		if err := rows.Scan(&event.ID, &event.OccurredAt, &event.ActorType, &event.ActorID, &event.Action,
			&event.TargetType, &event.TargetID, &event.Outcome, &ipHash, &event.UserAgent,
			&event.RequestID, &metadata, &prevHash, &event.EventHash); err != nil {
			return nil, err
		}
		event.IPHash = ipHash
		event.PrevHash = prevHash
		event.Metadata = map[string]string{}
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &event.Metadata)
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) CreateKYCCase(ctx context.Context, kyc *identity.KYCCase, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if kyc.ID == uuid.Nil {
			kyc.ID, _ = uuid.NewV7()
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity.kyc_cases
			(case_id, user_id, provider_ref, status, level, reason, submitted_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			kyc.ID, kyc.UserID, kyc.ProviderRef, kyc.Status, kyc.Level, kyc.Reason, kyc.SubmittedAt, kyc.UpdatedAt); err != nil {
			return mapError(err)
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func (s *Store) GetKYCCase(ctx context.Context, caseID uuid.UUID, owner *uuid.UUID) (*identity.KYCCase, error) {
	row := s.pool.QueryRow(ctx, `SELECT case_id, user_id, provider_ref, status, level, reason, submitted_at, updated_at, decided_at, decided_by
		FROM identity.kyc_cases WHERE case_id=$1`, caseID)
	kyc, err := scanKYC(row)
	if err != nil {
		return nil, mapError(err)
	}
	if owner != nil && kyc.UserID != *owner {
		return nil, identity.ErrStoreNotFound
	}
	return kyc, nil
}

func (s *Store) ListKYCCases(ctx context.Context, owner *uuid.UUID, limit int) ([]identity.KYCCase, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if owner != nil {
		rows, err = s.pool.Query(ctx, `SELECT case_id, user_id, provider_ref, status, level, reason, submitted_at, updated_at, decided_at, decided_by
			FROM identity.kyc_cases WHERE user_id=$1 ORDER BY submitted_at DESC LIMIT $2`, *owner, limit)
	} else {
		rows, err = s.pool.Query(ctx, `SELECT case_id, user_id, provider_ref, status, level, reason, submitted_at, updated_at, decided_at, decided_by
			FROM identity.kyc_cases ORDER BY submitted_at DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []identity.KYCCase
	for rows.Next() {
		kyc, err := scanKYC(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *kyc)
	}
	return out, rows.Err()
}

func (s *Store) DecideKYCCase(ctx context.Context, caseID uuid.UUID, status identity.KYCStatus, level int16, reason string, decidedBy uuid.UUID, now time.Time, audit *identity.AuditEvent) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE identity.kyc_cases SET status=$2, level=$3, reason=$4, decided_at=$5, decided_by=$6, updated_at=$5
			WHERE case_id=$1`, caseID, status, level, reason, now, decidedBy)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return identity.ErrStoreNotFound
		}
		return appendAuditTx(ctx, tx, audit)
	})
}

func scanKYC(row rowScanner) (*identity.KYCCase, error) {
	var kyc identity.KYCCase
	var decidedAt pgtype.Timestamptz
	var decidedBy pgtype.UUID
	err := row.Scan(&kyc.ID, &kyc.UserID, &kyc.ProviderRef, &kyc.Status, &kyc.Level, &kyc.Reason,
		&kyc.SubmittedAt, &kyc.UpdatedAt, &decidedAt, &decidedBy)
	if err != nil {
		return nil, err
	}
	kyc.DecidedAt = timePtr(decidedAt)
	if decidedBy.Valid {
		id := uuid.UUID(decidedBy.Bytes)
		kyc.DecidedBy = &id
	}
	return &kyc, nil
}

func (s *Store) EnqueueNotification(ctx context.Context, notification *identity.Notification) error {
	if notification.ID == uuid.Nil {
		notification.ID, _ = uuid.NewV7()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO identity.notification_outbox
		(notification_id, user_id, channel, kind, destination_ciphertext, payload_ciphertext, status, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,'pending',$7)`,
		notification.ID, notification.UserID, notification.Channel, notification.Kind,
		notification.DestinationCiphertext, notification.PayloadCiphertext, notification.CreatedAt)
	return mapError(err)
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
