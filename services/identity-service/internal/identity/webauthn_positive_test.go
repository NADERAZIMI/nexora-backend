package identity

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/google/uuid"

	"github.com/nexora-exchange/nexora/services/identity-service/internal/webauthntest"
)

const (
	testWebAuthnRPID   = "localhost"
	testWebAuthnOrigin = "http://localhost:8082"
)

func newSoftwareAuthenticator(t *testing.T) *webauthntest.Authenticator {
	t.Helper()
	authenticator, err := webauthntest.New(webauthntest.Options{RPID: testWebAuthnRPID, Origin: testWebAuthnOrigin})
	if err != nil {
		t.Fatalf("new software authenticator: %v", err)
	}
	return authenticator
}

func creationChallenge(t *testing.T, options *WebAuthnOptions) string {
	t.Helper()
	creation, ok := options.Options.(*protocol.CredentialCreation)
	if !ok {
		t.Fatalf("unexpected creation options type %T", options.Options)
	}
	return creation.Response.Challenge.String()
}

func assertionChallenge(t *testing.T, options *WebAuthnOptions) string {
	t.Helper()
	assertion, ok := options.Options.(*protocol.CredentialAssertion)
	if !ok {
		t.Fatalf("unexpected assertion options type %T", options.Options)
	}
	return assertion.Response.Challenge.String()
}

func registerSoftwareCredential(t *testing.T, h *testHarness, principal Principal, user *User, authenticator *webauthntest.Authenticator) {
	t.Helper()
	options, err := h.service.BeginWebAuthnRegistration(t.Context(), principal, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin webauthn registration: %v", err)
	}
	body, err := authenticator.Create(creationChallenge(t, options), user.ID[:])
	if err != nil {
		t.Fatalf("create registration response: %v", err)
	}
	if err := h.service.FinishWebAuthnRegistration(t.Context(), principal, body, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("finish webauthn registration: %v", err)
	}
}

func storedWebAuthnCredential(t *testing.T, h *testHarness, userID uuid.UUID) []Credential {
	t.Helper()
	credentials, err := h.store.GetCredentials(context.Background(), userID, CredentialWebAuthn)
	if err != nil {
		t.Fatalf("load webauthn credentials: %v", err)
	}
	return credentials
}

func storedSignCount(t *testing.T, h *testHarness, userID uuid.UUID) uint32 {
	t.Helper()
	credentials := storedWebAuthnCredential(t, h, userID)
	if len(credentials) != 1 {
		t.Fatalf("expected exactly one webauthn credential, got %d", len(credentials))
	}
	var stored struct {
		Authenticator struct {
			SignCount uint32 `json:"signCount"`
		} `json:"authenticator"`
	}
	if err := json.Unmarshal(credentials[0].Metadata, &stored); err != nil {
		t.Fatalf("decode stored webauthn credential: %v", err)
	}
	return stored.Authenticator.SignCount
}

