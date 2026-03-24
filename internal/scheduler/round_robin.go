package scheduler

import (
	"context"
	"fmt"
	"sync/atomic"

	"git.omukk.dev/wrenn/sandbox/internal/db"
)

// HostScheduler selects a host for a new sandbox. Implementations may use
// different strategies (round-robin, least-loaded, tag-based, etc.).
type HostScheduler interface {
	// SelectHost returns a host that can accept a new sandbox.
	// Returns an error if no suitable host is available.
	SelectHost(ctx context.Context) (db.Host, error)
}

// RoundRobinScheduler cycles through online hosts in round-robin order.
// It re-fetches the host list on every call so that newly registered or
// recovered hosts are considered immediately.
type RoundRobinScheduler struct {
	db      *db.Queries
	counter atomic.Int64
}

// NewRoundRobinScheduler creates a RoundRobinScheduler backed by the given DB.
func NewRoundRobinScheduler(queries *db.Queries) *RoundRobinScheduler {
	return &RoundRobinScheduler{db: queries}
}

// SelectHost returns the next online host in round-robin order.
func (s *RoundRobinScheduler) SelectHost(ctx context.Context) (db.Host, error) {
	hosts, err := s.db.ListActiveHosts(ctx)
	if err != nil {
		return db.Host{}, fmt.Errorf("list hosts: %w", err)
	}

	var online []db.Host
	for _, h := range hosts {
		if h.Status == "online" && h.Address.Valid && h.Address.String != "" {
			online = append(online, h)
		}
	}
	if len(online) == 0 {
		return db.Host{}, fmt.Errorf("no online hosts available")
	}

	idx := s.counter.Add(1) - 1
	return online[int(idx%int64(len(online)))], nil
}
