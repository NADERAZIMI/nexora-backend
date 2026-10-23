package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nexora-exchange/nexora/libs/go/refdata"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
)

// CreateProposal freezes the current draft into an immutable proposal.
func (s *Store) CreateProposal(ctx context.Context, request reference.CreateProposalRequest) (reference.Proposal, error) {
	parsed, err := refdata.Parse(request.Document)
	if err != nil {
		return reference.Proposal{}, reference.InvalidInput("proposal document cannot be decoded")
	}
	var proposal reference.Proposal
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		draft, err := loadDraftTx(ctx, tx)
		if err != nil {
			return err
		}
		if draft.Revision != request.ExpectedRevision {
			return reference.Conflict(reference.CodeStaleDraft, "draft revision changed", map[string]int64{"current_revision": draft.Revision})
		}
		if draft.ContentHash != request.ContentHash {
			return reference.Conflict(reference.CodeHashMismatch, "draft content changed before submission", nil)
		}
		latest, err := latestVersionTx(ctx, tx)
		if err != nil {
			return err
		}
		if latest > 0 {
			if request.BaseVersion == nil || *request.BaseVersion != latest {
				return reference.Conflict(reference.CodeStaleVersion, "draft is not based on the latest published version", map[string]any{
					"latest_version": latest, "base_version": request.BaseVersion,
				})
			}
			var latestHash string
			if err := tx.QueryRow(ctx, `SELECT content_hash FROM reference.versions WHERE version=$1`, latest).Scan(&latestHash); err != nil {
				return err
			}
			if latestHash == request.ContentHash {
				return reference.Conflict(reference.CodeConflict, "draft has no changes relative to the latest published version", nil)
			}
		} else if request.BaseVersion != nil {
			return reference.Conflict(reference.CodeStaleVersion, "no published version exists for this base", nil)
		}
		now := time.Now().UTC()
		proposal = reference.Proposal{
			ProposalID: uuid.New(), BaseVersion: request.BaseVersion, SchemaVersion: parsed.SchemaVersion,
			ContentHash: request.ContentHash, Document: request.Document, Parsed: parsed,
			Status: reference.ProposalPending, CreatedBy: request.Actor.UserID, CreatedAt: now, SubmittedAt: now,
		}
		if _, err := tx.Exec(ctx, `INSERT INTO reference.proposals
			(proposal_id, base_version, schema_version, content_hash, document, status, created_by, created_at, submitted_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			proposal.ProposalID, nullInt64(request.BaseVersion), proposal.SchemaVersion, proposal.ContentHash,
			proposal.Document, proposal.Status, proposal.CreatedBy, proposal.CreatedAt, proposal.SubmittedAt); err != nil {
			return err
		}
		event := AuditEvent(request.Actor, "reference.proposal.submit", "proposal", proposal.ProposalID.String(), "success", map[string]any{
			"content_hash": proposal.ContentHash, "base_version": proposal.BaseVersion,
		})
		return appendAuditTx(ctx, tx, event, primaryRole(request.Actor))
	})
	if err != nil {
		return reference.Proposal{}, mapPgError(err, reference.CodeConflict, "proposal could not be created")
	}
	return proposal, nil
}

// GetProposal returns one proposal with its decisions.
func (s *Store) GetProposal(ctx context.Context, proposalID uuid.UUID) (reference.Proposal, error) {
	proposal, err := getProposalRow(ctx, s.pool, proposalID, false)
	if err != nil {
		return reference.Proposal{}, err
	}
	approvals, err := loadApprovals(ctx, s.pool, proposalID)
	if err != nil {
		return reference.Proposal{}, err
	}
	proposal.Approvals = approvals
	return proposal, nil
}

// ListProposals returns recent proposals.
func (s *Store) ListProposals(ctx context.Context, limit int) ([]reference.Proposal, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT proposal_id FROM reference.proposals ORDER BY created_at DESC LIMIT `+itoa(limit))
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
	proposals := make([]reference.Proposal, 0, len(ids))
	for _, id := range ids {
		proposal, err := s.GetProposal(ctx, id)
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, proposal)
	}
	return proposals, nil
}