func TestWebAuthnRegistrationAndAssertionCeremonies(t *testing.T) {
	h := newTestHarness(t)
	const email = "webauthn-positive@example.com"
	user := h.registerVerified(t, email, "Correct-Horse-Battery-9!")
	login := h.login(t, email, "Correct-Horse-Battery-9!")
	principal, err := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}

	authenticator := newSoftwareAuthenticator(t)

	// Registration produces a real P-256 credential and CBOR "none" attestation.
	options, err := h.service.BeginWebAuthnRegistration(t.Context(), principal, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin webauthn registration: %v", err)
	}
	registrationBody, err := authenticator.Create(creationChallenge(t, options), user.ID[:])
	if err != nil {
		t.Fatalf("create registration response: %v", err)
	}
	if err := h.service.FinishWebAuthnRegistration(t.Context(), principal, registrationBody, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("finish webauthn registration: %v", err)
	}

	credentials := storedWebAuthnCredential(t, h, user.ID)
	if len(credentials) != 1 {
		t.Fatalf("registration did not persist exactly one credential: %d", len(credentials))
	}
	var stored struct {
		ID                string `json:"id"`
		PublicKey         []byte `json:"publicKey"`
		AttestationType   string `json:"attestationType"`
		AttestationFormat string `json:"attestationFormat"`
		Authenticator     struct {
			SignCount uint32 `json:"signCount"`
		} `json:"authenticator"`
	}
	if err := json.Unmarshal(credentials[0].Metadata, &stored); err != nil {
		t.Fatalf("decode stored credential: %v", err)
	}
	storedID, err := base64.StdEncoding.DecodeString(stored.ID)
	if err != nil {
		storedID, err = base64.RawURLEncoding.DecodeString(stored.ID)
	}
	if err != nil || !bytes.Equal(storedID, authenticator.CredentialID()) {
		t.Fatalf("stored credential id does not match authenticator: %q", stored.ID)
	}
	if len(stored.PublicKey) == 0 {
		t.Fatal("stored credential is missing the COSE public key")
	}
	if stored.AttestationFormat != "none" {
		t.Fatalf("unexpected attestation format %q", stored.AttestationFormat)
	}
	if stored.Authenticator.SignCount != 0 {
		t.Fatalf("registration should not advance the signature counter: %d", stored.Authenticator.SignCount)
	}
	enrolled, err := h.store.GetUserByID(context.Background(), user.ID)
	if err != nil || enrolled.MFAEnrolledAt == nil {
		t.Fatalf("registration did not enroll MFA: %+v %v", enrolled, err)
	}

	// Passwordless assertion creates a session and persists the new counter.
	assertionOptions, err := h.service.BeginWebAuthnLogin(t.Context(), email, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin webauthn login: %v", err)
	}
	firstChallenge := assertionChallenge(t, assertionOptions)
	assertionBody, err := authenticator.Assert(firstChallenge, user.ID[:])
	if err != nil {
		t.Fatalf("assert: %v", err)
	}
	// Independently verify the ES256 signature before the service sees it. This
	// proves the test fixture produces real P-256 signatures rather than a
	// mocked success.
	publicKey, err := authenticator.PublicKey()
	if err != nil {
		t.Fatalf("authenticator public key: %v", err)
	}
	if !verifyES256Assertion(t, publicKey, assertionBody) {
		t.Fatal("software authenticator assertion signature did not verify")
	}
	tokens, err := h.service.FinishWebAuthnLogin(t.Context(), email, assertionBody, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("finish webauthn login: %v", err)
	}
	if tokens == nil || tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatalf("webauthn login did not issue tokens: %+v", tokens)
	}
	if got, want := storedSignCount(t, h, user.ID), authenticator.SignCount(); got != want || got == 0 {
		t.Fatalf("signature counter not persisted: got %d want %d", got, want)
	}
	assertionPrincipal, err := h.service.ResolveSession(t.Context(), tokens.AccessToken)
	if err != nil || assertionPrincipal.UserID != user.ID {
		t.Fatalf("resolve webauthn session: %+v %v", assertionPrincipal, err)
	}

	// Replaying the exact same assertion must fail because the challenge is
	// single use, even though the signature is still cryptographically valid.
	_ = requireAppCode(t, finishWebAuthnLoginErr(t.Context(), h, email, assertionBody), CodeAuthWebAuthn)
	if got, want := storedSignCount(t, h, user.ID), authenticator.SignCount(); got != want {
		t.Fatalf("replay changed the stored counter: got %d want %d", got, want)
	}

	// A fresh ceremony advances the counter again.
	nextOptions, err := h.service.BeginWebAuthnLogin(t.Context(), email, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin second webauthn login: %v", err)
	}
	nextBody, err := authenticator.Assert(assertionChallenge(t, nextOptions), user.ID[:])
	if err != nil {
		t.Fatalf("second assert: %v", err)
	}
	if _, err := h.service.FinishWebAuthnLogin(t.Context(), email, nextBody, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("finish second webauthn login: %v", err)
	}
	if got, want := storedSignCount(t, h, user.ID), authenticator.SignCount(); got != want || got <= 1 {
		t.Fatalf("second ceremony did not advance the counter: got %d want %d", got, want)
	}
}

