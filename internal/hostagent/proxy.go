package hostagent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.omukk.dev/wrenn/wrenn/internal/sandbox"
)

const (
	// proxyDialAttempts is the number of connection attempts for the proxy
	// transport. Retries handle the delay between a process binding to a port
	// inside the guest and socat/Go-proxy starting to forward on the TAP IP.
	proxyDialAttempts = 3
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

	// proxies caches ReverseProxy instances per sandbox+port to avoid
	// per-request allocation under high-frequency REST polling.
	proxies sync.Map // key: "sandboxID/port" → *httputil.ReverseProxy
}

// newProxyTransport returns an HTTP transport dedicated to proxying user
// traffic into sandboxes. It is intentionally separate from the envdclient
// transport and http.DefaultTransport to prevent proxy traffic from
// interfering with Connect RPC streams (PTY, exec).
func newProxyTransport() http.RoundTripper {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 20 * time.Second,
	}

	return &http.Transport{
		ForceAttemptHTTP2:   false, // HTTP/1.1 only — avoids HTTP/2 HOL blocking
		MaxIdleConnsPerHost: 20,
		MaxIdleConns:        100,
		IdleConnTimeout:     120 * time.Second,
		DisableCompression:  true,
		// Retry with linear backoff to handle the delay between a process
		// binding inside the guest and the port forwarder making it reachable.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var conn net.Conn
			var err error
			for attempt := range proxyDialAttempts {
				conn, err = dialer.DialContext(ctx, network, addr)
				if err == nil {
					return conn, nil
				}
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				// Don't sleep on the last attempt.
				if attempt < proxyDialAttempts-1 {
					backoff := time.Duration(100*(attempt+1)) * time.Millisecond
					select {
					case <-time.After(backoff):
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
			}
			return nil, err
		},
	}
}

// NewProxyHandler creates a new sandbox proxy handler.
func NewProxyHandler(mgr *sandbox.Manager) *ProxyHandler {
	return &ProxyHandler{
		mgr:       mgr,
		transport: newProxyTransport(),
	}
}

// EvictProxy removes cached reverse proxy instances for a sandbox.
// Call this when a sandbox is destroyed.
func (h *ProxyHandler) EvictProxy(sandboxID string) {
	h.proxies.Range(func(key, _ any) bool {
		if k, ok := key.(string); ok && strings.HasPrefix(k, sandboxID+"/") {
			h.proxies.Delete(key)
		}
		return true
	})
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

	proxy := h.getOrCreateProxy(sandboxID, port, fmt.Sprintf("%s:%d", hostIP, portNum))
	proxy.ServeHTTP(w, r)
}

// getOrCreateProxy returns a cached ReverseProxy for the given sandbox+port+host,
// creating one if it doesn't exist. The targetHost is included in the key so
// that an IP change after pause/resume naturally misses the old entry.
func (h *ProxyHandler) getOrCreateProxy(sandboxID, port, targetHost string) *httputil.ReverseProxy {
	cacheKey := sandboxID + "/" + port + "/" + targetHost

	if v, ok := h.proxies.Load(cacheKey); ok {
		return v.(*httputil.ReverseProxy)
	}

	proxyPrefix := "/proxy/" + sandboxID + "/" + port

	proxy := &httputil.ReverseProxy{
		Transport: h.transport,
		Director: func(req *http.Request) {
			// Extract remainder from the original path: /proxy/{id}/{port}/{remainder}
			remainder := ""
			if trimmed := strings.TrimPrefix(req.URL.Path, proxyPrefix); trimmed != req.URL.Path {
				remainder = strings.TrimPrefix(trimmed, "/")
			}

			req.URL.Scheme = "http"
			req.URL.Host = targetHost
			req.URL.Path = "/" + remainder
			req.Host = targetHost
		},
		// Rewrite redirect Location headers so they include the /proxy/{id}/{port}
		// prefix. Handles both root-relative (/path) and absolute-URL redirects
		// (http://internal-ip:port/path) that would otherwise leak internal IPs
		// or break directory navigation.
		ModifyResponse: func(resp *http.Response) error {
			loc := resp.Header.Get("Location")
			if loc == "" {
				return nil
			}
			if strings.HasPrefix(loc, "/") {
				resp.Header.Set("Location", proxyPrefix+loc)
				return nil
			}
			// Rewrite absolute URLs pointing to the internal target host.
			if u, err := url.Parse(loc); err == nil && u.Host == targetHost {
				resp.Header.Set("Location", proxyPrefix+u.RequestURI())
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Debug("proxy error", "sandbox_id", sandboxID, "port", port, "error", err)
			http.Error(w, "proxy error: "+err.Error(), http.StatusBadGateway)
		},
	}

	actual, _ := h.proxies.LoadOrStore(cacheKey, proxy)
	return actual.(*httputil.ReverseProxy)
}
