package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/nexora-exchange/nexora/libs/go/refdata"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
)

// ListVersions returns immutable published version metadata, newest first.
func (s *Store) ListVersions(ctx context.Context, limit int) ([]reference.Version, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT version, schema_version, content_hash, document, effective_at,
		created_by, created_at, proposal_id, rollback_of, approval_count, canonicalization
		FROM reference.versions ORDER BY version DESC LIMIT `+itoa(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []reference.Version
	for rows.Next() {
		version, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

// GetVersion returns one immutable published version.
func (s *Store) GetVersion(ctx context.Context, version int64) (reference.Version, error) {
	return getVersionRow(ctx, s.pool, version, false)
}

func getVersionTx(ctx context.Context, tx pgx.Tx, version int64) (reference.Version, error) {
	return getVersionRow(ctx, tx, version, true)
}

func getVersionRow(ctx context.Context, source interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, version int64, forUpdate bool) (reference.Version, error) {
	query := `SELECT version, schema_version, content_hash, document, effective_at,
		created_by, created_at, proposal_id, rollback_of, approval_count, canonicalization
		FROM reference.versions WHERE version=$1`
	if forUpdate {
		query += ` FOR SHARE`
	}
	var published reference.Version
	var rollbackOf *int64
	var document []byte
	if err := source.QueryRow(ctx, query, version).Scan(
		&published.Version, &published.SchemaVersion, &published.ContentHash, &document,
		&published.EffectiveAt, &published.CreatedBy, &published.CreatedAt, &published.ProposalID,
		&rollbackOf, &published.ApprovalCount, &published.Canonicalization); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reference.Version{}, reference.NotFound("version", itoa64(version))
		}
		return reference.Version{}, err
	}
	parsed, err := refdata.Parse(document)
	if err != nil {
		return reference.Version{}, reference.NewError(500, reference.CodeInternal, "stored version is corrupt", nil)
	}
	published.Document = document
	published.Parsed = parsed
	published.RollbackOf = rollbackOf
	return published, nil
}

func scanVersion(rows pgx.Rows) (reference.Version, error) {
	var published reference.Version
	var rollbackOf *int64
	var document []byte
	if err := rows.Scan(&published.Version, &published.SchemaVersion, &published.ContentHash, &document,
		&published.EffectiveAt, &published.CreatedBy, &published.CreatedAt, &published.ProposalID,
		&rollbackOf, &published.ApprovalCount, &published.Canonicalization); err != nil {
		return reference.Version{}, err
	}
	parsed, err := refdata.Parse(document)
	if err != nil {
		return reference.Version{}, reference.NewError(500, reference.CodeInternal, "stored version is corrupt", nil)
	}
	published.Document = document
	published.Parsed = parsed
	published.RollbackOf = rollbackOf
	return published, nil
}
