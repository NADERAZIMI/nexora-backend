// Package grpcapi exposes the internal reference-data snapshot contract.
package grpcapi

import (
	"context"
	"log/slog"
	"net"

	referencev1 "github.com/nexora-exchange/nexora/gen/go/nexora/reference/v1"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/snapshot"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// VersionSource loads published immutable versions.
type VersionSource interface {
	GetVersion(ctx context.Context, version int64) (reference.Version, error)
}

// Server implements referencev1.ReferenceDataServiceServer.
type Server struct {
	referencev1.UnimplementedReferenceDataServiceServer
	versions  VersionSource
	snapshots *snapshot.Cache
	logger    *slog.Logger
}

// New constructs the internal gRPC server.
func New(versions VersionSource, snapshots *snapshot.Cache, logger *slog.Logger) *Server {
	return &Server{versions: versions, snapshots: snapshots, logger: logger}
}

// Register attaches the server to a gRPC registrar.
func Register(registrar grpc.ServiceRegistrar, server *Server) {
	referencev1.RegisterReferenceDataServiceServer(registrar, server)
}

// GetActiveSnapshot returns one complete, hash-verifiable active snapshot.
func (s *Server) GetActiveSnapshot(ctx context.Context, req *referencev1.GetActiveSnapshotRequest) (*referencev1.GetActiveSnapshotResponse, error) {
	shardID := req.GetShardId()
	if shardID == "" {
		shardID = reference.DefaultShard
	}
	value, err := s.snapshots.Reload(ctx, shardID)
	if err != nil {
		return nil, grpcError(err, s.logger)
	}
	return &referencev1.GetActiveSnapshotResponse{
		Version: value.Version, ContentHash: value.ContentHash, SchemaVersion: value.SchemaVersion,
		Document: value.Canonical, EffectiveAtUnix: value.EffectiveAt.Unix(), ShardId: value.ShardID,
		Rollback: value.Rollback, RequestId: req.GetRequestId(),
	}, nil
}

// GetVersionSnapshot returns one published version for recovery or rollback.
func (s *Server) GetVersionSnapshot(ctx context.Context, req *referencev1.GetVersionSnapshotRequest) (*referencev1.GetVersionSnapshotResponse, error) {
	if req.GetVersion() <= 0 {
		return nil, status.Error(codes.InvalidArgument, reference.CodeInvalidInput)
	}
	shardID := req.GetShardId()
	if shardID == "" {
		shardID = reference.DefaultShard
	}
	published, err := s.versions.GetVersion(ctx, req.GetVersion())
	if err != nil {
		return nil, grpcError(err, s.logger)
	}
	value, err := snapshot.Build(reference.Active{ShardID: shardID, Version: published.Version}, published)
	if err != nil {
		s.logger.ErrorContext(ctx, "reference-data version failed integrity verification", "version", published.Version, "error", err)
		return nil, status.Error(codes.Internal, reference.CodeInternal)
	}
	return &referencev1.GetVersionSnapshotResponse{
		Version: value.Version, ContentHash: value.ContentHash, SchemaVersion: value.SchemaVersion,
		Document: value.Canonical, EffectiveAtUnix: value.EffectiveAt.Unix(), ShardId: shardID,
		Rollback: value.Rollback, RequestId: req.GetRequestId(),
	}, nil
}

func grpcError(err error, logger *slog.Logger) error {
	appErr := reference.AsError(err)
	switch appErr.Status {
	case 400:
		return status.Error(codes.InvalidArgument, appErr.Code)
	case 401:
		return status.Error(codes.Unauthenticated, appErr.Code)
	case 403:
		return status.Error(codes.PermissionDenied, appErr.Code)
	case 404:
		return status.Error(codes.NotFound, appErr.Code)
	case 409:
		return status.Error(codes.Aborted, appErr.Code)
	case 503:
		return status.Error(codes.Unavailable, appErr.Code)
	default:
		logger.Error("reference-data grpc request failed", "code", appErr.Code, "error", err)
		return status.Error(codes.Internal, appErr.Code)
	}
}

// Listen opens a TCP listener for the gRPC server.
func Listen(ctx context.Context, addr string) (net.Listener, error) {
	var config net.ListenConfig
	return config.Listen(ctx, "tcp", addr)
}