func TestWebAuthnRejectsWrongOriginRPIDAndSignature(t *testing.T) {
	h := newTestHarness(t)
	const email = "webauthn-negative@example.com"
	user := h.registerVerified(t, email, "Correct-Horse-Battery-9!")
	login := h.login(t, email, "Correct-Horse-Battery-9!")
	principal, err := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	authenticator := newSoftwareAuthenticator(t)

	options, err := h.service.BeginWebAuthnRegistration(t.Context(), principal, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin webauthn registration: %v", err)
	}
	challenge := creationChallenge(t, options)

	wrongOrigin, err := authenticator.Create(challenge, user.ID[:], webauthntest.WithOrigin("https://evil.example"))
	if err != nil {
		t.Fatalf("create wrong-origin response: %v", err)
	}
	_ = requireAppCode(t, h.service.FinishWebAuthnRegistration(t.Context(), principal, wrongOrigin, authMeta("192.0.2.10")), CodeAuthWebAuthn)

	wrongRPID, err := authenticator.Create(challenge, user.ID[:], webauthntest.WithRPID("evil.example"))
	if err != nil {
		t.Fatalf("create wrong-rpid response: %v", err)
	}
	_ = requireAppCode(t, h.service.FinishWebAuthnRegistration(t.Context(), principal, wrongRPID, authMeta("192.0.2.10")), CodeAuthWebAuthn)

	if credentials := storedWebAuthnCredential(t, h, user.ID); len(credentials) != 0 {
		t.Fatalf("rejected registration persisted a credential: %d", len(credentials))
	}

	// The rejected attempts did not consume the challenge, so the legitimate
	// ceremony still succeeds with the same server-side challenge.
	valid, err := authenticator.Create(challenge, user.ID[:])
	if err != nil {
		t.Fatalf("create valid response: %v", err)
	}
	if err := h.service.FinishWebAuthnRegistration(t.Context(), principal, valid, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("valid registration after rejected attempts: %v", err)
	}

	// Assertion rejects a wrong origin.
	loginOptions, err := h.service.BeginWebAuthnLogin(t.Context(), email, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin webauthn login: %v", err)
	}
	loginChallenge := assertionChallenge(t, loginOptions)
	wrongOriginAssertion, err := authenticator.Assert(loginChallenge, user.ID[:], webauthntest.WithOrigin("https://evil.example"))
	if err != nil {
		t.Fatalf("create wrong-origin assertion: %v", err)
	}
	_ = requireAppCode(t, finishWebAuthnLoginErr(t.Context(), h, email, wrongOriginAssertion), CodeAuthInvalidCredentials)

	// Assertion rejects a wrong RP ID.
	wrongRPIDAssertion, err := authenticator.Assert(loginChallenge, user.ID[:], webauthntest.WithRPID("evil.example"))
	if err != nil {
		t.Fatalf("create wrong-rpid assertion: %v", err)
	}
	_ = requireAppCode(t, finishWebAuthnLoginErr(t.Context(), h, email, wrongRPIDAssertion), CodeAuthInvalidCredentials)

	// Assertion rejects a tampered ES256 signature.
	tamperedAssertion, err := authenticator.Assert(loginChallenge, user.ID[:], webauthntest.WithTamperedSignature())
	if err != nil {
		t.Fatalf("create tampered assertion: %v", err)
	}
	_ = requireAppCode(t, finishWebAuthnLoginErr(t.Context(), h, email, tamperedAssertion), CodeAuthInvalidCredentials)

	if got := storedSignCount(t, h, user.ID); got != 0 {
		t.Fatalf("rejected assertions changed the stored counter: %d", got)
	}

	// A valid assertion on the same challenge still succeeds.
	validAssertion, err := authenticator.Assert(loginChallenge, user.ID[:])
	if err != nil {
		t.Fatalf("create valid assertion: %v", err)
	}
	if _, err := h.service.FinishWebAuthnLogin(t.Context(), email, validAssertion, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("valid assertion after rejected attempts: %v", err)
	}
}

