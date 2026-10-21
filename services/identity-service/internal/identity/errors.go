//nolint:gosec // stable API error codes are not credentials.
package identity

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Stable error codes. These are part of the public API contract and must not
// be changed without a contract version update.
const (
	CodeAuthMissing            = "AUTH_MISSING"
	CodeAuthInvalidCredentials = "AUTH_INVALID_CREDENTIALS"
	CodeAuthInvalidSignature   = "AUTH_INVALID_SIGNATURE"
	CodeAuthTimestamp          = "AUTH_TIMESTAMP"
	CodeAuthScope              = "AUTH_SCOPE"
	CodeAuthIPNotAllowed       = "AUTH_IP_NOT_ALLOWED"
	CodeAuthMFARequired        = "AUTH_MFA_REQUIRED"
	CodeAuthMFAInvalid         = "AUTH_MFA_INVALID"
	CodeAuthMFAReplay          = "AUTH_MFA_REPLAY"
	CodeAuthAccountLocked      = "AUTH_ACCOUNT_LOCKED"
	CodeAuthRateLimited        = "AUTH_RATE_LIMITED"
	CodeAuthEmailUnverified    = "AUTH_EMAIL_UNVERIFIED"
	CodeAuthSessionRevoked     = "AUTH_SESSION_REVOKED"
	CodeAuthTokenExpired       = "AUTH_TOKEN_EXPIRED"
	CodeAuthRefreshReused      = "AUTH_REFRESH_REUSED"
	CodeAuthStepUpRequired     = "AUTH_STEP_UP_REQUIRED"
	CodeAuthForbidden          = "AUTH_FORBIDDEN"
	CodeAuthKeyRevoked         = "AUTH_KEY_REVOKED"
	CodeAuthKeyExpired         = "AUTH_KEY_EXPIRED"
	CodeAuthWebAuthn           = "AUTH_WEBAUTHN_FAILED"
	CodeValidation             = "VAL_SCHEMA"
	CodeNotFound               = "VAL_NOT_FOUND"
	CodeConflict               = "SYS_CONFLICT"
	CodeInternal               = "SYS_INTERNAL"
	CodeUnavailable            = "SYS_UNAVAILABLE"
)

// AppError is a safe, client-facing error. Cause is logged internally and is
// never serialized to clients.
type AppError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
	Cause      error
}

func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.Cause)
	}
	return e.Code + ": " + e.Message
}

// Unwrap exposes the internal cause to errors.Is/As.
func (e *AppError) Unwrap() error { return e.Cause }

// NewAppError builds a safe application error.
func NewAppError(status int, code, message string) *AppError {
	return &AppError{Status: status, Code: code, Message: message}
}

// WrapAppError adds an internal cause to a safe error.
func WrapAppError(status int, code, message string, cause error) *AppError {
	return &AppError{Status: status, Code: code, Message: message, Cause: cause}
}

// IsAppError extracts an AppError from an error chain.
func IsAppError(err error) (*AppError, bool) {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr, true
	}
	return nil, false
}

func validationError(message string) *AppError {
	return NewAppError(http.StatusBadRequest, CodeValidation, message)
}

func notFoundError() *AppError {
	return NewAppError(http.StatusNotFound, CodeNotFound, "resource not found")
}

func conflictError(message string) *AppError {
	return NewAppError(http.StatusConflict, CodeConflict, message)
}

func forbiddenError(message string) *AppError {
	return NewAppError(http.StatusForbidden, CodeAuthForbidden, message)
}

func invalidCredentialsError() *AppError {
	return NewAppError(http.StatusUnauthorized, CodeAuthInvalidCredentials, "invalid credentials")
}

func invalidTokenError() *AppError {
	return NewAppError(http.StatusUnauthorized, CodeAuthInvalidSignature, "invalid token")
}

func tokenExpiredError() *AppError {
	return NewAppError(http.StatusUnauthorized, CodeAuthTokenExpired, "token expired")
}

func sessionRevokedError() *AppError {
	return NewAppError(http.StatusUnauthorized, CodeAuthSessionRevoked, "session is no longer valid")
}

func mfaInvalidError() *AppError {
	return NewAppError(http.StatusUnauthorized, CodeAuthMFAInvalid, "invalid multi-factor code")
}

func mfaReplayError() *AppError {
	return NewAppError(http.StatusUnauthorized, CodeAuthMFAReplay, "multi-factor code already used")
}

func stepUpRequiredError() *AppError {
	return NewAppError(http.StatusForbidden, CodeAuthStepUpRequired, "step-up authentication required")
}

func lockedError(retry time.Duration) *AppError {
	err := NewAppError(http.StatusTooManyRequests, CodeAuthAccountLocked, "account temporarily locked")
	err.RetryAfter = retry
	return err
}

// MFARequiredCause carries the allowed second-factor methods without exposing
// them through the generic error string.
type MFARequiredCause struct {
	Methods []string
}

func (c *MFARequiredCause) Error() string { return "mfa required" }
