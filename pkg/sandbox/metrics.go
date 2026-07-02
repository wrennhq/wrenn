package sandbox

import (
	"sync"
	"time"
)

// MetricPoint holds one metrics sample.
type MetricPoint struct {
	Timestamp time.Time
	CPUPct    float64
	MemBytes  int64
	DiskBytes int64
}

// Ring buffer capacity constants.
const (
	ring10mCap = 600 // 1s × 600 = 10 min
	ring2hCap  = 240 // 30s × 240 = 2 h
	ring24hCap = 288 // 5min × 288 = 24 h

	downsample2hEvery  = 30 // 30 × 1s = 30s
	downsample24hEvery = 10 // 10 × 30s = 5min
)

// metricsRing holds three tiered ring buffers with automatic downsampling
// from the finest tier into coarser tiers.
type metricsRing struct {
	mu sync.Mutex

	// 10-minute tier: 500ms samples.
	buf10m   [ring10mCap]MetricPoint
	idx10m   int
	count10m int

	// 2-hour tier: 30s averages.
	buf2h   [ring2hCap]MetricPoint
	idx2h   int
	count2h int

	// 24-hour tier: 5min averages.
	buf24h   [ring24hCap]MetricPoint
	idx24h   int
	count24h int

	// Accumulators for downsampling.
	acc1s  [downsample2hEvery]MetricPoint
	acc1sN int

	acc30s  [downsample24hEvery]MetricPoint
	acc30sN int
}

// newMetricsRing creates an empty metrics ring buffer.
func newMetricsRing() *metricsRing {
	return &metricsRing{}
}

// Push adds a 1s sample to the finest tier and triggers downsampling
// into coarser tiers when enough samples have accumulated.
func (r *metricsRing) Push(p MetricPoint) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Write to 10m ring.
	r.buf10m[r.idx10m] = p
	r.idx10m = (r.idx10m + 1) % ring10mCap
	if r.count10m < ring10mCap {
		r.count10m++
	}

	// Accumulate for 2h downsample.
	r.acc1s[r.acc1sN] = p
	r.acc1sN++
	if r.acc1sN == downsample2hEvery {
		avg := averagePoints(r.acc1s[:downsample2hEvery])
		r.push2h(avg)
		r.acc1sN = 0
	}
}

func (r *metricsRing) push2h(p MetricPoint) {
	r.buf2h[r.idx2h] = p
	r.idx2h = (r.idx2h + 1) % ring2hCap
	if r.count2h < ring2hCap {
		r.count2h++
	}

	// Accumulate for 24h downsample.
	r.acc30s[r.acc30sN] = p
	r.acc30sN++
	if r.acc30sN == downsample24hEvery {
		avg := averagePoints(r.acc30s[:downsample24hEvery])
		r.push24h(avg)
		r.acc30sN = 0
	}
}

func (r *metricsRing) push24h(p MetricPoint) {
	r.buf24h[r.idx24h] = p
	r.idx24h = (r.idx24h + 1) % ring24hCap
	if r.count24h < ring24hCap {
		r.count24h++
	}
}

// Get10m returns the 10-minute tier points in chronological order.
func (r *metricsRing) Get10m() []MetricPoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readRing(r.buf10m[:], r.idx10m, r.count10m)
}

// Get2h returns the 2-hour tier points in chronological order.
func (r *metricsRing) Get2h() []MetricPoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readRing(r.buf2h[:], r.idx2h, r.count2h)
}

// Get24h returns the 24-hour tier points in chronological order.
func (r *metricsRing) Get24h() []MetricPoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readRing(r.buf24h[:], r.idx24h, r.count24h)
}

// Flush returns all three tiers and resets the ring buffer.
func (r *metricsRing) Flush() (pts10m, pts2h, pts24h []MetricPoint) {
	r.mu.Lock()
	defer r.mu.Unlock()

	pts10m = r.readRing(r.buf10m[:], r.idx10m, r.count10m)
	pts2h = r.readRing(r.buf2h[:], r.idx2h, r.count2h)
	pts24h = r.readRing(r.buf24h[:], r.idx24h, r.count24h)

	// Reset all state.
	r.idx10m, r.count10m = 0, 0
	r.idx2h, r.count2h = 0, 0
	r.idx24h, r.count24h = 0, 0
	r.acc1sN = 0
	r.acc30sN = 0

	return pts10m, pts2h, pts24h
}

// readRing extracts elements from a circular buffer in chronological order.
func (r *metricsRing) readRing(buf []MetricPoint, nextIdx, count int) []MetricPoint {
	if count == 0 {
		return nil
	}
	result := make([]MetricPoint, count)
	bufLen := len(buf)
	start := (nextIdx - count + bufLen) % bufLen
	for i := range count {
		result[i] = buf[(start+i)%bufLen]
	}
	return result
}

// averagePoints computes the average of a slice of MetricPoints.
// The timestamp is set to the last point's timestamp.
func averagePoints(pts []MetricPoint) MetricPoint {
	n := float64(len(pts))
	var cpu float64
	var mem, disk int64
	for _, p := range pts {
		cpu += p.CPUPct
		mem += p.MemBytes
		disk += p.DiskBytes
	}
	return MetricPoint{
		Timestamp: pts[len(pts)-1].Timestamp,
		CPUPct:    cpu / n,
		MemBytes:  int64(float64(mem) / n),
		DiskBytes: int64(float64(disk) / n),
	}
}
