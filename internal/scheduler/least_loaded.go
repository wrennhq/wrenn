package scheduler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/db"
)

// Resource overhead reserved for the host OS.
const (
	reservedMemoryMB = 8192
	reservedCPU      = 4
	reservedDiskMB   = 30720 // 30 GB
	cpuOvercommit    = 1.5
	pausedMemoryFrac = 0.5
	pausedDiskFrac   = 2.0 / 3.0
)

// LeastLoadedScheduler picks the online host with the most headroom at its
// tightest resource (bottleneck-first strategy).
//
// For each eligible host it computes the remaining fraction of each resource:
//
//	RAM:  usable / total  where total = host.memory_mb - 8192
//	CPU:  usable / total  where total = host.cpu_cores * 1.5 - 4
//	Disk: usable / total  where total = host.disk_gb * 1024 - 30720
//
// The host's score is min(ram_frac, cpu_frac, disk_frac). The host with the
// highest score wins. Admission control rejects when no host can fit the
// requested sandbox on RAM or disk; CPU overcommit is allowed.
type LeastLoadedScheduler struct {
	db *db.Queries
}

// NewLeastLoadedScheduler creates a LeastLoadedScheduler backed by the given DB.
func NewLeastLoadedScheduler(queries *db.Queries) *LeastLoadedScheduler {
	return &LeastLoadedScheduler{db: queries}
}

// hostResources holds the computed resource availability for a single host.
type hostResources struct {
	host       db.Host
	ramTotal   float64
	ramUsable  float64
	cpuTotal   float64
	cpuUsable  float64
	diskTotal  float64
	diskUsable float64
}

// bottleneckScore returns the fraction of the tightest resource remaining.
func (h *hostResources) bottleneckScore() float64 {
	ramFrac := safeFrac(h.ramUsable, h.ramTotal)
	cpuFrac := safeFrac(h.cpuUsable, h.cpuTotal)
	diskFrac := safeFrac(h.diskUsable, h.diskTotal)
	return min(ramFrac, cpuFrac, diskFrac)
}

// safeFrac returns usable/total, or 0 when total <= 0.
func safeFrac(usable, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return usable / total
}

// SelectHost returns the eligible host with the most resource headroom.
func (s *LeastLoadedScheduler) SelectHost(ctx context.Context, teamID pgtype.UUID, isByoc bool, memoryMb, diskSizeMb int32) (db.Host, error) {
	rows, err := s.db.GetHostsWithLoad(ctx)
	if err != nil {
		return db.Host{}, fmt.Errorf("get hosts with load: %w", err)
	}

	// Phase 1: filter eligible hosts and compute resources.
	var candidates []hostResources
	for i := range rows {
		row := &rows[i]

		if isByoc {
			if row.Type != "byoc" || !row.TeamID.Valid || row.TeamID != teamID {
				continue
			}
		} else {
			if row.Type != "regular" {
				continue
			}
		}

		hr := computeResources(row)
		candidates = append(candidates, hr)
	}

	if len(candidates) == 0 {
		if isByoc {
			return db.Host{}, fmt.Errorf("no online BYOC hosts available for team")
		}
		return db.Host{}, fmt.Errorf("no online platform hosts available")
	}

	// Phase 2: admission control + selection — pick the highest-scoring host
	// that can actually fit the requested sandbox (RAM and disk).
	best := -1
	bestScore := 0.0
	for i := range candidates {
		if memoryMb > 0 && candidates[i].ramUsable < float64(memoryMb) {
			continue
		}
		if diskSizeMb > 0 && candidates[i].diskUsable < float64(diskSizeMb) {
			continue
		}
		score := candidates[i].bottleneckScore()
		if best == -1 || score > bestScore {
			best = i
			bestScore = score
		}
	}

	if best == -1 {
		return db.Host{}, fmt.Errorf("no host has sufficient resources: need %d MB memory, %d MB disk", memoryMb, diskSizeMb)
	}

	return candidates[best].host, nil
}

// computeResources converts a raw DB row into computed resource availability.
func computeResources(row *db.GetHostsWithLoadRow) hostResources {
	ramTotal := float64(row.MemoryMb) - reservedMemoryMB
	cpuTotal := float64(row.CpuCores)*cpuOvercommit - reservedCPU
	diskTotal := float64(row.DiskGb)*1024 - reservedDiskMB

	usedMemory := float64(row.RunningMemoryMb) + pausedMemoryFrac*float64(row.PausedMemoryMb)
	usedCPU := float64(row.RunningVcpus)
	usedDisk := float64(row.RunningDiskMb) + pausedDiskFrac*float64(row.PausedDiskMb)

	return hostResources{
		host:       hostFromRow(row),
		ramTotal:   ramTotal,
		ramUsable:  ramTotal - usedMemory,
		cpuTotal:   cpuTotal,
		cpuUsable:  cpuTotal - usedCPU,
		diskTotal:  diskTotal,
		diskUsable: diskTotal - usedDisk,
	}
}

// hostFromRow converts the query row back to a plain db.Host.
func hostFromRow(r *db.GetHostsWithLoadRow) db.Host {
	return db.Host{
		ID:               r.ID,
		Type:             r.Type,
		TeamID:           r.TeamID,
		Provider:         r.Provider,
		AvailabilityZone: r.AvailabilityZone,
		Arch:             r.Arch,
		CpuCores:         r.CpuCores,
		MemoryMb:         r.MemoryMb,
		DiskGb:           r.DiskGb,
		Address:          r.Address,
		Status:           r.Status,
		LastHeartbeatAt:  r.LastHeartbeatAt,
		Metadata:         r.Metadata,
		CreatedBy:        r.CreatedBy,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
		CertFingerprint:  r.CertFingerprint,
		CertExpiresAt:    r.CertExpiresAt,
	}
}
