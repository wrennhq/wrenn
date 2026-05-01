// SPDX-License-Identifier: Apache-2.0
// Modifications by M/S Omukk

package api

import (
	"net/http"
)

// PostSnapshotPrepare quiesces continuous goroutines (port scanner, forwarder),
// closes idle HTTP connections, and forces a GC cycle before Firecracker takes
// a VM snapshot. Closing connections prevents Go runtime corruption from stale
// TCP state after snapshot restore. Keep-alives are disabled so the current
// request's connection also closes after the response.
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

	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
