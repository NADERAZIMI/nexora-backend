package postgres

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
)

var shardPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,63}$`)

// Activate activates a published version for one shard. The whole operation is
// one transaction, so a shard either keeps its previous complete snapshot or
// moves to the next complete snapshot. A partially applied version is never
// visible.
func (s *Store) Activate(ctx context.Context, request reference.StoreActivation) (reference.Activation, error) {
	shardID := request.ShardID
	if shardID == "" {
		shardID = reference.DefaultShard
	}
	if !shardPattern.MatchString(shardID) {
		return reference.Activation{}, reference.InvalidInput("invalid shard identifier")
	}
	if len(request.IdempotencyKey) == 0 || len(request.IdempotencyKey) > 128 {
		return reference.Activation{}, reference.InvalidInput("idempotency key must be between 1 and 128 characters")
	}
	now := time.Now().UTC()
	effectiveAt := request.EffectiveAt
	if effectiveAt.IsZero() {
		effectiveAt = now
	}
	effectiveAt = effectiveAt.UTC()

	var activation reference.Activation
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialize mutations for one shard so concurrent idempotent requests and
		// expected-version checks cannot race before the active row is locked.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('nexora_reference_activation:' || $1))`, shardID); err != nil {
			return err
		}
		existing, found, err := loadActivationByKey(ctx, tx, shardID, request.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if existing.Version != request.Version {
				return reference.Conflict(reference.CodeConflict, "idempotency key was already used for a different version", map[string]any{
					"activation_id": existing.ActivationID, "existing_version": existing.Version,
				})
			}
			activation = existing
			return nil
		}

		var activeVersion int64
		var activeActivation uuid.UUID
		hasActive := true
		if err := tx.QueryRow(ctx, `SELECT version, activation_id FROM reference.active WHERE shard_id=$1 FOR UPDATE`, shardID).
			Scan(&activeVersion, &activeActivation); err != nil {
			if err != pgx.ErrNoRows {
				return err
			}
			hasActive = false
		}

		var publishedHash string
		if err := tx.QueryRow(ctx, `SELECT content_hash FROM reference.versions WHERE version=$1`, request.Version).Scan(&publishedHash); err != nil {
			if err == pgx.ErrNoRows {
				return reference.NotFound("version", itoa64(request.Version))
			}
			return err
		}
		latest, err := latestVersionTx(ctx, tx)
		if err != nil {
			return err
		}

		expected := int64(0)
		if hasActive {
			expected = activeVersion
		}
		if request.ExpectedVersion != nil {
			allowed := *request.ExpectedVersion == expected
			if hasActive && *request.ExpectedVersion == request.Version && activeVersion == request.Version {
				allowed = true
			}
			if !allowed {
				return reference.Conflict(reference.CodeStaleVersion, "shard active version changed", map[string]any{
					"expected_version": *request.ExpectedVersion, "active_version": expected,
				})
			}
		}

		if !hasActive {
			if request.Rollback {
				return reference.Conflict(reference.CodeInvalidState, "cannot roll back a shard with no active version", nil)
			}
			if latest > 0 && request.Version != latest {
				return reference.Conflict(reference.CodeStaleVersion, "only the latest published version can be activated initially", map[string]any{
					"latest_version": latest, "requested_version": request.Version,
				})
			}
		} else if request.Version < activeVersion {
			if !request.Rollback {
				return reference.Conflict(reference.CodeRollbackRequired, "activating an older version requires an explicit rollback", map[string]any{
					"active_version": activeVersion, "requested_version": request.Version,
				})
			}
		} else if request.Version == activeVersion {
			activation, err = loadActivationTx(ctx, tx, activeActivation)
			return err
		} else {
			if request.Rollback {
				return reference.Conflict(reference.CodeInvalidState, "rollback target must be older than the active version", map[string]any{
					"active_version": activeVersion, "requested_version": request.Version,
				})
			}
			if latest > 0 && request.Version != latest {
				return reference.Conflict(reference.CodeStaleVersion, "only the latest published version can be activated", map[string]any{
					"latest_version": latest, "requested_version": request.Version,
				})
			}
		}

		status := reference.ActivationActive
		var activatedAt *time.Time
		scheduled := effectiveAt.After(now)
		if scheduled {
			status = reference.ActivationPending
		} else {
			activatedAt = &now
			if hasActive {
				if _, err := tx.Exec(ctx, `UPDATE reference.activations SET status='superseded'
					WHERE activation_id=$1 AND status='active'`, activeActivation); err != nil {
					return err
				}
			}
		}
		var rollbackOf *int64
		if request.Rollback {
			value := activeVersion
			rollbackOf = &value
		}
		activation = reference.Activation{
			ActivationID:    uuid.New(),
			ShardID:         shardID,
			Version:         request.Version,
			Status:          status,
			ExpectedVersion: &expected,
			RollbackOf:      rollbackOf,
			RequestedBy:     request.Actor.UserID,
			RequestedAt:     now,
			EffectiveAt:     effectiveAt,
			ActivatedAt:     activatedAt,
			IdempotencyKey:  request.IdempotencyKey,
		}
		if _, err := tx.Exec(ctx, `INSERT INTO reference.activations
			(activation_id, shard_id, version, status, expected_version, rollback_of, requested_by,
			 requested_at, effective_at, activated_at, idempotency_key)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			activation.ActivationID, activation.ShardID, activation.Version, activation.Status,
			activation.ExpectedVersion, nullInt64(activation.RollbackOf), activation.RequestedBy,
			activation.RequestedAt, activation.EffectiveAt, activation.ActivatedAt, activation.IdempotencyKey); err != nil {
			return err
		}
		if !scheduled {
			if _, err := tx.Exec(ctx, `INSERT INTO reference.active (shard_id, version, activation_id, activated_at)
				VALUES ($1,$2,$3,$4)
				ON CONFLICT (shard_id) DO UPDATE SET version=EXCLUDED.version,
				activation_id=EXCLUDED.activation_id, activated_at=EXCLUDED.activated_at`,
				shardID, request.Version, activation.ActivationID, now); err != nil {
				return err
			}
		}
		action := "reference.activation.schedule"
		if !scheduled {
			action = "reference.activation.activate"
			if request.Rollback {
				action = "reference.activation.rollback"
			}
		}
		event := AuditEvent(request.Actor, action, "activation", activation.ActivationID.String(), "success", map[string]any{
			"shard_id": shardID, "version": request.Version, "previous_version": expected,
			"effective_at": effectiveAt, "rollback": request.Rollback, "reason": request.Reason,
			"content_hash": publishedHash, "idempotency_key": request.IdempotencyKey,
		})
		return appendAuditTx(ctx, tx, event, primaryRole(request.Actor))
	})
	if err != nil {
		return reference.Activation{}, mapPgError(err, reference.CodeConflict, "activation failed")
	}
	return activation, nil
}

// PromoteDue promotes scheduled activations whose effective time has arrived.
// It is safe to call concurrently; each promotion is independently atomic.
func (s *Store) PromoteDue(ctx context.Context, now time.Time, limit int) ([]reference.Activation, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT activation_id FROM reference.activations
		WHERE status='pending' AND effective_at <= $1 ORDER BY effective_at LIMIT `+itoa(limit), now.UTC())
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	promoted := make([]reference.Activation, 0, len(ids))
	for _, id := range ids {
		activation, ok, err := s.promoteOne(ctx, id, now.UTC())
		if err != nil {
			return promoted, err
		}
		if ok {
			promoted = append(promoted, activation)
		}
	}
	return promoted, nil
}

