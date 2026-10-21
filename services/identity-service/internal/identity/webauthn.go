package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

// WebAuthnConfig configures the relying party. RP ID and origins are required
// and are validated by the upstream library.
type WebAuthnConfig struct {
	RPDisplayName string
	RPID          string
	RPOrigins     []string
	Timeout       time.Duration
}

// WebAuthnManager wraps go-webauthn and persists only server-side ceremony
// state. It never trusts client-supplied origin, RP ID, challenge, or
// signature without library verification.
type WebAuthnManager struct {
	handler *webauthn.WebAuthn
}

// NewWebAuthnManager constructs a relying party or fails closed.
func NewWebAuthnManager(cfg WebAuthnConfig) (*WebAuthnManager, error) {
	if cfg.RPID == "" || len(cfg.RPOrigins) == 0 {
		return nil, errors.New("webauthn RP ID and origins are required")
	}
	if cfg.Timeout <= 0 || cfg.Timeout > 5*time.Minute {
		cfg.Timeout = 60 * time.Second
	}
	handler, err := webauthn.New(&webauthn.Config{
		RPDisplayName: cfg.RPDisplayName,
		RPID:          cfg.RPID,
		RPOrigins:     cfg.RPOrigins,
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: cfg.Timeout, TimeoutUVD: cfg.Timeout},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: cfg.Timeout, TimeoutUVD: cfg.Timeout},
		},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			UserVerification: protocol.VerificationPreferred,
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
		},
	})
	if err != nil {
		return nil, err
	}
	return &WebAuthnManager{handler: handler}, nil
}

// BeginRegistration returns creation options and JSON session data that must be
// stored server-side before the finish call.
func (m *WebAuthnManager) BeginRegistration(user *User, credentials []Credential) (any, []byte, error) {
	adapter, err := newWebAuthnUser(user, credentials)
	if err != nil {
		return nil, nil, err
	}
	options, session, err := m.handler.BeginRegistration(adapter,
		webauthn.WithExclusions(adapter.excludeCredentials()))
	if err != nil {
		return nil, nil, err
	}
	data, err := json.Marshal(session)
	if err != nil {
		return nil, nil, err
	}
	return options, data, nil
}

// FinishRegistration validates the attestation response and returns the
// credential to persist.
func (m *WebAuthnManager) FinishRegistration(user *User, credentials []Credential, sessionData, body []byte) (*webauthn.Credential, error) {
	adapter, err := newWebAuthnUser(user, credentials)
	if err != nil {
		return nil, err
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(sessionData, &session); err != nil {
		return nil, errors.New("invalid webauthn session")
	}
	request, err := ceremonyRequest(body)
	if err != nil {
		return nil, err
	}
	credential, err := m.handler.FinishRegistration(adapter, session, request)
	if err != nil {
		return nil, err
	}
	return credential, nil
}

// BeginLogin returns assertion options and server-side session data.
func (m *WebAuthnManager) BeginLogin(user *User, credentials []Credential) (any, []byte, error) {
	adapter, err := newWebAuthnUser(user, credentials)
	if err != nil {
		return nil, nil, err
	}
	if len(adapter.webAuthnCredentials) == 0 {
		return nil, nil, errors.New("no webauthn credentials")
	}
	options, session, err := m.handler.BeginLogin(adapter)
	if err != nil {
		return nil, nil, err
	}
	data, err := json.Marshal(session)
	if err != nil {
		return nil, nil, err
	}
	return options, data, nil
}

// FinishLogin validates the assertion and returns the updated credential.
func (m *WebAuthnManager) FinishLogin(user *User, credentials []Credential, sessionData, body []byte) (*webauthn.Credential, error) {
	adapter, err := newWebAuthnUser(user, credentials)
	if err != nil {
		return nil, err
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(sessionData, &session); err != nil {
		return nil, errors.New("invalid webauthn session")
	}
	request, err := ceremonyRequest(body)
	if err != nil {
		return nil, err
	}
	credential, err := m.handler.FinishLogin(adapter, session, request)
	if err != nil {
		return nil, err
	}
	return credential, nil
}

func ceremonyRequest(body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://identity.nexora.invalid/webauthn", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	return request, nil
}

type webAuthnUser struct {
	id                  uuid.UUID
	name                string
	displayName         string
	webAuthnCredentials []webauthn.Credential
}

func newWebAuthnUser(user *User, credentials []Credential) (*webAuthnUser, error) {
	if user == nil {
		return nil, errors.New("webauthn user is required")
	}
	adapter := &webAuthnUser{
		id: user.ID, name: "user-" + user.ID.String(), displayName: "NEXORA user " + user.ID.String(),
	}
	for _, credential := range credentials {
		if credential.Kind != CredentialWebAuthn || credential.RevokedAt != nil {
			continue
		}
		var stored webauthn.Credential
		if err := json.Unmarshal(credential.Metadata, &stored); err != nil {
			return nil, errors.New("invalid stored webauthn credential")
		}
		if len(stored.ID) == 0 {
			return nil, errors.New("invalid stored webauthn credential id")
		}
		adapter.webAuthnCredentials = append(adapter.webAuthnCredentials, stored)
	}
	return adapter, nil
}

func (u *webAuthnUser) WebAuthnID() []byte                         { return append([]byte(nil), u.id[:]...) }
func (u *webAuthnUser) WebAuthnName() string                       { return u.name }
func (u *webAuthnUser) WebAuthnDisplayName() string                { return u.displayName }
func (u *webAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.webAuthnCredentials }

func (u *webAuthnUser) excludeCredentials() []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, 0, len(u.webAuthnCredentials))
	for _, credential := range u.webAuthnCredentials {
		out = append(out, protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: credential.ID,
			Transport:    credential.Transport,
		})
	}
	return out
}
