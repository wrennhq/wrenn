package sandbox

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// findChildPID reads the direct child PID of a given parent process.
// The Firecracker process is a direct child of the unshare wrapper because
// the init script uses `exec ip netns exec ... firecracker`, which replaces
// bash with ip-netns-exec, which in turn execs firecracker — same PID,
// direct child of unshare.
func findChildPID(parentPID int) (int, error) {
	path := fmt.Sprintf("/proc/%d/task/%d/children", parentPID, parentPID)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read children: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("no child processes found for PID %d", parentPID)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, fmt.Errorf("parse child PID %q: %w", fields[0], err)
	}
	return pid, nil
}

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

	// /proc/{pid}/stat format: pid (comm) state fields...
	// The comm field may contain spaces and parens, so find the last ')' first.
	content := string(data)
	idx := strings.LastIndex(content, ")")
	if idx < 0 {
		return cpuStat{}, fmt.Errorf("malformed /proc/%d/stat: no closing paren", pid)
	}
	// After ")" there is " state field3 field4 ... fieldN"
	// field1 after ')' is state (index 0), utime is field 11, stime is field 12
	// (0-indexed from after the closing paren).
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

// readMemRSS reads VmRSS from /proc/{pid}/status and returns bytes.
func readMemRSS(pid int) (int64, error) {
	path := fmt.Sprintf("/proc/%d/status", pid)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read status: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, fmt.Errorf("malformed VmRSS line")
			}
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse VmRSS: %w", err)
			}
			return kb * 1024, nil
		}
	}
	return 0, fmt.Errorf("VmRSS not found in /proc/%d/status", pid)
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