func (s *Store) promoteOne(ctx context.Context, activationID uuid.UUID, now time.Time) (reference.Activation, bool, error) {
	var activation reference.Activation
	promoted := false
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := loadActivationRow(ctx, tx, activationID, true)
		if err != nil {
			return err
		}
		if current.Status != reference.ActivationPending || current.EffectiveAt.After(now) {
			return nil
		}
		var activeVersion int64
		var activeActivation uuid.UUID
		hasActive := true
		if err := tx.QueryRow(ctx, `SELECT version, activation_id FROM reference.active WHERE shard_id=$1 FOR UPDATE`, current.ShardID).
			Scan(&activeVersion, &activeActivation); err != nil {
			if err != pgx.ErrNoRows {
				return err
			}
			hasActive = false
		}
		expected := int64(0)
		if hasActive {
			expected = activeVersion
		}
		if current.ExpectedVersion == nil || *current.ExpectedVersion != expected {
			failure := fmt.Sprintf("active version changed to %d before scheduled activation", expected)
			if _, err := tx.Exec(ctx, `UPDATE reference.activations SET status='failed', failure=$1 WHERE activation_id=$2`,
				failure, activationID); err != nil {
				return err
			}
			event := AuditEvent(reference.Principal{UserID: current.RequestedBy, RequestID: "scheduler"}, "reference.activation.fail", "activation", activationID.String(), "failure", map[string]any{
				"shard_id": current.ShardID, "version": current.Version, "expected_version": current.ExpectedVersion,
				"active_version": expected, "failure": failure,
			})
			return appendAuditTx(ctx, tx, event, "system:scheduler")
		}
		if hasActive {
			if _, err := tx.Exec(ctx, `UPDATE reference.activations SET status='superseded'
				WHERE activation_id=$1 AND status='active'`, activeActivation); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE reference.activations
			SET status='active', activated_at=$1 WHERE activation_id=$2`, now, activationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO reference.active (shard_id, version, activation_id, activated_at)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (shard_id) DO UPDATE SET version=EXCLUDED.version,
			activation_id=EXCLUDED.activation_id, activated_at=EXCLUDED.activated_at`,
			current.ShardID, current.Version, activationID, now); err != nil {
			return err
		}
		event := AuditEvent(reference.Principal{UserID: current.RequestedBy, RequestID: "scheduler"}, "reference.activation.promote", "activation", activationID.String(), "success", map[string]any{
			"shard_id": current.ShardID, "version": current.Version, "previous_version": expected,
			"scheduled_effective_at": current.EffectiveAt,
		})
		if err := appendAuditTx(ctx, tx, event, "system:scheduler"); err != nil {
			return err
		}
		activation, err = loadActivationRow(ctx, tx, activationID, false)
		if err != nil {
			return err
		}
		promoted = true
		return nil
	})
	if err != nil {
		return reference.Activation{}, false, mapPgError(err, reference.CodeConflict, "scheduled activation failed")
	}
	return activation, promoted, nil
}

