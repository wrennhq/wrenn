package vm

import "fmt"

// SandboxTmpDir returns the per-sandbox tmpfs mount point used inside the
// VMM's private mount namespace. Recorded as the disk path in CH's saved
// config.json, so restore paths must reconstruct it exactly to make the
// symlink prelude resolve.
func SandboxTmpDir(sandboxID string) string {
	return fmt.Sprintf("/tmp/ch-vm-%s", sandboxID)
}

// SandboxSocketPath returns the Cloud Hypervisor API socket path for a sandbox.
func SandboxSocketPath(sandboxID string) string {
	return fmt.Sprintf("/tmp/ch-%s.sock", sandboxID)
}

// VMConfig holds the configuration for creating a Cloud Hypervisor microVM.
type VMConfig struct {
	// SandboxID is the unique identifier for this sandbox (e.g., "cl-a1b2c3d4").
	SandboxID string

	// TemplateID is the template UUID string, passed to envd via PostInit.
	TemplateID string

	// KernelPath is the path to the uncompressed Linux kernel (vmlinux).
	KernelPath string

	// RootfsPath is the path to the rootfs block device for this sandbox.
	// Typically a dm-snapshot device (e.g., /dev/mapper/wrenn-sb-a1b2c3d4).
	RootfsPath string

	// VCPUs is the number of virtual CPUs to allocate (default: 1).
	VCPUs int

	// MemoryMB is the amount of RAM in megabytes (default: 512).
	MemoryMB int

	// NetworkNamespace is the name of the network namespace to launch
	// Cloud Hypervisor inside (e.g., "ns-1"). The namespace must already exist
	// with a TAP device configured.
	NetworkNamespace string

	// TapDevice is the name of the TAP device inside the network namespace
	// that Cloud Hypervisor will attach to (e.g., "tap0").
	TapDevice string

	// TapMAC is the MAC address for the TAP device.
	TapMAC string

	// GuestIP is the IP address assigned to the guest VM (e.g., "169.254.0.21").
	GuestIP string

	// GatewayIP is the gateway IP (the TAP device's IP, e.g., "169.254.0.22").
	GatewayIP string

	// NetMask is the subnet mask for the guest network (e.g., "255.255.255.252").
	NetMask string

	// VMMBin is the path to the cloud-hypervisor binary.
	VMMBin string

	// SocketPath is the path for the Cloud Hypervisor API Unix socket.
	SocketPath string

	// SandboxDir is the tmpfs mount point for per-sandbox files inside the
	// mount namespace (e.g., "/ch-vm").
	SandboxDir string

	// InitPath is the path to the init process inside the guest.
	// Defaults to "/sbin/init" if empty.
	InitPath string

	// RestoreFromDir, if non-empty, switches the process launcher into restore
	// mode. CH is invoked with `--restore source_url=file://{dir}/` instead of
	// the fresh-boot path. The directory must contain CH's snapshot artefacts
	// (config.json, state.json, memory-ranges, memory file).
	RestoreFromDir string

	// RestoreLazyMemory enables `memory_restore_mode=ondemand` so guest pages
	// fault in lazily via userfaultfd. Only honored when RestoreFromDir is set.
	RestoreLazyMemory bool

	// LogDir is the directory for Cloud Hypervisor log files. If set, CH
	// stdout/stderr are written to {LogDir}/ch-{SandboxID}.log instead of
	// the parent process's stdout/stderr.
	LogDir string
}

func (c *VMConfig) applyDefaults() {
	if c.VCPUs == 0 {
		c.VCPUs = 1
	}
	if c.MemoryMB == 0 {
		c.MemoryMB = 512
	}
	if c.VMMBin == "" {
		c.VMMBin = "/usr/local/bin/cloud-hypervisor"
	}
	if c.SocketPath == "" {
		c.SocketPath = SandboxSocketPath(c.SandboxID)
	}
	if c.SandboxDir == "" {
		c.SandboxDir = SandboxTmpDir(c.SandboxID)
	}
	if c.TapDevice == "" {
		c.TapDevice = "tap0"
	}
	if c.TapMAC == "" {
		c.TapMAC = "02:FC:00:00:00:05"
	}
	if c.InitPath == "" {
		c.InitPath = "/usr/local/bin/wrenn-init"
	}
}

// kernelArgs builds the kernel command line for the VM.
func (c *VMConfig) kernelArgs() string {
	// ip= format: <client-ip>::<gw-ip>:<netmask>:<hostname>:<iface>:<autoconf>
	ipArg := fmt.Sprintf("ip=%s::%s:%s:capsule:eth0:off",
		c.GuestIP, c.GatewayIP, c.NetMask,
	)

	return fmt.Sprintf(
		"console=ttyS0 root=/dev/vda rw rootflags=nodiscard reboot=k panic=1 quiet loglevel=1 init_on_free=1 clocksource=kvm-clock init=%s %s",
		c.InitPath, ipArg,
	)
}

func (c *VMConfig) validate() error {
	if c.SandboxID == "" {
		return fmt.Errorf("SandboxID is required")
	}
	if c.KernelPath == "" {
		return fmt.Errorf("KernelPath is required")
	}
	if c.RootfsPath == "" {
		return fmt.Errorf("RootfsPath is required")
	}
	if c.NetworkNamespace == "" {
		return fmt.Errorf("NetworkNamespace is required")
	}
	if c.GuestIP == "" {
		return fmt.Errorf("GuestIP is required")
	}
	if c.GatewayIP == "" {
		return fmt.Errorf("GatewayIP is required")
	}
	if c.NetMask == "" {
		return fmt.Errorf("NetMask is required")
	}
	return nil
}
