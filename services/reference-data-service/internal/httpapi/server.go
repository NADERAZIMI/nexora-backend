// Package httpapi exposes the protected Super Admin reference-data API and the
// token-protected internal snapshot API.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nexora-exchange/nexora/libs/go/httpx"
	"github.com/nexora-exchange/nexora/libs/go/observability"
	"github.com/nexora-exchange/nexora/libs/go/refdata"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/auth"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/snapshot"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type contextKey string

const principalKey contextKey = "reference-principal"

const maxBodyBytes = 8 << 20

// Store is the persistence surface used for readiness.
type Store interface {
	Ping(ctx context.Context) error
}

// Observer records bounded operational signals.
type Observer interface {
	Incident(category string)
}

// Server builds the reference-data HTTP API.
type Server struct {
	service       *reference.Service
	authorizer    auth.Authorizer
	snapshots     *snapshot.Cache
	store         Store
	logger        *slog.Logger
	version       string
	internalToken string
	observer      Observer
}

// New constructs the HTTP handler.
func New(service *reference.Service, authorizer auth.Authorizer, snapshots *snapshot.Cache, store Store,
	logger *slog.Logger, version, internalToken string, observer Observer, providers *observability.Providers) http.Handler {
	s := &Server{
		service: service, authorizer: authorizer, snapshots: snapshots, store: store,
		logger: logger, version: version, internalToken: internalToken, observer: observer,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.Handle("GET /metrics", promhttp.HandlerFor(providers.Registry, promhttp.HandlerOpts{EnableOpenMetrics: true}))

	mux.Handle("GET /v1/reference-data/active", s.internalOnly(http.HandlerFunc(s.internalActive)))
	mux.Handle("GET /v1/reference-data/versions/{version}/snapshot", s.internalOnly(http.HandlerFunc(s.internalVersion)))

	mux.Handle("GET /v1/admin/reference-data/assets", s.protected(reference.PermRead, "", http.HandlerFunc(s.listAssets)))
	mux.Handle("GET /v1/admin/reference-data/networks", s.protected(reference.PermRead, "", http.HandlerFunc(s.listNetworks)))
	mux.Handle("GET /v1/admin/reference-data/draft", s.protected(reference.PermRead, "", http.HandlerFunc(s.getDraft)))
	mux.Handle("PUT /v1/admin/reference-data/draft", s.protected(reference.PermWrite, reference.StepUpWrite, http.HandlerFunc(s.putDraft)))
	mux.Handle("POST /v1/admin/reference-data/draft/reset", s.protected(reference.PermWrite, reference.StepUpWrite, http.HandlerFunc(s.resetDraft)))
	mux.Handle("POST /v1/admin/reference-data/draft/validate", s.protected(reference.PermRead, "", http.HandlerFunc(s.validateDraft)))
	mux.Handle("POST /v1/admin/reference-data/proposals", s.protected(reference.PermWrite, reference.StepUpWrite, http.HandlerFunc(s.submitProposal)))
	mux.Handle("GET /v1/admin/reference-data/proposals", s.protected(reference.PermRead, "", http.HandlerFunc(s.listProposals)))
	mux.Handle("GET /v1/admin/reference-data/proposals/{proposalId}", s.protected(reference.PermRead, "", http.HandlerFunc(s.getProposal)))
	mux.Handle("POST /v1/admin/reference-data/proposals/{proposalId}/approve", s.protected(reference.PermApprove, reference.StepUpApprove, http.HandlerFunc(s.approveProposal)))
	mux.Handle("POST /v1/admin/reference-data/proposals/{proposalId}/reject", s.protected(reference.PermApprove, reference.StepUpApprove, http.HandlerFunc(s.rejectProposal)))
	mux.Handle("POST /v1/admin/reference-data/proposals/{proposalId}/publish", s.protected(reference.PermPublish, reference.StepUpPublish, http.HandlerFunc(s.publishProposal)))
	mux.Handle("GET /v1/admin/reference-data/versions", s.protected(reference.PermRead, "", http.HandlerFunc(s.listVersions)))
	mux.Handle("GET /v1/admin/reference-data/versions/{version}", s.protected(reference.PermRead, "", http.HandlerFunc(s.getVersion)))
	mux.Handle("GET /v1/admin/reference-data/diff", s.protected(reference.PermRead, "", http.HandlerFunc(s.diffVersions)))
	mux.Handle("GET /v1/admin/reference-data/diff/draft", s.protected(reference.PermRead, "", http.HandlerFunc(s.diffDraft)))
	mux.Handle("GET /v1/admin/reference-data/active", s.protected(reference.PermRead, "", http.HandlerFunc(s.active)))
	mux.Handle("POST /v1/admin/reference-data/activations", s.protected(reference.PermActivate, reference.StepUpActivate, http.HandlerFunc(s.activate)))
	mux.Handle("POST /v1/admin/reference-data/rollbacks", s.protected(reference.PermActivate, reference.StepUpActivate, http.HandlerFunc(s.rollback)))
	mux.Handle("GET /v1/admin/reference-data/activations", s.protected(reference.PermRead, "", http.HandlerFunc(s.listActivations)))
	mux.Handle("GET /v1/admin/reference-data/audit", s.protected(reference.PermRead, "", http.HandlerFunc(s.audit)))

	handler := httpx.Chain(mux,
		httpx.RequestID,
		s.securityHeaders,
		httpx.AccessLog(logger),
		httpx.Recover(logger),
	)
	if providers != nil {
		metrics := httpx.NewMetrics(providers.Registry)
		handler = httpx.Chain(handler, metrics.Middleware("reference-data-service"))
	}
	return otelhttp.NewHandler(handler, "reference.http")
}

func (s *Server) protected(permission, stepUpAction string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, reference.NewError(http.StatusUnauthorized, reference.CodeUnauthorized, "authentication is required", nil))
			return
		}
		requestID := httpx.RequestIDFromContext(r.Context())
		principal, err := s.authorizer.Authorize(r.Context(), token, permission, stepUpAction, requestID)
		if err != nil {
			if appErr := reference.AsError(err); appErr.Code == reference.CodeForbidden {
				s.incident("authorization_denied")
			}
			writeError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, principal)))
	})
}

