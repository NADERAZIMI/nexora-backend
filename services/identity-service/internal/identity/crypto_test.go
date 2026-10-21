package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func testHasher(t *testing.T) *PasswordHasher {
	t.Helper()
	hasher, err := NewPasswordHasher(Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}, []byte("test-password-pepper-0123456789abcdef"))
	if err != nil {
		t.Fatalf("new hasher: %v", err)
	}
	return hasher
}

func TestArgon2idHashVerifyAndRehash(t *testing.T) {
	hasher := testHasher(t)
	first, err := hasher.Hash("Correct-Horse-Battery-9!")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	second, err := hasher.Hash("Correct-Horse-Battery-9!")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if first == second {
		t.Fatal("password hashes must use independent salts")
	}
	if !strings.HasPrefix(first, "$argon2id$v=19$") {
		t.Fatalf("unexpected PHC encoding: %s", first)
	}
	if !hasher.Verify(first, "Correct-Horse-Battery-9!") {
		t.Fatal("correct password did not verify")
	}
	if hasher.Verify(first, "Correct-Horse-Battery-8!") {
		t.Fatal("incorrect password verified")
	}
	if hasher.Verify("not-a-phc-hash", "Correct-Horse-Battery-9!") {
		t.Fatal("malformed hash verified")
	}
	if hasher.NeedsRehash(first) {
		t.Fatal("current hash incorrectly flagged for rehash")
	}
	weaker := strings.Replace(first, "m=8192", "m=4096", 1)
	if !hasher.NeedsRehash(weaker) {
		t.Fatal("weaker parameters were not detected")
	}
}

func TestPasswordPolicy(t *testing.T) {
	cases := []struct {
		name     string
		password string
		email    string
		wantErr  bool
	}{
		{"valid", "Long-enough-passphrase-42!", "user@example.com", false},
		{"short", "Short-42!", "user@example.com", true},
		{"letters only", "abcdefghijklmnopqrstuvwx", "user@example.com", true},
		{"common", "passwordpassword", "user@example.com", true},
		{"contains email", "user@example.com-extra1", "user@example.com", true},
		{"whitespace", "            ", "user@example.com", true},
		{"too long", strings.Repeat("a1", 65), "user@example.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePasswordPolicy(tc.password, tc.email)
			if tc.wantErr && err == nil {
				t.Fatal("expected policy rejection")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected policy rejection: %v", err)
			}
		})
	}
}

func TestTOTPRFC6238Vector(t *testing.T) {
	// RFC 6238 SHA-1 public test secret, represented as base32.
	// nosemgrep: generic.secrets.security.detected-generic-secret.detected-generic-secret
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	code, err := TOTPCode(secret, 59, 8, 30*time.Second)
	if err != nil {
		t.Fatalf("totp code: %v", err)
	}
	if code != "94287082" {
		t.Fatalf("RFC 6238 vector mismatch: got %s", code)
	}
	step, ok, err := VerifyTOTP(secret, code, time.Unix(59, 0).UTC(), 1, 8, 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("valid TOTP rejected: ok=%v err=%v", ok, err)
	}
	if step != 1 {
		t.Fatalf("unexpected TOTP step: %d", step)
	}
	if _, ok, _ := VerifyTOTP(secret, "00000000", time.Unix(59, 0).UTC(), 0, 8, 30*time.Second); ok {
		t.Fatal("invalid TOTP accepted")
	}
}

func TestAESGCMCipherIsAuthenticated(t *testing.T) {
	cipher, err := NewAESGCMCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	plaintext := []byte("sensitive identity payload")
	ciphertext, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if strings.Contains(string(ciphertext), string(plaintext)) {
		t.Fatal("ciphertext contains plaintext")
	}
	again, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if string(ciphertext) == string(again) {
		t.Fatal("AEAD encryption reused a nonce")
	}
	decrypted, err := cipher.Decrypt(ciphertext)
	if err != nil || string(decrypted) != string(plaintext) {
		t.Fatalf("decrypt: %q %v", decrypted, err)
	}
	ciphertext[len(ciphertext)-1] ^= 0x01
	if _, err := cipher.Decrypt(ciphertext); err == nil {
		t.Fatal("tampered ciphertext decrypted")
	}
}

