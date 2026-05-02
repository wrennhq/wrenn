// SPDX-License-Identifier: Apache-2.0
// Modifications by M/S Omukk

package api

import (
	"net/http"
	"runtime"
	"runtime/debug"
)

// PostSnapshotPrepare quiesces continuous goroutines (port scanner, forwarder),
// closes idle HTTP connections, and forces a GC cycle before Firecracker takes
// a VM snapshot. Closing connections prevents Go runtime corruption from stale
// TCP state after snapshot restore. Keep-alives are disabled so the current
// request's connection also closes after the response.
//
// To prevent Go page allocator corruption, GOMAXPROCS is set to 1 after the
// final GC. With a single P, all goroutines (including any that allocate
// between now and the VM freeze) run sequentially. This eliminates concurrent
// page allocator access, so even if the freeze lands mid-allocation, the
// in-flight operation completes atomically on restore before any GC reads
// the summary tree. GOMAXPROCS is restored on the first health check after
// restore (see postRestoreRecovery).
//
// Called by the host agent as a best-effort signal before vm.Pause().
func (a *API) PostSnapshotPrepare(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	if a.portSubsystem != nil {
		a.portSubsystem.Stop()
		a.logger.Info().Msg("snapshot/prepare: port subsystem quiesced")
	}

	if a.connTracker != nil {
		a.connTracker.PrepareForSnapshot()
		a.logger.Info().Msg("snapshot/prepare: idle connections closed, keep-alives disabled")
	}

	// Send the response before the GC so HTTP buffer allocations happen
	// while GOMAXPROCS is still at its normal value.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Final GC pass after all major allocations (connection cleanup,
	// response write) are complete.
	runtime.GC()
	runtime.GC()
	debug.FreeOSMemory()

	// Reduce to a single P so any post-GC allocations (HTTP server
	// connection teardown) run sequentially — no concurrent page allocator
	// access that could leave the summary tree inconsistent if the VM
	// freezes mid-update.
	a.prevGOMAXPROCS = runtime.GOMAXPROCS(1)

	a.needsRestore.Store(true)
	a.logger.Info().Msg("snapshot/prepare: GOMAXPROCS=1, ready for freeze")
}