func (s *Server) internalOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.internalToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		provided := r.Header.Get("X-Nexora-Service-Token")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.internalToken)) != 1 {
			s.incident("internal_token_denied")
			writeError(w, reference.NewError(http.StatusUnauthorized, reference.CodeUnauthorized, "service authentication is required", nil))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "reference-data-service", "version": s.version})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "service": "reference-data-service"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "service": "reference-data-service", "version": s.version})
}

func (s *Server) listAssets(w http.ResponseWriter, r *http.Request) {
	draft, err := s.service.GetDraft(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if draft.IsEmpty() {
		writeError(w, reference.NewError(http.StatusNotFound, reference.CodeNotFound, "draft document is empty", nil))
		return
	}
	writeJSON(w, http.StatusOK, assetListView{
		Revision: draft.Revision, BaseVersion: draft.BaseVersion, ContentHash: draft.ContentHash,
		Assets: draft.Document.Assets,
	})
}

func (s *Server) listNetworks(w http.ResponseWriter, r *http.Request) {
	draft, err := s.service.GetDraft(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if draft.IsEmpty() {
		writeError(w, reference.NewError(http.StatusNotFound, reference.CodeNotFound, "draft document is empty", nil))
		return
	}
	writeJSON(w, http.StatusOK, networkListView{
		Revision: draft.Revision, BaseVersion: draft.BaseVersion, ContentHash: draft.ContentHash,
		Networks: draft.Document.Networks,
	})
}

func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) {
	draft, err := s.service.GetDraft(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, draftView(draft))
}

func (s *Server) putDraft(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ExpectedRevision int64           `json:"expected_revision"`
		BaseVersion      *int64          `json:"base_version"`
		Document         json.RawMessage `json:"document"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, reference.InvalidInput(err.Error()))
		return
	}
	document, err := refdata.Parse(request.Document)
	if err != nil {
		writeError(w, reference.InvalidInput("document cannot be decoded"))
		return
	}
	draft, err := s.service.PutDraft(r.Context(), principal(r), request.ExpectedRevision, request.BaseVersion, document)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, draftView(draft))
}

func (s *Server) resetDraft(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ExpectedRevision int64 `json:"expected_revision"`
		Version          int64 `json:"version"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, reference.InvalidInput(err.Error()))
		return
	}
	draft, err := s.service.ResetDraftFromVersion(r.Context(), principal(r), request.ExpectedRevision, request.Version)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, draftView(draft))
}

