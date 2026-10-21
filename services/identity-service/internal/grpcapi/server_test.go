package grpcapi

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	identityv1 "github.com/nexora-exchange/nexora/gen/go/nexora/identity/v1"
	"github.com/nexora-exchange/nexora/services/identity-service/internal/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestGRPCIdentityLifecycle(t *testing.T) {
	pepper := []byte("grpc-test-pepper-0123456789abcdef")
	hasher, err := identity.NewPasswordHasher(identity.Argon2Params{
		MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	}, pepper)
	if err != nil {
		t.Fatalf("password hasher: %v", err)
	}
	piiCipher, err := identity.NewAESGCMCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("PII cipher: %v", err)
	}
	secretCipher, err := identity.NewAESGCMCipher([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatalf("secret cipher: %v", err)
	}
	_, privateKey, err := identity.GenerateEd25519Key()
	if err != nil {
		t.Fatalf("JWT key: %v", err)
	}
	tokens, err := identity.NewTokenManager(privateKey, "nexora-identity-grpc-test", "nexora-api", 15*time.Minute, time.Minute)
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}
	notifier := &identity.CaptureNotifier{}
	cfg := identity.DefaultServiceConfig()
	cfg.ExposeDevTokens = true
	service, err := identity.NewService(
		identity.NewMemoryStore(), tokens, hasher, piiCipher, secretCipher, nil, notifier,
		pepper, []byte("grpc-ip-pepper-0123456789abcdef0123"),
		cfg, identity.DefaultLockoutPolicy(), "NEXORA",
	)
	if err != nil {
		t.Fatalf("identity service: %v", err)
	}

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	Register(server, New(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(server.Stop)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	connection, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("gRPC client: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := identityv1.NewIdentityServiceClient(connection)

	registered, err := client.Register(ctx, &identityv1.RegisterRequest{
		Email: "grpc@example.test", Password: "Correct-Horse-Battery-9!", CountryCode: "GB", RequestId: "grpc-register",
	})
	if err != nil {
		t.Fatalf("gRPC register: %v", err)
	}
	if !registered.GetVerificationRequired() || registered.GetRequestId() != "grpc-register" {
		t.Fatalf("unexpected register response: %+v", registered)
	}
	verification, ok := notifier.Find("email_verification")
	if !ok {
		t.Fatal("verification notification was not queued")
	}
	meta := identity.RequestMeta{IP: netip.MustParseAddr("192.0.2.10"), UserAgent: "grpc-test", RequestID: "grpc-verify"}
	if err := service.VerifyContact(ctx, verification.Token, "email", meta); err != nil {
		t.Fatalf("verify contact: %v", err)
	}

	loggedIn, err := client.Login(ctx, &identityv1.LoginRequest{
		Email: "grpc@example.test", Password: "Correct-Horse-Battery-9!", RequestId: "grpc-login",
	})
	if err != nil {
		t.Fatalf("gRPC login: %v", err)
	}
	if loggedIn.GetMfaRequired() || loggedIn.GetTokens().GetAccessToken() == "" || loggedIn.GetTokens().GetRefreshToken() == "" {
		t.Fatalf("unexpected login response: %+v", loggedIn)
	}
	accessToken := loggedIn.GetTokens().GetAccessToken()

	permissions, err := client.GetPermissions(ctx, &identityv1.GetPermissionsRequest{AccessToken: accessToken})
	if err != nil {
		t.Fatalf("gRPC permissions: %v", err)
	}
	if permissions.GetUserId() == "" || len(permissions.GetPermissions()) == 0 {
		t.Fatalf("unexpected permissions response: %+v", permissions)
	}

	refreshed, err := client.Refresh(ctx, &identityv1.RefreshRequest{RefreshToken: loggedIn.GetTokens().GetRefreshToken()})
	if err != nil {
		t.Fatalf("gRPC refresh: %v", err)
	}
	if refreshed.GetTokens().GetAccessToken() == accessToken {
		t.Fatal("gRPC refresh did not rotate the access token")
	}
	loggedOut, err := client.Logout(ctx, &identityv1.LogoutRequest{AccessToken: refreshed.GetTokens().GetAccessToken()})
	if err != nil || !loggedOut.GetSuccess() {
		t.Fatalf("gRPC logout: %+v %v", loggedOut, err)
	}
	if _, err := service.ResolveSession(ctx, refreshed.GetTokens().GetAccessToken()); err == nil {
		t.Fatal("gRPC logout did not revoke the session")
	}
}
