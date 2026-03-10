package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
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
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{
		Error: errorDetail{Code: code, Message: message},
	})
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

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
