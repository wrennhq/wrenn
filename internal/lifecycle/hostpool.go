package lifecycle

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/proto/hostagent/gen/hostagentv1connect"
)

// HostClientPool maintains a cache of Connect RPC clients keyed by host ID.
// Clients are created lazily on first access and evicted when a host is removed
// or goes unreachable. The pool is safe for concurrent use.
type HostClientPool struct {
	mu         sync.RWMutex
	clients    map[string]hostagentv1connect.HostAgentServiceClient
	httpClient *http.Client
}

// NewHostClientPool creates a new pool. The underlying HTTP client uses a
// 10-minute timeout to support long-running streaming operations.
func NewHostClientPool() *HostClientPool {
	return &HostClientPool{
		clients:    make(map[string]hostagentv1connect.HostAgentServiceClient),
		httpClient: &http.Client{Timeout: 10 * time.Minute},
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
	c = hostagentv1connect.NewHostAgentServiceClient(p.httpClient, EnsureScheme(address))
	p.clients[hostID] = c
	return c
}

// GetForHost is a convenience wrapper that extracts the address from a db.Host
// and returns an error if the host has no address recorded yet.
func (p *HostClientPool) GetForHost(h db.Host) (hostagentv1connect.HostAgentServiceClient, error) {
	if !h.Address.Valid || h.Address.String == "" {
		return nil, fmt.Errorf("host %s has no address", h.ID)
	}
	return p.Get(h.ID, h.Address.String), nil
}

// Evict removes the cached client for the given host, forcing a new client to be
// created on the next call to Get. Call this when a host's address changes or when
// a host is deleted.
func (p *HostClientPool) Evict(hostID string) {
	p.mu.Lock()
	delete(p.clients, hostID)
	p.mu.Unlock()
}

// EnsureScheme adds "http://" if the address has no scheme.
func EnsureScheme(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}