func (s *Server) validateDraft(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Document json.RawMessage `json:"document"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, reference.InvalidInput(err.Error()))
		return
	}
	document, err := refdata.Parse(request.Document)
	if err != nil {
		writeError(w, reference.InvalidInput("document cannot be decoded"))
		return
	}
	_, hash, issues := s.service.Validate(document)
	writeJSON(w, http.StatusOK, map[string]any{
		"valid": len(issues) == 0, "issues": issues, "content_hash": hash,
	})
}

func (s *Server) submitProposal(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, reference.InvalidInput(err.Error()))
		return
	}
	proposal, err := s.service.SubmitProposal(r.Context(), principal(r), request.ExpectedRevision)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, proposalView(proposal))
}

func (s *Server) listProposals(w http.ResponseWriter, r *http.Request) {
	proposals, err := s.service.ListProposals(r.Context(), queryInt(r, "limit", 50))
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]any, 0, len(proposals))
	for _, proposal := range proposals {
		out = append(out, proposalView(proposal))
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposals": out})
}

func (s *Server) getProposal(w http.ResponseWriter, r *http.Request) {
	proposalID, err := pathUUID(r, "proposalId")
	if err != nil {
		writeError(w, err)
		return
	}
	proposal, err := s.service.GetProposal(r.Context(), proposalID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, proposalView(proposal))
}

func (s *Server) approveProposal(w http.ResponseWriter, r *http.Request) {
	proposalID, err := pathUUID(r, "proposalId")
	if err != nil {
		writeError(w, err)
		return
	}
	var request struct {
		ProposalHash string `json:"proposal_hash"`
		Reason       string `json:"reason"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, reference.InvalidInput(err.Error()))
		return
	}
	proposal, err := s.service.ApproveProposal(r.Context(), principal(r), proposalID, request.ProposalHash, request.Reason)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, proposalView(proposal))
}

func (s *Server) rejectProposal(w http.ResponseWriter, r *http.Request) {
	proposalID, err := pathUUID(r, "proposalId")
	if err != nil {
		writeError(w, err)
		return
	}
	var request struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, reference.InvalidInput(err.Error()))
		return
	}
	proposal, err := s.service.RejectProposal(r.Context(), principal(r), proposalID, request.Reason)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, proposalView(proposal))
}

func (s *Server) publishProposal(w http.ResponseWriter, r *http.Request) {
	proposalID, err := pathUUID(r, "proposalId")
	if err != nil {
		writeError(w, err)
		return
	}
	var request struct {
		EffectiveAt string `json:"effective_at"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, reference.InvalidInput(err.Error()))
		return
	}
	var effectiveAt time.Time
	if strings.TrimSpace(request.EffectiveAt) != "" {
		effectiveAt, err = time.Parse(time.RFC3339Nano, request.EffectiveAt)
		if err != nil {
			writeError(w, reference.InvalidInput("effective_at must be RFC3339"))
			return
		}
	}
	version, err := s.service.PublishProposal(r.Context(), principal(r), proposalID, effectiveAt)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, versionView(version))
}

func (s *Server) listVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := s.service.ListVersions(r.Context(), queryInt(r, "limit", 50))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || version <= 0 {
		writeError(w, reference.InvalidInput("version must be a positive integer"))
		return
	}
	published, err := s.service.GetVersion(r.Context(), version)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, versionView(published))
}

func (s *Server) diffVersions(w http.ResponseWriter, r *http.Request) {
	from, err := queryInt64(r, "from")
	if err != nil {
		writeError(w, reference.InvalidInput("from must be a positive integer"))
		return
	}
	to, err := queryInt64(r, "to")
	if err != nil {
		writeError(w, reference.InvalidInput("to must be a positive integer"))
		return
	}
	diff, err := s.service.DiffVersions(r.Context(), from, to)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, diff)
}

func (s *Server) diffDraft(w http.ResponseWriter, r *http.Request) {
	base, err := queryInt64(r, "base")
	if err != nil {
		writeError(w, reference.InvalidInput("base must be a positive integer"))
		return
	}
	diff, err := s.service.DiffDraft(r.Context(), base)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, diff)
}

func (s *Server) active(w http.ResponseWriter, r *http.Request) {
	active, err := s.service.Active(r.Context(), r.URL.Query().Get("shard_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, active)
}

func (s *Server) activate(w http.ResponseWriter, r *http.Request) {
	s.changeActivation(w, r, false)
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	s.changeActivation(w, r, true)
}

func (s *Server) changeActivation(w http.ResponseWriter, r *http.Request, rollback bool) {
	var request struct {
		ShardID         string `json:"shard_id"`
		Version         int64  `json:"version"`
		ExpectedVersion *int64 `json:"expected_version"`
		EffectiveAt     string `json:"effective_at"`
		IdempotencyKey  string `json:"idempotency_key"`
		Reason          string `json:"reason"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, reference.InvalidInput(err.Error()))
		return
	}
	var effectiveAt time.Time
	if strings.TrimSpace(request.EffectiveAt) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, request.EffectiveAt)
		if err != nil {
			writeError(w, reference.InvalidInput("effective_at must be RFC3339"))
			return
		}
		effectiveAt = parsed
	}
	activationRequest := reference.ActivationRequest{
		ShardID: request.ShardID, Version: request.Version, ExpectedVersion: request.ExpectedVersion,
		EffectiveAt: effectiveAt, IdempotencyKey: request.IdempotencyKey, Reason: request.Reason,
	}
	var (
		activation reference.Activation
		err        error
	)
	if rollback {
		activation, err = s.service.Rollback(r.Context(), principal(r), activationRequest)
	} else {
		activation, err = s.service.Activate(r.Context(), principal(r), activationRequest)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	// Refresh the local consumer immediately so a rollback is observable to
	// this service without waiting for the periodic reload.
	refreshCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, err := s.snapshots.Reload(refreshCtx, activation.ShardID); err != nil {
		s.incident("post_activation_reload_failed")
		s.logger.WarnContext(r.Context(), "reference-data consumer refresh failed", "shard_id", activation.ShardID, "error", err)
	}
	writeJSON(w, http.StatusCreated, activation)
}

