package vm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// process represents a running Cloud Hypervisor process with mount and network
// namespace isolation.
type process struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc

	exitCh  chan struct{}
	exitErr error
	logFile *os.File

	// attachedPID is set instead of cmd when the CH process was started by an
	// earlier process and re-attached via Manager.Reattach. A non-child cannot
	// be cmd.Wait()ed; exit detection falls back to kill(pid, 0) polling.
	attachedPID int
}

// pid returns the process ID regardless of how the process was obtained.
func (p *process) pid() int {
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return p.attachedPID
}

// startProcess launches the Cloud Hypervisor binary inside an isolated mount
// namespace and the specified network namespace. Used for fresh boot (no
// snapshot). The launch sequence:
//
//  1. unshare -m: creates a private mount namespace
//  2. mount --make-rprivate /: prevents mount propagation to host
//  3. mount tmpfs at SandboxDir: ephemeral workspace for this VM
//  4. symlink kernel and rootfs into SandboxDir
//  5. ip netns exec <ns>: enters the network namespace where TAP is configured
//  6. exec cloud-hypervisor with the API socket path
func startProcess(cfg *VMConfig) (*process, error) {
	script := buildStartScript(cfg)
	return launchScript(script, cfg)
}

// startRestoreProcess launches CH in restore mode. It mirrors startProcess
// for namespace/tmpfs/symlink setup so the disk paths recorded in the
// snapshot's config.json remain valid, then execs CH with `--restore`.
func startRestoreProcess(cfg *VMConfig) (*process, error) {
	script := buildRestoreScript(cfg)
	return launchScript(script, cfg)
}

func launchScript(script string, cfg *VMConfig) (*process, error) {
	execCtx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(execCtx, "unshare", "-m", "--", "bash", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	var logFile *os.File
	if cfg.LogDir != "" {
		logPath := fmt.Sprintf("%s/ch-%s.log", cfg.LogDir, cfg.SandboxID)
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("open CH log file %s: %w", logPath, err)
		}
		cmd.Stdout = f
		cmd.Stderr = f
		logFile = f
	}

	if err := cmd.Start(); err != nil {
		cancel()
		if logFile != nil {
			logFile.Close()
		}
		return nil, fmt.Errorf("start cloud-hypervisor process: %w", err)
	}

	p := &process{
		cmd:     cmd,
		cancel:  cancel,
		exitCh:  make(chan struct{}),
		logFile: logFile,
	}

	go func() {
		p.exitErr = cmd.Wait()
		if p.logFile != nil {
			p.logFile.Close()
		}
		close(p.exitCh)
	}()

	slog.Info("cloud-hypervisor process started",
		"pid", cmd.Process.Pid,
		"sandbox", cfg.SandboxID,
	)

	return p, nil
}

// buildStartScript generates the bash script for fresh boot: sets up mount
// namespace, symlinks kernel/rootfs, and execs Cloud Hypervisor.
func buildStartScript(cfg *VMConfig) string {
	return buildLaunchScript(cfg, "")
}

// buildRestoreScript generates the bash script for restoring a VM from a
// snapshot directory. The mount/symlink prelude is identical to fresh boot
// so disk paths in the snapshot config.json resolve correctly.
func buildRestoreScript(cfg *VMConfig) string {
	dir := strings.TrimRight(cfg.RestoreFromDir, "/")
	restoreArg := fmt.Sprintf("--restore source_url=file://%s/", dir)
	if cfg.RestoreLazyMemory {
		restoreArg += ",memory_restore_mode=ondemand"
	}
	return buildLaunchScript(cfg, restoreArg)
}

// buildLaunchScript composes the namespace/tmpfs/symlink prelude and the
// final cloud-hypervisor exec line. extraArgs is appended verbatim — used
// to inject `--restore source_url=...` for restore launches.
func buildLaunchScript(cfg *VMConfig, extraArgs string) string {
	chCmd := fmt.Sprintf("ip netns exec %s %s --api-socket path=%s",
		cfg.NetworkNamespace, cfg.VMMBin, cfg.SocketPath)
	if extraArgs != "" {
		chCmd += " " + extraArgs
	}

	// One symlink per data volume, pointing the stable in-namespace path (baked
	// into CH's config.json) at the real backing file on the host. Recreated
	// identically on every restore so the snapshot's disk paths resolve.
	var volumeLinks strings.Builder
	for _, v := range cfg.Volumes {
		fmt.Fprintf(&volumeLinks, "ln -s %s %s/%s\n", v.HostPath, cfg.SandboxDir, v.symlinkName())
	}

	return fmt.Sprintf(`
set -euo pipefail

mount --make-rprivate /

mkdir -p %[1]s
mount -t tmpfs tmpfs %[1]s

ln -s %[2]s %[1]s/vmlinux
ln -s %[3]s %[1]s/rootfs.ext4
%[5]s
exec %[4]s
`,
		cfg.SandboxDir,       // 1
		cfg.KernelPath,       // 2
		cfg.RootfsPath,       // 3
		chCmd,                // 4
		volumeLinks.String(), // 5
	)
}

// stop sends SIGTERM and waits for the process to exit. If it doesn't exit
// within 10 seconds, SIGKILL is sent. Signals target the process group
// (Setsid at launch), which works for both child and re-attached processes.
func (p *process) stop() error {
	pid := p.pid()
	if pid == 0 {
		return nil
	}

	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		slog.Debug("sigterm failed, process may have exited", "error", err)
	}

	select {
	case <-p.exitCh:
		return nil
	case <-time.After(10 * time.Second):
		slog.Warn("cloud-hypervisor did not exit after SIGTERM, sending SIGKILL")
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			slog.Debug("sigkill failed", "error", err)
		}
		<-p.exitCh
		return nil
	}
}

// exited returns a channel that is closed when the process exits.
func (p *process) exited() <-chan struct{} {
	return p.exitCh
}
