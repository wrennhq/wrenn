package api

import (
	"net"
	"net/http"
	"sync"
)

// ServerConnTracker tracks active HTTP connections via http.Server.ConnState.
// Before a Firecracker snapshot, it closes idle connections, disables
// keep-alives, and records which connections existed pre-snapshot. After
// restore, it closes ALL pre-snapshot connections (they are zombie TCP
// sockets) while leaving post-restore connections (like the /init request)
// untouched.
type ServerConnTracker struct {
	mu          sync.Mutex
	conns       map[net.Conn]http.ConnState
	preSnapshot map[net.Conn]struct{}
	srv         *http.Server
}

func NewServerConnTracker() *ServerConnTracker {
	return &ServerConnTracker{
		conns: make(map[net.Conn]http.ConnState),
	}
}

// SetServer stores a reference to the http.Server for keep-alive control.
// Must be called before ListenAndServe.
func (t *ServerConnTracker) SetServer(srv *http.Server) {
	t.mu.Lock()
	t.srv = srv
	t.mu.Unlock()
}

// Track implements the http.Server.ConnState callback signature.
func (t *ServerConnTracker) Track(conn net.Conn, state http.ConnState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch state {
	case http.StateNew, http.StateActive, http.StateIdle:
		t.conns[conn] = state
	case http.StateHijacked, http.StateClosed:
		delete(t.conns, conn)
		delete(t.preSnapshot, conn)
	}
}

// PrepareForSnapshot closes idle connections, disables keep-alives, and
// records all remaining active connections. After the response completes
// (with keep-alives disabled, the connection closes), RestoreAfterSnapshot
// will close any that survived into the snapshot as zombie TCP sockets.
//
// GC cycles are handled by PortSubsystem.Stop() which runs before this.
func (t *ServerConnTracker) PrepareForSnapshot() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.srv != nil {
		t.srv.SetKeepAlivesEnabled(false)
	}

	t.preSnapshot = make(map[net.Conn]struct{}, len(t.conns))
	for conn, state := range t.conns {
		if state == http.StateIdle {
			conn.Close()
			delete(t.conns, conn)
		} else {
			t.preSnapshot[conn] = struct{}{}
		}
	}
}

// RestoreAfterSnapshot closes ALL pre-snapshot connections (zombie TCP
// sockets after restore) and re-enables keep-alives. Post-restore
// connections (like the /init request that triggers this call) are not
// in the preSnapshot set and are left untouched.
//
// Safe to call on first boot — preSnapshot is nil, so this is a no-op
// aside from enabling keep-alives (which are already enabled by default).
func (t *ServerConnTracker) RestoreAfterSnapshot() {
	t.mu.Lock()
	defer t.mu.Unlock()

	for conn := range t.preSnapshot {
		conn.Close()
		delete(t.conns, conn)
	}
	t.preSnapshot = nil

	if t.srv != nil {
		t.srv.SetKeepAlivesEnabled(true)
	}
}
