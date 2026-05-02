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

	"git.omukk.dev/wrenn/wrenn/pkg/id"
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
	case connect.CodeFailedPrecondition, connect.CodeAlreadyExists:
		return http.StatusConflict, "conflict", err.Error()
	case connect.CodePermissionDenied:
		return http.StatusForbidden, "forbidden", err.Error()
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable, "no_hosts_available", "no servers available — try again later"
	case connect.CodeUnimplemented:
		return http.StatusNotImplemented, "agent_error", err.Error()
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
	// Return generic messages for most cases to avoid leaking internal details.
	switch {
	case strings.Contains(msg, "not found"):
		return http.StatusNotFound, "not_found", "resource not found"
	case strings.Contains(msg, "not running"):
		return http.StatusConflict, "invalid_state", "resource is not running"
	case strings.Contains(msg, "not paused"):
		return http.StatusConflict, "invalid_state", "resource is not paused"
	case strings.Contains(msg, "conflict:"):
		return http.StatusConflict, "conflict", strings.TrimPrefix(msg, "conflict: ")
	case strings.Contains(msg, "forbidden"):
		return http.StatusForbidden, "forbidden", "forbidden"
	case strings.Contains(msg, "invalid or expired"):
		return http.StatusUnauthorized, "unauthorized", "invalid or expired credentials"
	case strings.Contains(msg, "no online") && strings.Contains(msg, "hosts available"),
		strings.Contains(msg, "no host has sufficient resources"):
		return http.StatusServiceUnavailable, "no_hosts_available", "no servers available — try again later"
	case strings.Contains(msg, "invalid"):
		return http.StatusBadRequest, "invalid_request", "invalid request"
	default:
		slog.Error("unhandled service error", "error", err)
		return http.StatusInternalServerError, "internal_error", "an internal error occurred"
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
