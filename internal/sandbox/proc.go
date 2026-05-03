package sandbox

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

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

// readEnvdMemUsed fetches mem_used from envd's /metrics endpoint. Returns
// guest-side total - MemAvailable (actual process memory, excluding reclaimable
// page cache). VmRSS of the Firecracker process includes guest page cache and
// never decreases, so this is the accurate metric for dashboard display.
func readEnvdMemUsed(client *envdclient.Client) (int64, error) {
	resp, err := client.HTTPClient().Get(client.BaseURL() + "/metrics")
	if err != nil {
		return 0, fmt.Errorf("fetch envd metrics: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("envd metrics: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read envd metrics body: %w", err)
	}

	var m struct {
		MemUsed int64 `json:"mem_used"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return 0, fmt.Errorf("decode envd metrics: %w", err)
	}

	return m.MemUsed, nil
}

// readDiskAllocated returns the actual allocated bytes (not apparent size)
// of the file at path. This uses stat's block count × 512.
func readDiskAllocated(path string) (int64, error) {
	var stat syscall.Stat_t
	if err := syscall.Stat(path, &stat); err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	return stat.Blocks * 512, nil
}