// Active returns the effective version for one shard.
func (s *Store) Active(ctx context.Context, shardID string) (reference.Active, error) {
	if shardID == "" {
		shardID = reference.DefaultShard
	}
	var active reference.Active
	var rollback bool
	if err := s.pool.QueryRow(ctx, `SELECT a.shard_id, a.version, a.activation_id, a.activated_at,
		(x.rollback_of IS NOT NULL) FROM reference.active a
		JOIN reference.activations x ON x.activation_id=a.activation_id WHERE a.shard_id=$1`, shardID).
		Scan(&active.ShardID, &active.Version, &active.ActivationID, &active.ActivatedAt, &rollback); err != nil {
		if err == pgx.ErrNoRows {
			return reference.Active{}, reference.NotFound("active configuration", shardID)
		}
		return reference.Active{}, err
	}
	active.Rollback = rollback
	return active, nil
}

// ActiveShards lists shards with an effective configuration.
func (s *Store) ActiveShards(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT shard_id FROM reference.active ORDER BY shard_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var shards []string
	for rows.Next() {
		var shard string
		if err := rows.Scan(&shard); err != nil {
			return nil, err
		}
		shards = append(shards, shard)
	}
	return shards, rows.Err()
}

// ListActivations returns activation history for a shard, newest first.
func (s *Store) ListActivations(ctx context.Context, shardID string, limit int) ([]reference.Activation, error) {
	if shardID == "" {
		shardID = reference.DefaultShard
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT activation_id, shard_id, version, status, expected_version,
		rollback_of, requested_by, requested_at, effective_at, activated_at, failure, idempotency_key
		FROM reference.activations WHERE shard_id=$1 ORDER BY requested_at DESC LIMIT `+itoa(limit), shardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var activations []reference.Activation
	for rows.Next() {
		activation, err := scanActivation(rows)
		if err != nil {
			return nil, err
		}
		activations = append(activations, activation)
	}
	return activations, rows.Err()
}

func loadActivationByKey(ctx context.Context, tx pgx.Tx, shardID, key string) (reference.Activation, bool, error) {
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT activation_id FROM reference.activations WHERE shard_id=$1 AND idempotency_key=$2`,
		shardID, key).Scan(&id); err != nil {
		if err == pgx.ErrNoRows {
			return reference.Activation{}, false, nil
		}
		return reference.Activation{}, false, err
	}
	activation, err := loadActivationTx(ctx, tx, id)
	return activation, true, err
}