// ApproveProposal records one checker approval.
func (s *Store) ApproveProposal(ctx context.Context, proposalID uuid.UUID, approver reference.Principal, proposalHash, reason string) (reference.Proposal, error) {
	var proposal reference.Proposal
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := getProposalRow(ctx, tx, proposalID, true)
		if err != nil {
			return err
		}
		if current.Status != reference.ProposalPending {
			return reference.Conflict(reference.CodeInvalidState, "proposal is not awaiting approval", map[string]string{"status": current.Status})
		}
		if current.ContentHash != proposalHash {
			return reference.Conflict(reference.CodeHashMismatch, "proposal hash does not match the submitted content", nil)
		}
		if current.CreatedBy == approver.UserID {
			return reference.NewError(403, reference.CodeSelfApproval, "a proposal creator cannot approve their own proposal", nil)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO reference.approvals
			(proposal_id, approver_id, decision, reason, proposal_hash, decided_at)
			VALUES ($1,$2,'approved',$3,$4,$5) ON CONFLICT (proposal_id, approver_id) DO NOTHING`,
			proposalID, approver.UserID, nullString(reason), proposalHash, time.Now().UTC()); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM reference.approvals WHERE proposal_id=$1 AND decision='approved'`, proposalID).Scan(&count); err != nil {
			return err
		}
		status := reference.ProposalPending
		if count >= reference.RequiredApprovals {
			status = reference.ProposalApproved
			if _, err := tx.Exec(ctx, `UPDATE reference.proposals SET status=$1, decided_at=$2 WHERE proposal_id=$3`,
				status, time.Now().UTC(), proposalID); err != nil {
				return err
			}
		}
		event := AuditEvent(approver, "reference.proposal.approve", "proposal", proposalID.String(), "success", map[string]any{
			"approval_count": count, "proposal_hash": proposalHash, "resulting_status": status,
		})
		if err := appendAuditTx(ctx, tx, event, primaryRole(approver)); err != nil {
			return err
		}
		proposal, err = getProposalRow(ctx, tx, proposalID, false)
		if err != nil {
			return err
		}
		approvals, err := loadApprovals(ctx, tx, proposalID)
		if err != nil {
			return err
		}
		proposal.Approvals = approvals
		return nil
	})
	if err != nil {
		return reference.Proposal{}, mapPgError(err, reference.CodeConflict, "proposal approval failed")
	}
	return proposal, nil
}

