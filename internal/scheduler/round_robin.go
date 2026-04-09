package scheduler

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/db"
)

// HostScheduler selects a host for a new sandbox. Implementations may use
// different strategies (round-robin, least-loaded, tag-based, etc.).
type HostScheduler interface {
	// SelectHost returns a host that can accept a new sandbox.
	// For BYOC teams (isByoc=true), only online BYOC hosts belonging to teamID
	// are considered. For non-BYOC teams, only online regular (platform) hosts
	// are considered. Returns an error if no suitable host is available.
	SelectHost(ctx context.Context, teamID pgtype.UUID, isByoc bool) (db.Host, error)
}

// RoundRobinScheduler cycles through eligible online hosts in round-robin order.
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

// SelectHost returns the next eligible online host in round-robin order.
func (s *RoundRobinScheduler) SelectHost(ctx context.Context, teamID pgtype.UUID, isByoc bool) (db.Host, error) {
	hosts, err := s.db.ListActiveHosts(ctx)
	if err != nil {
		return db.Host{}, fmt.Errorf("list hosts: %w", err)
	}

	var eligible []db.Host
	for _, h := range hosts {
		if h.Status != "online" || h.Address == "" {
			continue
		}
		if isByoc {
			// BYOC team: only use hosts belonging to this team.
			if h.Type != "byoc" || !h.TeamID.Valid || h.TeamID != teamID {
				continue
			}
		} else {
			// Non-BYOC team: only use platform (regular) hosts.
			if h.Type != "regular" {
				continue
			}
		}
		eligible = append(eligible, h)
	}

	if len(eligible) == 0 {
		if isByoc {
			return db.Host{}, fmt.Errorf("no online BYOC hosts available for team")
		}
		return db.Host{}, fmt.Errorf("no online platform hosts available")
	}

	idx := s.counter.Add(1) - 1
	return eligible[int(idx%int64(len(eligible)))], nil
}
