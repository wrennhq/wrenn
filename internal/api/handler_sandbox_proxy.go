package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
)

// sandboxHostPattern matches hostnames like "49999-cl-abcd1234.localhost" or
// "49999-cl-abcd1234.example.com". Captures: port, sandbox ID.
var sandboxHostPattern = regexp.MustCompile(`^(\d+)-(cl-[0-9a-z]+)\.`)

// SandboxProxyWrapper wraps an existing HTTP handler and intercepts requests
// whose Host header matches the {port}-{sandbox_id}.{domain} pattern. Matching
// requests are reverse-proxied through the host agent that owns the sandbox.
// All other requests are passed through to the inner handler.
//
// Authentication is via X-API-Key header only (no JWT). The API key's team
// must own the sandbox.
type SandboxProxyWrapper struct {
	inner     http.Handler
	db        *db.Queries
	pool      *lifecycle.HostClientPool
	transport http.RoundTripper
}

// NewSandboxProxyWrapper creates a new proxy wrapper.
func NewSandboxProxyWrapper(inner http.Handler, queries *db.Queries, pool *lifecycle.HostClientPool) *SandboxProxyWrapper {
	return &SandboxProxyWrapper{
		inner:     inner,
		db:        queries,
		pool:      pool,
		transport: http.DefaultTransport,
	}
}

func (h *SandboxProxyWrapper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	// Strip port from Host header (e.g. "49999-cl-abcd1234.localhost:8000" → "49999-cl-abcd1234.localhost")
	if colonIdx := strings.LastIndex(host, ":"); colonIdx != -1 {
		host = host[:colonIdx]
	}

	matches := sandboxHostPattern.FindStringSubmatch(host)
	if matches == nil {
		h.inner.ServeHTTP(w, r)
		return
	}

	port := matches[1]
	sandboxIDStr := matches[2]

	// Validate port.
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1 || portNum > 65535 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	// Authenticate: require API key or JWT, extract team ID.
	teamID, err := h.authenticateRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		http.Error(w, "invalid sandbox ID", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Look up sandbox and verify ownership.
	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{
		ID:     sandboxID,
		TeamID: teamID,
	})
	if err != nil {
		http.Error(w, "sandbox not found", http.StatusNotFound)
		return
	}

	if sb.Status != "running" {
		http.Error(w, fmt.Sprintf("sandbox is not running (status: %s)", sb.Status), http.StatusConflict)
		return
	}

	agentHost, err := h.db.GetHost(ctx, sb.HostID)
	if err != nil {
		http.Error(w, "host agent not found", http.StatusServiceUnavailable)
		return
	}

	if agentHost.Address == "" {
		http.Error(w, "host agent has no address", http.StatusServiceUnavailable)
		return
	}

	agentAddr := lifecycle.EnsureScheme(agentHost.Address)
	upstreamPath := fmt.Sprintf("/proxy/%s/%s%s", sandboxIDStr, port, r.URL.Path)

	target, err := url.Parse(agentAddr)
	if err != nil {
		http.Error(w, "invalid host agent address", http.StatusInternalServerError)
		return
	}

	proxy := &httputil.ReverseProxy{
		Transport: h.transport,
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = upstreamPath
			req.URL.RawQuery = r.URL.RawQuery
			req.Host = target.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Debug("sandbox proxy error",
				"sandbox_id", sandboxIDStr,
				"port", port,
				"error", err,
			)
			http.Error(w, "proxy error: "+err.Error(), http.StatusBadGateway)
		},
	}

	proxy.ServeHTTP(w, r)
}

// authenticateRequest validates the request's API key and returns the team ID.
// Only API key authentication is supported for sandbox proxy requests (not JWT).
func (h *SandboxProxyWrapper) authenticateRequest(r *http.Request) (pgtype.UUID, error) {
	key := r.Header.Get("X-API-Key")
	if key == "" {
		return pgtype.UUID{}, fmt.Errorf("X-API-Key header required")
	}

	hash := auth.HashAPIKey(key)
	row, err := h.db.GetAPIKeyByHash(r.Context(), hash)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid API key")
	}
	return row.TeamID, nil
}
