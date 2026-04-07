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
func NewSlot(index int) *Slot {
	hostBaseIP := net.ParseIP(hostBase).To4()
	vrtBaseIP := net.ParseIP(vrtBase).To4()

	hostIP := make(net.IP, 4)
	copy(hostIP, hostBaseIP)
	hostIP[2] += byte(index >> 8)
	hostIP[3] += byte(index & 0xFF)

	vethOffset := index * vrtAddressesPerSlot
	vethIP := make(net.IP, 4)
	copy(vethIP, vrtBaseIP)
	vethIP[2] += byte(vethOffset >> 8)
	vethIP[3] += byte(vethOffset & 0xFF)

	vpeerOffset := vethOffset + 1
	vpeerIP := make(net.IP, 4)
	copy(vpeerIP, vrtBaseIP)
	vpeerIP[2] += byte(vpeerOffset >> 8)
	vpeerIP[3] += byte(vpeerOffset & 0xFF)

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
//   - TAP device inside namespace for Firecracker
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
