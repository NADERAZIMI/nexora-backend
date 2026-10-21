// Package webauthntest provides a deterministic software WebAuthn authenticator
// for end-to-end ceremony tests.
//
// The authenticator performs real P-256/ES256 cryptography and emits
// standards-compliant client data, authenticator data, assertion signatures,
// and CBOR "none" attestation objects. It deliberately does not share any code
// with the relying-party verifier so the tests exercise the same path as a
// browser or hardware authenticator.
package webauthntest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/fxamacker/cbor/v2"
)

const (
	flagUserPresent       = 0x01
	flagUserVerified      = 0x04
	flagAttestedCredData  = 0x40
	coseKeyTypeEC2        = 2
	coseAlgorithmES256    = -7
	coseCurveP256         = 1
	credentialIDByteCount = 32
	authDataHeaderBytes   = 37
)

// Options configures a software authenticator.
type Options struct {
	// RPID and Origin are the relying-party ID and browser origin used when
	// producing ceremonies. They must match the relying party under test for a
	// successful ceremony.
	RPID   string
	Origin string
	// AAGUID is copied into every registration response. A zero AAGUID is
	// allowed and is what many platform authenticators report.
	AAGUID [16]byte
}

// Authenticator is a single software security key with one resident
// credential. It is safe for concurrent use.
type Authenticator struct {
	mu sync.Mutex

	rpID   string
	origin string
	aaguid [16]byte

	credentialID []byte
	privateKey   *ecdsa.PrivateKey
	coseKey      []byte
	signCount    uint32
}

// New creates an authenticator with a fresh P-256 key and a random 256-bit
// credential ID.
func New(options Options) (*Authenticator, error) {
	if options.RPID == "" || options.Origin == "" {
		return nil, errors.New("webauthn test authenticator requires RP ID and origin")
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate authenticator key: %w", err)
	}
	credentialID := make([]byte, credentialIDByteCount)
	if _, err := rand.Read(credentialID); err != nil {
		return nil, fmt.Errorf("generate credential ID: %w", err)
	}
	coseKey, err := encodeCOSEKey(&privateKey.PublicKey)
	if err != nil {
		return nil, err
	}
	return &Authenticator{
		rpID: options.RPID, origin: options.Origin, aaguid: options.AAGUID,
		credentialID: credentialID, privateKey: privateKey, coseKey: coseKey,
	}, nil
}

// RPID returns the relying-party ID the authenticator currently emits.
func (a *Authenticator) RPID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rpID
}

// Origin returns the browser origin the authenticator currently emits.
func (a *Authenticator) Origin() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.origin
}

// CredentialID returns a copy of the credential ID.
func (a *Authenticator) CredentialID() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]byte(nil), a.credentialID...)
}

// PublicKey returns the credential's P-256 public key.
func (a *Authenticator) PublicKey() (*ecdsa.PublicKey, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	encoded, err := a.privateKey.PublicKey.Bytes()
	if err != nil {
		return nil, fmt.Errorf("encode public key: %w", err)
	}
	public, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), encoded)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	return public, nil
}

// SignCount returns the last counter emitted by the authenticator.
func (a *Authenticator) SignCount() uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.signCount
}

// CeremonyOption changes how one ceremony response is produced. Options are
// intentionally limited to values an attacker or a non-conforming client can
// control so tests can exercise rejection paths without mocks.
type CeremonyOption func(*ceremonyOptions)

type ceremonyOptions struct {
	origin            string
	rpID              string
	tamperSignature   bool
	signCountOverride *uint32
}

// WithOrigin overrides the origin written into clientDataJSON.
func WithOrigin(origin string) CeremonyOption {
	return func(options *ceremonyOptions) {
		options.origin = origin
	}
}

// WithRPID overrides the RP ID hash written into authenticator data.
func WithRPID(rpID string) CeremonyOption {
	return func(options *ceremonyOptions) {
		options.rpID = rpID
	}
}

// WithTamperedSignature flips one byte in the ES256 signature while leaving the
// signed data unchanged.
func WithTamperedSignature() CeremonyOption {
	return func(options *ceremonyOptions) {
		options.tamperSignature = true
	}
}

// WithSignCount overrides the next assertion counter. It is used to exercise
// cloned-authenticator and concurrency behavior.
func WithSignCount(count uint32) CeremonyOption {
	return func(options *ceremonyOptions) {
		options.signCountOverride = &count
	}
}

