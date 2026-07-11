package network

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

const nsPrefix = "wrenn-ns-"

// CleanupStaleNamespaces removes leftover wrenn network namespaces from a
// previous crash. Called once at agent startup.
func CleanupStaleNamespaces() {
	entries, err := os.ReadDir("/run/netns")
	if err != nil {
		return // no /run/netns or unreadable — nothing to clean
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, nsPrefix) {
			continue
		}
		// Also remove the associated veth from the host side.
		vethName := "wrenn-veth-" + strings.TrimPrefix(name, nsPrefix)
		if link, err := netlink.LinkByName(vethName); err == nil {
			_ = netlink.LinkDel(link)
		}
		if err := netns.DeleteNamed(name); err != nil {
			slog.Warn("failed to remove stale namespace", "ns", name, "error", err)
		} else {
			slog.Info("removed stale namespace", "ns", name)
		}
	}

	// Clean up any stale wrenn iptables rules referencing old veth interfaces.
	cleanupStaleIptablesRules()

	// Flush any orphan conntrack rows for sandbox host-IPs. After a wedged
	// destroy the netfilter conntrack table can retain DNAT/SNAT entries
	// pointing at vanished interfaces, which makes new flows to recycled
	// slot IPs misroute. Best-effort; missing conntrack binary is OK.
	flushStaleConntrack()
}

// flushStaleConntrack removes conntrack rows referencing the sandbox host
// IP range (10.11.0.0/16) and the namespace veth range (10.12.0.0/16).
// Best-effort: silently skipped if conntrack(8) is absent.
func flushStaleConntrack() {
	if _, err := exec.LookPath("conntrack"); err != nil {
		slog.Debug("conntrack binary not found, skipping flush")
		return
	}
	flushed := 0
	for _, cidr := range []string{"10.11.0.0/16", "10.12.0.0/16"} {
		for _, dir := range []string{"--src", "--dst"} {
			out, err := exec.Command("conntrack", "-D", dir, cidr).CombinedOutput()
			if err != nil {
				// conntrack -D exits 1 when no entries match; not an
				// error from our perspective.
				slog.Debug("conntrack flush", "cidr", cidr, "dir", dir, "error", err)
				continue
			}
			// Output looks like "conntrack v1.4.x ... 3 flow entries have been deleted."
			// We only log INFO when at least one row was actually removed.
			if strings.Contains(string(out), "have been deleted") &&
				!strings.Contains(string(out), "0 flow entries") {
				flushed++
			}
		}
	}
	if flushed > 0 {
		slog.Info("flushed stale conntrack entries", "matched_filters", flushed)
	}
}

// cleanupStaleIptablesRules removes host iptables rules that reference
// wrenn-veth interfaces no longer present on the system.
func cleanupStaleIptablesRules() {
	for _, table := range []string{"filter", "nat"} {
		cmd := exec.Command("iptables-save", "-t", table)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.Contains(line, "wrenn-veth-") {
				continue
			}
			// Lines look like "-A FORWARD -i wrenn-veth-1 -o wlo1 -j ACCEPT"
			// Convert -A to -D to delete the rule.
			if !strings.HasPrefix(line, "-A ") {
				continue
			}
			delRule := "-D " + line[3:]
			args := strings.Fields(delRule)
			delCmd := exec.Command("iptables", append([]string{"-t", table}, args...)...)
			if err := delCmd.Run(); err != nil {
				slog.Debug("failed to remove stale iptables rule", "rule", line, "error", err)
			}
		}
	}

	// Also remove stale host routes to 10.11.0.x via wrenn-veth interfaces.
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return
	}
	for _, r := range routes {
		if r.LinkIndex == 0 {
			continue
		}
		link, err := netlink.LinkByIndex(r.LinkIndex)
		if err != nil {
			continue
		}
		if strings.HasPrefix(link.Attrs().Name, "wrenn-veth-") {
			_ = netlink.RouteDel(&r)
		}
	}
}

