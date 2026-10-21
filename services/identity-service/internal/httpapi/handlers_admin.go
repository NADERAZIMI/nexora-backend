package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/nexora-exchange/nexora/libs/go/httpx"
	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label         string   `json:"label"`
		Scopes        []string `json:"scopes"`
		IPAllowlist   []string `json:"ipAllowlist"`
		Algorithm     string   `json:"algorithm"`
		PublicKey     string   `json:"publicKey"`
		ExpiresInDays int      `json:"expiresInDays"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	principal, _ := PrincipalFromContext(r.Context())
	created, err := s.service.CreateAPIKey(r.Context(), principal, identity.CreateAPIKeyInput{
		Label: body.Label, Scopes: body.Scopes, IPAllowlist: body.IPAllowlist,
		Algorithm: body.Algorithm, PublicKey: body.PublicKey, ExpiresInDays: body.ExpiresInDays,
	}, s.meta(r))
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	keys, err := s.service.ListAPIKeys(r.Context(), principal)
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "requestId": httpx.RequestIDFromContext(r.Context())})
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	keyID, err := uuid.Parse(r.PathValue("keyId"))
	if err != nil {
		writeError(w, r, identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "invalid API key ID"), s.logger)
		return
	}
	principal, _ := PrincipalFromContext(r.Context())
	if err := s.service.RevokeAPIKey(r.Context(), principal, keyID, s.meta(r)); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) submitKYC(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProviderRef string `json:"providerRef"`
		Level       int16  `json:"level"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	principal, _ := PrincipalFromContext(r.Context())
	kyc, err := s.service.SubmitKYC(r.Context(), principal, body.ProviderRef, body.Level, s.meta(r))
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusCreated, kyc)
}

func (s *Server) listOwnKYC(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	cases, err := s.service.ListOwnKYC(r.Context(), principal)
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cases": cases, "requestId": httpx.RequestIDFromContext(r.Context())})
}

func (s *Server) adminGetUser(w http.ResponseWriter, r *http.Request) {
	target, err := uuid.Parse(r.PathValue("userId"))
	if err != nil {
		writeError(w, r, identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "invalid user ID"), s.logger)
		return
	}
	actor, _ := PrincipalFromContext(r.Context())
	view, err := s.service.AdminGetUser(r.Context(), actor, target)
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) adminFreeze(w http.ResponseWriter, r *http.Request) {
	s.adminSetStatus(w, r, identity.UserFrozen)
}

func (s *Server) adminUnfreeze(w http.ResponseWriter, r *http.Request) {
	s.adminSetStatus(w, r, identity.UserActive)
}

func (s *Server) adminSetStatus(w http.ResponseWriter, r *http.Request, status identity.UserStatus) {
	target, err := uuid.Parse(r.PathValue("userId"))
	if err != nil {
		writeError(w, r, identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "invalid user ID"), s.logger)
		return
	}
	actor, _ := PrincipalFromContext(r.Context())
	if err := s.service.AdminSetStatus(r.Context(), actor, target, status, s.meta(r)); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminSetRoles(w http.ResponseWriter, r *http.Request) {
	target, err := uuid.Parse(r.PathValue("userId"))
	if err != nil {
		writeError(w, r, identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "invalid user ID"), s.logger)
		return
	}
	var body struct {
		Roles []string `json:"roles"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	roles := make([]identity.Role, 0, len(body.Roles))
	for _, value := range body.Roles {
		role, err := identity.ParseRole(value)
		if err != nil {
			writeError(w, r, identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "unknown role"), s.logger)
			return
		}
		roles = append(roles, role)
	}
	actor, _ := PrincipalFromContext(r.Context())
	if err := s.service.AdminSetRoles(r.Context(), actor, target, roles, s.meta(r)); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminRevokeSessions(w http.ResponseWriter, r *http.Request) {
	target, err := uuid.Parse(r.PathValue("userId"))
	if err != nil {
		writeError(w, r, identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "invalid user ID"), s.logger)
		return
	}
	actor, _ := PrincipalFromContext(r.Context())
	count, err := s.service.AdminRevokeSessions(r.Context(), actor, target, s.meta(r))
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": count})
}

func (s *Server) adminRevokeAPIKeys(w http.ResponseWriter, r *http.Request) {
	target, err := uuid.Parse(r.PathValue("userId"))
	if err != nil {
		writeError(w, r, identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "invalid user ID"), s.logger)
		return
	}
	actor, _ := PrincipalFromContext(r.Context())
	count, err := s.service.AdminRevokeAPIKeys(r.Context(), actor, target, s.meta(r))
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": count})
}

func (s *Server) adminListAudit(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	actor, _ := PrincipalFromContext(r.Context())
	events, err := s.service.AdminListAudit(r.Context(), actor, identity.AuditFilter{
		ActorID: r.URL.Query().Get("actorId"), Action: r.URL.Query().Get("action"),
		TargetID: r.URL.Query().Get("targetId"), Limit: limit,
	})
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "requestId": httpx.RequestIDFromContext(r.Context())})
}

func (s *Server) adminListKYC(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	actor, _ := PrincipalFromContext(r.Context())
	cases, err := s.service.AdminListKYC(r.Context(), actor, limit)
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cases": cases})
}

func (s *Server) adminDecideKYC(w http.ResponseWriter, r *http.Request) {
	caseID, err := uuid.Parse(r.PathValue("caseId"))
	if err != nil {
		writeError(w, r, identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "invalid KYC case ID"), s.logger)
		return
	}
	var body struct {
		Status string `json:"status"`
		Level  int16  `json:"level"`
		Reason string `json:"reason"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	status := identity.KYCStatus(strings.ToLower(strings.TrimSpace(body.Status)))
	actor, _ := PrincipalFromContext(r.Context())
	if err := s.service.AdminDecideKYC(r.Context(), actor, caseID, status, body.Level, body.Reason, s.meta(r)); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
