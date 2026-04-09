package api

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/id"
)

type errorResponse struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{
		Error: errorDetail{Code: code, Message: message},
	})
}

// formatUUIDForRPC converts a pgtype.UUID to a hex string for RPC messages.
func formatUUIDForRPC(u pgtype.UUID) string {
	return id.UUIDString(u)
}

// agentErrToHTTP maps a Connect RPC error to an HTTP status, error code, and message.
func agentErrToHTTP(err error) (int, string, string) {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound:
		return http.StatusNotFound, "not_found", err.Error()
	case connect.CodeInvalidArgument:
		return http.StatusBadRequest, "invalid_request", err.Error()
	case connect.CodeFailedPrecondition:
		return http.StatusConflict, "conflict", err.Error()
	default:
		return http.StatusBadGateway, "agent_error", err.Error()
	}
}

// requestLogger returns middleware that logs each request.
func requestLogger() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			slog.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration", time.Since(start),
			)
		})
	}
}

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// serviceErrToHTTP maps a service-layer error to an HTTP status, code, and message.
// It inspects the underlying Connect RPC error if present, otherwise returns 500.
func serviceErrToHTTP(err error) (int, string, string) {
	msg := err.Error()

	// Check for Connect RPC errors wrapped by the service layer.
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return agentErrToHTTP(connectErr)
	}

	// Map well-known service error patterns.
	switch {
	case strings.Contains(msg, "not found"):
		return http.StatusNotFound, "not_found", msg
	case strings.Contains(msg, "not running"), strings.Contains(msg, "not paused"):
		return http.StatusConflict, "invalid_state", msg
	case strings.Contains(msg, "conflict:"):
		return http.StatusConflict, "conflict", msg
	case strings.Contains(msg, "forbidden"):
		return http.StatusForbidden, "forbidden", msg
	case strings.Contains(msg, "invalid or expired"):
		return http.StatusUnauthorized, "unauthorized", msg
	case strings.Contains(msg, "invalid"):
		return http.StatusBadRequest, "invalid_request", msg
	default:
		return http.StatusInternalServerError, "internal_error", msg
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Hijack implements http.Hijacker, required for WebSocket upgrade.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// Flush implements http.Flusher, required for streaming responses.
func (w *statusWriter) Flush() {
	if fl, ok := w.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}
