package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.omukk.dev/wrenn/sandbox/internal/hostagent"
	"git.omukk.dev/wrenn/sandbox/internal/sandbox"
	"git.omukk.dev/wrenn/sandbox/proto/hostagent/gen/hostagentv1connect"
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

	listenAddr := envOrDefault("AGENT_LISTEN_ADDR", ":50051")
	kernelPath := envOrDefault("AGENT_KERNEL_PATH", "/var/lib/wrenn/kernels/vmlinux")
	imagesPath := envOrDefault("AGENT_IMAGES_PATH", "/var/lib/wrenn/images")
	sandboxesPath := envOrDefault("AGENT_SANDBOXES_PATH", "/var/lib/wrenn/sandboxes")
	snapshotsPath := envOrDefault("AGENT_SNAPSHOTS_PATH", "/var/lib/wrenn/snapshots")

	cfg := sandbox.Config{
		KernelPath:   kernelPath,
		ImagesDir:    imagesPath,
		SandboxesDir: sandboxesPath,
		SnapshotsDir: snapshotsPath,
	}

	mgr := sandbox.New(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.StartTTLReaper(ctx)

	srv := hostagent.NewServer(mgr)
	path, handler := hostagentv1connect.NewHostAgentServiceHandler(srv)

	mux := http.NewServeMux()
	mux.Handle(path, handler)

	httpServer := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}

	// Graceful shutdown on signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		mgr.Shutdown(shutdownCtx)

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("http server shutdown error", "error", err)
		}
	}()

	slog.Info("host agent starting", "addr", listenAddr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("http server error", "error", err)
		os.Exit(1)
	}

	slog.Info("host agent stopped")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