const (
	// Fixed addresses inside each network namespace (safe because each
	// sandbox gets its own netns).
	tapName      = "tap0"
	tapIP        = "169.254.0.22"
	tapMask      = 30
	tapMAC       = "02:FC:00:00:00:05"
	guestIP      = "169.254.0.21"
	guestNetMask = "255.255.255.252"

	// Base IPs for host-reachable and veth addressing.
	hostBase = "10.11.0.0"
	vrtBase  = "10.12.0.0"

	// Each slot gets a /31 from the vrt range (2 IPs per slot).
	vrtAddressesPerSlot = 2
)

// Slot holds the network addressing for a single sandbox.
type Slot struct {
	Index int

	// Derived addresses
	HostIP  net.IP // 10.11.0.{idx} — reachable from host
	VethIP  net.IP // 10.12.0.{idx*2} — host side of veth pair
	VpeerIP net.IP // 10.12.0.{idx*2+1} — namespace side of veth

	// Fixed per-namespace
	TapIP        string // 169.254.0.22
	TapMask      int    // 30
	TapMAC       string // 02:FC:00:00:00:05
	GuestIP      string // 169.254.0.21
	GuestNetMask string // 255.255.255.252
	TapName      string // tap0

	// Names
	NamespaceID string // ns-{idx}
	VethName    string // veth-{idx}
}

// NewSlot computes the addressing for the given slot index (1-based).
// Index must be in [1, 32767] so that veth offset (index*2) fits in 16 bits.
func NewSlot(index int) *Slot {
	if index < 1 || index > 32767 {
		panic(fmt.Sprintf("slot index %d out of range [1, 32767]", index))
	}

	hostBaseIP := net.ParseIP(hostBase).To4()
	vrtBaseIP := net.ParseIP(vrtBase).To4()

	hostIP := make(net.IP, 4)
	copy(hostIP, hostBaseIP)
	hostIP[2] = hostBaseIP[2] + byte(index>>8)
	hostIP[3] = hostBaseIP[3] + byte(index&0xFF)

	vethOffset := index * vrtAddressesPerSlot
	vethIP := make(net.IP, 4)
	copy(vethIP, vrtBaseIP)
	vethIP[2] = vrtBaseIP[2] + byte(vethOffset>>8)
	vethIP[3] = vrtBaseIP[3] + byte(vethOffset&0xFF)

	vpeerOffset := vethOffset + 1
	vpeerIP := make(net.IP, 4)
	copy(vpeerIP, vrtBaseIP)
	vpeerIP[2] = vrtBaseIP[2] + byte(vpeerOffset>>8)
	vpeerIP[3] = vrtBaseIP[3] + byte(vpeerOffset&0xFF)

	return &Slot{
		Index:        index,
		HostIP:       hostIP,
		VethIP:       vethIP,
		VpeerIP:      vpeerIP,
		TapIP:        tapIP,
		TapMask:      tapMask,
		TapMAC:       tapMAC,
		GuestIP:      guestIP,
		GuestNetMask: guestNetMask,
		TapName:      tapName,
		NamespaceID:  fmt.Sprintf("wrenn-ns-%d", index),
		VethName:     fmt.Sprintf("wrenn-veth-%d", index),
	}
}

