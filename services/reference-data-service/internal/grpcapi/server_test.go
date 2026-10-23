package grpcapi

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	referencev1 "github.com/nexora-exchange/nexora/gen/go/nexora/reference/v1"
	"github.com/nexora-exchange/nexora/libs/go/refdata"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/seed"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/snapshot"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type grpcSource struct {
	active  reference.Active
	version reference.Version
}

func (s *grpcSource) Active(context.Context, string) (reference.Active, error) {
	return s.active, nil
}

func (s *grpcSource) GetVersion(context.Context, int64) (reference.Version, error) {
	return s.version, nil
}

func (s *grpcSource) ActiveShards(context.Context) ([]string, error) {
	return []string{s.active.ShardID}, nil
}

func TestGRPCReferenceSnapshotContract(t *testing.T) {
	document := seed.Document()
	canonical, err := document.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	source := &grpcSource{
		active: reference.Active{ShardID: "shard-a", Version: 7, ActivatedAt: time.Now().UTC()},
		version: reference.Version{
			Version: 7, SchemaVersion: refdata.SchemaVersion, ContentHash: refdata.ContentHash(canonical),
			Document: canonical, Parsed: document, EffectiveAt: time.Now().UTC(),
			Canonicalization: "sha256/canonical-json-v1",
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cache := snapshot.New(source, logger, nil, time.Hour, nil)
	server := grpc.NewServer()
	Register(server, New(source, cache, logger))
	listener := bufconn.Listen(1 << 20)
	go func() {
		if err := server.Serve(listener); err != nil {
			t.Logf("bufconn server stopped: %v", err)
		}
	}()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := referencev1.NewReferenceDataServiceClient(connection)

	active, err := client.GetActiveSnapshot(context.Background(), &referencev1.GetActiveSnapshotRequest{
		ShardId: "shard-a", RequestId: "grpc-active",
	})
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if active.GetVersion() != 7 || active.GetShardId() != "shard-a" || active.GetRequestId() != "grpc-active" {
		t.Fatalf("unexpected active response: %+v", active)
	}
	if active.GetContentHash() != refdata.ContentHash(canonical) || string(active.GetDocument()) != string(canonical) {
		t.Fatal("active snapshot bytes do not match the published canonical document")
	}
	version, err := client.GetVersionSnapshot(context.Background(), &referencev1.GetVersionSnapshotRequest{
		ShardId: "shard-a", Version: 7,
	})
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	if version.GetVersion() != 7 || string(version.GetDocument()) != string(canonical) {
		t.Fatalf("unexpected version response: version=%d bytes=%d", version.GetVersion(), len(version.GetDocument()))
	}
	if _, err := client.GetVersionSnapshot(context.Background(), &referencev1.GetVersionSnapshotRequest{Version: 0}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("zero version code = %v, want InvalidArgument", status.Code(err))
	}
}