// Create produces a PublicKeyCredential JSON response for a registration
// ceremony. challenge must be the base64url challenge string from the creation
// options. userHandle is returned in the attestation user entity and should be
// the relying party's stable user ID bytes.
func (a *Authenticator) Create(challenge string, userHandle []byte, ceremony ...CeremonyOption) ([]byte, error) {
	if challenge == "" {
		return nil, errors.New("registration challenge is required")
	}
	if len(userHandle) == 0 {
		return nil, errors.New("user handle is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	options := a.resolve(ceremony...)
	clientData, err := clientDataJSON("webauthn.create", challenge, options.origin)
	if err != nil {
		return nil, err
	}
	flags := byte(flagUserPresent | flagUserVerified | flagAttestedCredData)
	authData := a.authData(options.rpID, flags, a.signCount, true)
	attestationObject, err := cbor.Marshal(map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": authData,
	})
	if err != nil {
		return nil, fmt.Errorf("encode attestation object: %w", err)
	}
	response := map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.credentialID),
		"rawId": base64.RawURLEncoding.EncodeToString(a.credentialID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attestationObject),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults":  map[string]any{},
		"authenticatorAttachment": "platform",
	}
	return json.Marshal(response)
}

// Assert produces a PublicKeyCredential JSON response for an authentication
// ceremony. The returned signature is ES256 over
// authenticatorData || SHA-256(clientDataJSON).
func (a *Authenticator) Assert(challenge string, userHandle []byte, ceremony ...CeremonyOption) ([]byte, error) {
	if challenge == "" {
		return nil, errors.New("assertion challenge is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.signCount++

	options := a.resolve(ceremony...)
	counter := a.signCount
	if options.signCountOverride != nil {
		counter = *options.signCountOverride
		a.signCount = counter
	}
	clientData, err := clientDataJSON("webauthn.get", challenge, options.origin)
	if err != nil {
		return nil, err
	}
	flags := byte(flagUserPresent | flagUserVerified)
	authData := a.authData(options.rpID, flags, counter, false)
	clientDataHash := sha256.Sum256(clientData)
	signedData := make([]byte, 0, len(authData)+len(clientDataHash))
	signedData = append(signedData, authData...)
	signedData = append(signedData, clientDataHash[:]...)
	digest := sha256.Sum256(signedData)
	signature, err := ecdsa.SignASN1(rand.Reader, a.privateKey, digest[:])
	if err != nil {
		return nil, fmt.Errorf("sign assertion: %w", err)
	}
	if options.tamperSignature {
		signature[len(signature)-1] ^= 0x01
	}
	response := map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.credentialID),
		"rawId": base64.RawURLEncoding.EncodeToString(a.credentialID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"signature":         base64.RawURLEncoding.EncodeToString(signature),
			"userHandle":        base64.RawURLEncoding.EncodeToString(userHandle),
		},
		"clientExtensionResults":  map[string]any{},
		"authenticatorAttachment": "platform",
	}
	return json.Marshal(response)
}

func (a *Authenticator) resolve(ceremony ...CeremonyOption) ceremonyOptions {
	options := ceremonyOptions{origin: a.origin, rpID: a.rpID}
	for _, option := range ceremony {
		option(&options)
	}
	return options
}

func (a *Authenticator) authData(rpID string, flags byte, signCount uint32, attested bool) []byte {
	rpIDHash := sha256.Sum256([]byte(rpID))
	buffer := make([]byte, authDataHeaderBytes, authDataHeaderBytes+64+len(a.credentialID)+len(a.coseKey))
	copy(buffer, rpIDHash[:])
	buffer[32] = flags
	binary.BigEndian.PutUint32(buffer[33:37], signCount)
	if !attested {
		return buffer
	}
	buffer = append(buffer, a.aaguid[:]...)
	credentialIDLength := len(a.credentialID)
	if credentialIDLength > 0xFFFF {
		panic("webauthn test credential ID exceeds the Uint16 length prefix")
	}
	var idLength [2]byte
	binary.BigEndian.PutUint16(idLength[:], uint16(credentialIDLength))
	buffer = append(buffer, idLength[:]...)
	buffer = append(buffer, a.credentialID...)
	buffer = append(buffer, a.coseKey...)
	return buffer
}

func clientDataJSON(ceremony, challenge, origin string) ([]byte, error) {
	if origin == "" {
		return nil, errors.New("ceremony origin is required")
	}
	data, err := json.Marshal(map[string]any{
		"type":        ceremony,
		"challenge":   challenge,
		"origin":      origin,
		"crossOrigin": false,
	})
	if err != nil {
		return nil, fmt.Errorf("encode client data: %w", err)
	}
	return data, nil
}

func encodeCOSEKey(publicKey *ecdsa.PublicKey) ([]byte, error) {
	encoded, err := publicKey.Bytes()
	if err != nil {
		return nil, fmt.Errorf("encode P-256 public key: %w", err)
	}
	const uncompressedPointBytes = 65
	if len(encoded) != uncompressedPointBytes {
		return nil, fmt.Errorf("unexpected P-256 public key length %d", len(encoded))
	}
	data, err := cbor.Marshal(map[int]any{
		1:  coseKeyTypeEC2,
		3:  coseAlgorithmES256,
		-1: coseCurveP256,
		-2: encoded[1:33],
		-3: encoded[33:65],
	})
	if err != nil {
		return nil, fmt.Errorf("encode COSE public key: %w", err)
	}
	return data, nil
}