// CreateNetwork sets up the full network topology for a sandbox:
//   - Named network namespace
//   - Veth pair bridging host and namespace
//   - TAP device inside namespace for Cloud Hypervisor
//   - Routes and NAT rules for connectivity
//
// On error, all partially created resources are rolled back.
func CreateNetwork(slot *Slot) error {
	// Lock this goroutine to the OS thread — required for netns manipulation.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Save host namespace.
	hostNS, err := netns.Get()
	if err != nil {
		return fmt.Errorf("get host namespace: %w", err)
	}
	defer hostNS.Close()
	defer func() { _ = netns.Set(hostNS) }()

	// rollbacks accumulates cleanup functions; on error they run in reverse.
	var rollbacks []func()
	rollback := func() {
		for i := len(rollbacks) - 1; i >= 0; i-- {
			rollbacks[i]()
		}
	}

	// Create named network namespace.
	ns, err := netns.NewNamed(slot.NamespaceID)
	if err != nil {
		return fmt.Errorf("create namespace %s: %w", slot.NamespaceID, err)
	}
	defer ns.Close()
	// Deleting the namespace also cleans up TAP, loopback, namespace-internal
	// routes, and namespace-internal iptables rules.
	rollbacks = append(rollbacks, func() {
		_ = netns.DeleteNamed(slot.NamespaceID)
	})
	// We are now inside the new namespace.

	slog.Info("created network namespace", "ns", slot.NamespaceID)

	// Create veth pair. Both ends start in the new namespace.
	vethAttrs := netlink.NewLinkAttrs()
	vethAttrs.Name = slot.VethName
	veth := &netlink.Veth{
		LinkAttrs: vethAttrs,
		PeerName:  "eth0",
	}
	if err := netlink.LinkAdd(veth); err != nil {
		rollback()
		return fmt.Errorf("create veth pair: %w", err)
	}

	// Configure vpeer (eth0) inside namespace.
	vpeer, err := netlink.LinkByName("eth0")
	if err != nil {
		rollback()
		return fmt.Errorf("find eth0: %w", err)
	}
	vpeerAddr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   slot.VpeerIP,
			Mask: net.CIDRMask(31, 32),
		},
	}
	if err := netlink.AddrAdd(vpeer, vpeerAddr); err != nil {
		rollback()
		return fmt.Errorf("set vpeer addr: %w", err)
	}
	if err := netlink.LinkSetUp(vpeer); err != nil {
		rollback()
		return fmt.Errorf("bring up vpeer: %w", err)
	}

	// Move veth to host namespace.
	vethLink, err := netlink.LinkByName(slot.VethName)
	if err != nil {
		rollback()
		return fmt.Errorf("find veth: %w", err)
	}
	if err := netlink.LinkSetNsFd(vethLink, int(hostNS)); err != nil {
		rollback()
		return fmt.Errorf("move veth to host ns: %w", err)
	}
	// Once the veth is in the host namespace, we need to clean it up from there.
	rollbacks = append(rollbacks, func() {
		if l, err := netlink.LinkByName(slot.VethName); err == nil {
			_ = netlink.LinkDel(l)
		}
	})

	// Create TAP device inside namespace.
	tapAttrs := netlink.NewLinkAttrs()
	tapAttrs.Name = tapName
	tapAttrs.TxQLen = 5000 // Up from default 1000 to reduce drops under bursty traffic.
	tap := &netlink.Tuntap{
		LinkAttrs: tapAttrs,
		Mode:      netlink.TUNTAP_MODE_TAP,
	}
	if err := netlink.LinkAdd(tap); err != nil {
		rollback()
		return fmt.Errorf("create tap device: %w", err)
	}
	tapLink, err := netlink.LinkByName(tapName)
	if err != nil {
		rollback()
		return fmt.Errorf("find tap: %w", err)
	}
	tapAddr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   net.ParseIP(tapIP),
			Mask: net.CIDRMask(tapMask, 32),
		},
	}
	if err := netlink.AddrAdd(tapLink, tapAddr); err != nil {
		rollback()
		return fmt.Errorf("set tap addr: %w", err)
	}
	if err := netlink.LinkSetUp(tapLink); err != nil {
		rollback()
		return fmt.Errorf("bring up tap: %w", err)
	}

	// Bring up loopback.
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		rollback()
		return fmt.Errorf("find loopback: %w", err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		rollback()
		return fmt.Errorf("bring up loopback: %w", err)
	}

	// Default route inside namespace — traffic exits via veth on host.
	if err := netlink.RouteAdd(&netlink.Route{
		Scope: netlink.SCOPE_UNIVERSE,
		Gw:    slot.VethIP,
	}); err != nil {
		rollback()
		return fmt.Errorf("add default route in namespace: %w", err)
	}

	// Enable IP forwarding inside namespace (eth0 -> tap0).
	if err := nsExec(slot.NamespaceID,
		"sysctl", "-w", "net.ipv4.ip_forward=1",
	); err != nil {
		rollback()
		return fmt.Errorf("enable ip_forward in namespace: %w", err)
	}

	// NAT rules inside namespace:
	// Outbound: guest (169.254.0.21) -> internet. SNAT to vpeer IP so replies return.
	if err := iptables(slot.NamespaceID,
		"-t", "nat", "-A", "POSTROUTING",
		"-o", "eth0", "-s", guestIP,
		"-j", "SNAT", "--to", slot.VpeerIP.String(),
	); err != nil {
		rollback()
		return fmt.Errorf("add SNAT rule: %w", err)
	}
	// Inbound: host -> guest. Packets arrive with dst=hostIP, DNAT to guest IP.
	if err := iptables(slot.NamespaceID,
		"-t", "nat", "-A", "PREROUTING",
		"-i", "eth0", "-d", slot.HostIP.String(),
		"-j", "DNAT", "--to", guestIP,
	); err != nil {
		rollback()
		return fmt.Errorf("add DNAT rule: %w", err)
	}

	// Switch back to host namespace for host-side config.
	if err := netns.Set(hostNS); err != nil {
		rollback()
		return fmt.Errorf("switch to host ns: %w", err)
	}

	// Configure veth on host side.
	hostVeth, err := netlink.LinkByName(slot.VethName)
	if err != nil {
		rollback()
		return fmt.Errorf("find veth in host: %w", err)
	}
	vethAddr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   slot.VethIP,
			Mask: net.CIDRMask(31, 32),
		},
	}
	if err := netlink.AddrAdd(hostVeth, vethAddr); err != nil {
		rollback()
		return fmt.Errorf("set veth addr: %w", err)
	}
	if err := netlink.LinkSetUp(hostVeth); err != nil {
		rollback()
		return fmt.Errorf("bring up veth: %w", err)
	}

	// Route to sandbox's host IP via vpeer.
	_, hostNet, _ := net.ParseCIDR(fmt.Sprintf("%s/32", slot.HostIP.String()))
	if err := netlink.RouteAdd(&netlink.Route{
		Dst: hostNet,
		Gw:  slot.VpeerIP,
	}); err != nil {
		rollback()
		return fmt.Errorf("add host route: %w", err)
	}
	rollbacks = append(rollbacks, func() {
		_ = netlink.RouteDel(&netlink.Route{Dst: hostNet, Gw: slot.VpeerIP})
	})

	// Find default gateway interface for FORWARD rules.
	defaultIface, err := getDefaultInterface()
	if err != nil {
		rollback()
		return fmt.Errorf("get default interface: %w", err)
	}

	// FORWARD rules: allow traffic between veth and default interface.
	if err := iptablesHost(
		"-A", "FORWARD",
		"-i", slot.VethName, "-o", defaultIface,
		"-j", "ACCEPT",
	); err != nil {
		rollback()
		return fmt.Errorf("add forward rule (out): %w", err)
	}
	rollbacks = append(rollbacks, func() {
		_ = iptablesHost("-D", "FORWARD", "-i", slot.VethName, "-o", defaultIface, "-j", "ACCEPT")
	})

	if err := iptablesHost(
		"-A", "FORWARD",
		"-i", defaultIface, "-o", slot.VethName,
		"-j", "ACCEPT",
	); err != nil {
		rollback()
		return fmt.Errorf("add forward rule (in): %w", err)
	}
	rollbacks = append(rollbacks, func() {
		_ = iptablesHost("-D", "FORWARD", "-i", defaultIface, "-o", slot.VethName, "-j", "ACCEPT")
	})

	// MASQUERADE for outbound traffic from sandbox.
	// After SNAT inside the namespace, outbound packets arrive on the host
	// with source = vpeerIP, so we match on that (not hostIP).
	if err := iptablesHost(
		"-t", "nat", "-A", "POSTROUTING",
		"-s", fmt.Sprintf("%s/32", slot.VpeerIP.String()),
		"-o", defaultIface,
		"-j", "MASQUERADE",
	); err != nil {
		rollback()
		return fmt.Errorf("add masquerade rule: %w", err)
	}
	rollbacks = append(rollbacks, func() {
		_ = iptablesHost("-t", "nat", "-D", "POSTROUTING", "-s", fmt.Sprintf("%s/32", slot.VpeerIP.String()), "-o", defaultIface, "-j", "MASQUERADE")
	})

	slog.Info("network created",
		"ns", slot.NamespaceID,
		"host_ip", slot.HostIP.String(),
		"guest_ip", guestIP,
	)

	return nil
}

