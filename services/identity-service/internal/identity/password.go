package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
)

// Argon2Params configures Argon2id. Production defaults target OWASP ASVS L2:
// 64 MiB memory, 3 iterations, 4 lanes, 16-byte salt, 32-byte output.
type Argon2Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultArgon2Params returns production password-hashing parameters.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 4, SaltLength: 16, KeyLength: 32}
}

// Validate bounds the parameters so a misconfiguration cannot make hashing
// trivially weak or unbounded.
func (p Argon2Params) Validate() error {
	if p.MemoryKiB < 8*1024 || p.MemoryKiB > 1024*1024 {
		return errors.New("argon2 memory must be between 8 MiB and 1 GiB")
	}
	if p.Iterations < 1 || p.Iterations > 10 {
		return errors.New("argon2 iterations must be between 1 and 10")
	}
	if p.Parallelism < 1 || p.Parallelism > 16 {
		return errors.New("argon2 parallelism must be between 1 and 16")
	}
	if p.SaltLength < 16 || p.SaltLength > 64 {
		return errors.New("argon2 salt length must be between 16 and 64")
	}
	if p.KeyLength < 32 || p.KeyLength > 64 {
		return errors.New("argon2 key length must be between 32 and 64")
	}
	return nil
}

// PasswordHasher hashes passwords with Argon2id and a service-wide pepper. The
// pepper is combined with HMAC-SHA256 before hashing so a stolen database does
// not permit offline cracking without the pepper.
type PasswordHasher struct {
	params    Argon2Params
	pepper    []byte
	dummyHash string
	common    map[string]struct{}
}

// NewPasswordHasher constructs a hasher and a dummy hash used to equalize
// timing for unknown accounts.
func NewPasswordHasher(params Argon2Params, pepper []byte) (*PasswordHasher, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}
	if len(pepper) < 32 {
		return nil, errors.New("password pepper must be at least 32 bytes")
	}
	h := &PasswordHasher{params: params, pepper: append([]byte(nil), pepper...), common: commonPasswords()}
	dummy, err := h.Hash("nexora-dummy-password-that-is-never-used-42!")
	if err != nil {
		return nil, err
	}
	h.dummyHash = dummy
	return h, nil
}

// Hash hashes a password with a fresh random salt.
func (h *PasswordHasher) Hash(password string) (string, error) {
	if password == "" {
		return "", errors.New("password is empty")
	}
	salt, err := RandomBytes(int(h.params.SaltLength))
	if err != nil {
		return "", err
	}
	key := argon2.IDKey(h.peppered(password), salt, h.params.Iterations, h.params.MemoryKiB, h.params.Parallelism, h.params.KeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		h.params.MemoryKiB, h.params.Iterations, h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify checks a password against a PHC string in constant time. It returns
// false rather than distinguishing malformed hashes.
func (h *PasswordHasher) Verify(encoded, password string) bool {
	params, salt, expected, err := parsePHC(encoded)
	if err != nil {
		// Run a dummy hash to keep the timing profile stable.
		h.verifyDummy(password)
		return false
	}
	actual := argon2.IDKey(h.peppered(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, uint32(len(expected))) //nolint:gosec // parsePHC bounds key length
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// VerifyDummy performs a constant-work verification for unknown users.
func (h *PasswordHasher) VerifyDummy(password string) {
	h.verifyDummy(password)
}

func (h *PasswordHasher) verifyDummy(password string) {
	params, salt, expected, err := parsePHC(h.dummyHash)
	if err != nil {
		return
	}
	_ = argon2.IDKey(h.peppered(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, uint32(len(expected))) //nolint:gosec // parsePHC bounds key length
}

// NeedsRehash reports whether an encoded hash uses weaker parameters.
func (h *PasswordHasher) NeedsRehash(encoded string) bool {
	params, _, _, err := parsePHC(encoded)
	if err != nil {
		return true
	}
	return params.MemoryKiB < h.params.MemoryKiB ||
		params.Iterations < h.params.Iterations ||
		params.Parallelism < h.params.Parallelism ||
		params.KeyLength < h.params.KeyLength
}

func (h *PasswordHasher) peppered(password string) []byte {
	mac := hmac.New(sha256.New, h.pepper)
	_, _ = mac.Write([]byte("nexora-password-v1\x00"))
	_, _ = mac.Write([]byte(password))
	return mac.Sum(nil)
}

func parsePHC(encoded string) (Argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return Argon2Params{}, nil, nil, errors.New("invalid argon2id hash")
	}
	var params Argon2Params
	for _, field := range strings.Split(parts[3], ",") {
		keyValue := strings.SplitN(field, "=", 2)
		if len(keyValue) != 2 {
			return Argon2Params{}, nil, nil, errors.New("invalid argon2 parameters")
		}
		value, err := strconv.ParseUint(keyValue[1], 10, 32)
		if err != nil {
			return Argon2Params{}, nil, nil, errors.New("invalid argon2 parameter value")
		}
		switch keyValue[0] {
		case "m":
			params.MemoryKiB = uint32(value)
		case "t":
			params.Iterations = uint32(value)
		case "p":
			if value < 1 || value > 16 {
				return Argon2Params{}, nil, nil, errors.New("invalid argon2 parallelism")
			}
			params.Parallelism = uint8(value) //nolint:gosec // bounded above
		default:
			return Argon2Params{}, nil, nil, errors.New("unknown argon2 parameter")
		}
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Argon2Params{}, nil, nil, errors.New("invalid argon2 salt")
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Argon2Params{}, nil, nil, errors.New("invalid argon2 key")
	}
	if len(salt) < 16 || len(salt) > 64 {
		return Argon2Params{}, nil, nil, errors.New("argon2 hash salt length is invalid")
	}
	if len(expected) < 32 || len(expected) > 64 {
		return Argon2Params{}, nil, nil, errors.New("argon2 hash key length is invalid")
	}
	params.SaltLength = uint32(len(salt))    //nolint:gosec // bounded above
	params.KeyLength = uint32(len(expected)) //nolint:gosec // bounded above
	if err := params.Validate(); err != nil {
		return Argon2Params{}, nil, nil, err
	}
	return params, salt, expected, nil
}

// ValidatePasswordPolicy enforces ASVS L2 length and a small local deny list.
// It deliberately does not impose composition rules, which encourage weaker
// predictable passwords.
func ValidatePasswordPolicy(password, email string) error {
	if len(password) < 12 {
		return errors.New("password must be at least 12 characters")
	}
	if len(password) > 128 {
		return errors.New("password must be at most 128 characters")
	}
	if strings.TrimSpace(password) == "" {
		return errors.New("password must not be only whitespace")
	}
	if !hasLetter(password) || !hasDigitOrSymbol(password) {
		return errors.New("password must contain a letter and a digit or symbol")
	}
	lower := strings.ToLower(password)
	if _, found := commonPasswords()[lower]; found {
		return errors.New("password is too common")
	}
	if email != "" {
		local := strings.ToLower(strings.SplitN(email, "@", 2)[0])
		if len(local) >= 4 && strings.Contains(lower, local) {
			return errors.New("password must not contain the email local part")
		}
	}
	return nil
}

func hasLetter(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func hasDigitOrSymbol(value string) bool {
	for _, r := range value {
		if unicode.IsDigit(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return true
		}
	}
	return false
}

func commonPasswords() map[string]struct{} {
	values := []string{
		"password", "password1", "password123", "123456789012", "qwertyuiop12",
		"letmein12345", "welcome12345", "admin1234567", "iloveyou1234",
		"changeme1234", "nexora12345", "bitcoin12345", "trading12345",
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}
