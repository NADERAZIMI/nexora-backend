// Package httpapi exposes the identity service REST API.
package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nexora-exchange/nexora/libs/go/httpx"
	"github.com/nexora-exchange/nexora/libs/go/observability"
	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/time/rate"
)

type contextKey string

const principalKey contextKey = "identity-principal"

// Server builds the identity HTTP API.
type Server struct {
	service    *identity.Service
	cfg        identity.ServiceConfig
	logger     *slog.Logger
	version    string
	trustProxy bool
	providers  *observability.Providers
	limiter    *ipLimiter
}

// New constructs the HTTP handler.
func New(service *identity.Service, cfg identity.ServiceConfig, logger *slog.Logger, version string, trustProxy bool, providers *observability.Providers, ratePerMinute, burst int) http.Handler {
	s := &Server{
		service: service, cfg: cfg, logger: logger, version: version,
		trustProxy: trustProxy, providers: providers,
		limiter: newIPLimiter(rate.Limit(float64(ratePerMinute)/60.0), burst),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.Handle("GET /metrics", promhttp.HandlerFor(providers.Registry, promhttp.HandlerOpts{EnableOpenMetrics: true}))
	mux.HandleFunc("GET /v1/.well-known/jwks.json", s.jwks)
	mux.HandleFunc("GET /v1/.well-known/openid-configuration", s.openIDConfiguration)

	mux.HandleFunc("POST /v1/auth/register", s.register)
	mux.HandleFunc("POST /v1/auth/verify/email", s.verifyEmail)
	mux.HandleFunc("POST /v1/auth/verify/phone", s.verifyPhone)
	mux.HandleFunc("POST /v1/auth/resend-verification", s.resendVerification)
	mux.HandleFunc("POST /v1/auth/login", s.login)
	mux.HandleFunc("POST /v1/auth/mfa/verify", s.verifyMFA)
	mux.HandleFunc("POST /v1/auth/refresh", s.refresh)
	mux.HandleFunc("POST /v1/auth/password/reset/request", s.passwordResetRequest)
	mux.HandleFunc("POST /v1/auth/password/reset/confirm", s.passwordResetConfirm)
	mux.HandleFunc("POST /v1/auth/webauthn/login/begin", s.webauthnLoginBegin)
	mux.HandleFunc("POST /v1/auth/webauthn/login/finish", s.webauthnLoginFinish)

	mux.Handle("POST /v1/auth/logout", s.sessionAuth(http.HandlerFunc(s.logout)))
	mux.Handle("GET /v1/auth/session", s.sessionAuth(http.HandlerFunc(s.currentSession)))
	mux.Handle("GET /v1/auth/security-status", s.sessionAuth(http.HandlerFunc(s.securityStatus)))
	mux.Handle("GET /v1/auth/sessions", s.sessionAuth(http.HandlerFunc(s.listSessions)))
	mux.Handle("DELETE /v1/auth/sessions/{sessionId}", s.sessionAuth(http.HandlerFunc(s.revokeSession)))
	mux.Handle("POST /v1/auth/step-up", s.sessionAuth(http.HandlerFunc(s.stepUp)))
	mux.Handle("POST /v1/auth/2fa/totp/begin", s.sessionAuth(http.HandlerFunc(s.beginTOTP)))
	mux.Handle("POST /v1/auth/2fa/totp/confirm", s.sessionAuth(http.HandlerFunc(s.confirmTOTP)))
	mux.Handle("POST /v1/auth/2fa/recovery-codes", s.sessionAuth(s.requireStepUp("mfa:recovery_codes", http.HandlerFunc(s.generateRecoveryCodes))))
	mux.Handle("POST /v1/auth/webauthn/register/begin", s.sessionAuth(http.HandlerFunc(s.webauthnRegisterBegin)))
	mux.Handle("POST /v1/auth/webauthn/register/finish", s.sessionAuth(http.HandlerFunc(s.webauthnRegisterFinish)))

	mux.Handle("GET /v1/me", s.principalAuth("read", http.HandlerFunc(s.me)))
	mux.Handle("GET /v1/auth/permissions", s.principalAuth("", http.HandlerFunc(s.permissions)))

	mux.Handle("POST /v1/api-keys", s.sessionAuth(s.requireStepUp("api_key:create", http.HandlerFunc(s.createAPIKey))))
	mux.Handle("GET /v1/api-keys", s.sessionAuth(http.HandlerFunc(s.listAPIKeys)))
	mux.Handle("DELETE /v1/api-keys/{keyId}", s.sessionAuth(s.requireStepUp("api_key:revoke", http.HandlerFunc(s.revokeAPIKey))))

	mux.Handle("POST /v1/kyc/cases", s.sessionAuth(http.HandlerFunc(s.submitKYC)))
	mux.Handle("GET /v1/kyc/cases", s.sessionAuth(http.HandlerFunc(s.listOwnKYC)))

	mux.Handle("GET /v1/admin/users/{userId}", s.adminAuth(identity.PermAdminUserRead, "", http.HandlerFunc(s.adminGetUser)))
	mux.Handle("POST /v1/admin/users/{userId}/freeze", s.adminAuth(identity.PermAdminUserFreeze, "admin:user:freeze", http.HandlerFunc(s.adminFreeze)))
	mux.Handle("POST /v1/admin/users/{userId}/unfreeze", s.adminAuth(identity.PermAdminUserFreeze, "admin:user:unfreeze", http.HandlerFunc(s.adminUnfreeze)))
	mux.Handle("POST /v1/admin/users/{userId}/roles", s.adminAuth(identity.PermAdminRoleManage, "admin:role:manage", http.HandlerFunc(s.adminSetRoles)))
	mux.Handle("POST /v1/admin/users/{userId}/revoke-sessions", s.adminAuth(identity.PermAdminSessionRevoke, "admin:session:revoke", http.HandlerFunc(s.adminRevokeSessions)))
	mux.Handle("POST /v1/admin/users/{userId}/revoke-api-keys", s.adminAuth(identity.PermAdminAPIKeyRevoke, "admin:apikey:revoke", http.HandlerFunc(s.adminRevokeAPIKeys)))
	mux.Handle("GET /v1/admin/audit-events", s.adminAuth(identity.PermAdminAuditRead, "", http.HandlerFunc(s.adminListAudit)))
	mux.Handle("GET /v1/admin/kyc/cases", s.adminAuth(identity.PermAdminKYCRead, "", http.HandlerFunc(s.adminListKYC)))
	mux.Handle("POST /v1/admin/kyc/cases/{caseId}/decision", s.adminAuth(identity.PermAdminKYCDecide, "admin:kyc:decide", http.HandlerFunc(s.adminDecideKYC)))

	handler := httpx.Chain(mux,
		httpx.RequestID,
		s.securityHeaders,
		s.rateLimit,
		httpx.AccessLog(logger),
		httpx.Recover(logger),
	)
	if providers != nil {
		metrics := httpx.NewMetrics(providers.Registry)
		handler = httpx.Chain(handler, metrics.Middleware("identity-service"))
	}
	return otelhttp.NewHandler(handler, "identity.http")
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "identity-service", "version": s.version})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.service.Store().Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "service": "identity-service"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "service": "identity-service", "version": s.version})
}

