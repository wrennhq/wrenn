package sandbox

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"git.omukk.dev/wrenn/wrenn/pkg/devicemapper"
	"git.omukk.dev/wrenn/wrenn/pkg/layout"
	"git.omukk.dev/wrenn/wrenn/pkg/network"
	"git.omukk.dev/wrenn/wrenn/pkg/vm"
)

// DefaultCHBin is the conventional install path for the Cloud Hypervisor
// binary, used when SetupOptions.CHBin is empty.
const DefaultCHBin = "/usr/local/bin/cloud-hypervisor"

// CheckPrivileges verifies the process has all Linux capabilities required to
// run sandboxes (DAC_OVERRIDE, KILL, NET_ADMIN, NET_RAW, SYS_PTRACE,
// SYS_ADMIN, MKNOD). It always reads CapEff — even for root — because a root
// process inside a restricted container (e.g. docker --cap-drop=all) may not
// have all caps.
func CheckPrivileges() error {
	capEff, err := readEffectiveCaps()
	if err != nil {
		return fmt.Errorf("read capabilities: %w", err)
	}

	required := []struct {
		bit  uint
		name string
	}{
		{1, "CAP_DAC_OVERRIDE"}, // /dev/loop*, /dev/mapper/*, /dev/net/tun
		{5, "CAP_KILL"},         // SIGTERM/SIGKILL to cloud-hypervisor processes
		{12, "CAP_NET_ADMIN"},   // netlink, iptables, routing, TAP/veth
		{13, "CAP_NET_RAW"},     // raw sockets (iptables)
		{19, "CAP_SYS_PTRACE"},  // reading /proc/self/ns/net (netns.Get)
		{21, "CAP_SYS_ADMIN"},   // netns, mount ns, losetup, dmsetup
		{27, "CAP_MKNOD"},       // device-mapper node creation
	}

	var missing []string
	for _, c := range required {
		if capEff&(1<<c.bit) == 0 {
			missing = append(missing, c.name)
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

// EnsureIPForward enables IPv4 forwarding (required for sandbox NAT) and
// verifies the resulting value. The write may fail when the caller lacks
// DAC_OVERRIDE on /proc/sys — that is tolerated as long as forwarding was
// already enabled externally (e.g. by a systemd unit's ExecStartPre).
func EnsureIPForward() error {
	_ = os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)
	b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return fmt.Errorf("read ip_forward: %w", err)
	}
	if strings.TrimSpace(string(b)) != "1" {
		return fmt.Errorf("ip_forward is not enabled — sandbox networking will be broken")
	}
	return nil
}

// SetupOptions controls which phases of the host startup ritual run.
type SetupOptions struct {
	// WrennDir is the root data directory (e.g. /var/lib/wrenn).
	WrennDir string
	// CHBin is the cloud-hypervisor binary path; empty → DefaultCHBin.
	CHBin string
	// DefaultRootfsSizeMB is forwarded to EnsureImageSizes; 0 → DefaultDiskSizeMB.
	DefaultRootfsSizeMB int
	// SkipCleanup skips the stale-resource sweep (kill orphaned CH processes,
	// remove stale dm devices and network namespaces).
	//
	// The sweep assumes this process is the only sandbox owner on the host —
	// it kills EVERY cloud-hypervisor process not tracked by this process.
	// A short-lived CLI re-attaching to sandboxes created by earlier
	// invocations MUST set this, or it will destroy its own running VMs.
	SkipCleanup bool
	// SkipEnsureImages skips expanding base images to the configured disk size.
	SkipEnsureImages bool
}

// SetupResult holds environment facts resolved by Setup, ready to be copied
// into Config.
type SetupResult struct {
	KernelPath    string
	KernelVersion string
	CHBin         string
	CHVersion     string
}

// Setup performs the host startup ritual shared by the host agent and
// standalone library consumers: stale-resource cleanup, base-image expansion,
// kernel resolution, and cloud-hypervisor version detection.
//
// Call CheckPrivileges and EnsureIPForward first; they are separate because
// callers surface their failures differently.
func Setup(opts SetupOptions) (*SetupResult, error) {
	if opts.CHBin == "" {
		opts.CHBin = DefaultCHBin
	}

	if !opts.SkipCleanup {
		// Order matters: kill stale CH processes first — they hold dm-snapshot
		// devices open and would otherwise cause "Device or resource busy" on
		// dmsetup remove.
		vm.CleanupStaleProcesses()
		devicemapper.CleanupStaleDevices()
		devicemapper.LogLoopState()
		network.CleanupStaleNamespaces()
		// The sweep above killed every sandbox process on the host, so all
		// slot claim files are stale. Remove them; live claims are
		// re-established when paused/running sandboxes are restored.
		if err := os.RemoveAll(layout.SlotsDir(opts.WrennDir)); err != nil {
			return nil, fmt.Errorf("reset slot claims: %w", err)
		}
	}

	if !opts.SkipEnsureImages {
		// Expand base images to the configured disk size (sparse, no extra
		// physical disk) so dm-snapshot sandboxes see the full size from boot.
		if err := EnsureImageSizes(opts.WrennDir, opts.DefaultRootfsSizeMB); err != nil {
			return nil, fmt.Errorf("expand base images: %w", err)
		}
	}

	kernelPath, kernelVersion, err := layout.LatestKernel(opts.WrennDir)
	if err != nil {
		return nil, fmt.Errorf("find kernel: %w", err)
	}

	chVersion, err := DetectCHVersion(opts.CHBin)
	if err != nil {
		return nil, fmt.Errorf("detect cloud-hypervisor version: %w", err)
	}

	// Remove any *.staging-* / *.trash-* directories left behind by a
	// previous Pause that crashed before completing the atomic swap. Must
	// run before any Resume so we don't race a sandbox restoration.
	CleanupOrphanPauseDirs(opts.WrennDir)

	return &SetupResult{
		KernelPath:    kernelPath,
		KernelVersion: kernelVersion,
		CHBin:         opts.CHBin,
		CHVersion:     chVersion,
	}, nil
}
