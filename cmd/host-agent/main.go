package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"git.omukk.dev/wrenn/wrenn/internal/devicemapper"
	"git.omukk.dev/wrenn/wrenn/internal/hostagent"
	"git.omukk.dev/wrenn/wrenn/internal/layout"
	"git.omukk.dev/wrenn/wrenn/internal/network"
	"git.omukk.dev/wrenn/wrenn/internal/sandbox"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/logging"
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

	if err := checkPrivileges(); err != nil {
		slog.Error("insufficient privileges", "error", err)
		os.Exit(1)
	}

	// Enable IP forwarding (required for NAT). The write may fail if running
	// as non-root without DAC_OVERRIDE on this path — that's OK if the systemd
	// unit's ExecStartPre already set it. We verify the value regardless.
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644); err != nil {
		slog.Warn("failed to enable ip_forward (may have been set by systemd unit)", "error", err)
	}
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward"); err != nil || strings.TrimSpace(string(b)) != "1" {
		slog.Error("ip_forward is not enabled — sandbox networking will be broken", "error", err)
		os.Exit(1)
	}

	// Clean up stale resources from a previous crash.
	devicemapper.CleanupStaleDevices()
	network.CleanupStaleNamespaces()

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

	// Expand base images to the configured disk size (sparse, no extra physical
	// disk). This ensures dm-snapshot sandboxes see the full size from boot.
	if err := sandbox.EnsureImageSizes(rootDir, defaultRootfsSizeMB); err != nil {
		slog.Error("failed to expand base images", "error", err)
		os.Exit(1)
	}

	// Resolve latest kernel version.
	kernelPath, kernelVersion, err := layout.LatestKernel(rootDir)
	if err != nil {
		slog.Error("failed to find kernel", "error", err)
		os.Exit(1)
	}
	slog.Info("resolved kernel", "version", kernelVersion, "path", kernelPath)

	// Detect firecracker version.
	fcBin := envOrDefault("WRENN_FIRECRACKER_BIN", "/usr/local/bin/firecracker")
	fcVersion, err := sandbox.DetectFirecrackerVersion(fcBin)
	if err != nil {
		slog.Error("failed to detect firecracker version", "error", err)
		os.Exit(1)
	}
	slog.Info("resolved firecracker", "version", fcVersion, "path", fcBin)

	cfg := sandbox.Config{
		WrennDir:            rootDir,
		DefaultRootfsSizeMB: defaultRootfsSizeMB,
		KernelPath:          kernelPath,
		KernelVersion:       kernelVersion,
		FirecrackerBin:      fcBin,
		FirecrackerVersion:  fcVersion,
		AgentVersion:        version,
	}

	mgr := sandbox.New(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.StartTTLReaper(ctx)

	// Register with the control plane and start heartbeating.
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
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer shutdownCancel()
			mgr.Shutdown(shutdownCtx)
			sandbox.ShrinkMinimalImage(rootDir)
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
		// onCredsRefreshed: hot-swap the TLS certificate after a JWT refresh.
		func(tf *hostagent.TokenFile) {
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

	// Graceful shutdown on SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		doShutdown("signal: " + sig.String())
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

// checkPrivileges verifies the process has the required Linux capabilities.
// Always reads CapEff — even for root — because a root process inside a
// restricted container (e.g. docker --cap-drop=all) may not have all caps.
func checkPrivileges() error {
	capEff, err := readEffectiveCaps()
	if err != nil {
		return fmt.Errorf("read capabilities: %w", err)
	}

	// All capabilities required by the host agent at runtime.
	required := []struct {
		bit  uint
		name string
	}{
		{1, "CAP_DAC_OVERRIDE"}, // /dev/loop*, /dev/mapper/*, /dev/net/tun
		{5, "CAP_KILL"},         // SIGTERM/SIGKILL to Firecracker processes
		{12, "CAP_NET_ADMIN"},   // netlink, iptables, routing, TAP/veth
		{13, "CAP_NET_RAW"},     // raw sockets (iptables)
		{19, "CAP_SYS_PTRACE"},  // reading /proc/self/ns/net (netns.Get)
		{21, "CAP_SYS_ADMIN"},   // netns, mount ns, losetup, dmsetup
		{27, "CAP_MKNOD"},       // device-mapper node creation
	}

	var missing []string
	for _, cap := range required {
		if capEff&(1<<cap.bit) == 0 {
			missing = append(missing, cap.name)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing capabilities: %s — run as root or apply setcap to the binary",
			strings.Join(missing, ", "))
	}

	return nil
}

// readEffectiveCaps parses the CapEff bitmask from /proc/self/status.
func readEffectiveCaps() (uint64, error) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if hexStr, ok := strings.CutPrefix(line, "CapEff:"); ok {
			return strconv.ParseUint(strings.TrimSpace(hexStr), 16, 64)
		}
	}

	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read /proc/self/status: %w", err)
	}
	return 0, fmt.Errorf("CapEff not found in /proc/self/status")
}