func (s *Server) listActivations(w http.ResponseWriter, r *http.Request) {
	activations, err := s.service.ListActivations(r.Context(), r.URL.Query().Get("shard_id"), queryInt(r, "limit", 50))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"activations": activations})
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	var before time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, reference.InvalidInput("before must be RFC3339"))
			return
		}
		before = parsed
	}
	events, err := s.service.ListAudit(r.Context(), queryInt(r, "limit", 50), before)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) internalActive(w http.ResponseWriter, r *http.Request) {
	snapshotValue, err := s.snapshots.Reload(r.Context(), r.URL.Query().Get("shard_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeSnapshot(w, snapshotValue)
}

func (s *Server) internalVersion(w http.ResponseWriter, r *http.Request) {
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || version <= 0 {
		writeError(w, reference.InvalidInput("version must be a positive integer"))
		return
	}
	published, err := s.service.GetVersion(r.Context(), version)
	if err != nil {
		writeError(w, err)
		return
	}
	shardID := r.URL.Query().Get("shard_id")
	if shardID == "" {
		shardID = reference.DefaultShard
	}
	snapshotValue, err := snapshot.Build(reference.Active{ShardID: shardID, Version: version}, published)
	if err != nil {
		writeError(w, reference.NewError(500, reference.CodeInternal, "published version failed integrity verification", nil))
		return
	}
	writeSnapshot(w, snapshotValue)
}

func writeSnapshot(w http.ResponseWriter, value refdata.Snapshot) {
	if err := refdata.VerifyHash(value.Canonical, value.ContentHash); err != nil {
		writeError(w, reference.NewError(500, reference.CodeInternal, "snapshot integrity verification failed", nil))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Nexora-Config-Version", strconv.FormatInt(value.Version, 10))
	w.Header().Set("X-Nexora-Config-Hash", value.ContentHash)
	w.Header().Set("X-Nexora-Config-Schema", value.SchemaVersion)
	w.Header().Set("X-Nexora-Config-Effective-At", value.EffectiveAt.UTC().Format(time.RFC3339Nano))
	w.Header().Set("X-Nexora-Config-Shard", value.ShardID)
	w.Header().Set("X-Nexora-Config-Rollback", strconv.FormatBool(value.Rollback))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(value.Canonical)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) incident(category string) {
	if s.observer != nil {
		s.observer.Incident(category)
	}
}

func principal(r *http.Request) reference.Principal {
	value, _ := r.Context().Value(principalKey).(reference.Principal)
	return value
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is required")
		}
		return errors.New("request body is not valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body contains trailing data")
	}
	return nil
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, reference.InvalidInput(name + " must be a UUID")
	}
	return id, nil
}

