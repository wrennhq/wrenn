package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
)

// Sentinel errors returned by proxyTarget, used to map to HTTP status codes
// without relying on error message text.
var (
	errProxySandboxNotFound = errors.New("sandbox not found")
	errProxyNoHostAddress   = errors.New("host agent has no address")
)

const proxyCacheTTL = 120 * time.Second

// sandboxHostPattern matches hostnames like "49999-cl-abcd1234.localhost" or
// "49999-cl-abcd1234.example.com". Captures: port, sandbox ID.
var sandboxHostPattern = regexp.MustCompile(`^(\d+)-(cl-[0-9a-z]+)\.`)

// errProxySandboxNotRunning carries the sandbox status so callers can include
// it in the HTTP response without parsing error strings.
type errProxySandboxNotRunning struct{ status string }

func (e errProxySandboxNotRunning) Error() string {
	return fmt.Sprintf("sandbox is not running (status: %s)", e.status)
}

// proxyCacheEntry caches the resolved agent URL for a (sandbox, team) pair.
// The *httputil.ReverseProxy is built per-request (cheap) so the Director closure
// can capture the correct port without the cache key needing to include it.
type proxyCacheEntry struct {
	agentURL  *url.URL
	expiresAt time.Time
}

// proxyCacheKey is a fixed-size key from two UUIDs, avoids string allocation.
type proxyCacheKey [32]byte

func makeProxyCacheKey(sandboxID, teamID pgtype.UUID) proxyCacheKey {
	var k proxyCacheKey
	copy(k[:16], sandboxID.Bytes[:])
	copy(k[16:], teamID.Bytes[:])
	return k
}

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

	cacheMu sync.Mutex
	cache   map[proxyCacheKey]proxyCacheEntry
}

// NewSandboxProxyWrapper creates a new proxy wrapper.
func NewSandboxProxyWrapper(inner http.Handler, queries *db.Queries, pool *lifecycle.HostClientPool) *SandboxProxyWrapper {
	return &SandboxProxyWrapper{
		inner:     inner,
		db:        queries,
		pool:      pool,
		transport: pool.Transport(),
		cache:     make(map[proxyCacheKey]proxyCacheEntry),
	}
}

// proxyTarget looks up the cached agent URL for (sandboxID, teamID).
// On a miss it queries the DB, resolves the address, and populates the cache.
// The *httputil.ReverseProxy is built by the caller so the Director closure
// captures the correct port without the cache key needing to include it.
func (h *SandboxProxyWrapper) proxyTarget(ctx context.Context, sandboxID, teamID pgtype.UUID) (*url.URL, error) {
	cacheKey := makeProxyCacheKey(sandboxID, teamID)

	h.cacheMu.Lock()
	entry, ok := h.cache[cacheKey]
	h.cacheMu.Unlock()

	if ok && time.Now().Before(entry.expiresAt) {
		return entry.agentURL, nil
	}

	// Cache miss or expired — query DB.
	target, err := h.db.GetSandboxProxyTarget(ctx, db.GetSandboxProxyTargetParams{
		ID:     sandboxID,
		TeamID: teamID,
	})
	if err != nil {
		return nil, errProxySandboxNotFound
	}
	if target.Status != "running" {
		return nil, errProxySandboxNotRunning{status: target.Status}
	}
	if target.HostAddress == "" {
		return nil, errProxyNoHostAddress
	}

	agentURL, err := url.Parse(h.pool.ResolveAddr(target.HostAddress))
	if err != nil {
		return nil, fmt.Errorf("invalid host agent address: %w", err)
	}

	h.cacheMu.Lock()
	h.cache[cacheKey] = proxyCacheEntry{
		agentURL:  agentURL,
		expiresAt: time.Now().Add(proxyCacheTTL),
	}
	h.cacheMu.Unlock()

	return agentURL, nil
}

// evictProxyCache removes the cached entry for a (sandbox, team) pair.
// Called on 502 so a stopped/moved sandbox is re-resolved on the next request.
func (h *SandboxProxyWrapper) evictProxyCache(sandboxID, teamID pgtype.UUID) {
	h.cacheMu.Lock()
	delete(h.cache, makeProxyCacheKey(sandboxID, teamID))
	h.cacheMu.Unlock()
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

	agentURL, err := h.proxyTarget(r.Context(), sandboxID, teamID)
	if err != nil {
		switch {
		case errors.Is(err, errProxySandboxNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.As(err, new(errProxySandboxNotRunning)):
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		}
		return
	}

	proxy := &httputil.ReverseProxy{
		Transport: h.transport,
		Director: func(req *http.Request) {
			req.URL.Scheme = agentURL.Scheme
			req.URL.Host = agentURL.Host
			req.URL.Path = "/proxy/" + sandboxIDStr + "/" + port + req.URL.Path
			req.Host = agentURL.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Debug("sandbox proxy error",
				"sandbox_id", sandboxIDStr,
				"port", port,
				"error", err,
			)
			h.evictProxyCache(sandboxID, teamID)
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
