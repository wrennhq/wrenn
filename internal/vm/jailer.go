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

// process represents a running Firecracker process with mount and network
// namespace isolation.
type process struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc

	exitCh  chan struct{}
	exitErr error
}

// startProcess launches the Firecracker binary inside an isolated mount namespace
// and the specified network namespace. The launch sequence:
//
//  1. unshare -m: creates a private mount namespace
//  2. mount --make-rprivate /: prevents mount propagation to host
//  3. mount tmpfs at SandboxDir: ephemeral workspace for this VM
//  4. symlink kernel and rootfs into SandboxDir
//  5. ip netns exec <ns>: enters the network namespace where TAP is configured
//  6. exec firecracker with the API socket path
func startProcess(ctx context.Context, cfg *VMConfig) (*process, error) {
	execCtx, cancel := context.WithCancel(ctx)

	script := buildStartScript(cfg)

	cmd := exec.CommandContext(execCtx, "unshare", "-m", "--", "bash", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // new session so signals don't propagate from parent
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start firecracker process: %w", err)
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

	slog.Info("firecracker process started",
		"pid", cmd.Process.Pid,
		"sandbox", cfg.SandboxID,
	)

	return p, nil
}

// buildStartScript generates the bash script that sets up the mount namespace,
// symlinks kernel/rootfs, and execs Firecracker inside the network namespace.
func buildStartScript(cfg *VMConfig) string {
	return fmt.Sprintf(`
set -euo pipefail

# Prevent mount propagation to the host
mount --make-rprivate /

# Create ephemeral tmpfs workspace
mkdir -p %[1]s
mount -t tmpfs tmpfs %[1]s

# Symlink kernel and rootfs into the workspace
ln -s %[2]s %[1]s/vmlinux
ln -s %[3]s %[1]s/rootfs.ext4

# Launch Firecracker inside the network namespace
exec ip netns exec %[4]s %[5]s --api-sock %[6]s
`,
		cfg.SandboxDir,       // 1
		cfg.KernelPath,       // 2
		cfg.RootfsPath,       // 3
		cfg.NetworkNamespace, // 4
		cfg.FirecrackerBin,   // 5
		cfg.SocketPath,       // 6
	)
}

// stop sends SIGTERM and waits for the process to exit. If it doesn't exit
// within 10 seconds, SIGKILL is sent.
func (p *process) stop() error {
	if p.cmd.Process == nil {
		return nil
	}

	// Send SIGTERM to the process group (negative PID).
	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM); err != nil {
		slog.Debug("sigterm failed, process may have exited", "error", err)
	}

	select {
	case <-p.exitCh:
		return nil
	case <-time.After(10 * time.Second):
		slog.Warn("firecracker did not exit after SIGTERM, sending SIGKILL")
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
