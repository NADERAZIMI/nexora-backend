package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nexora-exchange/nexora/libs/go/httpx"
	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
)

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email             string `json:"email"`
		Phone             string `json:"phone"`
		Password          string `json:"password"`
		CountryCode       string `json:"countryCode"`
		DeviceFingerprint string `json:"deviceFingerprint"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	meta := s.meta(r)
	meta.DeviceFingerprint = body.DeviceFingerprint
	result, err := s.service.Register(r.Context(), identity.RegisterInput{
		Email: body.Email, Phone: body.Phone, Password: body.Password,
		CountryCode: body.CountryCode, DeviceFingerprint: body.DeviceFingerprint,
	}, meta)
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"verificationRequired": result.VerificationRequired,
		"devVerificationToken": result.DevVerificationToken,
		"devPhoneToken":        result.DevPhoneToken,
		"requestId":            httpx.RequestIDFromContext(r.Context()),
	})
}

func (s *Server) verifyEmail(w http.ResponseWriter, r *http.Request) {
	s.verifyContact(w, r, "email")
}

func (s *Server) verifyPhone(w http.ResponseWriter, r *http.Request) {
	s.verifyContact(w, r, "phone")
}

func (s *Server) verifyContact(w http.ResponseWriter, r *http.Request, kind string) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	if err := s.service.VerifyContact(r.Context(), body.Token, kind, s.meta(r)); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resendVerification(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
		Kind  string `json:"kind"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	if strings.TrimSpace(body.Kind) == "" {
		body.Kind = "email"
	}
	if err := s.service.ResendVerification(r.Context(), body.Email, body.Kind, s.meta(r)); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "requestId": httpx.RequestIDFromContext(r.Context())})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email             string `json:"email"`
		Password          string `json:"password"`
		TOTPCode          string `json:"totpCode"`
		RecoveryCode      string `json:"recoveryCode"`
		DeviceFingerprint string `json:"deviceFingerprint"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	meta := s.meta(r)
	meta.DeviceFingerprint = body.DeviceFingerprint
	result, err := s.service.Login(r.Context(), identity.LoginInput{
		Email: body.Email, Password: body.Password, TOTPCode: body.TOTPCode,
		RecoveryCode: body.RecoveryCode, DeviceFingerprint: body.DeviceFingerprint,
	}, meta)
	if err != nil {
		if appErr, ok := identity.IsAppError(err); ok && appErr.Code == identity.CodeAuthMFARequired {
			response := map[string]any{
				"mfaRequired": true, "code": appErr.Code, "message": appErr.Message,
				"mfaToken": "", "methods": []string{}, "requestId": httpx.RequestIDFromContext(r.Context()),
			}
			var cause *identity.MFARequiredCause
			if errors.As(appErr.Cause, &cause) {
				response["methods"] = cause.Methods
			}
			writeJSON(w, http.StatusUnauthorized, response)
			return
		}
		writeError(w, r, err, s.logger)
		return
	}
	if result.MFARequired {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"mfaRequired": true, "code": identity.CodeAuthMFARequired,
			"message": "multi-factor authentication required", "mfaToken": result.MFAToken,
			"methods": result.Methods, "requestId": httpx.RequestIDFromContext(r.Context()),
		})
		return
	}
	writeTokenPair(w, r, result.Tokens)
}

func (s *Server) verifyMFA(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MFAToken     string `json:"mfaToken"`
		TOTPCode     string `json:"totpCode"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	result, err := s.service.VerifyMFA(r.Context(), body.MFAToken, body.TOTPCode, body.RecoveryCode, s.meta(r))
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeTokenPair(w, r, result.Tokens)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	tokens, err := s.service.Refresh(r.Context(), body.RefreshToken, s.meta(r))
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeTokenPair(w, r, tokens)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	if err := s.service.Logout(r.Context(), principal, s.meta(r)); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) currentSession(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	sessions, err := s.service.ListSessions(r.Context(), principal)
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	response := map[string]any{
		"userId": principal.UserID, "sessionId": principal.SessionID, "kind": principal.Kind,
		"roles": principal.Roles, "permissions": identity.PermissionsFor(principal.Roles),
		"mfa": principal.MFA, "requestId": httpx.RequestIDFromContext(r.Context()),
	}
	for _, session := range sessions {
		if session.Current {
			response["session"] = session
			break
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) securityStatus(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	status, err := s.service.SecurityStatus(r.Context(), principal)
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	sessions, err := s.service.ListSessions(r.Context(), principal)
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "requestId": httpx.RequestIDFromContext(r.Context())})
}

