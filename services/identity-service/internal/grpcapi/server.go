// Package grpcapi exposes the internal mTLS identity contract. It mirrors REST
// authorization and never exposes secrets in errors or logs.
//
//nolint:revive
package grpcapi

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/google/uuid"
	identityv1 "github.com/nexora-exchange/nexora/gen/go/nexora/identity/v1"
	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Server implements identityv1.IdentityServiceServer.
type Server struct {
	identityv1.UnimplementedIdentityServiceServer
	service *identity.Service
	logger  *slog.Logger
}

// New constructs the internal gRPC identity server.
func New(service *identity.Service, logger *slog.Logger) *Server {
	return &Server{service: service, logger: logger}
}

// Register attaches the identity service to a gRPC server.
func Register(registrar grpc.ServiceRegistrar, server *Server) {
	identityv1.RegisterIdentityServiceServer(registrar, server)
}

func (s *Server) Register(ctx context.Context, req *identityv1.RegisterRequest) (*identityv1.RegisterResponse, error) {
	result, err := s.service.Register(ctx, identity.RegisterInput{
		Email: req.GetEmail(), Phone: req.GetPhone(), Password: req.GetPassword(),
		CountryCode: req.GetCountryCode(), DeviceFingerprint: req.GetDeviceFingerprint(),
	}, grpcMeta(ctx, req.GetRequestId()))
	if err != nil {
		return nil, grpcError(err, s.logger)
	}
	return &identityv1.RegisterResponse{VerificationRequired: result.VerificationRequired, RequestId: req.GetRequestId()}, nil
}

func (s *Server) Login(ctx context.Context, req *identityv1.LoginRequest) (*identityv1.LoginResponse, error) {
	result, err := s.service.Login(ctx, identity.LoginInput{
		Email: req.GetEmail(), Password: req.GetPassword(), TOTPCode: req.GetTotpCode(),
		RecoveryCode: req.GetRecoveryCode(), DeviceFingerprint: req.GetDeviceFingerprint(),
	}, grpcMeta(ctx, req.GetRequestId()))
	if err != nil {
		return nil, grpcError(err, s.logger)
	}
	response := &identityv1.LoginResponse{
		MfaRequired: result.MFARequired, MfaToken: result.MFAToken, Methods: result.Methods,
		RequestId: req.GetRequestId(),
	}
	if result.Tokens != nil {
		response.Tokens = tokenPair(result.Tokens)
	}
	return response, nil
}

func (s *Server) Refresh(ctx context.Context, req *identityv1.RefreshRequest) (*identityv1.RefreshResponse, error) {
	tokens, err := s.service.Refresh(ctx, req.GetRefreshToken(), grpcMeta(ctx, req.GetRequestId()))
	if err != nil {
		return nil, grpcError(err, s.logger)
	}
	return &identityv1.RefreshResponse{Tokens: tokenPair(tokens), RequestId: req.GetRequestId()}, nil
}

func (s *Server) Logout(ctx context.Context, req *identityv1.LogoutRequest) (*identityv1.LogoutResponse, error) {
	principal, err := s.service.ResolveSession(ctx, req.GetAccessToken())
	if err != nil {
		return nil, grpcError(err, s.logger)
	}
	if err := s.service.Logout(ctx, principal, grpcMeta(ctx, req.GetRequestId())); err != nil {
		return nil, grpcError(err, s.logger)
	}
	return &identityv1.LogoutResponse{Success: true, RequestId: req.GetRequestId()}, nil
}

func (s *Server) CreateApiKey(ctx context.Context, req *identityv1.CreateApiKeyRequest) (*identityv1.CreateApiKeyResponse, error) {
	principal, err := s.service.ResolveSession(ctx, req.GetAccessToken())
	if err != nil {
		return nil, grpcError(err, s.logger)
	}
	if err := s.service.ResolveStepUp(stepUpToken(ctx), "api_key:create", principal); err != nil {
		return nil, grpcError(err, s.logger)
	}
	created, err := s.service.CreateAPIKey(ctx, principal, identity.CreateAPIKeyInput{
		Label: req.GetLabel(), Scopes: req.GetScopes(), IPAllowlist: req.GetIpAllowlist(),
		Algorithm: req.GetAlgorithm(), PublicKey: req.GetPublicKey(), ExpiresInDays: int(req.GetExpiresInDays()),
	}, grpcMeta(ctx, req.GetRequestId()))
	if err != nil {
		return nil, grpcError(err, s.logger)
	}
	response := &identityv1.CreateApiKeyResponse{
		KeyId: created.ID.String(), KeyPrefix: created.KeyPrefix, Secret: created.Secret,
		Algorithm: created.Algorithm, Scopes: created.Scopes, IpAllowlist: created.IPAllowlist,
		RequestId: req.GetRequestId(),
	}
	if created.ExpiresAt != nil {
		response.ExpiresAtUnix = created.ExpiresAt.Unix()
	}
	return response, nil
}

