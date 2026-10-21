package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
)

// RandomBytes returns n bytes from the operating system CSPRNG.
func RandomBytes(n int) ([]byte, error) {
	if n <= 0 {
		return nil, errors.New("random byte count must be positive")
	}
	out := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, out); err != nil {
		return nil, err
	}
	return out, nil
}

// RandomToken returns a URL-safe 256-bit token by default.
func RandomToken(n int) (string, error) {
	value, err := RandomBytes(n)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// SHA256 returns the SHA-256 digest of value.
func SHA256(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}

// KeyedHash applies HMAC-SHA256 with domain separation. It is used for email
// and phone lookup hashes and for IP pseudonymization.
func KeyedHash(pepper []byte, domain, value string) []byte {
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

// SecureCompare compares two byte strings in constant time.
func SecureCompare(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// SecretCipher encrypts secrets at rest. Production deployments must supply a
// KMS-backed implementation. AESGCMCipher is the local envelope used by
// development and tests, with a 256-bit key that must never be committed.
type SecretCipher interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// AESGCMCipher is an AES-256-GCM secret cipher. The nonce is prefixed to the
// ciphertext; no nonce is ever reused.
type AESGCMCipher struct {
	aead cipher.AEAD
}

// NewAESGCMCipher validates key length and constructs a cipher.
func NewAESGCMCipher(key []byte) (*AESGCMCipher, error) {
	if len(key) != 32 {
		return nil, errors.New("AES-256-GCM key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AESGCMCipher{aead: aead}, nil
}

// Encrypt encrypts plaintext with a fresh 96-bit nonce.
func (c *AESGCMCipher) Encrypt(plaintext []byte) ([]byte, error) {
	nonce, err := RandomBytes(c.aead.NonceSize())
	if err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt authenticates and decrypts ciphertext.
func (c *AESGCMCipher) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < c.aead.NonceSize() {
		return nil, errors.New("ciphertext is truncated")
	}
	nonce, body := ciphertext[:c.aead.NonceSize()], ciphertext[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, errors.New("ciphertext authentication failed")
	}
	return plaintext, nil
}

// GenerateEd25519Key creates a signing key in memory. Callers must persist
// production keys outside source control.
func GenerateEd25519Key() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// ParseEd25519PrivateKeyPEM parses a PKCS#8 Ed25519 private key.
func ParseEd25519PrivateKeyPEM(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("private key is not PEM encoded")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not Ed25519")
	}
	return edKey, nil
}

// MarshalEd25519PrivateKeyPEM renders a PKCS#8 PEM block.
func MarshalEd25519PrivateKeyPEM(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// MarshalEd25519PublicKeyPEM renders a PKIX PEM public key.
func MarshalEd25519PublicKeyPEM(key ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// DecodeKey accepts standard or raw URL-safe base64.
func DecodeKey(value string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := encoding.DecodeString(value); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64 key")
}

func hmacSHA256(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(message)
	return mac.Sum(nil)
}
