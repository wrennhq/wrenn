// SPDX-License-Identifier: Apache-2.0
// Modifications by M/S Omukk

package api

import (
	"net/http"
)

// PostSnapshotPrepare quiesces continuous goroutines (port scanner, forwarder)
// and forces a GC cycle before Firecracker takes a VM snapshot. This ensures
// the Go runtime's page allocator is in a consistent state when vCPUs are frozen.
//
// Called by the host agent as a best-effort signal before vm.Pause().
func (a *API) PostSnapshotPrepare(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	if a.portSubsystem != nil {
		a.portSubsystem.Stop()
		a.logger.Info().Msg("snapshot/prepare: port subsystem quiesced")
	}

	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
