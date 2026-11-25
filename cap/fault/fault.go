// Package fault defines shared result categories without storage ownership.
package fault

import "errors"

var (
	ErrConflict    = errors.New("version or idempotency conflict")
	ErrNotFound    = errors.New("not found")
	ErrUnavailable = errors.New("storage unavailable")
	ErrInput       = errors.New("invalid input")
	// ErrTooLarge rejects content over a configured size limit.
	ErrTooLarge = errors.New("content too large")
)

// Error explains a failure to a caller: Kind keeps the category (one of the
// errors above, or an identity/authorization error) so errors.Is still holds;
// Code is a stable snake_case reason and Field, when set, names the request
// field at fault. It carries no raw value, credential or internal detail.
type Error struct {
	Kind        error
	Code, Field string
}

func (e *Error) Error() string {
	if e.Field != "" {
		return e.Kind.Error() + ": " + e.Code + " (" + e.Field + ")"
	}
	return e.Kind.Error() + ": " + e.Code
}
func (e *Error) Unwrap() error { return e.Kind }

// Coded returns kind explained by code and, optionally, the field at fault.
func Coded(kind error, code string, field ...string) error {
	e := &Error{Kind: kind, Code: code}
	if len(field) > 0 {
		e.Field = field[0]
	}
	return e
}

// Input reports a missing or invalid request field.
func Input(code, field string) error { return Coded(ErrInput, code, field) }

// Conflict reports that current facts no longer allow the request.
func Conflict(code string) error { return Coded(ErrConflict, code) }

// Stable codes shared across modules; command-specific codes live with their
// capability (for example the Blocked reasons of cap/workflow).
const (
	CodeIdempotencyKeyRequired = "idempotency_key_required"
	CodeIdempotencyKeyReused   = "idempotency_key_reused"
	CodeUnknownField           = "unknown_field"
	CodeMalformedBody          = "malformed_body"
	CodeRequired               = "field_required"
	CodeInvalid                = "field_invalid"
	CodeBasisChanged           = "basis_changed"
	CodeRoundChanged           = "round_changed"
	CodeVersionChanged         = "version_changed"
	CodeTaskNotActive          = "task_not_active"
	// CodeResyncRequired: a change-feed cursor precedes the retained events.
	CodeResyncRequired = "resync_required"
)
