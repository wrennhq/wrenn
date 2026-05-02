// SPDX-License-Identifier: Apache-2.0
// Modifications by M/S Omukk

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog"

	"git.omukk.dev/wrenn/sandbox/envd/internal/execcontext"
	"git.omukk.dev/wrenn/sandbox/envd/internal/host"
	publicport "git.omukk.dev/wrenn/sandbox/envd/internal/port"
	"git.omukk.dev/wrenn/sandbox/envd/internal/utils"
)

// MMDSClient provides access to MMDS metadata.
type MMDSClient interface {
	GetAccessTokenHash(ctx context.Context) (string, error)
}

// DefaultMMDSClient is the production implementation that calls the real MMDS endpoint.
type DefaultMMDSClient struct{}

func (c *DefaultMMDSClient) GetAccessTokenHash(ctx context.Context) (string, error) {
	return host.GetAccessTokenHashFromMMDS(ctx)
}

type API struct {
	isNotFC     bool
	logger      *zerolog.Logger
	accessToken *SecureToken
	defaults    *execcontext.Defaults
	version     string

	mmdsChan      chan *host.MMDSOpts
	hyperloopLock sync.Mutex
	mmdsClient    MMDSClient

	lastSetTime *utils.AtomicMax
	initLock    sync.Mutex

	// rootCtx is the parent context from main(), used to restart
	// long-lived goroutines after snapshot restore.
	rootCtx       context.Context
	portSubsystem *publicport.PortSubsystem
	connTracker   *ServerConnTracker

	// needsRestore is set by PostSnapshotPrepare and cleared on the first
	// health check or PostInit after restore. While set, GOMAXPROCS is 1
	// to prevent concurrent page allocator access during the freeze window.
	needsRestore   atomic.Bool
	prevGOMAXPROCS int // GOMAXPROCS value before PrepareSnapshot reduced it to 1
}

func New(l *zerolog.Logger, defaults *execcontext.Defaults, mmdsChan chan *host.MMDSOpts, isNotFC bool, rootCtx context.Context, portSubsystem *publicport.PortSubsystem, connTracker *ServerConnTracker, version string) *API {
	return &API{
		logger:        l,
		defaults:      defaults,
		mmdsChan:      mmdsChan,
		isNotFC:       isNotFC,
		mmdsClient:    &DefaultMMDSClient{},
		lastSetTime:   utils.NewAtomicMax(),
		accessToken:   &SecureToken{},
		rootCtx:       rootCtx,
		portSubsystem: portSubsystem,
		connTracker:   connTracker,
		version:       version,
	}
}

func (a *API) GetHealth(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	// On the first health check after snapshot restore, re-enable GC and
	// clean up stale state. By this point, any goroutine that was mid-
	// allocation when the VM was frozen has completed, so the page allocator
	// summary tree is consistent and safe for GC to read.
	if a.needsRestore.CompareAndSwap(true, false) {
		a.postRestoreRecovery()
	}

	a.logger.Trace().Msg("Health check")

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(map[string]string{
		"version": a.version,
	})
}

// postRestoreRecovery restores GOMAXPROCS, runs a clean GC cycle, closes
// zombie TCP connections from before the snapshot, re-enables HTTP keep-alives,
// and restarts the port subsystem. Called exactly once per restore cycle,
// guarded by a CAS on needsRestore in both GetHealth and PostInit.
func (a *API) postRestoreRecovery() {
	// Restore parallelism first — any goroutine that was mid-allocation
	// when the VM froze has already completed by the time a health check
	// or PostInit request is being served, so the page allocator summary
	// tree is consistent and safe for a full GC.
	prev := a.prevGOMAXPROCS
	if prev > 0 {
		runtime.GOMAXPROCS(prev)
	}
	runtime.GC()
	runtime.GC()
	debug.FreeOSMemory()
	a.logger.Info().Msg("restore: GOMAXPROCS restored, GC complete")

	if a.connTracker != nil {
		a.connTracker.RestoreAfterSnapshot()
		a.logger.Info().Msg("restore: zombie connections closed, keep-alives re-enabled")
	}

	if a.portSubsystem != nil {
		a.portSubsystem.Start(a.rootCtx)
		a.logger.Info().Msg("restore: port subsystem restarted")
	}
}

func (a *API) GetMetrics(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	a.logger.Trace().Msg("Get metrics")

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")

	metrics, err := host.GetMetrics()
	if err != nil {
		a.logger.Error().Err(err).Msg("Failed to get metrics")
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(metrics); err != nil {
		a.logger.Error().Err(err).Msg("Failed to encode metrics")
	}
}

func (a *API) getLogger(err error) *zerolog.Event {
	if err != nil {
		return a.logger.Error().Err(err) //nolint:zerologlint // this is only prep
	}

	return a.logger.Info() //nolint:zerologlint // this is only prep
}