func (s *Server) jwks(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, s.service.TokenManager().JWKS())
}

func (s *Server) openIDConfiguration(w http.ResponseWriter, r *http.Request) {
	base := "http://" + r.Host
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		base = "https://" + r.Host
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer": "nexora-identity", "jwks_uri": base + "/v1/.well-known/jwks.json",
		"id_token_signing_alg_values_supported": []string{"EdDSA"},
		"response_types_supported":              []string{"token"},
		"subject_types_supported":               []string{"public"},
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := s.clientIP(r)
		if !s.limiter.Allow(ip.String()) {
			err := identity.NewAppError(http.StatusTooManyRequests, identity.CodeAuthRateLimited, "rate limit exceeded")
			err.RetryAfter = time.Second
			writeAppError(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sessionAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := s.resolveBearer(r)
		if err != nil {
			writeError(w, r, err, s.logger)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, principal)))
	})
}

func (s *Server) principalAuth(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("X-Nexora-Key-Id")) != "" {
			principal, err := s.resolveAPIKey(w, r)
			if err != nil {
				writeError(w, r, err, s.logger)
				return
			}
			if scope != "" && !principal.HasScope(scope) {
				writeError(w, r, identity.NewAppError(http.StatusForbidden, identity.CodeAuthScope, "API key does not have the required scope"), s.logger)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, principal)))
			return
		}
		principal, err := s.resolveBearer(r)
		if err != nil {
			writeError(w, r, err, s.logger)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, principal)))
	})
}

func (s *Server) adminAuth(permission, stepUpAction string, next http.Handler) http.Handler {
	h := next
	if stepUpAction != "" {
		h = s.requireStepUp(stepUpAction, h)
	}
	h = s.requirePermission(permission, h)
	return s.sessionAuth(h)
}

