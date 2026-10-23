package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nexora-exchange/nexora/libs/go/refdata"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
)

// LoadDraft returns the working draft.
func (s *Store) LoadDraft(ctx context.Context) (reference.Draft, error) {
	return loadDraft(ctx, s.pool)
}

// SaveDraft replaces the working draft with optimistic concurrency.
func (s *Store) SaveDraft(ctx context.Context, request reference.SaveDraftRequest) (reference.Draft, error) {
	document, err := refdata.Parse(request.Document)
	if err != nil {
		return reference.Draft{}, reference.InvalidInput("draft document cannot be decoded")
	}
	normalized, err := document.CanonicalBytes()
	if err != nil {
		return reference.Draft{}, reference.InvalidInput("draft document cannot be encoded")
	}
	if refdata.ContentHash(normalized) != request.ContentHash {
		return reference.Draft{}, reference.Conflict(reference.CodeHashMismatch, "draft content hash does not match its document", nil)
	}
	var saved reference.Draft
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := loadDraftTx(ctx, tx)
		if err != nil {
			return err
		}
		if current.Revision != request.ExpectedRevision {
			return reference.Conflict(reference.CodeStaleDraft, "draft revision changed", map[string]int64{"current_revision": current.Revision})
		}
		if request.BaseVersion != nil {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reference.versions WHERE version=$1)`, *request.BaseVersion).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return reference.NotFound("version", itoa64(*request.BaseVersion))
			}
		}
		latest, err := latestVersionTx(ctx, tx)
		if err != nil {
			return err
		}
		if latest > 0 && (request.BaseVersion == nil || *request.BaseVersion != latest) {
			return reference.Conflict(reference.CodeStaleVersion, "draft must be based on the latest published version", map[string]any{
				"latest_version": latest, "base_version": request.BaseVersion,
			})
		}
		if latest == 0 && request.BaseVersion != nil {
			return reference.Conflict(reference.CodeStaleVersion, "no published version exists for this base", nil)
		}
		sameBase := (current.BaseVersion == nil && request.BaseVersion == nil) ||
			(current.BaseVersion != nil && request.BaseVersion != nil && *current.BaseVersion == *request.BaseVersion)
		if current.ContentHash == request.ContentHash && sameBase && len(current.Canonical) > 0 {
			saved = current
			return nil
		}
		revision := current.Revision + 1
		if _, err := tx.Exec(ctx, `UPDATE reference.draft_state
			SET revision=$1, base_version=$2, content_hash=$3, document=$4, updated_by=$5, updated_at=$6 WHERE id=1`,
			revision, nullInt64(request.BaseVersion), request.ContentHash, normalized, request.Actor.UserID, time.Now().UTC()); err != nil {
			return err
		}
		event := AuditEvent(request.Actor, request.Action, "draft", "draft", "success", map[string]any{
			"revision": revision, "content_hash": request.ContentHash, "base_version": request.BaseVersion,
		})
		if err := appendAuditTx(ctx, tx, event, primaryRole(request.Actor)); err != nil {
			return err
		}
		saved = reference.Draft{
			Revision: revision, BaseVersion: request.BaseVersion, ContentHash: request.ContentHash,
			Document: document, Canonical: normalized, UpdatedBy: &request.Actor.UserID, UpdatedAt: time.Now().UTC(),
		}
		return nil
	})
	if err != nil {
		return reference.Draft{}, mapPgError(err, reference.CodeConflict, "draft could not be saved")
	}
	return saved, nil
}

func loadDraft(ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (reference.Draft, error) {
	var revision int64
	var baseVersion *int64
	var contentHash *string
	var document []byte
	var updatedBy pgtype.UUID
	var updatedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT revision, base_version, content_hash, document, updated_by, updated_at
		FROM reference.draft_state WHERE id=1`).Scan(&revision, &baseVersion, &contentHash, &document, &updatedBy, &updatedAt); err != nil {
		return reference.Draft{}, err
	}
	draft := reference.Draft{
		Revision: revision, BaseVersion: baseVersion, UpdatedBy: uuidPtr(updatedBy), UpdatedAt: updatedAt,
	}
	if contentHash != nil {
		draft.ContentHash = *contentHash
	}
	if len(document) > 0 {
		parsed, err := refdata.Parse(document)
		if err != nil {
			return reference.Draft{}, reference.NewError(500, reference.CodeInternal, "stored draft is corrupt", nil)
		}
		draft.Document = parsed
		draft.Canonical = document
	}
	return draft, nil
}

func loadDraftTx(ctx context.Context, tx pgx.Tx) (reference.Draft, error) {
	var revision int64
	var baseVersion *int64
	var contentHash *string
	var document []byte
	var updatedBy pgtype.UUID
	var updatedAt time.Time
	if err := tx.QueryRow(ctx, `SELECT revision, base_version, content_hash, document, updated_by, updated_at
		FROM reference.draft_state WHERE id=1 FOR UPDATE`).Scan(&revision, &baseVersion, &contentHash, &document, &updatedBy, &updatedAt); err != nil {
		return reference.Draft{}, err
	}
	draft := reference.Draft{Revision: revision, BaseVersion: baseVersion, UpdatedBy: uuidPtr(updatedBy), UpdatedAt: updatedAt}
	if contentHash != nil {
		draft.ContentHash = *contentHash
	}
	if len(document) > 0 {
		parsed, err := refdata.Parse(document)
		if err != nil {
			return reference.Draft{}, reference.NewError(500, reference.CodeInternal, "stored draft is corrupt", nil)
		}
		draft.Document = parsed
		draft.Canonical = document
	}
	return draft, nil
}

func uuidPtr(value pgtype.UUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	id := uuid.UUID(value.Bytes)
	return &id
}

func primaryRole(principal reference.Principal) string {
	if len(principal.Roles) == 0 {
		return "authenticated"
	}
	return principal.Roles[0]
}