func TestJWTValidationRejectsConfusionAndWrongAudience(t *testing.T) {
	_, private, err := GenerateEd25519Key()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	manager, err := NewTokenManager(private, "issuer-a", "audience-a", 15*time.Minute, time.Minute)
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}
	userID := uuid.New()
	sessionID := uuid.New()
	raw, _, err := manager.IssueAccess(userID, sessionID, []Role{RoleCustomer}, true)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := manager.ParseAccess(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.Subject != userID.String() || claims.SessionID != sessionID.String() || !claims.MFA {
		t.Fatalf("claims mismatch: %+v", claims)
	}

	wrongAudience, err := NewTokenManager(private, "issuer-a", "audience-b", 15*time.Minute, time.Minute)
	if err != nil {
		t.Fatalf("wrong audience manager: %v", err)
	}
	if _, err := wrongAudience.ParseAccess(raw); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("wrong audience accepted: %v", err)
	}
	wrongIssuer, err := NewTokenManager(private, "issuer-b", "audience-a", 15*time.Minute, time.Minute)
	if err != nil {
		t.Fatalf("wrong issuer manager: %v", err)
	}
	if _, err := wrongIssuer.ParseAccess(raw); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("wrong issuer accepted: %v", err)
	}

	unsignedClaims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "issuer-a", Subject: userID.String(), Audience: jwt.ClaimStrings{"audience-a"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)), IssuedAt: jwt.NewNumericDate(time.Now()),
		},
		SessionID: sessionID.String(), Roles: []string{"customer"}, Type: "access",
	}
	// This is a deliberate negative test that must be rejected by ParseAccess.
	// nosemgrep: go.jwt-go.security.jwt-none-alg.jwt-go-none-algorithm
	unsigned := jwt.NewWithClaims(jwt.SigningMethodNone, unsignedClaims)
	// nosemgrep: go.jwt-go.security.jwt-none-alg.jwt-go-none-algorithm
	noneToken, err := unsigned.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}
	if _, err := manager.ParseAccess(noneToken); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("algorithm none accepted: %v", err)
	}

	// Deliberate algorithm-confusion negative test; this key is not a credential.
	// nosemgrep: go.jwt-go.security.jwt.hardcoded-jwt-key
	hmacToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, unsignedClaims).SignedString([]byte("attacker-secret"))
	if err != nil {
		t.Fatalf("sign hmac: %v", err)
	}
	if _, err := manager.ParseAccess(hmacToken); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("algorithm confusion accepted: %v", err)
	}

	if _, err := manager.ParseAccess(raw + "tampered"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("tampered token accepted: %v", err)
	}

	expiredClaims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "issuer-a", Subject: userID.String(), Audience: jwt.ClaimStrings{"audience-a"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)), IssuedAt: jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
		SessionID: sessionID.String(), Roles: []string{"customer"}, Type: "access",
	}
	expired, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, expiredClaims).SignedString(private)
	if err != nil {
		t.Fatalf("sign expired: %v", err)
	}
	if _, err := manager.ParseAccess(expired); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired token not detected: %v", err)
	}
}

func TestStepUpTokenIsBoundToActionSessionAndUser(t *testing.T) {
	_, private, err := GenerateEd25519Key()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	manager, err := NewTokenManager(private, "issuer", "audience", 15*time.Minute, time.Minute)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	userID, sessionID := uuid.New(), uuid.New()
	raw, _, err := manager.IssueStepUp(userID, sessionID, "admin.user.freeze")
	if err != nil {
		t.Fatalf("issue step-up: %v", err)
	}
	if _, err := manager.ParseStepUp(raw, "admin.user.freeze", userID, sessionID); err != nil {
		t.Fatalf("valid step-up rejected: %v", err)
	}
	if _, err := manager.ParseStepUp(raw, "admin.user.roles", userID, sessionID); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("step-up accepted for another action: %v", err)
	}
	if _, err := manager.ParseStepUp(raw, "admin.user.freeze", uuid.New(), sessionID); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("step-up accepted for another user: %v", err)
	}
	if _, err := manager.ParseStepUp(raw, "admin.user.freeze", userID, uuid.New()); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("step-up accepted for another session: %v", err)
	}
	if _, err := manager.ParseAccess(raw); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("step-up token accepted as access token: %v", err)
	}
}

