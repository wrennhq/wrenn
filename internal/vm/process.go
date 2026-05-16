package vm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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

// startProcessForRestore launches a bare Cloud Hypervisor process (no --restore).
// The restore is performed via the API after the socket is ready, which allows
// passing memory_restore_mode=OnDemand for UFFD lazy paging.
func startProcessForRestore(cfg *VMConfig) (*process, error) {
	script := buildRestoreScript(cfg)
	return launchScript(script, cfg)
}

func launchScript(script string, cfg *VMConfig) (*process, error) {
	execCtx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(execCtx, "unshare", "-m", "--", "bash", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start cloud-hypervisor process: %w", err)
	}

	p := &process{
		cmd:    cmd,
		cancel: cancel,
		exitCh: make(chan struct{}),
	}

	go func() {
		p.exitErr = cmd.Wait()
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
	return fmt.Sprintf(`
set -euo pipefail

mount --make-rprivate /

mkdir -p %[1]s
mount -t tmpfs tmpfs %[1]s

ln -s %[2]s %[1]s/vmlinux
ln -s %[3]s %[1]s/rootfs.ext4

exec ip netns exec %[4]s %[5]s --api-socket path=%[6]s
`,
		cfg.SandboxDir,       // 1
		cfg.KernelPath,       // 2
		cfg.RootfsPath,       // 3
		cfg.NetworkNamespace, // 4
		cfg.VMMBin,           // 5
		cfg.SocketPath,       // 6
	)
}

// buildRestoreScript generates the bash script for snapshot restore: sets up
// mount namespace, symlinks rootfs, and starts a bare Cloud Hypervisor process.
// The actual restore is done via the API (PUT /vm.restore) after the socket is
// ready, which enables memory_restore_mode=OnDemand for UFFD lazy paging.
func buildRestoreScript(cfg *VMConfig) string {
	return fmt.Sprintf(`
set -euo pipefail

mount --make-rprivate /

mkdir -p %[1]s
mount -t tmpfs tmpfs %[1]s

ln -s %[2]s %[1]s/rootfs.ext4

exec ip netns exec %[3]s %[4]s --api-socket path=%[5]s
`,
		cfg.SandboxDir,       // 1
		cfg.RootfsPath,       // 2
		cfg.NetworkNamespace, // 3
		cfg.VMMBin,           // 4
		cfg.SocketPath,       // 5
	)
}

// stop sends SIGTERM and waits for the process to exit. If it doesn't exit
// within 10 seconds, SIGKILL is sent.
func (p *process) stop() error {
	if p.cmd.Process == nil {
		return nil
	}

	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM); err != nil {
		slog.Debug("sigterm failed, process may have exited", "error", err)
	}

	select {
	case <-p.exitCh:
		return nil
	case <-time.After(10 * time.Second):
		slog.Warn("cloud-hypervisor did not exit after SIGTERM, sending SIGKILL")
		if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil {
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
