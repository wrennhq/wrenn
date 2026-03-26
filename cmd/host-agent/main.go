package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"git.omukk.dev/wrenn/sandbox/internal/devicemapper"
	"git.omukk.dev/wrenn/sandbox/internal/hostagent"
	"git.omukk.dev/wrenn/sandbox/internal/sandbox"
	"git.omukk.dev/wrenn/sandbox/proto/hostagent/gen/hostagentv1connect"
)

func main() {
	// Best-effort load — missing .env file is fine.
	_ = godotenv.Load()

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

	// Expand base images to the standard disk size (sparse, no extra physical
	// disk). This ensures dm-snapshot sandboxes see the full size from boot.
	imagesDir := filepath.Join(rootDir, "images")
	if err := sandbox.EnsureImageSizes(imagesDir, sandbox.DefaultDiskSizeMB); err != nil {
		slog.Error("failed to expand base images", "error", err)
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

	// httpServer is declared here so the shutdown func can reference it.
	httpServer := &http.Server{Addr: listenAddr}

	// doShutdown is the single shutdown path. sync.Once ensures mgr.Shutdown
	// and httpServer.Shutdown are each called exactly once regardless of
	// whether shutdown is triggered by a signal, a heartbeat 404, or the
	// Terminate RPC.
	var shutdownOnce sync.Once
	doShutdown := func(reason string) {
		shutdownOnce.Do(func() {
			slog.Info("shutting down", "reason", reason)
			cancel()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer shutdownCancel()
			mgr.Shutdown(shutdownCtx)
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				slog.Error("http server shutdown error", "error", err)
			}
		})
	}

	srv := hostagent.NewServer(mgr, func() {
		doShutdown("Terminate RPC received")
	})
	path, handler := hostagentv1connect.NewHostAgentServiceHandler(srv)

	proxyHandler := hostagent.NewProxyHandler(mgr)

	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.Handle("/proxy/", proxyHandler)
	httpServer.Handler = mux

	// Start heartbeat loop. Handler must be set before this because the
	// immediate beat can trigger doShutdown → httpServer.Shutdown synchronously.
	hostagent.StartHeartbeat(ctx, cpURL, tokenFile, hostID, 30*time.Second,
		// pauseAll: called on 3 consecutive network failures.
		func() {
			pauseCtx, pauseCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer pauseCancel()
			mgr.PauseAll(pauseCtx)
		},
		// onDeleted: called when CP returns 404 (host was deleted).
		func() {
			doShutdown("host deleted from CP")
		},
	)

	// Graceful shutdown on SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		doShutdown("signal: " + sig.String())
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
