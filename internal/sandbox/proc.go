package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"git.omukk.dev/wrenn/wrenn/internal/envdclient"
)

// cpuStat holds raw CPU jiffies read from /proc/{pid}/stat.
type cpuStat struct {
	utime uint64
	stime uint64
}

// readCPUStat reads user and system CPU jiffies from /proc/{pid}/stat.
// Fields 14 (utime) and 15 (stime) are 1-indexed in the man page;
// after splitting on space, they are at indices 13 and 14.
func readCPUStat(pid int) (cpuStat, error) {
	path := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(path)
	if err != nil {
		return cpuStat{}, fmt.Errorf("read stat: %w", err)
	}

	content := string(data)
	idx := strings.LastIndex(content, ")")
	if idx < 0 {
		return cpuStat{}, fmt.Errorf("malformed /proc/%d/stat: no closing paren", pid)
	}
	fields := strings.Fields(content[idx+2:])
	if len(fields) < 13 {
		return cpuStat{}, fmt.Errorf("malformed /proc/%d/stat: too few fields (%d)", pid, len(fields))
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return cpuStat{}, fmt.Errorf("parse utime: %w", err)
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return cpuStat{}, fmt.Errorf("parse stime: %w", err)
	}
	return cpuStat{utime: utime, stime: stime}, nil
}

// envdMetrics holds metric values read from envd's /metrics endpoint.
type envdMetrics struct {
	MemBytes  int64
	DiskBytes int64
}

// readEnvdMetrics fetches mem_used and disk_used from envd's /metrics endpoint.
// Returns guest-side process memory (total - available) and filesystem usage
// from statfs("/"). These are the guest-visible metrics users care about.
func readEnvdMetrics(ctx context.Context, client *envdclient.Client) (envdMetrics, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.BaseURL()+"/metrics", nil)
	if err != nil {
		return envdMetrics{}, fmt.Errorf("build metrics request: %w", err)
	}

	resp, err := client.HTTPClient().Do(req)
	if err != nil {
		return envdMetrics{}, fmt.Errorf("fetch envd metrics: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return envdMetrics{}, fmt.Errorf("envd metrics: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return envdMetrics{}, fmt.Errorf("read envd metrics body: %w", err)
	}

	var m struct {
		MemUsed  int64 `json:"mem_used"`
		DiskUsed int64 `json:"disk_used"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return envdMetrics{}, fmt.Errorf("decode envd metrics: %w", err)
	}

	return envdMetrics{MemBytes: m.MemUsed, DiskBytes: m.DiskUsed}, nil
}
