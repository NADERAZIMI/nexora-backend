package reference

import (
	"time"

	"github.com/google/uuid"

	"github.com/nexora-exchange/nexora/libs/go/refdata"
)

// Proposal lifecycle states.
const (
	ProposalPending   = "pending_approval"
	ProposalApproved  = "approved"
	ProposalRejected  = "rejected"
	ProposalPublished = "published"
	ProposalCancelled = "cancelled"
)

// Approval decisions.
const (
	DecisionApproved = "approved"
	DecisionRejected = "rejected"
)

// Activation lifecycle states.
const (
	ActivationPending    = "pending"
	ActivationActive     = "active"
	ActivationFailed     = "failed"
	ActivationSuperseded = "superseded"
)

// RequiredApprovals is the maker/checker threshold for publication.
const RequiredApprovals = 2

// Administrative permissions resolved by the identity service.
const (
	PermRead     = "admin:reference:read"
	PermWrite    = "admin:reference:write"
	PermApprove  = "admin:reference:approve"
	PermPublish  = "admin:reference:publish"
	PermActivate = "admin:reference:activate"
)

// Step-up actions required for privileged mutations.
const (
	StepUpWrite    = "admin:reference:write"
	StepUpApprove  = "admin:reference:approve"
	StepUpPublish  = "admin:reference:publish"
	StepUpActivate = "admin:reference:activate"
)

// DefaultShard is used by single-shard deployments and seeds.
const DefaultShard = "default"

// Principal is an authoritative identity resolved by the identity service.
type Principal struct {
	UserID      uuid.UUID
	Roles       []string
	Permissions []string
	MFA         bool
	RequestID   string
}

// HasPermission reports an authoritative permission grant.
func (p Principal) HasPermission(permission string) bool {
	for _, candidate := range p.Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

// Draft is the mutable working configuration.
type Draft struct {
	Revision    int64
	BaseVersion *int64
	ContentHash string
	Document    refdata.Document
	Canonical   []byte
	UpdatedBy   *uuid.UUID
	UpdatedAt   time.Time
}

// IsEmpty reports whether no configuration has been stored yet.
func (d Draft) IsEmpty() bool {
	return d.Revision == 0 && len(d.Canonical) == 0
}

// Proposal is an immutable configuration submission awaiting review.
type Proposal struct {
	ProposalID       uuid.UUID
	BaseVersion      *int64
	SchemaVersion    string
	ContentHash      string
	Document         []byte
	Parsed           refdata.Document
	Status           string
	CreatedBy        uuid.UUID
	CreatedAt        time.Time
	SubmittedAt      time.Time
	DecidedAt        *time.Time
	RejectionReason  *string
	PublishedVersion *int64
	Approvals        []Approval
}

// Approval is one immutable maker/checker decision.
type Approval struct {
	ProposalID   uuid.UUID
	ApproverID   uuid.UUID
	Decision     string
	Reason       *string
	ProposalHash string
	DecidedAt    time.Time
}

// ApprovalCount returns the number of distinct approvals.
func (p Proposal) ApprovalCount() int {
	count := 0
	for _, approval := range p.Approvals {
		if approval.Decision == DecisionApproved {
			count++
		}
	}
	return count
}

// Version is one immutable published configuration.
type Version struct {
	Version          int64
	SchemaVersion    string
	ContentHash      string
	Document         []byte
	Parsed           refdata.Document
	EffectiveAt      time.Time
	CreatedBy        uuid.UUID
	CreatedAt        time.Time
	ProposalID       uuid.UUID
	RollbackOf       *int64
	ApprovalCount    int
	Canonicalization string
}

// VersionSummary omits the document body.
type VersionSummary struct {
	Version          int64     `json:"version"`
	SchemaVersion    string    `json:"schema_version"`
	ContentHash      string    `json:"content_hash"`
	EffectiveAt      time.Time `json:"effective_at"`
	CreatedBy        uuid.UUID `json:"created_by"`
	CreatedAt        time.Time `json:"created_at"`
	ProposalID       uuid.UUID `json:"proposal_id"`
	RollbackOf       *int64    `json:"rollback_of,omitempty"`
	ApprovalCount    int       `json:"approval_count"`
	Canonicalization string    `json:"canonicalization"`
}

// Summary returns the metadata-only view.
func (v Version) Summary() VersionSummary {
	return VersionSummary{
		Version: v.Version, SchemaVersion: v.SchemaVersion, ContentHash: v.ContentHash,
		EffectiveAt: v.EffectiveAt, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt,
		ProposalID: v.ProposalID, RollbackOf: v.RollbackOf, ApprovalCount: v.ApprovalCount,
		Canonicalization: v.Canonicalization,
	}
}

// Activation is one requested shard activation.
type Activation struct {
	ActivationID    uuid.UUID  `json:"activation_id"`
	ShardID         string     `json:"shard_id"`
	Version         int64      `json:"version"`
	Status          string     `json:"status"`
	ExpectedVersion *int64     `json:"expected_version,omitempty"`
	RollbackOf      *int64     `json:"rollback_of,omitempty"`
	RequestedBy     uuid.UUID  `json:"requested_by"`
	RequestedAt     time.Time  `json:"requested_at"`
	EffectiveAt     time.Time  `json:"effective_at"`
	ActivatedAt     *time.Time `json:"activated_at,omitempty"`
	Failure         *string    `json:"failure,omitempty"`
	IdempotencyKey  string     `json:"idempotency_key"`
}

// Active is the effective version for a shard.
type Active struct {
	ShardID      string    `json:"shard_id"`
	Version      int64     `json:"version"`
	ActivationID uuid.UUID `json:"activation_id"`
	ActivatedAt  time.Time `json:"activated_at"`
	Rollback     bool      `json:"rollback"`
}

// ActivationRequest creates or repeats an activation.
type ActivationRequest struct {
	ShardID         string
	Version         int64
	ExpectedVersion *int64
	EffectiveAt     time.Time
	IdempotencyKey  string
	Reason          string
	Rollback        bool
}

// AuditEvent is one append-only, hash-chained administrative event.
type AuditEvent struct {
	EventID    uuid.UUID      `json:"event_id"`
	OccurredAt time.Time      `json:"occurred_at"`
	ActorID    uuid.UUID      `json:"actor_id"`
	ActorRole  string         `json:"actor_role"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Outcome    string         `json:"outcome"`
	RequestID  string         `json:"request_id"`
	Metadata   map[string]any `json:"metadata"`
	PrevHash   string         `json:"prev_hash"`
	EventHash  string         `json:"event_hash"`
}

// Change is one structural difference between two documents.
type Change struct {
	Path   string `json:"path"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
	Kind   string `json:"kind"`
}

// Diff summarizes two immutable documents.
type Diff struct {
	FromVersion int64    `json:"from_version"`
	ToVersion   int64    `json:"to_version"`
	Changes     []Change `json:"changes"`
	Truncated   bool     `json:"truncated"`
}
