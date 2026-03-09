package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"git.omukk.dev/wrenn/sandbox/internal/envdclient"
	"git.omukk.dev/wrenn/sandbox/internal/network"
	"git.omukk.dev/wrenn/sandbox/internal/vm"
)

const (
	kernelPath    = "/var/lib/wrenn/kernels/vmlinux"
	baseRootfs    = "/var/lib/wrenn/sandboxes/rootfs.ext4"
	sandboxesDir  = "/var/lib/wrenn/sandboxes"
	sandboxID     = "sb-demo0001"
	slotIndex     = 1
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	if os.Geteuid() != 0 {
		slog.Error("host agent must run as root")
		os.Exit(1)
	}

	// Enable IP forwarding (required for NAT).
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644); err != nil {
		slog.Warn("failed to enable ip_forward", "error", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals for clean shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	if err := run(ctx); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	// Step 1: Clone rootfs for this sandbox.
	sandboxRootfs := filepath.Join(sandboxesDir, fmt.Sprintf("%s-rootfs.ext4", sandboxID))
	slog.Info("cloning rootfs", "src", baseRootfs, "dst", sandboxRootfs)

	if err := cloneRootfs(baseRootfs, sandboxRootfs); err != nil {
		return fmt.Errorf("clone rootfs: %w", err)
	}
	defer os.Remove(sandboxRootfs)

	// Step 2: Set up network.
	slot := network.NewSlot(slotIndex)

	slog.Info("setting up network", "slot", slotIndex)
	if err := network.CreateNetwork(slot); err != nil {
		return fmt.Errorf("create network: %w", err)
	}
	defer func() {
		slog.Info("tearing down network")
		network.RemoveNetwork(slot)
	}()

	// Step 3: Boot the VM.
	mgr := vm.NewManager()

	cfg := vm.VMConfig{
		SandboxID:        sandboxID,
		KernelPath:       kernelPath,
		RootfsPath:       sandboxRootfs,
		VCPUs:            1,
		MemoryMB:         512,
		NetworkNamespace: slot.NamespaceID,
		TapDevice:        slot.TapName,
		TapMAC:           slot.TapMAC,
		GuestIP:          slot.GuestIP,
		GatewayIP:        slot.TapIP,
		NetMask:          slot.GuestNetMask,
	}

	vmInstance, err := mgr.Create(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create VM: %w", err)
	}
	_ = vmInstance
	defer func() {
		slog.Info("destroying VM")
		mgr.Destroy(context.Background(), sandboxID)
	}()

	// Step 4: Wait for envd to be ready.
	client := envdclient.New(slot.HostIP.String())

	waitCtx, waitCancel := context.WithTimeout(ctx, 30*time.Second)
	defer waitCancel()

	if err := client.WaitUntilReady(waitCtx); err != nil {
		return fmt.Errorf("wait for envd: %w", err)
	}

	// Step 5: Run "echo hello" inside the sandbox.
	slog.Info("executing command", "cmd", "echo hello")

	result, err := client.Exec(ctx, "/bin/sh", "-c", "echo hello")
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}

	fmt.Printf("\n=== Command Output ===\n")
	fmt.Printf("stdout: %s", string(result.Stdout))
	if len(result.Stderr) > 0 {
		fmt.Printf("stderr: %s", string(result.Stderr))
	}
	fmt.Printf("exit code: %d\n", result.ExitCode)
	fmt.Printf("======================\n\n")

	// Step 6: Clean shutdown.
	slog.Info("demo complete, cleaning up")

	return nil
}

// cloneRootfs creates a copy-on-write clone of the base rootfs image.
// Uses reflink if supported by the filesystem, falls back to regular copy.
func cloneRootfs(src, dst string) error {
	// Try reflink first (instant, CoW).
	cmd := exec.Command("cp", "--reflink=auto", src, dst)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cp --reflink=auto: %w", err)
	}
	return nil
}