func queryInt(r *http.Request, name string, fallback int) int {
	parsed, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func queryInt64(r *http.Request, name string) (int64, error) {
	parsed, err := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	if err != nil || parsed <= 0 {
		return 0, errors.New("invalid integer")
	}
	return parsed, nil
}

type draftViewValue struct {
	Revision    int64      `json:"revision"`
	BaseVersion *int64     `json:"base_version"`
	ContentHash string     `json:"content_hash"`
	Document    any        `json:"document"`
	UpdatedBy   *uuid.UUID `json:"updated_by,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type assetListView struct {
	Revision    int64           `json:"revision"`
	BaseVersion *int64          `json:"base_version"`
	ContentHash string          `json:"content_hash"`
	Assets      []refdata.Asset `json:"assets"`
}

type networkListView struct {
	Revision    int64             `json:"revision"`
	BaseVersion *int64            `json:"base_version"`
	ContentHash string            `json:"content_hash"`
	Networks    []refdata.Network `json:"networks"`
}

func draftView(draft reference.Draft) draftViewValue {
	view := draftViewValue{
		Revision: draft.Revision, BaseVersion: draft.BaseVersion, ContentHash: draft.ContentHash,
		UpdatedBy: draft.UpdatedBy, UpdatedAt: draft.UpdatedAt,
	}
	if !draft.IsEmpty() {
		view.Document = draft.Document
	}
	return view
}

type approvalView struct {
	ApproverID   uuid.UUID `json:"approver_id"`
	Decision     string    `json:"decision"`
	Reason       *string   `json:"reason,omitempty"`
	ProposalHash string    `json:"proposal_hash"`
	DecidedAt    time.Time `json:"decided_at"`
}

type proposalViewValue struct {
	ProposalID       uuid.UUID        `json:"proposal_id"`
	BaseVersion      *int64           `json:"base_version"`
	SchemaVersion    string           `json:"schema_version"`
	ContentHash      string           `json:"content_hash"`
	Document         refdata.Document `json:"document"`
	Status           string           `json:"status"`
	CreatedBy        uuid.UUID        `json:"created_by"`
	CreatedAt        time.Time        `json:"created_at"`
	SubmittedAt      time.Time        `json:"submitted_at"`
	DecidedAt        *time.Time       `json:"decided_at,omitempty"`
	RejectionReason  *string          `json:"rejection_reason,omitempty"`
	PublishedVersion *int64           `json:"published_version,omitempty"`
	Approvals        []approvalView   `json:"approvals"`
}

func proposalView(proposal reference.Proposal) proposalViewValue {
	approvals := make([]approvalView, 0, len(proposal.Approvals))
	for _, approval := range proposal.Approvals {
		approvals = append(approvals, approvalView{
			ApproverID: approval.ApproverID, Decision: approval.Decision, Reason: approval.Reason,
			ProposalHash: approval.ProposalHash, DecidedAt: approval.DecidedAt,
		})
	}
	return proposalViewValue{
		ProposalID: proposal.ProposalID, BaseVersion: proposal.BaseVersion, SchemaVersion: proposal.SchemaVersion,
		ContentHash: proposal.ContentHash, Document: proposal.Parsed, Status: proposal.Status,
		CreatedBy: proposal.CreatedBy, CreatedAt: proposal.CreatedAt, SubmittedAt: proposal.SubmittedAt,
		DecidedAt: proposal.DecidedAt, RejectionReason: proposal.RejectionReason,
		PublishedVersion: proposal.PublishedVersion, Approvals: approvals,
	}
}

type versionViewValue struct {
	Version          int64            `json:"version"`
	SchemaVersion    string           `json:"schema_version"`
	ContentHash      string           `json:"content_hash"`
	Document         refdata.Document `json:"document"`
	EffectiveAt      time.Time        `json:"effective_at"`
	CreatedBy        uuid.UUID        `json:"created_by"`
	CreatedAt        time.Time        `json:"created_at"`
	ProposalID       uuid.UUID        `json:"proposal_id"`
	RollbackOf       *int64           `json:"rollback_of,omitempty"`
	ApprovalCount    int              `json:"approval_count"`
	Canonicalization string           `json:"canonicalization"`
}

func versionView(version reference.Version) versionViewValue {
	return versionViewValue{
		Version: version.Version, SchemaVersion: version.SchemaVersion, ContentHash: version.ContentHash,
		Document: version.Parsed, EffectiveAt: version.EffectiveAt, CreatedBy: version.CreatedBy,
		CreatedAt: version.CreatedAt, ProposalID: version.ProposalID, RollbackOf: version.RollbackOf,
		ApprovalCount: version.ApprovalCount, Canonicalization: version.Canonicalization,
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	appErr := reference.AsError(err)
	writeJSON(w, appErr.Status, map[string]any{"error": appErr})
}
