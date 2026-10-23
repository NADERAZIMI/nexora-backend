// Package auth resolves authoritative authorization decisions from the
// identity service. It never trusts roles, permissions, or step-up assertions
// supplied by an HTTP caller.
package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	identityv1 "github.com/nexora-exchange/nexora/gen/go/nexora/identity/v1"
	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Authorizer performs a live identity authorization check.
type Authorizer interface {
	Authorize(ctx context.Context, accessToken, permission, stepUpAction, requestID string) (reference.Principal, error)
}

// IdentityClient calls the identity service's internal Authorize RPC.
type IdentityClient struct {
	client  identityv1.IdentityServiceClient
	conn    *grpc.ClientConn
	timeout time.Duration
}

// NewIdentityClient dials the identity service. An empty creds value is allowed
// only when the caller's configuration explicitly permits plaintext gRPC.
func NewIdentityClient(address string, creds credentials.TransportCredentials, timeout time.Duration) (*IdentityClient, error) {
	if strings.TrimSpace(address) == "" {
		return nil, errors.New("identity gRPC address is required")
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if creds == nil {
		creds = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, err
	}
	return &IdentityClient{client: identityv1.NewIdentityServiceClient(conn), conn: conn, timeout: timeout}, nil
}

// Close releases the identity connection.
func (c *IdentityClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Authorize returns the live principal only when identity grants the requested
// permission and, when supplied, the exact step-up action.
func (c *IdentityClient) Authorize(ctx context.Context, accessToken, permission, stepUpAction, requestID string) (reference.Principal, error) {
	if strings.TrimSpace(accessToken) == "" {
		return reference.Principal{}, reference.NewError(401, reference.CodeUnauthorized, "authentication is required", nil)
	}
	if strings.TrimSpace(permission) == "" {
		return reference.Principal{}, reference.Forbidden("no permission was requested")
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	response, err := c.client.Authorize(callCtx, &identityv1.AuthorizeRequest{
		AccessToken: accessToken, Permission: permission, StepUpAction: stepUpAction, RequestId: requestID,
	})
	if err != nil {
		switch status.Code(err) {
		case codes.Unauthenticated, codes.InvalidArgument:
			return reference.Principal{}, reference.NewError(401, reference.CodeUnauthorized, "authentication is invalid or expired", nil)
		case codes.PermissionDenied:
			return reference.Principal{}, reference.Forbidden("authorization denied")
		case codes.Unavailable, codes.DeadlineExceeded:
			return reference.Principal{}, reference.NewError(503, reference.CodeInternal, "identity service is unavailable", nil)
		default:
			return reference.Principal{}, reference.NewError(502, reference.CodeInternal, "identity authorization failed", nil)
		}
	}
	if !response.GetAllowed() {
		switch response.GetReason() {
		case "step_up_required":
			return reference.Principal{}, reference.NewError(403, reference.CodeStepUpRequired, "step-up authentication is required", nil)
		default:
			return reference.Principal{}, reference.Forbidden("authorization denied")
		}
	}
	userID, err := uuid.Parse(response.GetUserId())
	if err != nil || userID == uuid.Nil {
		return reference.Principal{}, reference.NewError(502, reference.CodeInternal, "identity returned an invalid principal", nil)
	}
	return reference.Principal{
		UserID: userID, Roles: response.GetRoles(), Permissions: response.GetPermissions(),
		MFA: response.GetMfa(), RequestID: requestID,
	}, nil
}
