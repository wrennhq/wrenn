// SPDX-License-Identifier: Apache-2.0
// Modifications by M/S Omukk

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

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

	a.logger.Trace().Msg("Health check")

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(map[string]string{
		"version": a.version,
	})
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