func TestWebAuthnRejectsClonedAuthenticatorCounter(t *testing.T) {
	h := newTestHarness(t)
	const email = "webauthn-clone@example.com"
	user := h.registerVerified(t, email, "Correct-Horse-Battery-9!")
	login := h.login(t, email, "Correct-Horse-Battery-9!")
	principal, err := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	authenticator := newSoftwareAuthenticator(t)
	registerSoftwareCredential(t, h, principal, user, authenticator)

	options, err := h.service.BeginWebAuthnLogin(t.Context(), email, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin webauthn login: %v", err)
	}
	body, err := authenticator.Assert(assertionChallenge(t, options), user.ID[:])
	if err != nil {
		t.Fatalf("assert: %v", err)
	}
	if _, err := h.service.FinishWebAuthnLogin(t.Context(), email, body, authMeta("192.0.2.10")); err != nil {
		t.Fatalf("finish webauthn login: %v", err)
	}
	if got := storedSignCount(t, h, user.ID); got != 1 {
		t.Fatalf("expected stored counter 1 after first assertion, got %d", got)
	}

	regressionOptions, err := h.service.BeginWebAuthnLogin(t.Context(), email, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin regression webauthn login: %v", err)
	}
	regressionBody, err := authenticator.Assert(assertionChallenge(t, regressionOptions), user.ID[:], webauthntest.WithSignCount(1))
	if err != nil {
		t.Fatalf("create counter-regression assertion: %v", err)
	}
	appErr := requireAppCode(t, finishWebAuthnLoginErr(t.Context(), h, email, regressionBody), CodeAuthWebAuthn)
	if appErr.Status != 401 {
		t.Fatalf("clone warning returned status %d", appErr.Status)
	}
	if got := storedSignCount(t, h, user.ID); got != 1 {
		t.Fatalf("counter regression updated the stored counter: %d", got)
	}
	audits, err := h.store.ListAudit(t.Context(), AuditFilter{Action: "auth.webauthn.clone_warning", Limit: 10})
	if err != nil || len(audits) == 0 {
		t.Fatalf("clone warning audit missing: %d %v", len(audits), err)
	}
	if audits[0].Outcome != "denied" {
		t.Fatalf("clone warning audit outcome %q", audits[0].Outcome)
	}
}

