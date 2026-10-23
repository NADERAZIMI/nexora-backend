package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
)

const auditActionRole = "actor_role"

// appendAuditTx appends one event to the hash chain inside the caller's
// transaction. The head row is locked so concurrent writers cannot fork it.
func appendAuditTx(ctx context.Context, tx pgx.Tx, event *reference.AuditEvent, actorRole string) error {
	if event.EventID == uuid.Nil {
		event.EventID = uuid.New()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	// PostgreSQL timestamptz stores microsecond precision. Truncate before
	// hashing so a verifier reading the persisted value recomputes the same
	// digest.
	event.OccurredAt = event.OccurredAt.UTC().Truncate(time.Microsecond)
	if event.Metadata == nil {
		event.Metadata = map[string]any{}
	}
	if event.RequestID == "" {
		event.RequestID = "unknown"
	}
	if actorRole == "" {
		actorRole = "authenticated"
	}
	var previous []byte
	if err := tx.QueryRow(ctx, `SELECT last_hash FROM reference.audit_heads WHERE id=1 FOR UPDATE`).Scan(&previous); err != nil {
		return err
	}
	hash := auditDigest(previous, *event, actorRole)
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO reference.audit_events
		(event_id, occurred_at, actor_id, actor_role, action, target_type, target_id, outcome, request_id, metadata, prev_hash, event_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12)`,
		event.EventID, event.OccurredAt, event.ActorID, actorRole, event.Action, event.TargetType,
		event.TargetID, event.Outcome, event.RequestID, string(metadata), nullBytes(previous), hash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE reference.audit_heads SET last_hash=$1, last_event_id=$2, updated_at=$3 WHERE id=1`,
		hash, event.EventID, event.OccurredAt); err != nil {
		return err
	}
	event.PrevHash = hex.EncodeToString(previous)
	event.EventHash = hex.EncodeToString(hash)
	event.ActorRole = actorRole
	return nil
}

func auditDigest(previous []byte, event reference.AuditEvent, actorRole string) []byte {
	metadata, _ := json.Marshal(event.Metadata)
	hash := sha256.New()
	fields := []string{
		hex.EncodeToString(previous),
		event.EventID.String(),
		event.OccurredAt.UTC().Format(time.RFC3339Nano),
		event.ActorID.String(),
		actorRole,
		event.Action,
		event.TargetType,
		event.TargetID,
		event.Outcome,
		event.RequestID,
		string(metadata),
	}
	for _, field := range fields {
		_, _ = hash.Write([]byte(field))
		_, _ = hash.Write([]byte{0})
	}
	return hash.Sum(nil)
}

// ListAudit returns the append-only audit trail newest first.
func (s *Store) ListAudit(ctx context.Context, limit int, before time.Time) ([]reference.AuditEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT event_id, occurred_at, actor_id, actor_role, action, target_type, target_id,
		outcome, request_id, metadata, prev_hash, event_hash
		FROM reference.audit_events`
	args := []any{}
	if !before.IsZero() {
		query += ` WHERE occurred_at < $1`
		args = append(args, before)
	}
	query += ` ORDER BY occurred_at DESC, event_id DESC LIMIT ` + itoa(limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []reference.AuditEvent
	for rows.Next() {
		var event reference.AuditEvent
		var metadata []byte
		var prevHash []byte
		var eventHash []byte
		if err := rows.Scan(&event.EventID, &event.OccurredAt, &event.ActorID, &event.ActorRole,
			&event.Action, &event.TargetType, &event.TargetID, &event.Outcome, &event.RequestID,
			&metadata, &prevHash, &eventHash); err != nil {
			return nil, err
		}
		event.PrevHash = hex.EncodeToString(prevHash)
		event.EventHash = hex.EncodeToString(eventHash)
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &event.Metadata); err != nil {
				return nil, err
			}
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// AuditEvent builds an event for a mutation.
func AuditEvent(actor reference.Principal, action, targetType, targetID, outcome string, metadata map[string]any) *reference.AuditEvent {
	return &reference.AuditEvent{
		EventID:    uuid.New(),
		OccurredAt: time.Now().UTC(),
		ActorID:    actor.UserID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Outcome:    outcome,
		RequestID:  actor.RequestID,
		Metadata:   metadata,
	}
}

func nullBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
