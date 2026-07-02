package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"git.omukk.dev/wrenn/wrenn/internal/hostagent"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/logging"
	"git.omukk.dev/wrenn/wrenn/pkg/sandbox"
	"git.omukk.dev/wrenn/wrenn/proto/hostagent/gen/hostagentv1connect"
)

// Set via -ldflags at build time.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	// Best-effort load — missing .env file is fine.
	_ = godotenv.Load()

	registrationToken := flag.String("register", "", "One-time registration token from the control plane (required on first run)")
	advertiseAddr := flag.String("address", "", "Externally-reachable address (ip:port) for this host agent")
	flag.Parse()

	rootDir := envOrDefault("WRENN_DIR", "/var/lib/wrenn")
	cleanupLog := logging.Setup(filepath.Join(rootDir, "logs"), "host-agent")
	defer cleanupLog()

	if err := sandbox.CheckPrivileges(); err != nil {
		slog.Error("insufficient privileges", "error", err)
		os.Exit(1)
	}

	// Enable IP forwarding (required for NAT). The write may fail if running
	// as non-root without DAC_OVERRIDE on this path — that's OK if the systemd
	// unit's ExecStartPre already set it.
	if err := sandbox.EnsureIPForward(); err != nil {
		slog.Error("ip_forward is not enabled — sandbox networking will be broken", "error", err)
		os.Exit(1)
	}

	listenAddr := envOrDefault("WRENN_HOST_LISTEN_ADDR", ":50051")
	cpURL := os.Getenv("WRENN_CP_URL")
	credsFile := filepath.Join(rootDir, "host-credentials.json")

	if cpURL == "" {
		slog.Error("WRENN_CP_URL environment variable is required")
		os.Exit(1)
	}
	if *advertiseAddr == "" {
		slog.Error("--address flag is required (externally-reachable ip:port)")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register with the control plane before touching rootfs images. If the
	// agent can't reach the CP there's no point inflating images (and crashing
	// afterward would leave them in the expanded state).
	creds, err := hostagent.Register(ctx, hostagent.RegistrationConfig{
		CPURL:             cpURL,
		RegistrationToken: *registrationToken,
		TokenFile:         credsFile,
		Address:           *advertiseAddr,
	})
	if err != nil {
		slog.Error("host registration failed", "error", err)
		os.Exit(1)
	}

	slog.Info("host registered", "host_id", creds.HostID)

	// Parse default rootfs size from env (e.g. "5G", "2Gi", "1000M").
	defaultRootfsSizeMB := sandbox.DefaultDiskSizeMB
	if sizeStr := os.Getenv("WRENN_DEFAULT_ROOTFS_SIZE"); sizeStr != "" {
		parsed, err := sandbox.ParseSizeToMB(sizeStr)
		if err != nil {
			slog.Error("invalid WRENN_DEFAULT_ROOTFS_SIZE", "value", sizeStr, "error", err)
			os.Exit(1)
		}
		defaultRootfsSizeMB = parsed
		slog.Info("using custom rootfs size", "size_mb", defaultRootfsSizeMB)
	}

	// Run the startup ritual: stale-resource cleanup, base image expansion,
	// kernel resolution, cloud-hypervisor detection, orphan pause-dir GC.
	env, err := sandbox.Setup(sandbox.SetupOptions{
		WrennDir:            rootDir,
		CHBin:               envOrDefault("WRENN_CH_BIN", sandbox.DefaultCHBin),
		DefaultRootfsSizeMB: defaultRootfsSizeMB,
	})
	if err != nil {
		slog.Error("host setup failed", "error", err)
		os.Exit(1)
	}
	slog.Info("resolved kernel", "version", env.KernelVersion, "path", env.KernelPath)
	slog.Info("resolved cloud-hypervisor", "version", env.CHVersion, "path", env.CHBin)

	cfg := sandbox.Config{
		WrennDir:            rootDir,
		DefaultRootfsSizeMB: defaultRootfsSizeMB,
		KernelPath:          env.KernelPath,
		KernelVersion:       env.KernelVersion,
		VMMBin:              env.CHBin,
		VMMVersion:          env.CHVersion,
		AgentVersion:        version,
		ProxyDomain:         envOrDefault("WRENN_PROXY_DOMAIN", "wrenn.dev"),

		// Activity sampler tuning (all optional; zero → sandbox package default).
		ActivitySampleInterval: envDuration("WRENN_ACTIVITY_SAMPLE_INTERVAL"),
		CPUBusyPct:             envFloat32("WRENN_CPU_BUSY_THRESHOLD"),
		NetFloorBps:            envUint64("WRENN_NET_FLOOR_BPS"),
		DiskFloorBps:           envUint64("WRENN_DISK_FLOOR_BPS"),
	}

	mgr := sandbox.New(cfg)

	// Set up lifecycle event callback sender so autonomous events
	// (auto-pause, auto-destroy) are pushed to the CP proactively.
	cb := hostagent.NewCallbackSender(cpURL, credsFile, creds.HostID)
	mgr.SetEventSender(hostagent.NewEventSender(cb))

	// Sweep stale running-state files first (Setup's stale cleanup killed
	// every CH process, so nothing can actually be re-attached), then restore
	// paused sandboxes from disk so ListSandboxes reports them as 'paused'
	// immediately. Without the latter, the CP's HostMonitor would mark every
	// paused-on-disk sandbox 'stopped' via the missing→stopped reconcile path
	// on the first ListSandboxes after agent restart. Must run before the
	// HTTP server starts serving (an early Create would race the slot
	// reservation).
	mgr.RestoreRunningSandboxes()
	mgr.RestorePausedSandboxes()

	mgr.StartTTLReaper(ctx)
	mgr.StartActivitySampler(ctx)

	// httpServer is declared here so the shutdown func can reference it.
	// ReadTimeout/WriteTimeout are intentionally omitted — they would kill
	// long-lived Connect RPC streams and WebSocket proxy connections.
	httpServer := &http.Server{
		Addr:              listenAddr,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       620 * time.Second, // > typical LB upstream timeout (600s)
		// Disable HTTP/2: empty non-nil map prevents Go from registering
		// the h2 ALPN token. Connect RPC works over HTTP/1.1; HTTP/2
		// multiplexing causes HOL blocking when a slow sandbox RPC stalls
		// the shared connection.
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}

	// mTLS is mandatory — refuse to start without a valid certificate.
	var certStore hostagent.CertStore
	if creds.CertPEM == "" || creds.KeyPEM == "" || creds.CACertPEM == "" {
		slog.Error("mTLS certificate not received from CP — ensure WRENN_CA_CERT and WRENN_CA_KEY are configured on the control plane")
		os.Exit(1)
	}
	if err := certStore.ParseAndStore(creds.CertPEM, creds.KeyPEM); err != nil {
		slog.Error("failed to load host TLS certificate", "error", err)
		os.Exit(1)
	}
	tlsCfg := auth.AgentTLSConfigFromPEM(creds.CACertPEM, certStore.GetCert)
	if tlsCfg == nil {
		slog.Error("failed to build agent TLS config: invalid CA certificate PEM")
		os.Exit(1)
	}
	httpServer.TLSConfig = tlsCfg
	slog.Info("mTLS enabled on agent server")

	// doShutdown is the single shutdown path. sync.Once ensures mgr.Shutdown
	// and httpServer.Shutdown are each called exactly once regardless of
	// whether shutdown is triggered by a signal, a heartbeat 404, or the
	// Terminate RPC.
	var shutdownOnce sync.Once
	doShutdown := func(reason string) {
		shutdownOnce.Do(func() {
			slog.Info("shutting down", "reason", reason)
			cancel()
			// Shutdown pauses every running sandbox in parallel (PauseAll uses
			// a worker pool). Per-sandbox Pause can take 10–30s (memory loader
			// wait + ch.snapshot of guest RAM). 5 minutes is enough headroom for
			// a busy host while still bounded so a wedged sandbox can't keep the
			// process alive indefinitely — a second signal force-exits anyway.
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer shutdownCancel()
			// Order matters: mgr.Shutdown FIRST so it runs to completion
			// before httpServer.Shutdown unblocks main's Serve and lets the
			// process exit. mgr.Shutdown internally flips a draining flag
			// that rejects new Create/Resume RPCs with Unavailable so any
			// in-flight HTTP handlers can't add sandboxes after PauseAll
			// snapshotted state. User-initiated Pauses already running are
			// awaited by PauseAll/Destroy's lifecycleMu serialization.
			mgr.Shutdown(shutdownCtx)
			sandbox.ShrinkSystemImages(rootDir)
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
	mgr.SetOnDestroy(proxyHandler.EvictProxy)

	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.Handle("/proxy/", proxyHandler)
	httpServer.Handler = mux

	// Start heartbeat loop. Handler must be set before this because the
	// immediate beat can trigger doShutdown → httpServer.Shutdown synchronously.
	hostagent.StartHeartbeat(ctx, cpURL, credsFile, creds.HostID, 30*time.Second,
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
		// onCredsRefreshed: hot-swap the TLS certificate and update callback JWT.
		func(tf *hostagent.TokenFile) {
			cb.UpdateJWT(tf.JWT)
			if tf.CertPEM == "" || tf.KeyPEM == "" {
				return
			}
			if err := certStore.ParseAndStore(tf.CertPEM, tf.KeyPEM); err != nil {
				slog.Error("failed to hot-swap TLS cert after credentials refresh", "error", err)
			} else {
				slog.Info("TLS cert hot-swapped after credentials refresh")
			}
		},
	)

	// Graceful shutdown on SIGINT/SIGTERM. A second signal force-exits
	// so the operator can always kill the process if shutdown hangs.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		go doShutdown("signal: " + sig.String())
		sig = <-sigCh
		slog.Error("received second signal, force exiting", "signal", sig.String())
		os.Exit(1)
	}()

	slog.Info("host agent starting", "addr", listenAddr, "host_id", creds.HostID, "version", version, "commit", commit)
	// TLSConfig is always set (mTLS is mandatory). Create the TLS listener
	// manually because ListenAndServeTLS requires on-disk cert/key paths
	// but we use GetCertificate callback for hot-swap support.
	ln, err := tls.Listen("tcp", listenAddr, httpServer.TLSConfig)
	if err != nil {
		slog.Error("failed to start TLS listener", "error", err)
		os.Exit(1)
	}
	if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
		slog.Error("https server error", "error", err)
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

// envDuration parses an optional duration env var (e.g. "5s"). Empty or
// invalid → zero, letting the sandbox package apply its default.
func envDuration(key string) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		slog.Warn("invalid duration env var, using default", "key", key, "value", v)
		return 0
	}
	return d
}

// envFloat32 parses an optional float env var. Empty or invalid → 0.
func envFloat32(key string) float32 {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 32)
	if err != nil {
		slog.Warn("invalid float env var, using default", "key", key, "value", v)
		return 0
	}
	return float32(f)
}

// envUint64 parses an optional unsigned-int env var. Empty or invalid → 0.
func envUint64(key string) uint64 {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		slog.Warn("invalid uint env var, using default", "key", key, "value", v)
		return 0
	}
	return n
}
