package envdclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// hostileServer streams an effectively endless body, counting the bytes the
// client actually pulled before it disconnected. A cap on the host-side read
// makes the client stop early, so served stays near the cap; without a cap the
// client drains up to the hard ceiling. The ceiling only exists so a REGRESSED
// (uncapped) build still terminates instead of hanging CI.
func hostileServer(t *testing.T, path string, ceiling int64, served *int64) *httptest.Server {
	t.Helper()
	// A JSON-object opening so a decoder starts consuming, then endless
	// whitespace/filler that never closes the object — the decoder keeps
	// reading until the source ends (or the cap cuts it off).
	prefix := []byte(`{"cpu_count":1,"cpu_used_pct":0,"net_bps":0,"disk_bps":0` + strings.Repeat(" ", 4096))
	chunk := make([]byte, 64<<10)
	for i := range chunk {
		chunk[i] = ' '
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		if _, err := w.Write(prefix); err != nil {
			return
		}
		atomic.AddInt64(served, int64(len(prefix)))
		if flusher != nil {
			flusher.Flush()
		}
		for atomic.LoadInt64(served) < ceiling {
			n, err := w.Write(chunk)
			atomic.AddInt64(served, int64(n))
			if err != nil {
				return // client disconnected — the cap did its job
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSec_FetchActivity_Capped is the regression guard for the guest→host OOM
// via uncapped reads. FetchActivity runs on the always-on 5s activity poller
// and decodes a guest-controlled /activity body. With the cap it returns
// promptly having read ~MaxEnvdControlBytes; without it (regressed build) the
// client drains toward the ceiling. FAILs pre-fix, PASSes post-fix.
func TestSec_FetchActivity_Capped(t *testing.T) {
	const ceiling = 512 << 20 // 512 MiB — only a backstop for a regressed build
	var served int64
	srv := hostileServer(t, "/activity", ceiling, &served)

	c := NewWithBaseURL(srv.URL)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// A hostile body never yields a valid object, so an error is expected;
		// what matters is that the call RETURNS instead of buffering forever.
		_, _ = c.FetchActivity(context.Background())
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("FetchActivity did not return: read %d MiB of an endless body (uncapped)", atomic.LoadInt64(&served)>>20)
	}

	// The client must have stopped near the cap, not near the ceiling. Allow
	// generous slack for socket/transport buffering past the LimitReader.
	limit := int64(MaxEnvdControlBytes) + 8<<20
	if got := atomic.LoadInt64(&served); got > limit {
		t.Fatalf("VULN: read %d MiB from one hostile /activity response (cap ~%d MiB) — unbounded",
			got>>20, int64(MaxEnvdControlBytes)>>20)
	}
	t.Logf("SECURE: FetchActivity returned after reading %d MiB, under the %d MiB cap + slack",
		atomic.LoadInt64(&served)>>20, limit>>20)
}

// TestSec_FetchVersion_Capped covers the per-create/restore /health version
// read on the same principle.
func TestSec_FetchVersion_Capped(t *testing.T) {
	const ceiling = 512 << 20
	var served int64
	srv := hostileServer(t, "/health", ceiling, &served)

	c := NewWithBaseURL(srv.URL)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.FetchVersion(context.Background())
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("FetchVersion did not return: read %d MiB (uncapped)", atomic.LoadInt64(&served)>>20)
	}

	limit := int64(MaxEnvdControlBytes) + 8<<20
	if got := atomic.LoadInt64(&served); got > limit {
		t.Fatalf("VULN: read %d MiB from one hostile /health response — unbounded", got>>20)
	}
}

func TestReadCapped(t *testing.T) {
	if _, err := readCapped(strings.NewReader(strings.Repeat("x", 100)), 50); err == nil {
		t.Fatal("readCapped: expected error when source exceeds cap")
	}
	data, err := readCapped(strings.NewReader("hello"), 50)
	if err != nil {
		t.Fatalf("readCapped: unexpected error: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("readCapped: got %q, want %q", data, "hello")
	}
}
