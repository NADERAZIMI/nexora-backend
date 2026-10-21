package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Token validation errors are intentionally generic for callers.
var (
	// ErrTokenInvalid indicates a malformed, unsigned, or unverifiable token.
	ErrTokenInvalid = errors.New("invalid token")
	// ErrTokenExpired indicates a token outside its validity window.
	ErrTokenExpired = errors.New("token expired")
)

// TokenManager issues and validates Ed25519-signed JWTs. Issuer, audience,
// algorithm, type, and expiry are always validated; an unsigned or
// algorithm-confused token is rejected.
type TokenManager struct {
	private   ed25519.PrivateKey
	public    ed25519.PublicKey
	keyID     string
	issuer    string
	audience  string
	accessTTL time.Duration
	stepUpTTL time.Duration
	now       func() time.Time
}

// Claims is the access-token claim set.
type Claims struct {
	jwt.RegisteredClaims
	SessionID string   `json:"sid"`
	Roles     []string `json:"roles"`
	MFA       bool     `json:"mfa"`
	Type      string   `json:"typ"`
	Action    string   `json:"action,omitempty"`
}

// NewTokenManager validates key material and TTL policy.
func NewTokenManager(private ed25519.PrivateKey, issuer, audience string, accessTTL, stepUpTTL time.Duration) (*TokenManager, error) {
	if len(private) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid Ed25519 private key")
	}
	if issuer == "" || audience == "" {
		return nil, errors.New("jwt issuer and audience are required")
	}
	if accessTTL < time.Minute || accessTTL > time.Hour {
		return nil, errors.New("access token TTL must be between 1 minute and 1 hour")
	}
	if stepUpTTL < 10*time.Second || stepUpTTL > 5*time.Minute {
		return nil, errors.New("step-up token TTL must be between 10 seconds and 5 minutes")
	}
	public := private.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(public)
	return &TokenManager{
		private: private, public: public,
		keyID:  base64.RawURLEncoding.EncodeToString(sum[:8]),
		issuer: issuer, audience: audience,
		accessTTL: accessTTL, stepUpTTL: stepUpTTL, now: time.Now,
	}, nil
}

// SetClock overrides the clock for deterministic tests.
func (m *TokenManager) SetClock(now func() time.Time) { m.now = now }

// AccessTTL returns the configured access token lifetime.
func (m *TokenManager) AccessTTL() time.Duration { return m.accessTTL }

// IssueAccess creates a short-lived access token bound to a live session.
func (m *TokenManager) IssueAccess(userID, sessionID uuid.UUID, roles []Role, mfa bool) (string, time.Time, error) {
	now := m.now().UTC()
	expires := now.Add(m.accessTTL)
	roleNames := make([]string, 0, len(roles))
	for _, role := range roles {
		if !ValidRole(role) {
			return "", time.Time{}, errors.New("invalid role in token")
		}
		roleNames = append(roleNames, string(role))
	}
	jti, err := RandomToken(16)
	if err != nil {
		return "", time.Time{}, err
	}
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: m.issuer, Subject: userID.String(),
			Audience:  jwt.ClaimStrings{m.audience},
			ExpiresAt: jwt.NewNumericDate(expires),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
			ID:        jti,
		},
		SessionID: sessionID.String(), Roles: roleNames, MFA: mfa, Type: "access",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = m.keyID
	signed, err := token.SignedString(m.private)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expires, nil
}

// IssueStepUp creates a single-action, short-lived privilege elevation token.
func (m *TokenManager) IssueStepUp(userID, sessionID uuid.UUID, action string) (string, time.Time, error) {
	if action == "" {
		return "", time.Time{}, errors.New("step-up action is required")
	}
	now := m.now().UTC()
	expires := now.Add(m.stepUpTTL)
	jti, err := RandomToken(16)
	if err != nil {
		return "", time.Time{}, err
	}
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: m.issuer, Subject: userID.String(),
			Audience:  jwt.ClaimStrings{m.audience},
			ExpiresAt: jwt.NewNumericDate(expires),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
			ID:        jti,
		},
		SessionID: sessionID.String(), Type: "step_up", Action: action, MFA: true,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = m.keyID
	signed, err := token.SignedString(m.private)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expires, nil
}

// ParseAccess validates signature, issuer, audience, algorithm, type, and
// expiry, then returns the access claims.
func (m *TokenManager) ParseAccess(raw string) (*Claims, error) {
	claims, err := m.parse(raw)
	if err != nil {
		return nil, err
	}
	if claims.Type != "access" {
		return nil, ErrTokenInvalid
	}
	if claims.SessionID == "" {
		return nil, ErrTokenInvalid
	}
	if _, err := uuid.Parse(claims.Subject); err != nil {
		return nil, ErrTokenInvalid
	}
	for _, role := range claims.Roles {
		if !ValidRole(Role(role)) {
			return nil, ErrTokenInvalid
		}
	}
	return claims, nil
}

// ParseStepUp validates a step-up token and binds it to the expected action,
// user, and session.
func (m *TokenManager) ParseStepUp(raw, action string, userID, sessionID uuid.UUID) (*Claims, error) {
	claims, err := m.parse(raw)
	if err != nil {
		return nil, err
	}
	if claims.Type != "step_up" || claims.Action != action || !claims.MFA {
		return nil, ErrTokenInvalid
	}
	if claims.Subject != userID.String() || claims.SessionID != sessionID.String() {
		return nil, ErrTokenInvalid
	}
	return claims, nil
}

func (m *TokenManager) parse(raw string) (*Claims, error) {
	if raw == "" {
		return nil, ErrTokenInvalid
	}
	claims := &Claims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(m.issuer),
		jwt.WithAudience(m.audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(5*time.Second),
		jwt.WithTimeFunc(m.now),
	)
	token, err := parser.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodEdDSA {
			return nil, ErrTokenInvalid
		}
		return m.public, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrTokenInvalid
	}
	if !token.Valid || claims.Issuer != m.issuer {
		return nil, ErrTokenInvalid
	}
	return claims, nil
}

// KeyID returns the stable JWKS key identifier.
func (m *TokenManager) KeyID() string { return m.keyID }

// JWKS returns the public JWK document used by downstream Go and Rust services.
func (m *TokenManager) JWKS() map[string]any {
	return map[string]any{
		"keys": []map[string]any{{
			"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA",
			"kid": m.keyID,
			"x":   base64.RawURLEncoding.EncodeToString(m.public),
		}},
	}
}

// PublicKeyPEM returns the public key for out-of-band distribution.
func (m *TokenManager) PublicKeyPEM() ([]byte, error) {
	return MarshalEd25519PublicKeyPEM(m.public)
}

// NewAccessClaimsForTest is intentionally not exported outside the package.
func (m *TokenManager) String() string {
	return fmt.Sprintf("TokenManager{kid=%s, issuer=%s}", m.keyID, m.issuer)
}