// RemoveNetwork tears down the network topology for a sandbox.
// All steps are attempted even if earlier ones fail. Returns a combined
// error describing which cleanup steps failed.
func RemoveNetwork(slot *Slot) error {
	if slot == nil {
		return nil
	}
	var errs []error

	defaultIface, _ := getDefaultInterface()

	// Remove host-side iptables rules.
	if defaultIface != "" {
		if err := iptablesHost(
			"-D", "FORWARD",
			"-i", slot.VethName, "-o", defaultIface,
			"-j", "ACCEPT",
		); err != nil {
			errs = append(errs, fmt.Errorf("remove forward rule (out): %w", err))
		}
		if err := iptablesHost(
			"-D", "FORWARD",
			"-i", defaultIface, "-o", slot.VethName,
			"-j", "ACCEPT",
		); err != nil {
			errs = append(errs, fmt.Errorf("remove forward rule (in): %w", err))
		}
		if err := iptablesHost(
			"-t", "nat", "-D", "POSTROUTING",
			"-s", fmt.Sprintf("%s/32", slot.VpeerIP.String()),
			"-o", defaultIface,
			"-j", "MASQUERADE",
		); err != nil {
			errs = append(errs, fmt.Errorf("remove masquerade rule: %w", err))
		}
	} else {
		errs = append(errs, fmt.Errorf("could not determine default interface; host iptables rules not removed"))
	}

	// Remove host route.
	_, hostNet, _ := net.ParseCIDR(fmt.Sprintf("%s/32", slot.HostIP.String()))
	if err := netlink.RouteDel(&netlink.Route{
		Dst: hostNet,
		Gw:  slot.VpeerIP,
	}); err != nil {
		errs = append(errs, fmt.Errorf("remove host route: %w", err))
	}

	// Delete veth (also destroys the peer in the namespace).
	if veth, err := netlink.LinkByName(slot.VethName); err == nil {
		if err := netlink.LinkDel(veth); err != nil {
			errs = append(errs, fmt.Errorf("delete veth: %w", err))
		}
	}

	// Delete the named namespace.
	if err := netns.DeleteNamed(slot.NamespaceID); err != nil {
		errs = append(errs, fmt.Errorf("delete namespace: %w", err))
	}

	slog.Info("network removed", "ns", slot.NamespaceID, "cleanup_errors", len(errs))

	return errors.Join(errs...)
}