func TestWebAuthnConcurrentAssertionSingleWinner(t *testing.T) {
	h := newTestHarness(t)
	const email = "webauthn-race@example.com"
	user := h.registerVerified(t, email, "Correct-Horse-Battery-9!")
	login := h.login(t, email, "Correct-Horse-Battery-9!")
	principal, err := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	authenticator := newSoftwareAuthenticator(t)
	registerSoftwareCredential(t, h, principal, user, authenticator)

	options, err := h.service.BeginWebAuthnLogin(t.Context(), email, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin webauthn login: %v", err)
	}
	body, err := authenticator.Assert(assertionChallenge(t, options), user.ID[:])
	if err != nil {
		t.Fatalf("assert: %v", err)
	}

	const workers = 8
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(_ int) {
			defer wg.Done()
			_, err := h.service.FinishWebAuthnLogin(t.Context(), email, body,
				RequestMeta{IP: mustAddr("192.0.2.10"), UserAgent: "race", RequestID: "req-race"})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		appErr, ok := IsAppError(err)
		if !ok || appErr.Code != CodeAuthWebAuthn {
			t.Fatalf("losing concurrent ceremony returned %T: %v", err, err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent assertion produced %d winners", successes)
	}
	if got, want := storedSignCount(t, h, user.ID), authenticator.SignCount(); got != want {
		t.Fatalf("concurrent ceremony corrupted the counter: got %d want %d", got, want)
	}
}

func TestWebAuthnRejectsModifiedChallengeUnknownCredentialAndInvalidAuthData(t *testing.T) {
	h := newTestHarness(t)
	const email = "webauthn-integrity@example.com"
	user := h.registerVerified(t, email, "Correct-Horse-Battery-9!")
	login := h.login(t, email, "Correct-Horse-Battery-9!")
	principal, err := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	authenticator := newSoftwareAuthenticator(t)
	registerSoftwareCredential(t, h, principal, user, authenticator)

	options, err := h.service.BeginWebAuthnLogin(t.Context(), email, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin webauthn login: %v", err)
	}
	challenge := assertionChallenge(t, options)

	t.Run("modified_challenge", func(t *testing.T) {
		body, err := authenticator.Assert("bW9kaWZpZWQtY2hhbGxlbmdl", user.ID[:])
		if err != nil {
			t.Fatalf("assert: %v", err)
		}
		_ = requireAppCode(t, finishWebAuthnLoginErr(t.Context(), h, email, body), CodeAuthInvalidCredentials)
	})

	t.Run("unknown_credential", func(t *testing.T) {
		unregistered := newSoftwareAuthenticator(t)
		body, err := unregistered.Assert(challenge, user.ID[:])
		if err != nil {
			t.Fatalf("assert: %v", err)
		}
		_ = requireAppCode(t, finishWebAuthnLoginErr(t.Context(), h, email, body), CodeAuthInvalidCredentials)
	})

	t.Run("invalid_authenticator_data", func(t *testing.T) {
		body, err := authenticator.Assert(challenge, user.ID[:])
		if err != nil {
			t.Fatalf("assert: %v", err)
		}
		_ = requireAppCode(t, finishWebAuthnLoginErr(t.Context(), h, email, tamperAuthenticatorData(t, body)), CodeAuthInvalidCredentials)
	})

	if got := storedSignCount(t, h, user.ID); got != 0 {
		t.Fatalf("rejected ceremonies changed the stored counter: %d", got)
	}
}

func TestWebAuthnRejectsExpiredCeremonyAndRevokedCredential(t *testing.T) {
	h := newTestHarness(t)
	const email = "webauthn-expiry@example.com"
	user := h.registerVerified(t, email, "Correct-Horse-Battery-9!")
	login := h.login(t, email, "Correct-Horse-Battery-9!")
	principal, err := h.service.ResolveSession(t.Context(), login.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	authenticator := newSoftwareAuthenticator(t)
	registerSoftwareCredential(t, h, principal, user, authenticator)

	options, err := h.service.BeginWebAuthnLogin(t.Context(), email, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin webauthn login: %v", err)
	}
	body, err := authenticator.Assert(assertionChallenge(t, options), user.ID[:])
	if err != nil {
		t.Fatalf("assert: %v", err)
	}
	h.clock.Advance(h.service.cfg.WebAuthnTTL + time.Minute)
	_ = requireAppCode(t, finishWebAuthnLoginErr(t.Context(), h, email, body), CodeAuthWebAuthn)

	// Restore a usable clock and revoke the credential through the store. A
	// revoked credential is not offered to the ceremony and cannot be used to
	// start authentication.
	h.clock.Advance(-(h.service.cfg.WebAuthnTTL + time.Minute))
	credentials := storedWebAuthnCredential(t, h, user.ID)
	if len(credentials) != 1 {
		t.Fatalf("expected one credential, got %d", len(credentials))
	}
	revokedAt := h.clock.Now()
	credentials[0].RevokedAt = &revokedAt
	if err := h.store.UpdateCredential(t.Context(), &credentials[0], nil); err != nil {
		t.Fatalf("revoke credential: %v", err)
	}
	if _, err := h.service.BeginWebAuthnLogin(t.Context(), email, authMeta("192.0.2.10")); err == nil {
		t.Fatal("revoked credential started an authentication ceremony")
	}
}

func TestWebAuthnRejectsCrossAccountCredential(t *testing.T) {
	h := newTestHarness(t)
	const victimEmail = "webauthn-victim@example.com"
	const attackerEmail = "webauthn-attacker@example.com"
	victim := h.registerVerified(t, victimEmail, "Correct-Horse-Battery-9!")
	attacker := h.registerVerified(t, attackerEmail, "Correct-Horse-Battery-9!")

	victimLogin := h.login(t, victimEmail, "Correct-Horse-Battery-9!")
	victimPrincipal, err := h.service.ResolveSession(t.Context(), victimLogin.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("resolve victim session: %v", err)
	}
	victimAuthenticator := newSoftwareAuthenticator(t)
	registerSoftwareCredential(t, h, victimPrincipal, victim, victimAuthenticator)

	attackerLogin := h.login(t, attackerEmail, "Correct-Horse-Battery-9!")
	attackerPrincipal, err := h.service.ResolveSession(t.Context(), attackerLogin.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("resolve attacker session: %v", err)
	}
	attackerAuthenticator := newSoftwareAuthenticator(t)
	registerSoftwareCredential(t, h, attackerPrincipal, attacker, attackerAuthenticator)

	options, err := h.service.BeginWebAuthnLogin(t.Context(), victimEmail, authMeta("192.0.2.10"))
	if err != nil {
		t.Fatalf("begin victim webauthn login: %v", err)
	}
	// The attacker controls a valid key and a registered credential of their
	// own, but neither belongs to the victim account.
	body, err := attackerAuthenticator.Assert(assertionChallenge(t, options), victim.ID[:])
	if err != nil {
		t.Fatalf("assert: %v", err)
	}
	_ = requireAppCode(t, finishWebAuthnLoginErr(t.Context(), h, victimEmail, body), CodeAuthInvalidCredentials)
	if got := storedSignCount(t, h, victim.ID); got != 0 {
		t.Fatalf("cross-account attempt changed the victim counter: %d", got)
	}
}

func verifyES256Assertion(t *testing.T, publicKey *ecdsa.PublicKey, body []byte) bool {
	t.Helper()
	var payload struct {
		Response struct {
			ClientDataJSON    string `json:"clientDataJSON"`
			AuthenticatorData string `json:"authenticatorData"`
			Signature         string `json:"signature"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode assertion: %v", err)
	}
	clientData, err := base64.RawURLEncoding.DecodeString(payload.Response.ClientDataJSON)
	if err != nil {
		t.Fatalf("decode clientDataJSON: %v", err)
	}
	authData, err := base64.RawURLEncoding.DecodeString(payload.Response.AuthenticatorData)
	if err != nil {
		t.Fatalf("decode authenticatorData: %v", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(payload.Response.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	clientDataHash := sha256.Sum256(clientData)
	signedData := append(append([]byte(nil), authData...), clientDataHash[:]...)
	digest := sha256.Sum256(signedData)
	return ecdsa.VerifyASN1(publicKey, digest[:], signature)
}

func tamperAuthenticatorData(t *testing.T, body []byte) []byte {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode assertion: %v", err)
	}
	response, ok := payload["response"].(map[string]any)
	if !ok {
		t.Fatalf("assertion response missing: %T", payload["response"])
	}
	encoded, ok := response["authenticatorData"].(string)
	if !ok {
		t.Fatalf("authenticatorData missing: %T", response["authenticatorData"])
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 {
		t.Fatalf("decode authenticatorData: %v", err)
	}
	raw[0] ^= 0x01
	response["authenticatorData"] = base64.RawURLEncoding.EncodeToString(raw)
	mutated, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode tampered assertion: %v", err)
	}
	return mutated
}

// finishWebAuthnLoginErr drops the token pair so rejection assertions can use
// the ordinary single-value error helpers.
func finishWebAuthnLoginErr(ctx context.Context, h *testHarness, email string, body []byte) error {
	_, err := h.service.FinishWebAuthnLogin(ctx, email, body, authMeta("192.0.2.10"))
	return err
}
