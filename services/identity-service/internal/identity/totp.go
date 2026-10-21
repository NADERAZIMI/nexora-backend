package identity

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP requires HMAC-SHA1
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TOTP parameters follow RFC 6238: HMAC-SHA1, 30-second steps, 6 digits.
const (
	TOTPPeriod = 30 * time.Second
	TOTPDigits = 6
	TOTPSkew   = 1
)

// GenerateTOTPSecret returns a 160-bit base32 secret without padding.
func GenerateTOTPSecret() (string, error) {
	secret, err := RandomBytes(20)
	if err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret), nil
}

// TOTPCode computes the RFC 6238 code at unix seconds.
func TOTPCode(secret string, unixSeconds int64, digits int, period time.Duration) (string, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return "", err
	}
	if digits < 6 || digits > 10 {
		return "", errors.New("totp digits must be between 6 and 10")
	}
	if unixSeconds < 0 {
		return "", errors.New("totp time must not be negative")
	}
	if period < time.Second || period%time.Second != 0 {
		return "", errors.New("totp period must be a positive whole number of seconds")
	}
	step := uint64(unixSeconds / int64(period/time.Second)) //nolint:gosec // unixSeconds is checked non-negative
	return hotp(key, step, digits), nil
}

// VerifyTOTP checks the current step and a bounded skew. It returns the matched
// step so the caller can atomically reject replay.
func VerifyTOTP(secret, code string, now time.Time, skew int, digits int, period time.Duration) (int64, bool, error) {
	code = strings.TrimSpace(code)
	if len(code) != digits {
		return 0, false, nil
	}
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return 0, false, err
	}
	if skew < 0 || skew > 2 {
		return 0, false, errors.New("totp skew must be between 0 and 2")
	}
	step := now.Unix() / int64(period/time.Second)
	for offset := -int64(skew); offset <= int64(skew); offset++ {
		candidateStep := step + offset
		if candidateStep < 0 {
			continue
		}
		candidate := hotp(key, uint64(candidateStep), digits)
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return candidateStep, true, nil
		}
	}
	return 0, false, nil
}

// TOTPProvisioningURI returns an otpauth:// URI for authenticator applications.
func TOTPProvisioningURI(issuer, account, secret string) string {
	label := issuer + ":" + account
	return fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=%s&algorithm=SHA1&digits=%d&period=%d",
		urlEscape(label), secret, urlEscape(issuer), TOTPDigits, int(TOTPPeriod/time.Second))
}

func hotp(key []byte, counter uint64, digits int) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	value %= mod
	out := strconv.FormatUint(uint64(value), 10)
	for len(out) < digits {
		out = "0" + out
	}
	return out
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	normalized := strings.ToUpper(strings.TrimSpace(secret))
	normalized = strings.ReplaceAll(normalized, " ", "")
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalized)
	if err != nil {
		return nil, errors.New("invalid totp secret")
	}
	if len(key) < 10 || len(key) > 64 {
		return nil, errors.New("invalid totp secret length")
	}
	return key, nil
}

func urlEscape(value string) string {
	replacer := strings.NewReplacer(
		"%", "%25", " ", "%20", "/", "%2F", "?", "%3F", "&", "%26",
		"=", "%3D", ":", "%3A", "@", "%40",
	)
	return replacer.Replace(value)
}
