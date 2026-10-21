package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TestPublishedJWKSVerifiesAccessTokenForDownstreamServices exercises the
// contract a future Go or Rust service consumes: only the public JWKS, issuer,
// audience, algorithm, expiry, and token type are needed to verify an access
// token. No private key or identity-service code is used.
func TestPublishedJWKSVerifiesAccessTokenForDownstreamServices(t *testing.T) {
	_, private, err := GenerateEd25519Key()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	const issuer, audience = "nexora-identity-test", "nexora-api-test"
	manager, err := NewTokenManager(private, issuer, audience, 15*time.Minute, time.Minute)
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}
	userID, sessionID := uuid.New(), uuid.New()
	raw, _, err := manager.IssueAccess(userID, sessionID, []Role{RoleCustomer}, true)
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	jwks := manager.JWKS()
	keys, ok := jwks["keys"].([]map[string]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("unexpected JWKS shape: %#v", jwks)
	}
	key := keys[0]
	if key["kty"] != "OKP" || key["crv"] != "Ed25519" || key["use"] != "sig" || key["alg"] != "EdDSA" {
		t.Fatalf("unexpected JWK: %#v", key)
	}
	if _, leaked := key["d"]; leaked {
		t.Fatal("JWKS exposed private key material")
	}
	encoded, ok := key["x"].(string)
	if !ok {
		t.Fatalf("missing public key coordinate: %#v", key)
	}
	material, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(material) != ed25519.PublicKeySize {
		t.Fatalf("invalid public key coordinate: %d %v", len(material), err)
	}
	public := ed25519.PublicKey(material)

	claims := &Claims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(5*time.Second),
	)
	token, err := parser.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodEdDSA {
			return nil, errors.New("unexpected signing algorithm")
		}
		kid, _ := token.Header["kid"].(string)
		if kid == "" || kid != manager.KeyID() {
			return nil, errors.New("untrusted key identifier")
		}
		return public, nil
	})
	if err != nil || !token.Valid {
		t.Fatalf("JWKS verification failed: %v", err)
	}
	if claims.Subject != userID.String() || claims.SessionID != sessionID.String() || claims.Type != "access" || !claims.MFA {
		t.Fatalf("verified claims mismatch: %+v", claims)
	}

	// A JWK from another issuer must not verify this token.
	_, otherPrivate, err := GenerateEd25519Key()
	if err != nil {
		t.Fatalf("other key: %v", err)
	}
	otherPublic := otherPrivate.Public().(ed25519.PublicKey)
	if _, err := parser.ParseWithClaims(raw, &Claims{}, func(_ *jwt.Token) (any, error) {
		return otherPublic, nil
	}); err == nil {
		t.Fatal("token verified with an untrusted JWKS key")
	}
}

// TestJWTClaimsAreNotAuthoritative proves that authorization comes from the
// PostgreSQL-backed user record, not from role claims in the token. A token
// bearing an admin claim for an ordinary customer must not grant admin
// permissions, and a role removal in the store must take effect on the next
// request for an already-issued token.
func TestJWTClaimsAreNotAuthoritative(t *testing.T) {
	h := newTestHarness(t)
	const email = "authorization@example.com"
	user := h.registerVerified(t, email, "Correct-Horse-Battery-9!")
	login := h.login(t, email, "Correct-Horse-Battery-9!")

	// Mint a token that asks for administrator authorization while the
	// authoritative user record still only holds the customer role.
	adminClaimToken, _, err := h.service.tokens.IssueAccess(user.ID, login.Tokens.SessionID, []Role{RoleAdmin}, true)
	if err != nil {
		t.Fatalf("issue admin-claim token: %v", err)
	}
	principal, err := h.service.ResolveSession(t.Context(), adminClaimToken)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	if principal.HasRole(RoleAdmin) || principal.HasPermission(PermAdminUserFreeze) {
		t.Fatalf("JWT role claim granted authoritative permission: %+v", principal.Roles)
	}
	if !principal.HasRole(RoleCustomer) {
		t.Fatalf("authoritative customer role missing: %+v", principal.Roles)
	}

	// Role changes in the authoritative store apply to an already-issued
	// token. The token claim is deliberately left stale.
	if err := h.store.SetUserRoles(t.Context(), user.ID, []Role{RoleSupport}, user.ID, h.clock.Now(), nil); err != nil {
		t.Fatalf("update roles: %v", err)
	}
	updated, err := h.service.ResolveSession(t.Context(), adminClaimToken)
	if err != nil {
		t.Fatalf("resolve updated session: %v", err)
	}
	if updated.HasRole(RoleCustomer) || !updated.HasRole(RoleSupport) {
		t.Fatalf("store role change was not authoritative: %+v", updated.Roles)
	}
}
