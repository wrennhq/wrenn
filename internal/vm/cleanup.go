package vm

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// CleanupStaleProcesses kills any cloud-hypervisor processes left behind by a
// previous agent that crashed without graceful shutdown. Must run at agent
// startup before devicemapper.CleanupStaleDevices — a still-running CH process
// holds the dm-snapshot open and would cause "Device or resource busy" on
// dmsetup remove.
//
// Matches processes by argv containing the wrenn CH API socket path
// (/tmp/ch-<sandboxID>.sock) so we don't kill unrelated cloud-hypervisor VMs
// the operator may be running.
//
// Also removes stale /tmp/ch-*.sock files once the owning process is gone.
func CleanupStaleProcesses() {
	socketPattern := regexp.MustCompile(`/tmp/ch-[A-Za-z0-9-]+\.sock`)

	pids, err := scanProcs()
	if err != nil {
		slog.Debug("scan procs failed", "error", err)
		return
	}

	killed := 0
	for _, pid := range pids {
		cmdline, err := readCmdline(pid)
		if err != nil {
			continue
		}
		if !strings.Contains(cmdline, "cloud-hypervisor") {
			continue
		}
		if !socketPattern.MatchString(cmdline) {
			continue
		}
		slog.Warn("killing stale cloud-hypervisor process", "pid", pid, "cmdline", cmdline)
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			slog.Warn("SIGTERM stale CH failed", "pid", pid, "error", err)
		}
		killed++
	}

	// Give SIGTERM'd processes a brief window to exit so subsequent dm/loop
	// teardown sees no open fd, then SIGKILL anything still alive.
	if killed > 0 {
		time.Sleep(500 * time.Millisecond)
		for _, pid := range pids {
			cmdline, err := readCmdline(pid)
			if err != nil {
				continue
			}
			if !strings.Contains(cmdline, "cloud-hypervisor") || !socketPattern.MatchString(cmdline) {
				continue
			}
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(200 * time.Millisecond)
	}

	matches, _ := filepath.Glob("/tmp/ch-*.sock")
	for _, sock := range matches {
		if err := os.Remove(sock); err == nil {
			slog.Info("removed stale CH socket", "path", sock)
		}
	}
}

func scanProcs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

func readCmdline(pid int) (string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return "", err
	}
	// /proc/<pid>/cmdline is NUL-separated; convert to spaces for substring match.
	return strings.ReplaceAll(string(b), "\x00", " "), nil
}
