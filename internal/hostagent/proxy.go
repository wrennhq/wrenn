package hostagent

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"

	"git.omukk.dev/wrenn/wrenn/internal/sandbox"
)

// ProxyHandler reverse-proxies HTTP requests to services running inside
// sandboxes. It handles requests of the form:
//
//	/proxy/{sandbox_id}/{port}/{path...}
//
// The sandbox's HostIP (routable on this machine) is used as the upstream.
// This supports any protocol that rides on HTTP, including WebSocket upgrades.
type ProxyHandler struct {
	mgr       *sandbox.Manager
	transport http.RoundTripper
}

// NewProxyHandler creates a new sandbox proxy handler.
func NewProxyHandler(mgr *sandbox.Manager) *ProxyHandler {
	return &ProxyHandler{
		mgr:       mgr,
		transport: http.DefaultTransport,
	}
}

// ServeHTTP implements http.Handler.
func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Expected path: /proxy/{sandbox_id}/{port}/...
	// After trimming "/proxy/", we get "{sandbox_id}/{port}/..."
	trimmed := strings.TrimPrefix(r.URL.Path, "/proxy/")
	if trimmed == r.URL.Path {
		http.Error(w, "invalid proxy path", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) < 2 {
		http.Error(w, "expected /proxy/{sandbox_id}/{port}/...", http.StatusBadRequest)
		return
	}

	sandboxID := parts[0]
	port := parts[1]
	remainder := ""
	if len(parts) == 3 {
		remainder = parts[2]
	}

	// Validate port is a number in the valid range.
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1 || portNum > 65535 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	hostIP, tracker, ok := h.mgr.AcquireProxyConn(sandboxID)
	if !ok {
		http.Error(w, "sandbox is not available", http.StatusServiceUnavailable)
		return
	}
	defer tracker.Release()

	targetHost := fmt.Sprintf("%s:%d", hostIP, portNum)

	proxy := &httputil.ReverseProxy{
		Transport: h.transport,
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = targetHost
			req.URL.Path = "/" + remainder
			req.URL.RawQuery = r.URL.RawQuery
			req.Host = targetHost
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Debug("proxy error", "sandbox_id", sandboxID, "port", port, "error", err)
			http.Error(w, "proxy error: "+err.Error(), http.StatusBadGateway)
		},
	}

	proxy.ServeHTTP(w, r)
}