func loadActivationTx(ctx context.Context, tx pgx.Tx, activationID uuid.UUID) (reference.Activation, error) {
	return loadActivationRow(ctx, tx, activationID, false)
}

func loadActivationRow(ctx context.Context, source interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, activationID uuid.UUID, forUpdate bool) (reference.Activation, error) {
	query := `SELECT activation_id, shard_id, version, status, expected_version, rollback_of,
		requested_by, requested_at, effective_at, activated_at, failure, idempotency_key
		FROM reference.activations WHERE activation_id=$1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var activation reference.Activation
	var expectedVersion *int64
	var rollbackOf *int64
	var activatedAt pgtype.Timestamptz
	var failure pgtype.Text
	if err := source.QueryRow(ctx, query, activationID).Scan(
		&activation.ActivationID, &activation.ShardID, &activation.Version, &activation.Status,
		&expectedVersion, &rollbackOf, &activation.RequestedBy, &activation.RequestedAt,
		&activation.EffectiveAt, &activatedAt, &failure, &activation.IdempotencyKey); err != nil {
		if err == pgx.ErrNoRows {
			return reference.Activation{}, reference.NotFound("activation", activationID.String())
		}
		return reference.Activation{}, err
	}
	activation.ExpectedVersion = expectedVersion
	activation.RollbackOf = rollbackOf
	if activatedAt.Valid {
		value := activatedAt.Time
		activation.ActivatedAt = &value
	}
	if failure.Valid {
		value := failure.String
		activation.Failure = &value
	}
	return activation, nil
}

func scanActivation(rows pgx.Rows) (reference.Activation, error) {
	var activation reference.Activation
	var expectedVersion *int64
	var rollbackOf *int64
	var activatedAt pgtype.Timestamptz
	var failure pgtype.Text
	if err := rows.Scan(&activation.ActivationID, &activation.ShardID, &activation.Version, &activation.Status,
		&expectedVersion, &rollbackOf, &activation.RequestedBy, &activation.RequestedAt,
		&activation.EffectiveAt, &activatedAt, &failure, &activation.IdempotencyKey); err != nil {
		return reference.Activation{}, err
	}
	activation.ExpectedVersion = expectedVersion
	activation.RollbackOf = rollbackOf
	if activatedAt.Valid {
		value := activatedAt.Time
		activation.ActivatedAt = &value
	}
	if failure.Valid {
		value := failure.String
		activation.Failure = &value
	}
	return activation, nil
}
