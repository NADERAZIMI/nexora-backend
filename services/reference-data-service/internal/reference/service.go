package reference

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nexora-exchange/nexora/libs/go/refdata"
)

// Store is the PostgreSQL persistence contract. Implementations must perform
// each mutation in one transaction and must never treat a cache as
// authoritative.
type Store interface {
	LoadDraft(ctx context.Context) (Draft, error)
	SaveDraft(ctx context.Context, request SaveDraftRequest) (Draft, error)
	CreateProposal(ctx context.Context, request CreateProposalRequest) (Proposal, error)
	GetProposal(ctx context.Context, proposalID uuid.UUID) (Proposal, error)
	ListProposals(ctx context.Context, limit int) ([]Proposal, error)
	ApproveProposal(ctx context.Context, proposalID uuid.UUID, approver Principal, proposalHash string, reason string) (Proposal, error)
	RejectProposal(ctx context.Context, proposalID uuid.UUID, actor Principal, reason string) (Proposal, error)
	PublishProposal(ctx context.Context, request PublishRequest) (Version, error)
	ListVersions(ctx context.Context, limit int) ([]Version, error)
	GetVersion(ctx context.Context, version int64) (Version, error)
	Activate(ctx context.Context, request StoreActivation) (Activation, error)
	PromoteDue(ctx context.Context, now time.Time, limit int) ([]Activation, error)
	Active(ctx context.Context, shardID string) (Active, error)
	ListActivations(ctx context.Context, shardID string, limit int) ([]Activation, error)
	ListAudit(ctx context.Context, limit int, before time.Time) ([]AuditEvent, error)
}

// SaveDraftRequest replaces the complete working draft.
type SaveDraftRequest struct {
	ExpectedRevision int64
	BaseVersion      *int64
	Document         []byte
	ContentHash      string
	Actor            Principal
	Action           string
}

// CreateProposalRequest snapshots the working draft.
type CreateProposalRequest struct {
	ExpectedRevision int64
	BaseVersion      *int64
	Document         []byte
	ContentHash      string
	Actor            Principal
}

// PublishRequest creates an immutable version from an approved proposal.
type PublishRequest struct {
	ProposalID  uuid.UUID
	Actor       Principal
	EffectiveAt time.Time
}

// StoreActivation creates or repeats a shard activation.
type StoreActivation struct {
	ShardID         string
	Version         int64
	ExpectedVersion *int64
	EffectiveAt     time.Time
	IdempotencyKey  string
	Rollback        bool
	Reason          string
	Actor           Principal
}

// Metrics receives bounded-cardinality operational signals.
type Metrics interface {
	ValidationFailed(section string, count int)
	ProposalSubmitted(status string)
	Publication(result string)
	Activation(result string)
	RollbackCompleted(duration time.Duration)
	PendingApprovals(delta int)
}

type noopMetrics struct{}

func (noopMetrics) ValidationFailed(string, int)    {}
func (noopMetrics) ProposalSubmitted(string)        {}
func (noopMetrics) Publication(string)              {}
func (noopMetrics) Activation(string)               {}
func (noopMetrics) RollbackCompleted(time.Duration) {}
func (noopMetrics) PendingApprovals(int)            {}

// Service implements the reference-data use cases.
type Service struct {
	store   Store
	clock   func() time.Time
	metrics Metrics
	logger  *slog.Logger
}

