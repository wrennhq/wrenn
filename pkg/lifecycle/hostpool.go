package lifecycle

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/proto/hostagent/gen/hostagentv1connect"
)

// HostClientPool maintains a cache of Connect RPC clients keyed by host ID.
// Clients are created lazily on first access and evicted when a host is removed
// or goes unreachable. The pool is safe for concurrent use.
type HostClientPool struct {
	mu         sync.RWMutex
	clients    map[string]hostagentv1connect.HostAgentServiceClient
	httpClient *http.Client
	scheme     string // "http://" or "https://"
}

// NewHostClientPool creates a pool that connects to agents over plain HTTP.
// Use NewHostClientPoolTLS when mTLS is required.
func NewHostClientPool() *HostClientPool {
	return &HostClientPool{
		clients:    make(map[string]hostagentv1connect.HostAgentServiceClient),
		httpClient: &http.Client{Timeout: 10 * time.Minute},
		scheme:     "http://",
	}
}

// NewHostClientPoolTLS creates a pool that connects to agents over mTLS.
// tlsCfg should already carry the CP client cert and CA trust anchor
// (use auth.CPClientTLSConfig to construct it).
func NewHostClientPoolTLS(tlsCfg *tls.Config) *HostClientPool {
	transport := &http.Transport{
		TLSClientConfig:   tlsCfg,
		ForceAttemptHTTP2: false,
		// Empty non-nil map disables HTTP/2 ALPN negotiation, forcing HTTP/1.1.
		// Connect RPC works over HTTP/1.1; HTTP/2 multiplexing causes HOL
		// blocking when a single slow sandbox RPC stalls the shared connection.
		TLSNextProto:          make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 45 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	return &HostClientPool{
		clients: make(map[string]hostagentv1connect.HostAgentServiceClient),
		httpClient: &http.Client{
			Timeout:   10 * time.Minute,
			Transport: transport,
		},
		scheme: "https://",
	}
}

// Get returns a Connect RPC client for the given host, creating one if necessary.
// address is the host agent address (ip:port or full URL). The scheme is added if absent.
func (p *HostClientPool) Get(hostID, address string) hostagentv1connect.HostAgentServiceClient {
	p.mu.RLock()
	c, ok := p.clients[hostID]
	p.mu.RUnlock()
	if ok {
		return c
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	// Double-check after acquiring write lock.
	if c, ok = p.clients[hostID]; ok {
		return c
	}
	c = hostagentv1connect.NewHostAgentServiceClient(p.httpClient, p.ensureScheme(address))
	p.clients[hostID] = c
	return c
}

// GetForHost is a convenience wrapper that extracts the address from a db.Host
// and returns an error if the host has no address recorded yet.
func (p *HostClientPool) GetForHost(h db.Host) (hostagentv1connect.HostAgentServiceClient, error) {
	if h.Address == "" {
		return nil, fmt.Errorf("host %s has no address", id.FormatHostID(h.ID))
	}
	return p.Get(id.FormatHostID(h.ID), h.Address), nil
}

// Evict removes the cached client for the given host, forcing a new client to be
// created on the next call to Get. Call this when a host's address changes or when
// a host is deleted.
func (p *HostClientPool) Evict(hostID string) {
	p.mu.Lock()
	delete(p.clients, hostID)
	p.mu.Unlock()
}

// ensureScheme prepends the pool's configured scheme if the address has none.
func (p *HostClientPool) ensureScheme(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return p.scheme + addr
}

// Transport returns the http.RoundTripper used by this pool. Use this when you
// need to make raw HTTP requests to agent addresses with the same TLS settings
// as the pool's Connect RPC clients (e.g., the sandbox reverse proxy).
func (p *HostClientPool) Transport() http.RoundTripper {
	if p.httpClient.Transport != nil {
		return p.httpClient.Transport
	}
	return http.DefaultTransport
}

// ResolveAddr prepends the pool's configured scheme to addr if it has none.
// Use this when constructing URLs that must use the same transport as the pool
// (e.g., the sandbox proxy handler). Calling Get/GetForHost internally does
// the same thing, but ResolveAddr exposes it for callers that only need the URL.
func (p *HostClientPool) ResolveAddr(addr string) string {
	return p.ensureScheme(addr)
}

// NewProxyTransport returns a new http.RoundTripper configured for proxying
// user traffic to sandbox services. It is intentionally separate from the RPC
// transport returned by Transport() so that heavy proxy traffic (Jupyter
// WebSocket, REST API polling) cannot interfere with Connect RPC streams (PTY,
// exec) via HTTP/2 flow control or connection pool contention.
func (p *HostClientPool) NewProxyTransport() http.RoundTripper {
	t := &http.Transport{
		ForceAttemptHTTP2:   false, // HTTP/1.1 only — avoids HTTP/2 HOL blocking
		MaxIdleConnsPerHost: 20,
		MaxIdleConns:        100,
		IdleConnTimeout:     120 * time.Second,
		DisableCompression:  true,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 20 * time.Second,
		}).DialContext,
	}

	// If the pool uses TLS, the proxy transport must too.
	if p.httpClient.Transport != nil {
		if ht, ok := p.httpClient.Transport.(*http.Transport); ok && ht.TLSClientConfig != nil {
			t.TLSClientConfig = ht.TLSClientConfig.Clone()
		}
	}

	return t
}

// EnsureScheme adds "http://" if the address has no scheme.
// Deprecated: use pool.ResolveAddr which respects the pool's TLS setting.
func EnsureScheme(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}
