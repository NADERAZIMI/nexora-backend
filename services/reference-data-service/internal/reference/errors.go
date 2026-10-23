// Package reference implements NEXORA reference-data versioning, approval, and
// activation rules. PostgreSQL is authoritative; no in-memory state is used as
// a source of truth.
package reference

import (
	"errors"
	"fmt"
	"net/http"
)

// Stable error codes returned by the REST and gRPC APIs.
const (
	CodeNotFound         = "REF_NOT_FOUND"
	CodeInvalidInput     = "REF_INVALID_INPUT"
	CodeValidationFailed = "REF_VALIDATION_FAILED"
	CodeStaleDraft       = "REF_STALE_DRAFT"
	CodeStaleVersion     = "REF_STALE_VERSION"
	CodeApprovalRequired = "REF_APPROVAL_REQUIRED"
	CodeSelfApproval     = "REF_SELF_APPROVAL"
	CodeInvalidState     = "REF_INVALID_STATE"
	CodeHashMismatch     = "REF_HASH_MISMATCH"
	CodeRollbackRequired = "REF_ROLLBACK_REQUIRED"
	CodeConflict         = "REF_CONFLICT"
	CodeUnauthorized     = "REF_UNAUTHORIZED"
	CodeForbidden        = "REF_FORBIDDEN"
	CodeStepUpRequired   = "REF_STEP_UP_REQUIRED"
	CodeInternal         = "REF_INTERNAL"
)

// Error is a typed API error with a stable machine-readable code.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
	Status  int    `json:"-"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NewError constructs a typed error.
func NewError(status int, code, message string, details any) *Error {
	return &Error{Status: status, Code: code, Message: message, Details: details}
}

// AsError converts any error into a typed API error without leaking internals.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	return NewError(http.StatusInternalServerError, CodeInternal, "reference-data operation failed", nil)
}

// NotFound reports a missing object.
func NotFound(kind, id string) *Error {
	return NewError(http.StatusNotFound, CodeNotFound, kind+" not found", map[string]string{"id": id})
}

// InvalidInput reports malformed request data.
func InvalidInput(message string) *Error {
	return NewError(http.StatusBadRequest, CodeInvalidInput, message, nil)
}

// ValidationFailed reports semantic configuration issues.
func ValidationFailed(details any) *Error {
	return NewError(http.StatusUnprocessableEntity, CodeValidationFailed, "reference-data validation failed", details)
}

// Conflict reports a state or concurrency conflict.
func Conflict(code, message string, details any) *Error {
	return NewError(http.StatusConflict, code, message, details)
}

// Forbidden reports an authorization denial.
func Forbidden(message string) *Error {
	return NewError(http.StatusForbidden, CodeForbidden, message, nil)
}
