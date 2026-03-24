package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"git.omukk.dev/wrenn/sandbox/internal/devicemapper"
	"git.omukk.dev/wrenn/sandbox/internal/hostagent"
	"git.omukk.dev/wrenn/sandbox/internal/sandbox"
	"git.omukk.dev/wrenn/sandbox/proto/hostagent/gen/hostagentv1connect"
)

func main() {
	registrationToken := flag.String("register", "", "One-time registration token from the control plane (required on first run)")
	advertiseAddr := flag.String("address", "", "Externally-reachable address (ip:port) for this host agent")
	flag.Parse()

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

	// Clean up any stale dm-snapshot devices from a previous crash.
	devicemapper.CleanupStaleDevices()

	listenAddr := envOrDefault("AGENT_LISTEN_ADDR", ":50051")
	rootDir := envOrDefault("AGENT_FILES_ROOTDIR", "/var/lib/wrenn")
	cpURL := os.Getenv("AGENT_CP_URL")
	tokenFile := filepath.Join(rootDir, "host.jwt")

	if cpURL == "" {
		slog.Error("AGENT_CP_URL environment variable is required")
		os.Exit(1)
	}
	if *advertiseAddr == "" {
		slog.Error("--address flag is required (externally-reachable ip:port)")
		os.Exit(1)
	}

	cfg := sandbox.Config{
		KernelPath:   filepath.Join(rootDir, "kernels", "vmlinux"),
		ImagesDir:    filepath.Join(rootDir, "images"),
		SandboxesDir: filepath.Join(rootDir, "sandboxes"),
		SnapshotsDir: filepath.Join(rootDir, "snapshots"),
	}

	mgr := sandbox.New(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.StartTTLReaper(ctx)

	// Register with the control plane and start heartbeating.
	hostToken, err := hostagent.Register(ctx, hostagent.RegistrationConfig{
		CPURL:             cpURL,
		RegistrationToken: *registrationToken,
		TokenFile:         tokenFile,
		Address:           *advertiseAddr,
	})
	if err != nil {
		slog.Error("host registration failed", "error", err)
		os.Exit(1)
	}

	hostID, err := hostagent.HostIDFromToken(hostToken)
	if err != nil {
		slog.Error("failed to extract host ID from token", "error", err)
		os.Exit(1)
	}

	slog.Info("host registered", "host_id", hostID)

	// Start heartbeat loop. On CP rejection: try JWT refresh. If that fails,
	// pause all running sandboxes to ensure they're not left orphaned.
	hostagent.StartHeartbeat(ctx, cpURL, tokenFile, hostID, 30*time.Second, func() {
		pauseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		mgr.PauseAll(pauseCtx)
	})

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

	slog.Info("host agent starting", "addr", listenAddr, "host_id", hostID)
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