// New constructs the service.
func New(store Store, clock func() time.Time, metrics Metrics, logger *slog.Logger) *Service {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	if metrics == nil {
		metrics = noopMetrics{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, clock: clock, metrics: metrics, logger: logger}
}

// GetDraft returns the working draft.
func (s *Service) GetDraft(ctx context.Context) (Draft, error) {
	return s.store.LoadDraft(ctx)
}

// PutDraft validates and atomically replaces the complete working draft.
func (s *Service) PutDraft(ctx context.Context, principal Principal, expectedRevision int64, baseVersion *int64, document refdata.Document) (Draft, error) {
	canonical, issues := s.validate(document)
	if len(issues) > 0 {
		s.metrics.ValidationFailed("draft", len(issues))
		return Draft{}, ValidationFailed(issues)
	}
	hash := refdata.ContentHash(canonical)
	draft, err := s.store.SaveDraft(ctx, SaveDraftRequest{
		ExpectedRevision: expectedRevision,
		BaseVersion:      baseVersion,
		Document:         canonical,
		ContentHash:      hash,
		Actor:            principal,
		Action:           "reference.draft.update",
	})
	if err != nil {
		return Draft{}, err
	}
	s.metrics.ValidationFailed("draft", 0)
	return draft, nil
}

// ResetDraftFromVersion replaces the working draft with a published version.
func (s *Service) ResetDraftFromVersion(ctx context.Context, principal Principal, expectedRevision int64, version int64) (Draft, error) {
	published, err := s.store.GetVersion(ctx, version)
	if err != nil {
		return Draft{}, err
	}
	draft, err := s.store.SaveDraft(ctx, SaveDraftRequest{
		ExpectedRevision: expectedRevision,
		BaseVersion:      &published.Version,
		Document:         published.Document,
		ContentHash:      published.ContentHash,
		Actor:            principal,
		Action:           "reference.draft.reset",
	})
	if err != nil {
		return Draft{}, err
	}
	return draft, nil
}

// SubmitProposal captures an immutable proposal from the current draft.
func (s *Service) SubmitProposal(ctx context.Context, principal Principal, expectedRevision int64) (Proposal, error) {
	draft, err := s.store.LoadDraft(ctx)
	if err != nil {
		return Proposal{}, err
	}
	if draft.IsEmpty() {
		return Proposal{}, Conflict(CodeInvalidState, "draft is empty", nil)
	}
	if draft.Revision != expectedRevision {
		return Proposal{}, Conflict(CodeStaleDraft, "draft revision changed", map[string]int64{"current_revision": draft.Revision})
	}
	parsed, parseErr := refdata.Parse(draft.Canonical)
	if parseErr != nil {
		return Proposal{}, Conflict(CodeInvalidState, "stored draft cannot be decoded", nil)
	}
	_, issues := s.validate(parsed)
	if len(issues) > 0 {
		s.metrics.ValidationFailed("submit", len(issues))
		return Proposal{}, ValidationFailed(issues)
	}
	proposal, err := s.store.CreateProposal(ctx, CreateProposalRequest{
		ExpectedRevision: expectedRevision,
		BaseVersion:      draft.BaseVersion,
		Document:         draft.Canonical,
		ContentHash:      draft.ContentHash,
		Actor:            principal,
	})
	if err != nil {
		return Proposal{}, err
	}
	s.metrics.ProposalSubmitted(proposal.Status)
	s.metrics.PendingApprovals(1)
	return proposal, nil
}

// GetProposal returns one proposal.
func (s *Service) GetProposal(ctx context.Context, proposalID uuid.UUID) (Proposal, error) {
	return s.store.GetProposal(ctx, proposalID)
}

// ListProposals returns recent proposals.
func (s *Service) ListProposals(ctx context.Context, limit int) ([]Proposal, error) {
	return s.store.ListProposals(ctx, clampLimit(limit))
}

// ApproveProposal records the caller's approval. The creator cannot approve
// their own proposal; the store enforces that invariant in PostgreSQL.
func (s *Service) ApproveProposal(ctx context.Context, principal Principal, proposalID uuid.UUID, proposalHash string, reason string) (Proposal, error) {
	proposal, err := s.store.GetProposal(ctx, proposalID)
	if err != nil {
		return Proposal{}, err
	}
	if proposal.Status != ProposalPending {
		return Proposal{}, Conflict(CodeInvalidState, "proposal is not awaiting approval", map[string]string{"status": proposal.Status})
	}
	if strings.TrimSpace(proposalHash) == "" || !strings.EqualFold(strings.TrimSpace(proposalHash), proposal.ContentHash) {
		return Proposal{}, Conflict(CodeHashMismatch, "proposal hash does not match the submitted content", nil)
	}
	if proposal.CreatedBy == principal.UserID {
		return Proposal{}, NewError(403, CodeSelfApproval, "a proposal creator cannot approve their own proposal", nil)
	}
	approved, err := s.store.ApproveProposal(ctx, proposalID, principal, proposal.ContentHash, reason)
	if err != nil {
		return Proposal{}, err
	}
	delta := 0
	if approved.Status == ProposalApproved {
		delta = -1
	}
	s.metrics.PendingApprovals(delta)
	return approved, nil
}

// RejectProposal rejects a pending proposal.
func (s *Service) RejectProposal(ctx context.Context, principal Principal, proposalID uuid.UUID, reason string) (Proposal, error) {
	if strings.TrimSpace(reason) == "" {
		return Proposal{}, InvalidInput("a rejection reason is required")
	}
	rejected, err := s.store.RejectProposal(ctx, proposalID, principal, strings.TrimSpace(reason))
	if err != nil {
		return Proposal{}, err
	}
	s.metrics.PendingApprovals(-1)
	return rejected, nil
}

// PublishProposal publishes an approved proposal as an immutable version.
func (s *Service) PublishProposal(ctx context.Context, principal Principal, proposalID uuid.UUID, effectiveAt time.Time) (Version, error) {
	if effectiveAt.IsZero() {
		effectiveAt = s.clock()
	}
	proposal, err := s.store.GetProposal(ctx, proposalID)
	if err != nil {
		return Version{}, err
	}
	// Publication is idempotent per proposal: a replay returns the exact
	// immutable version created by the first successful call.
	if proposal.PublishedVersion != nil {
		version, err := s.store.GetVersion(ctx, *proposal.PublishedVersion)
		if err != nil {
			return Version{}, err
		}
		s.metrics.Publication("idempotent")
		return version, nil
	}
	if proposal.Status != ProposalApproved {
		return Version{}, Conflict(CodeApprovalRequired, "proposal requires two distinct approvals before publication", map[string]string{"status": proposal.Status})
	}
	if proposal.ApprovalCount() < RequiredApprovals {
		return Version{}, Conflict(CodeApprovalRequired, "proposal requires two distinct approvals before publication", map[string]int{"approvals": proposal.ApprovalCount()})
	}
	if _, issues := s.validate(proposal.Parsed); len(issues) > 0 {
		s.metrics.ValidationFailed("publish", len(issues))
		return Version{}, ValidationFailed(issues)
	}
	version, err := s.store.PublishProposal(ctx, PublishRequest{
		ProposalID:  proposalID,
		Actor:       principal,
		EffectiveAt: effectiveAt.UTC(),
	})
	if err != nil {
		s.metrics.Publication("rejected")
		return Version{}, err
	}
	s.metrics.Publication("success")
	return version, nil
}

// Activate makes a published version effective for one shard. A lower version
// is rejected here; use Rollback so the operation is explicit and audited.
func (s *Service) Activate(ctx context.Context, principal Principal, request ActivationRequest) (Activation, error) {
	request.ShardID = normalizeShard(request.ShardID)
	if request.Version <= 0 {
		return Activation{}, InvalidInput("version must be positive")
	}
	if request.IdempotencyKey == "" {
		return Activation{}, InvalidInput("idempotency_key is required")
	}
	if request.EffectiveAt.IsZero() {
		request.EffectiveAt = s.clock()
	}
	activation, err := s.store.Activate(ctx, StoreActivation{
		ShardID: request.ShardID, Version: request.Version, ExpectedVersion: request.ExpectedVersion,
		EffectiveAt: request.EffectiveAt.UTC(), IdempotencyKey: request.IdempotencyKey,
		Actor: principal,
	})
	if err != nil {
		s.metrics.Activation("rejected")
		return Activation{}, err
	}
	s.metrics.Activation(activation.Status)
	return activation, nil
}

// Rollback explicitly reactivates an older published version.
func (s *Service) Rollback(ctx context.Context, principal Principal, request ActivationRequest) (Activation, error) {
	request.ShardID = normalizeShard(request.ShardID)
	if request.Version <= 0 {
		return Activation{}, InvalidInput("version must be positive")
	}
	if strings.TrimSpace(request.Reason) == "" {
		return Activation{}, InvalidInput("a rollback reason is required")
	}
	if request.IdempotencyKey == "" {
		return Activation{}, InvalidInput("idempotency_key is required")
	}
	active, err := s.store.Active(ctx, request.ShardID)
	if err != nil {
		return Activation{}, err
	}
	if request.Version >= active.Version {
		return Activation{}, Conflict(CodeInvalidState, "rollback target must be older than the active version", map[string]int64{"active_version": active.Version})
	}
	start := s.clock()
	activation, err := s.store.Activate(ctx, StoreActivation{
		ShardID: request.ShardID, Version: request.Version, ExpectedVersion: request.ExpectedVersion,
		EffectiveAt: request.EffectiveAt.UTC(), IdempotencyKey: request.IdempotencyKey,
		Rollback: true, Reason: request.Reason, Actor: principal,
	})
	if err != nil {
		s.metrics.Activation("rollback_rejected")
		return Activation{}, err
	}
	s.metrics.Activation("rollback")
	s.metrics.RollbackCompleted(s.clock().Sub(start))
	return activation, nil
}

// Active returns the effective version for a shard.
func (s *Service) Active(ctx context.Context, shardID string) (Active, error) {
	return s.store.Active(ctx, normalizeShard(shardID))
}

// ListVersions returns published version metadata.
func (s *Service) ListVersions(ctx context.Context, limit int) ([]VersionSummary, error) {
	versions, err := s.store.ListVersions(ctx, clampLimit(limit))
	if err != nil {
		return nil, err
	}
	out := make([]VersionSummary, 0, len(versions))
	for _, version := range versions {
		out = append(out, version.Summary())
	}
	return out, nil
}

// GetVersion returns one immutable version including its document.
func (s *Service) GetVersion(ctx context.Context, version int64) (Version, error) {
	return s.store.GetVersion(ctx, version)
}

// ActivateDue promotes scheduled activations. It is safe to call concurrently.
func (s *Service) ActivateDue(ctx context.Context) ([]Activation, error) {
	return s.store.PromoteDue(ctx, s.clock(), 100)
}

// ListActivations returns activation history for a shard.
func (s *Service) ListActivations(ctx context.Context, shardID string, limit int) ([]Activation, error) {
	return s.store.ListActivations(ctx, normalizeShard(shardID), clampLimit(limit))
}

// ListAudit returns append-only audit history.
func (s *Service) ListAudit(ctx context.Context, limit int, before time.Time) ([]AuditEvent, error) {
	return s.store.ListAudit(ctx, clampLimit(limit), before)
}

// DiffVersions compares two published documents.
func (s *Service) DiffVersions(ctx context.Context, from, to int64) (Diff, error) {
	fromVersion, err := s.store.GetVersion(ctx, from)
	if err != nil {
		return Diff{}, err
	}
	toVersion, err := s.store.GetVersion(ctx, to)
	if err != nil {
		return Diff{}, err
	}
	changes := diffJSON(fromVersion.Parsed, toVersion.Parsed, 200)
	return Diff{FromVersion: from, ToVersion: to, Changes: changes, Truncated: len(changes) >= 200}, nil
}

// DiffDraft compares the working draft to a published base version.
func (s *Service) DiffDraft(ctx context.Context, base int64) (Diff, error) {
	draft, err := s.store.LoadDraft(ctx)
	if err != nil {
		return Diff{}, err
	}
	baseVersion, err := s.store.GetVersion(ctx, base)
	if err != nil {
		return Diff{}, err
	}
	changes := diffJSON(baseVersion.Parsed, draft.Document, 200)
	return Diff{FromVersion: base, ToVersion: 0, Changes: changes, Truncated: len(changes) >= 200}, nil
}

// Validate normalizes and validates a document without persisting it.
func (s *Service) Validate(document refdata.Document) ([]byte, string, []Issue) {
	canonical, issues := s.validate(document)
	if len(issues) > 0 {
		s.metrics.ValidationFailed("validate", len(issues))
		return nil, "", issues
	}
	hash := refdata.ContentHash(canonical)
	s.metrics.ValidationFailed("validate", 0)
	return canonical, hash, nil
}

// Seed installs the non-production fixture only when the working draft is
// empty. It is idempotent and never overwrites an operator's draft.
func (s *Service) Seed(ctx context.Context, document refdata.Document, actor Principal) error {
	if issues := document.Validate(); len(issues) > 0 {
		return ValidationFailed(issues)
	}
	canonical, err := document.CanonicalBytes()
	if err != nil {
		return err
	}
	hash := refdata.ContentHash(canonical)
	draft, err := s.store.LoadDraft(ctx)
	if err != nil {
		return err
	}
	if !draft.IsEmpty() {
		if draft.ContentHash == hash {
			return nil
		}
		s.logger.InfoContext(ctx, "reference-data seed skipped because a draft already exists")
		return nil
	}
	_, err = s.store.SaveDraft(ctx, SaveDraftRequest{
		ExpectedRevision: draft.Revision, Document: canonical, ContentHash: hash,
		Actor: actor, Action: "reference.seed",
	})
	return err
}

func (s *Service) validate(document refdata.Document) ([]byte, []Issue) {
	normalized := document.Normalize()
	canonical, err := normalized.CanonicalBytes()
	if err != nil {
		return nil, []Issue{{Path: "document", Code: "REF_ENCODE", Message: err.Error()}}
	}
	raw := normalized.Validate()
	issues := make([]Issue, 0, len(raw))
	for _, issue := range raw {
		issues = append(issues, Issue{Path: issue.Path, Code: issue.Code, Message: issue.Message})
	}
	return canonical, issues
}

// Issue is the service-facing validation issue.
type Issue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func normalizeShard(shardID string) string {
	shardID = strings.TrimSpace(shardID)
	if shardID == "" {
		return DefaultShard
	}
	return shardID
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func diffJSON(before, after refdata.Document, limit int) []Change {
	beforeBytes, _ := before.CanonicalBytes()
	afterBytes, _ := after.CanonicalBytes()
	var beforeValue, afterValue any
	if err := json.Unmarshal(beforeBytes, &beforeValue); err != nil {
		return []Change{{Path: "$", Kind: "decode_error"}}
	}
	if err := json.Unmarshal(afterBytes, &afterValue); err != nil {
		return []Change{{Path: "$", Kind: "decode_error"}}
	}
	var changes []Change
	var walk func(path string, left, right any)
	walk = func(path string, left, right any) {
		if len(changes) >= limit {
			return
		}
		leftMap, leftIsMap := left.(map[string]any)
		rightMap, rightIsMap := right.(map[string]any)
		if leftIsMap && rightIsMap {
			keys := make(map[string]struct{}, len(leftMap)+len(rightMap))
			for key := range leftMap {
				keys[key] = struct{}{}
			}
			for key := range rightMap {
				keys[key] = struct{}{}
			}
			ordered := make([]string, 0, len(keys))
			for key := range keys {
				ordered = append(ordered, key)
			}
			sort.Strings(ordered)
			for _, key := range ordered {
				walk(path+"."+key, leftMap[key], rightMap[key])
			}
			return
		}
		leftSlice, leftIsSlice := left.([]any)
		rightSlice, rightIsSlice := right.([]any)
		if leftIsSlice && rightIsSlice {
			maxLen := len(leftSlice)
			if len(rightSlice) > maxLen {
				maxLen = len(rightSlice)
			}
			for index := 0; index < maxLen; index++ {
				var leftItem, rightItem any
				if index < len(leftSlice) {
					leftItem = leftSlice[index]
				}
				if index < len(rightSlice) {
					rightItem = rightSlice[index]
				}
				walk(fmt.Sprintf("%s[%d]", path, index), leftItem, rightItem)
			}
			return
		}
		if !jsonEqual(left, right) {
			kind := "changed"
			if left == nil {
				kind = "added"
			} else if right == nil {
				kind = "removed"
			}
			changes = append(changes, Change{Path: path, Before: left, After: right, Kind: kind})
		}
	}
	walk("$", beforeValue, afterValue)
	return changes
}

func jsonEqual(left, right any) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}