func (s *Server) RevokeApiKey(ctx context.Context, req *identityv1.RevokeApiKeyRequest) (*identityv1.RevokeApiKeyResponse, error) {
	principal, err := s.service.ResolveSession(ctx, req.GetAccessToken())
	if err != nil {
		return nil, grpcError(err, s.logger)
	}
	if err := s.service.ResolveStepUp(stepUpToken(ctx), "api_key:revoke", principal); err != nil {
		return nil, grpcError(err, s.logger)
	}
	keyID, parseErr := uuid.Parse(req.GetKeyId())
	if parseErr != nil {
		return nil, status.Error(codes.InvalidArgument, identity.CodeValidation)
	}
	if err := s.service.RevokeAPIKey(ctx, principal, keyID, grpcMeta(ctx, req.GetRequestId())); err != nil {
		return nil, grpcError(err, s.logger)
	}
	return &identityv1.RevokeApiKeyResponse{Success: true, RequestId: req.GetRequestId()}, nil
}

func (s *Server) GetPermissions(ctx context.Context, req *identityv1.GetPermissionsRequest) (*identityv1.GetPermissionsResponse, error) {
	principal, err := s.service.ResolveSession(ctx, req.GetAccessToken())
	if err != nil {
		return nil, grpcError(err, s.logger)
	}
	view := s.service.Permissions(principal)
	roles := make([]string, 0, len(view.Roles))
	for _, role := range view.Roles {
		roles = append(roles, string(role))
	}
	return &identityv1.GetPermissionsResponse{
		UserId: view.UserID.String(), Roles: roles, Permissions: view.Permissions,
		Scopes: view.Scopes, RequestId: req.GetRequestId(),
	}, nil
}

func (s *Server) SubmitKyc(ctx context.Context, req *identityv1.SubmitKycRequest) (*identityv1.SubmitKycResponse, error) {
	principal, err := s.service.ResolveSession(ctx, req.GetAccessToken())
	if err != nil {
		return nil, grpcError(err, s.logger)
	}
	level := req.GetLevel()
	if level < 0 || level > 5 {
		return nil, status.Error(codes.InvalidArgument, "invalid KYC level")
	}
	kyc, err := s.service.SubmitKYC(ctx, principal, req.GetProviderRef(), int16(level), grpcMeta(ctx, req.GetRequestId()))
	if err != nil {
		return nil, grpcError(err, s.logger)
	}
	return &identityv1.SubmitKycResponse{CaseId: kyc.ID.String(), Status: string(kyc.Status), RequestId: req.GetRequestId()}, nil
}

func tokenPair(tokens *identity.TokenPair) *identityv1.TokenPair {
	if tokens == nil {
		return nil
	}
	return &identityv1.TokenPair{
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken,
		AccessExpiresAtUnix: tokens.AccessExpiresAt.Unix(), RefreshExpiresAtUnix: tokens.RefreshExpiresAt.Unix(),
		SessionId: tokens.SessionID.String(), Mfa: tokens.MFA,
	}
}

func grpcMeta(ctx context.Context, requestID string) identity.RequestMeta {
	meta := identity.RequestMeta{RequestID: requestID}
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		if len(incoming.Get("x-request-id")) > 0 && meta.RequestID == "" {
			meta.RequestID = incoming.Get("x-request-id")[0]
		}
		if len(incoming.Get("user-agent")) > 0 {
			meta.UserAgent = incoming.Get("user-agent")[0]
		}
		if len(incoming.Get("x-device-fingerprint")) > 0 {
			meta.DeviceFingerprint = incoming.Get("x-device-fingerprint")[0]
		}
	}
	if peerInfo, ok := peer.FromContext(ctx); ok && peerInfo.Addr != nil {
		if host, _, err := net.SplitHostPort(peerInfo.Addr.String()); err == nil {
			if addr, err := netip.ParseAddr(host); err == nil {
				meta.IP = addr.Unmap()
			}
		} else if addr, err := netip.ParseAddr(peerInfo.Addr.String()); err == nil {
			meta.IP = addr.Unmap()
		}
	}
	return meta
}

func stepUpToken(ctx context.Context) string {
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		if values := incoming.Get("x-step-up-token"); len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func grpcError(err error, logger *slog.Logger) error {
	appErr, ok := identity.IsAppError(err)
	if !ok {
		logger.Error("identity grpc request failed", "error", err)
		return status.Error(codes.Internal, identity.CodeInternal)
	}
	if appErr.Status >= 500 {
		logger.Error("identity grpc request failed", "code", appErr.Code, "error", appErr.Cause)
	}
	code := codes.Internal
	switch appErr.Status {
	case 400:
		code = codes.InvalidArgument
	case 401:
		code = codes.Unauthenticated
	case 403:
		code = codes.PermissionDenied
	case 404:
		code = codes.NotFound
	case 409:
		code = codes.Aborted
	case 429:
		code = codes.ResourceExhausted
	case 503:
		code = codes.Unavailable
	}
	if appErr.RetryAfter > 0 {
		return status.Errorf(code, "%s retry_after_ms=%d", appErr.Code, int(appErr.RetryAfter/time.Millisecond))
	}
	return status.Error(code, appErr.Code)
}