func TestRBACDenyByDefault(t *testing.T) {
	customer := Principal{UserID: uuid.New(), Roles: []Role{RoleCustomer}, Kind: "session"}
	if customer.HasPermission(PermAdminUserRead) {
		t.Fatal("customer received admin permission")
	}
	if !customer.HasPermission(PermSelfRead) {
		t.Fatal("customer lacks self-read permission")
	}
	if customer.HasScope("read") {
		t.Fatal("session principal unexpectedly scope-limited")
	}
	admin := Principal{UserID: uuid.New(), Roles: []Role{RoleAdmin}, Kind: "session"}
	if !admin.HasPermission(PermAdminUserRead) || !admin.HasPermission(PermAdminAuditRead) {
		t.Fatal("admin lacks required permission")
	}
	if admin.HasScope("read") {
		t.Fatal("admin session treated as API key")
	}
	key := Principal{UserID: uuid.New(), Scopes: []string{"read"}, Kind: "api_key"}
	if !key.HasScope("read") || key.HasScope("trade") {
		t.Fatal("API key scope enforcement failed")
	}
	if key.HasPermission(PermAdminUserRead) {
		t.Fatal("API key gained RBAC permission")
	}
	anonymous := Principal{}
	if anonymous.HasPermission(PermSelfRead) || anonymous.HasScope("read") {
		t.Fatal("anonymous principal gained access")
	}
	if len(PermissionsFor([]Role{RoleAdmin})) == 0 {
		t.Fatal("admin permissions empty")
	}
}

func TestParseScopesAndPrefixesRejectsEscalation(t *testing.T) {
	scopes, err := ParseScopes([]string{"read", "read"})
	if err != nil {
		t.Fatalf("duplicate scopes should be normalized, not accepted as escalation: %v", err)
	}
	if len(scopes) != 1 || scopes[0] != "read" {
		t.Fatalf("duplicate scopes were not normalized: %#v", scopes)
	}
	if _, err := ParseScopes([]string{"admin"}); err == nil {
		t.Fatal("unknown scope accepted")
	}
	if _, err := ParsePrefixes([]string{"0.0.0.0/0"}); err == nil {
		t.Fatal("global IP allowlist accepted")
	}
	if _, err := ParsePrefixes([]string{"not-a-cidr"}); err == nil {
		t.Fatal("invalid prefix accepted")
	}
	if _, err := ParsePrefixes([]string{"192.0.2.5"}); err != nil {
		t.Fatalf("host prefix rejected: %v", err)
	}
}

func TestDecodePublicKeyValidation(t *testing.T) {
	public, _, err := GenerateEd25519Key()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(public)
	if _, err := decodePublicKey(encoded); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if _, err := decodePublicKey("not-base64"); err == nil {
		t.Fatal("invalid base64 key accepted")
	}
	if _, err := decodePublicKey(base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))); err == nil {
		t.Fatal("all-zero public key accepted")
	}
}

func TestRandomTokenEntropyAndFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 128; i++ {
		token, err := RandomToken(32)
		if err != nil {
			t.Fatalf("random: %v", err)
		}
		if len(token) != 43 {
			t.Fatalf("unexpected token length %d", len(token))
		}
		if seen[token] {
			t.Fatal("random token repeated")
		}
		seen[token] = true
	}
	if _, err := RandomBytes(8); err != nil {
		t.Fatalf("random bytes: %v", err)
	}
	if _, err := rand.Read(make([]byte, 1)); err != nil {
		t.Fatalf("system randomness unavailable: %v", err)
	}
}
