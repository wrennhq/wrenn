package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.omukk.dev/wrenn/wrenn/pkg/envdclient"
)

// preloadServer fakes envd's /memory/preload endpoint. GET returns the states
// in sequence (last one repeats); POST marks the loader started and returns
// the current state.
type preloadServer struct {
	states []string
	idx    atomic.Int64
	posts  atomic.Int64
	gets   atomic.Int64
}

func (s *preloadServer) handler(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/memory/preload") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.posts.Add(1)
	case http.MethodGet:
		s.gets.Add(1)
	}
	i := s.idx.Load()
	if int(i) >= len(s.states) {
		i = int64(len(s.states) - 1)
	} else {
		s.idx.Add(1)
	}
	state := s.states[i]
	resp := map[string]any{"state": state}
	if state == "failed" {
		resp["error"] = "kcore walk read no pages"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func TestEnsureMemoryMaterialized(t *testing.T) {
	tests := []struct {
		name      string
		lazy      bool
		nilClient bool
		states    []string // envd responses in order
		wantErr   string   // substring; "" = expect nil
		wantPosts int64    // exact POST count; -1 = don't check
	}{
		{
			name:      "cold boot is a no-op",
			lazy:      false,
			states:    []string{"idle"},
			wantErr:   "",
			wantPosts: 0,
		},
		{
			name:      "lazy and already done",
			lazy:      true,
			states:    []string{"done"},
			wantErr:   "",
			wantPosts: 0,
		},
		{
			name:      "lazy idle self-heals via start",
			lazy:      true,
			states:    []string{"idle", "running", "done", "done"},
			wantErr:   "",
			wantPosts: 1,
		},
		{
			name:      "lazy failed is a hard error",
			lazy:      true,
			states:    []string{"failed"},
			wantErr:   "memory preload failed",
			wantPosts: 0,
		},
		{
			name:      "lazy start ends in failed",
			lazy:      true,
			states:    []string{"idle", "idle", "failed"},
			wantErr:   "memory preload failed",
			wantPosts: -1,
		},
		{
			name:      "lazy cancelled is a hard error",
			lazy:      true,
			states:    []string{"cancelled"},
			wantErr:   "memory preload cancelled",
			wantPosts: 0,
		},
		{
			name:      "lazy without client fails",
			lazy:      true,
			nilClient: true,
			wantErr:   "envd client unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := &preloadServer{states: tt.states}
			ts := httptest.NewServer(http.HandlerFunc(srv.handler))
			defer ts.Close()

			m := &Manager{}
			sb := &sandboxState{lazyRestore: tt.lazy}
			if !tt.nilClient {
				sb.client.Store(envdclient.NewWithBaseURL(ts.URL))
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := m.ensureMemoryMaterialized(ctx, sb)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ensureMemoryMaterialized: %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
			}
			if tt.wantPosts >= 0 && srv.posts.Load() != tt.wantPosts {
				t.Fatalf("POST count = %d, want %d", srv.posts.Load(), tt.wantPosts)
			}
			if !tt.lazy && srv.gets.Load() != 0 {
				t.Fatalf("cold boot must not query envd, got %d GETs", srv.gets.Load())
			}
		})
	}
}

// ensureMemoryMaterialized must first drain an in-process loader goroutine
// before consulting envd, and must honor ctx cancellation while blocked on it.
func TestEnsureMemoryMaterializedWaitsForLoader(t *testing.T) {
	m := &Manager{}
	sb := &sandboxState{lazyRestore: true}
	sb.memLoadDone = make(chan struct{}) // never closed

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := m.ensureMemoryMaterialized(ctx, sb)
	if err == nil || !strings.Contains(err.Error(), "wait for memory loader") {
		t.Fatalf("want loader-wait timeout error, got %v", err)
	}
}
