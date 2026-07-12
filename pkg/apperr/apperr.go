// Package apperr is Wrenn's application error domain — the single source of
// truth for error codes, HTTP statuses, client-safe messages, and retryability.
//
// Every error that can reach a client is declared once as a Def in
// catalog.go. Code that fails wraps its cause with a Def
// (Def.Wrap, Def.Msg, ...), and the HTTP layer resolves any error to a
// wire-safe *Error with From. The cause chain is preserved for logs but never
// serialized to clients.
package apperr

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
)

// Error is a resolved application error. Code, Status, Message, Retryable and
// Details are client-safe; the wrapped cause is for logs only.
type Error struct {
	Code      string
	Status    int
	Message   string
	Retryable bool
	Details   map[string]any

	cause error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.cause }

// Is makes errors.Is(err, SomeDef.New()) and cross-instance comparisons match
// on Code, so two Errors with the same code compare equal regardless of
// message, details, or cause.
func (e *Error) Is(target error) bool {
	var t *Error
	if errors.As(target, &t) {
		return t.Code == e.Code
	}
	return false
}

// With returns a copy of the error with an extra client-visible detail.
// Details must be safe to show to API consumers.
func (e *Error) With(key string, value any) *Error {
	c := *e
	c.Details = maps.Clone(c.Details)
	if c.Details == nil {
		c.Details = map[string]any{}
	}
	c.Details[key] = value
	return &c
}

// Def is a catalog entry: an error code with its fixed HTTP status,
// default client-safe message, and retryability. Defs are declared in
// catalog.go and instantiated at failure sites.
type Def struct {
	Code      string
	Status    int
	Retryable bool
	Message   string
}

// New returns an Error with the Def's default message.
func (d Def) New() *Error {
	return &Error{Code: d.Code, Status: d.Status, Message: d.Message, Retryable: d.Retryable}
}

// Msg returns an Error with a custom client-safe message.
func (d Def) Msg(message string) *Error {
	e := d.New()
	e.Message = message
	return e
}

// Msgf returns an Error with a formatted client-safe message.
// The arguments become part of the client response — never pass raw
// internal error values; use Wrap for those.
func (d Def) Msgf(format string, args ...any) *Error {
	return d.Msg(fmt.Sprintf(format, args...))
}

// Wrap returns an Error carrying cause for logs, with the Def's default
// client-safe message. The cause is never serialized to clients.
func (d Def) Wrap(cause error) *Error {
	e := d.New()
	e.cause = cause
	return e
}

// WrapMsg is Wrap with a custom client-safe message.
func (d Def) WrapMsg(cause error, message string) *Error {
	e := d.Wrap(cause)
	e.Message = message
	return e
}

// Is reports whether err resolves to this Def's code.
func (d Def) Is(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == d.Code
}

// From resolves any error to a client-safe *Error.
//
//   - *Error anywhere in the chain: returned as-is.
//   - context deadline/cancellation: Timeout — matched on the cause chain
//     (errors.Is) before the Connect branch, so a transport error that wraps a
//     live context deadline still surfaces as a retryable timeout rather than
//     being flattened to internal_error.
//   - Connect RPC errors: resolved via the attached ErrorInfo detail when the
//     far side provided one, otherwise Internal (see fromConnect).
//   - anything else: Internal — the cause is preserved for logging, the
//     client sees only the generic message.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if isContextTimeout(err) {
		return Timeout.Wrap(err)
	}
	if ce := asConnectError(err); ce != nil {
		return fromConnect(ce)
	}
	return Internal.Wrap(err)
}

// Cause returns the wrapped internal cause, or nil.
func (e *Error) Cause() error { return e.cause }

// HTTPStatus returns the error's HTTP status, defaulting to 500 for
// zero values so a misconstructed Error can never turn into a 200.
func (e *Error) HTTPStatus() int {
	if e.Status == 0 {
		return http.StatusInternalServerError
	}
	return e.Status
}