func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	sessionID, err := uuid.Parse(r.PathValue("sessionId"))
	if err != nil {
		writeError(w, r, identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "invalid session ID"), s.logger)
		return
	}
	if err := s.service.RevokeSession(r.Context(), principal, sessionID, s.meta(r)); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) stepUp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action       string `json:"action"`
		TOTPCode     string `json:"totpCode"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	principal, _ := PrincipalFromContext(r.Context())
	token, expires, err := s.service.IssueStepUp(r.Context(), principal, body.TOTPCode, body.RecoveryCode, body.Action, s.meta(r))
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stepUpToken": token, "expiresAt": expires, "action": body.Action})
}

func (s *Server) beginTOTP(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	enrollment, err := s.service.BeginTOTP(r.Context(), principal, s.meta(r))
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, enrollment)
}

func (s *Server) confirmTOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CredentialID string `json:"credentialId"`
		Code         string `json:"code"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	credentialID, err := uuid.Parse(body.CredentialID)
	if err != nil {
		writeError(w, r, identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "invalid credential ID"), s.logger)
		return
	}
	principal, _ := PrincipalFromContext(r.Context())
	if err := s.service.ConfirmTOTP(r.Context(), principal, credentialID, body.Code, s.meta(r)); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) generateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	codes, err := s.service.GenerateRecoveryCodes(r.Context(), principal, s.meta(r))
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"codes": codes.Codes, "requestId": httpx.RequestIDFromContext(r.Context())})
}

func (s *Server) webauthnRegisterBegin(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	options, err := s.service.BeginWebAuthnRegistration(r.Context(), principal, s.meta(r))
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, options.Options)
}

func (s *Server) webauthnRegisterFinish(w http.ResponseWriter, r *http.Request) {
	body, err := readRawBody(w, r)
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	principal, _ := PrincipalFromContext(r.Context())
	if err := s.service.FinishWebAuthnRegistration(r.Context(), principal, body, s.meta(r)); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) webauthnLoginBegin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	options, err := s.service.BeginWebAuthnLogin(r.Context(), body.Email, s.meta(r))
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeJSON(w, http.StatusOK, options.Options)
}

func (s *Server) webauthnLoginFinish(w http.ResponseWriter, r *http.Request) {
	email := r.Header.Get("X-WebAuthn-Email")
	if strings.TrimSpace(email) == "" {
		writeError(w, r, identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "X-WebAuthn-Email header is required"), s.logger)
		return
	}
	body, err := readRawBody(w, r)
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	tokens, err := s.service.FinishWebAuthnLogin(r.Context(), email, body, s.meta(r))
	if err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	writeTokenPair(w, r, tokens)
}

func (s *Server) passwordResetRequest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	_ = s.service.RequestPasswordReset(r.Context(), body.Email, s.meta(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "requestId": httpx.RequestIDFromContext(r.Context())})
}

func (s *Server) passwordResetConfirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"newPassword"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	if err := s.service.ConfirmPasswordReset(r.Context(), body.Token, body.NewPassword, s.meta(r)); err != nil {
		writeError(w, r, err, s.logger)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"userId": principal.UserID, "kind": principal.Kind, "roles": principal.Roles,
		"permissions": identity.PermissionsFor(principal.Roles), "scopes": principal.Scopes,
		"requestId": httpx.RequestIDFromContext(r.Context()),
	})
}

func (s *Server) permissions(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	writeJSON(w, http.StatusOK, s.service.Permissions(principal))
}

func writeTokenPair(w http.ResponseWriter, r *http.Request, tokens *identity.TokenPair) {
	if tokens == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":     map[string]any{"code": identity.CodeInternal, "message": "internal server error"},
			"requestId": httpx.RequestIDFromContext(r.Context()),
		})
		return
	}
	expiresIn := int(time.Until(tokens.AccessExpiresAt).Seconds())
	if expiresIn < 0 {
		expiresIn = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accessToken": tokens.AccessToken, "refreshToken": tokens.RefreshToken,
		"tokenType": "Bearer", "expiresIn": expiresIn,
		"accessExpiresAt": tokens.AccessExpiresAt, "refreshExpiresAt": tokens.RefreshExpiresAt,
		"sessionId": tokens.SessionID, "mfa": tokens.MFA,
		"requestId": httpx.RequestIDFromContext(r.Context()),
	})
}

func readRawBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return nil, identity.NewAppError(http.StatusRequestEntityTooLarge, identity.CodeValidation, "request body too large")
	}
	if len(body) == 0 {
		return nil, identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "request body is required")
	}
	var value json.RawMessage
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "invalid JSON request body")
	}
	return body, nil
}