func (s *Server) requirePermission(permission string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || principal.Kind != "session" {
			writeError(w, r, identity.NewAppError(http.StatusUnauthorized, identity.CodeAuthMissing, "authentication required"), s.logger)
			return
		}
		if !principal.HasPermission(permission) {
			writeError(w, r, identity.NewAppError(http.StatusForbidden, identity.CodeAuthForbidden, "insufficient permission"), s.logger)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireStepUp(action string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, _ := PrincipalFromContext(r.Context())
		if err := s.service.ResolveStepUp(r.Header.Get("X-Step-Up-Token"), action, principal); err != nil {
			writeError(w, r, err, s.logger)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) resolveBearer(r *http.Request) (identity.Principal, error) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if authorization == "" {
		return identity.Principal{}, identity.NewAppError(http.StatusUnauthorized, identity.CodeAuthMissing, "authentication required")
	}
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return identity.Principal{}, identity.NewAppError(http.StatusUnauthorized, identity.CodeAuthMissing, "authentication required")
	}
	return s.service.ResolveSession(r.Context(), parts[1])
}

func (s *Server) resolveAPIKey(w http.ResponseWriter, r *http.Request) (identity.Principal, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return identity.Principal{}, identity.NewAppError(http.StatusRequestEntityTooLarge, identity.CodeValidation, "request body too large")
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	auth := identity.APIKeyAuth{
		KeyID: r.Header.Get("X-Nexora-Key-Id"), Timestamp: r.Header.Get("X-Nexora-Timestamp"),
		Nonce: r.Header.Get("X-Nexora-Nonce"), Signature: r.Header.Get("X-Nexora-Signature"),
		Method: r.Method, Path: r.URL.EscapedPath(), Body: body, IP: s.clientIP(r),
	}
	return s.service.AuthenticateAPIKey(r.Context(), auth, s.meta(r))
}

// PrincipalFromContext extracts an authenticated principal.
func PrincipalFromContext(ctx context.Context) (identity.Principal, bool) {
	principal, ok := ctx.Value(principalKey).(identity.Principal)
	return principal, ok
}

func (s *Server) meta(r *http.Request) identity.RequestMeta {
	return identity.RequestMeta{
		IP: s.clientIP(r), UserAgent: r.UserAgent(), RequestID: httpx.RequestIDFromContext(r.Context()),
		DeviceFingerprint: firstNonEmpty(r.Header.Get("X-Device-Fingerprint"), ""),
	}
}

func (s *Server) clientIP(r *http.Request) netip.Addr {
	if s.trustProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			for _, part := range strings.Split(forwarded, ",") {
				if addr, err := netip.ParseAddr(strings.TrimSpace(part)); err == nil {
					return addr.Unmap()
				}
			}
		}
	}
	host := r.RemoteAddr
	if parsed, err := netip.ParseAddrPort(host); err == nil {
		return parsed.Addr().Unmap()
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Unmap()
	}
	return netip.Addr{}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload != nil {
		_ = json.NewEncoder(w).Encode(payload)
	}
}

func writeError(w http.ResponseWriter, r *http.Request, err error, logger *slog.Logger) {
	appErr, ok := identity.IsAppError(err)
	if !ok {
		logger.ErrorContext(r.Context(), "identity request failed", "request_id", httpx.RequestIDFromContext(r.Context()), "error", err)
		appErr = identity.NewAppError(http.StatusInternalServerError, identity.CodeInternal, "internal server error")
	}
	if appErr.Status >= 500 {
		logger.ErrorContext(r.Context(), "identity request failed", "request_id", httpx.RequestIDFromContext(r.Context()), "code", appErr.Code, "error", appErr.Cause)
	}
	writeAppError(w, r, appErr)
}

func writeAppError(w http.ResponseWriter, r *http.Request, appErr *identity.AppError) {
	if appErr.RetryAfter > 0 {
		seconds := int(appErr.RetryAfter / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
	}
	writeJSON(w, appErr.Status, map[string]any{
		"error": map[string]any{
			"code": appErr.Code, "message": appErr.Message,
			"retryAfterSeconds": int(appErr.RetryAfter / time.Second),
		},
		"requestId": httpx.RequestIDFromContext(r.Context()),
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "invalid JSON request body")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return identity.NewAppError(http.StatusBadRequest, identity.CodeValidation, "invalid JSON request body")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type ipLimiter struct {
	mu      sync.Mutex
	limit   rate.Limit
	burst   int
	clients map[string]*limiterEntry
}

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newIPLimiter(limit rate.Limit, burst int) *ipLimiter {
	return &ipLimiter{limit: limit, burst: burst, clients: map[string]*limiterEntry{}}
}

func (l *ipLimiter) Allow(key string) bool {
	if key == "" || key == "invalid IP" {
		key = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.clients) >= 10000 {
		for candidate, entry := range l.clients {
			if now.Sub(entry.lastSeen) > 10*time.Minute {
				delete(l.clients, candidate)
			}
		}
	}
	if len(l.clients) >= 10000 {
		var oldestKey string
		var oldest time.Time
		for candidate, entry := range l.clients {
			if oldestKey == "" || entry.lastSeen.Before(oldest) {
				oldestKey, oldest = candidate, entry.lastSeen
			}
		}
		delete(l.clients, oldestKey)
	}
	entry, ok := l.clients[key]
	if !ok {
		entry = &limiterEntry{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.clients[key] = entry
	}
	entry.lastSeen = now
	return entry.limiter.Allow()
}