// RejectProposal rejects a pending proposal.
func (s *Store) RejectProposal(ctx context.Context, proposalID uuid.UUID, actor reference.Principal, reason string) (reference.Proposal, error) {
	var proposal reference.Proposal
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := getProposalRow(ctx, tx, proposalID, true)
		if err != nil {
			return err
		}
		if current.Status != reference.ProposalPending {
			return reference.Conflict(reference.CodeInvalidState, "proposal is not awaiting approval", map[string]string{"status": current.Status})
		}
		if _, err := tx.Exec(ctx, `INSERT INTO reference.approvals
			(proposal_id, approver_id, decision, reason, proposal_hash, decided_at)
			VALUES ($1,$2,'rejected',$3,$4,$5) ON CONFLICT (proposal_id, approver_id) DO NOTHING`,
			proposalID, actor.UserID, reason, current.ContentHash, time.Now().UTC()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE reference.proposals SET status='rejected', decided_at=$1, rejection_reason=$2 WHERE proposal_id=$3`,
			time.Now().UTC(), reason, proposalID); err != nil {
			return err
		}
		event := AuditEvent(actor, "reference.proposal.reject", "proposal", proposalID.String(), "success", map[string]any{
			"reason": reason, "proposal_hash": current.ContentHash,
		})
		if err := appendAuditTx(ctx, tx, event, primaryRole(actor)); err != nil {
			return err
		}
		proposal, err = getProposalRow(ctx, tx, proposalID, false)
		if err != nil {
			return err
		}
		approvals, err := loadApprovals(ctx, tx, proposalID)
		if err != nil {
			return err
		}
		proposal.Approvals = approvals
		return nil
	})
	if err != nil {
		return reference.Proposal{}, mapPgError(err, reference.CodeConflict, "proposal rejection failed")
	}
	return proposal, nil
}

// PublishProposal creates an immutable version from an approved proposal.
func (s *Store) PublishProposal(ctx context.Context, request reference.PublishRequest) (reference.Version, error) {
	var published reference.Version
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		proposal, err := getProposalRow(ctx, tx, request.ProposalID, true)
		if err != nil {
			return err
		}
		if proposal.PublishedVersion != nil {
			published, err = getVersionTx(ctx, tx, *proposal.PublishedVersion)
			return err
		}
		if proposal.Status != reference.ProposalApproved {
			return reference.Conflict(reference.CodeApprovalRequired, "proposal requires two distinct approvals before publication", map[string]string{"status": proposal.Status})
		}
		var approvalCount int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM reference.approvals WHERE proposal_id=$1 AND decision='approved'`, proposal.ProposalID).Scan(&approvalCount); err != nil {
			return err
		}
		if approvalCount < reference.RequiredApprovals {
			return reference.Conflict(reference.CodeApprovalRequired, "proposal requires two distinct approvals before publication", map[string]int{"approvals": approvalCount})
		}
		var selfApproved bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reference.approvals WHERE proposal_id=$1 AND approver_id=$2 AND decision='approved')`,
			proposal.ProposalID, proposal.CreatedBy).Scan(&selfApproved); err != nil {
			return err
		}
		if selfApproved {
			return reference.NewError(403, reference.CodeSelfApproval, "a proposal creator cannot approve their own proposal", nil)
		}
		latest, err := latestVersionTx(ctx, tx)
		if err != nil {
			return err
		}
		if proposal.BaseVersion == nil {
			if latest != 0 {
				return reference.Conflict(reference.CodeStaleVersion, "proposal is not based on the latest published version", map[string]any{"latest_version": latest})
			}
		} else if *proposal.BaseVersion != latest {
			return reference.Conflict(reference.CodeStaleVersion, "proposal is not based on the latest published version", map[string]any{
				"latest_version": latest, "base_version": *proposal.BaseVersion,
			})
		}
		var version int64
		if err := tx.QueryRow(ctx, `SELECT nextval('reference.version_seq')`).Scan(&version); err != nil {
			return err
		}
		now := time.Now().UTC()
		published = reference.Version{
			Version: version, SchemaVersion: proposal.SchemaVersion, ContentHash: proposal.ContentHash,
			Document: proposal.Document, Parsed: proposal.Parsed, EffectiveAt: request.EffectiveAt,
			CreatedBy: request.Actor.UserID, CreatedAt: now, ProposalID: proposal.ProposalID,
			ApprovalCount: approvalCount, Canonicalization: "sha256/canonical-json-v1",
		}
		if _, err := tx.Exec(ctx, `INSERT INTO reference.versions
			(version, schema_version, content_hash, document, effective_at, created_by, created_at, proposal_id, approval_count, canonicalization)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			published.Version, published.SchemaVersion, published.ContentHash, published.Document,
			published.EffectiveAt, published.CreatedBy, published.CreatedAt, published.ProposalID,
			published.ApprovalCount, published.Canonicalization); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE reference.proposals SET status='published', published_version=$1, decided_at=$2 WHERE proposal_id=$3`,
			version, now, proposal.ProposalID); err != nil {
			return err
		}
		// Keep the working draft's base pointer current when it still contains
		// exactly the published content. This does not modify the draft body or
		// increment its revision.
		if _, err := tx.Exec(ctx, `UPDATE reference.draft_state SET base_version=$1 WHERE id=1 AND content_hash=$2`,
			version, proposal.ContentHash); err != nil {
			return err
		}
		event := AuditEvent(request.Actor, "reference.version.publish", "version", itoa64(version), "success", map[string]any{
			"proposal_id": proposal.ProposalID.String(), "content_hash": proposal.ContentHash,
			"approval_count": approvalCount, "effective_at": request.EffectiveAt,
		})
		return appendAuditTx(ctx, tx, event, primaryRole(request.Actor))
	})
	if err != nil {
		return reference.Version{}, mapPgError(err, reference.CodeConflict, "publication failed")
	}
	return published, nil
}

func getProposalRow(ctx context.Context, source interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, proposalID uuid.UUID, forUpdate bool) (reference.Proposal, error) {
	query := `SELECT proposal_id, base_version, schema_version, content_hash, document, status,
		created_by, created_at, submitted_at, decided_at, rejection_reason, published_version
		FROM reference.proposals WHERE proposal_id=$1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var proposal reference.Proposal
	var baseVersion *int64
	var decidedAt *time.Time
	var rejection pgtype.Text
	var publishedVersion *int64
	if err := source.QueryRow(ctx, query, proposalID).Scan(
		&proposal.ProposalID, &baseVersion, &proposal.SchemaVersion, &proposal.ContentHash, &proposal.Document,
		&proposal.Status, &proposal.CreatedBy, &proposal.CreatedAt, &proposal.SubmittedAt, &decidedAt,
		&rejection, &publishedVersion); err != nil {
		if err == pgx.ErrNoRows {
			return reference.Proposal{}, reference.NotFound("proposal", proposalID.String())
		}
		return reference.Proposal{}, err
	}
	parsed, err := refdata.Parse(proposal.Document)
	if err != nil {
		return reference.Proposal{}, reference.NewError(500, reference.CodeInternal, "stored proposal is corrupt", nil)
	}
	proposal.BaseVersion = baseVersion
	proposal.DecidedAt = decidedAt
	proposal.PublishedVersion = publishedVersion
	proposal.Parsed = parsed
	if rejection.Valid {
		proposal.RejectionReason = &rejection.String
	}
	return proposal, nil
}

func loadApprovals(ctx context.Context, source interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, proposalID uuid.UUID) ([]reference.Approval, error) {
	rows, err := source.Query(ctx, `SELECT proposal_id, approver_id, decision, reason, proposal_hash, decided_at
		FROM reference.approvals WHERE proposal_id=$1 ORDER BY decided_at, approver_id`, proposalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var approvals []reference.Approval
	for rows.Next() {
		var approval reference.Approval
		var reason pgtype.Text
		if err := rows.Scan(&approval.ProposalID, &approval.ApproverID, &approval.Decision, &reason,
			&approval.ProposalHash, &approval.DecidedAt); err != nil {
			return nil, err
		}
		if reason.Valid {
			approval.Reason = &reason.String
		}
		approvals = append(approvals, approval)
	}
	return approvals, rows.Err()
}

func latestVersionTx(ctx context.Context, tx pgx.Tx) (int64, error) {
	var latest int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM reference.versions`).Scan(&latest); err != nil {
		return 0, err
	}
	return latest, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func itoa(value int) string { return fmt.Sprintf("%d", value) }

func itoa64(value int64) string { return fmt.Sprintf("%d", value) }