var privateEgressGuardNets = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16",
	"100.64.0.0/10",
}

var sandboxSuperNets = []string{"10.11.0.0/16", "10.12.0.0/16"}

const vethMatch = "wrenn-veth+"

// Installs host-level protections that stop capsule from (a) leaking traffic
// destined to private ranges onto the upstream network via the public NIC
// and (b) reaching another capsule's host-reachable IP.
func EnsureEgressGuard() error {
	defaultIface, err := getDefaultInterface()
	if err != nil {
		return fmt.Errorf("resolve default interface: %w", err)
	}

	// Drop forwarded capsule traffic destined to private ranges out the public
	// NIC. Insert at the head of FORWARD so it precedes the per-sandbox ACCEPT
	// rules appended when a sandbox is created.
	for _, n := range privateEgressGuardNets {
		if err := ensureHostRule(
			[]string{"-C", "FORWARD", "-o", defaultIface, "-d", n, "-j", "DROP"},
			[]string{"-I", "FORWARD", "1", "-o", defaultIface, "-d", n, "-j", "DROP"},
		); err != nil {
			return fmt.Errorf("install egress guard for %s: %w", n, err)
		}
	}

	// Deny capsule-to-capsule forwarding outright: a veth may reach the uplink,
	// never another sandbox's veth. This closes cross-tenant reach to the
	// unauthenticated guest agent even for allocated slots.
	if err := ensureHostRule(
		[]string{"-C", "FORWARD", "-i", vethMatch, "-o", vethMatch, "-j", "DROP"},
		[]string{"-I", "FORWARD", "1", "-i", vethMatch, "-o", vethMatch, "-j", "DROP"},
	); err != nil {
		return fmt.Errorf("install inter-sandbox guard: %w", err)
	}

	// Deny capsule-initiated connections to the host's own services. The
	// FORWARD guard above only covers transit traffic; packets addressed to
	// one of the host's own IPs are delivered locally via INPUT and would
	// otherwise reach anything the host binds on all interfaces (control
	// plane, host agent, SSH). Replies to host-initiated connections
	// (e.g. envd health checks / exec, where the host is the client) arrive
	// on the same veth and MUST still be accepted, so allow ESTABLISHED first
	// and drop only NEW capsule-sourced flows. Guest DNS points at public
	// resolvers (routed out the uplink, not INPUT), so this does not affect
	// name resolution.
	// Insert the DROP first, then prepend the ESTABLISHED accept ahead of it
	// (each "-I INPUT 1" prepends), so the final order is always accept-then-
	// drop without hardcoding a chain position that other host rules could
	// shift.
	if err := ensureHostRule(
		[]string{"-C", "INPUT", "-i", vethMatch, "-j", "DROP"},
		[]string{"-I", "INPUT", "1", "-i", vethMatch, "-j", "DROP"},
	); err != nil {
		return fmt.Errorf("install host input guard: %w", err)
	}
	if err := ensureHostRule(
		[]string{"-C", "INPUT", "-i", vethMatch, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
		[]string{"-I", "INPUT", "1", "-i", vethMatch, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	); err != nil {
		return fmt.Errorf("install host input allow-established: %w", err)
	}

	// Blackhole the sandbox supernets so packets to unallocated slot IPs are
	// dropped rather than routed to the default gateway.
	for _, cidr := range sandboxSuperNets {
		if err := ensureBlackholeRoute(cidr); err != nil {
			return fmt.Errorf("blackhole %s: %w", cidr, err)
		}
	}

	slog.Info("egress guard installed", "default_iface", defaultIface)
	return nil
}

// ensureHostRule inserts a host iptables rule only when an identical rule is
// not already present, keeping EnsureEgressGuard idempotent across restarts.
func ensureHostRule(checkArgs, insertArgs []string) error {
	if exec.Command("iptables", checkArgs...).Run() == nil {
		return nil // already present
	}
	return iptablesHost(insertArgs...)
}

// ensureBlackholeRoute idempotently installs a blackhole route via `ip route
// replace` (a no-op if the route already matches).
func ensureBlackholeRoute(cidr string) error {
	cmd := exec.Command("ip", "route", "replace", "blackhole", cidr)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip route replace blackhole %s: %s: %w", cidr, string(out), err)
	}
	return nil
}

// nsExec runs a command inside a network namespace.
func nsExec(nsName string, command string, args ...string) error {
	cmdArgs := append([]string{"netns", "exec", nsName, command}, args...)
	cmd := exec.Command("ip", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %s: %w", command, args, string(out), err)
	}
	return nil
}

// iptables runs an iptables command inside a network namespace.
func iptables(nsName string, args ...string) error {
	cmdArgs := append([]string{"netns", "exec", nsName, "iptables"}, args...)
	cmd := exec.Command("ip", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %v: %s: %w", args, string(out), err)
	}
	return nil
}

// iptablesHost runs an iptables command in the host namespace.
func iptablesHost(args ...string) error {
	cmd := exec.Command("iptables", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %v: %s: %w", args, string(out), err)
	}
	return nil
}

// getDefaultInterface returns the name of the host's default gateway interface.
func getDefaultInterface() (string, error) {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return "", fmt.Errorf("list routes: %w", err)
	}
	for _, r := range routes {
		if r.Dst == nil || r.Dst.String() == "0.0.0.0/0" {
			link, err := netlink.LinkByIndex(r.LinkIndex)
			if err != nil {
				return "", fmt.Errorf("get link by index %d: %w", r.LinkIndex, err)
			}
			return link.Attrs().Name, nil
		}
	}
	return "", fmt.Errorf("no default route found")
}
