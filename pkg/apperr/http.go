package apperr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
)

type ctxKey struct{}

// RequestIDHeader is the response header carrying the request ID.
const RequestIDHeader = "X-Request-ID"

// Middleware assigns each request an ID (req_ + 16 hex), stores it in the
// context, and echoes it in the X-Request-ID response header. Error responses
// include it so users can quote an ID that log lines are keyed by.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "req_" + hex.EncodeToString(b)
}

// WithRequestID returns a context carrying the request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestID returns the request ID from the context, or "" if none.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

type wireError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id,omitempty"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

type wireEnvelope struct {
	Error wireError `json:"error"`
}

// WriteHTTP resolves err via From and writes the error envelope. The full
// cause chain is logged keyed by request ID — 5xx at ERROR, wrapped 4xx at
// WARN — and never serialized to the client.
func WriteHTTP(w http.ResponseWriter, r *http.Request, err error) {
	e := From(err)
	if e == nil {
		// Defensive: a nil error reaching here is a caller bug. Never emit a
		// bare 200 — surface a generic 500 so the client still gets an envelope.
		e = Internal.New()
	}
	reqID := RequestID(r.Context())

	if e.HTTPStatus() >= 500 {
		slog.Error("request failed",
			"request_id", reqID,
			"code", e.Code,
			"status", e.HTTPStatus(),
			"method", r.Method,
			"path", r.URL.Path,
			"error", e.Error(),
		)
	} else if e.Cause() != nil {
		slog.Warn("request rejected",
			"request_id", reqID,
			"code", e.Code,
			"status", e.HTTPStatus(),
			"method", r.Method,
			"path", r.URL.Path,
			"error", e.Error(),
		)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.HTTPStatus())
	_ = json.NewEncoder(w).Encode(wireEnvelope{Error: wireError{
		Code:      e.Code,
		Message:   e.Message,
		RequestID: reqID,
		Retryable: e.Retryable,
		Details:   e.Details,
	}})
}
