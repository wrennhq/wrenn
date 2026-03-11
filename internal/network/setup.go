package network

import (
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"runtime"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

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
	hostIP[2] += byte(index / 256)
	hostIP[3] += byte(index % 256)

	vethOffset := index * vrtAddressesPerSlot
	vethIP := make(net.IP, 4)
	copy(vethIP, vrtBaseIP)
	vethIP[2] += byte(vethOffset / 256)
	vethIP[3] += byte(vethOffset % 256)

	vpeerIP := make(net.IP, 4)
	copy(vpeerIP, vrtBaseIP)
	vpeerIP[2] += byte((vethOffset + 1) / 256)
	vpeerIP[3] += byte((vethOffset + 1) % 256)

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
		NamespaceID:  fmt.Sprintf("ns-%d", index),
		VethName:     fmt.Sprintf("veth-%d", index),
	}
}

// CreateNetwork sets up the full network topology for a sandbox:
//   - Named network namespace
//   - Veth pair bridging host and namespace
//   - TAP device inside namespace for Firecracker
//   - Routes and NAT rules for connectivity
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
	defer netns.Set(hostNS)

	// Create named network namespace.
	ns, err := netns.NewNamed(slot.NamespaceID)
	if err != nil {
		return fmt.Errorf("create namespace %s: %w", slot.NamespaceID, err)
	}
	defer ns.Close()
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
		return fmt.Errorf("create veth pair: %w", err)
	}

	// Configure vpeer (eth0) inside namespace.
	vpeer, err := netlink.LinkByName("eth0")
	if err != nil {
		return fmt.Errorf("find eth0: %w", err)
	}
	vpeerAddr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   slot.VpeerIP,
			Mask: net.CIDRMask(31, 32),
		},
	}
	if err := netlink.AddrAdd(vpeer, vpeerAddr); err != nil {
		return fmt.Errorf("set vpeer addr: %w", err)
	}
	if err := netlink.LinkSetUp(vpeer); err != nil {
		return fmt.Errorf("bring up vpeer: %w", err)
	}

	// Move veth to host namespace.
	vethLink, err := netlink.LinkByName(slot.VethName)
	if err != nil {
		return fmt.Errorf("find veth: %w", err)
	}
	if err := netlink.LinkSetNsFd(vethLink, int(hostNS)); err != nil {
		return fmt.Errorf("move veth to host ns: %w", err)
	}

	// Create TAP device inside namespace.
	tapAttrs := netlink.NewLinkAttrs()
	tapAttrs.Name = tapName
	tap := &netlink.Tuntap{
		LinkAttrs: tapAttrs,
		Mode:      netlink.TUNTAP_MODE_TAP,
	}
	if err := netlink.LinkAdd(tap); err != nil {
		return fmt.Errorf("create tap device: %w", err)
	}
	tapLink, err := netlink.LinkByName(tapName)
	if err != nil {
		return fmt.Errorf("find tap: %w", err)
	}
	tapAddr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   net.ParseIP(tapIP),
			Mask: net.CIDRMask(tapMask, 32),
		},
	}
	if err := netlink.AddrAdd(tapLink, tapAddr); err != nil {
		return fmt.Errorf("set tap addr: %w", err)
	}
	if err := netlink.LinkSetUp(tapLink); err != nil {
		return fmt.Errorf("bring up tap: %w", err)
	}

	// Bring up loopback.
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("find loopback: %w", err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		return fmt.Errorf("bring up loopback: %w", err)
	}

	// Default route inside namespace — traffic exits via veth on host.
	if err := netlink.RouteAdd(&netlink.Route{
		Scope: netlink.SCOPE_UNIVERSE,
		Gw:    slot.VethIP,
	}); err != nil {
		return fmt.Errorf("add default route in namespace: %w", err)
	}

	// Enable IP forwarding inside namespace (eth0 -> tap0).
	if err := nsExec(slot.NamespaceID,
		"sysctl", "-w", "net.ipv4.ip_forward=1",
	); err != nil {
		return fmt.Errorf("enable ip_forward in namespace: %w", err)
	}

	// NAT rules inside namespace:
	// Outbound: guest (169.254.0.21) -> internet. SNAT to vpeer IP so replies return.
	if err := iptables(slot.NamespaceID,
		"-t", "nat", "-A", "POSTROUTING",
		"-o", "eth0", "-s", guestIP,
		"-j", "SNAT", "--to", slot.VpeerIP.String(),
	); err != nil {
		return fmt.Errorf("add SNAT rule: %w", err)
	}
	// Inbound: host -> guest. Packets arrive with dst=hostIP, DNAT to guest IP.
	if err := iptables(slot.NamespaceID,
		"-t", "nat", "-A", "PREROUTING",
		"-i", "eth0", "-d", slot.HostIP.String(),
		"-j", "DNAT", "--to", guestIP,
	); err != nil {
		return fmt.Errorf("add DNAT rule: %w", err)
	}

	// Switch back to host namespace for host-side config.
	if err := netns.Set(hostNS); err != nil {
		return fmt.Errorf("switch to host ns: %w", err)
	}

	// Configure veth on host side.
	hostVeth, err := netlink.LinkByName(slot.VethName)
	if err != nil {
		return fmt.Errorf("find veth in host: %w", err)
	}
	vethAddr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   slot.VethIP,
			Mask: net.CIDRMask(31, 32),
		},
	}
	if err := netlink.AddrAdd(hostVeth, vethAddr); err != nil {
		return fmt.Errorf("set veth addr: %w", err)
	}
	if err := netlink.LinkSetUp(hostVeth); err != nil {
		return fmt.Errorf("bring up veth: %w", err)
	}

	// Route to sandbox's host IP via vpeer.
	_, hostNet, _ := net.ParseCIDR(fmt.Sprintf("%s/32", slot.HostIP.String()))
	if err := netlink.RouteAdd(&netlink.Route{
		Dst: hostNet,
		Gw:  slot.VpeerIP,
	}); err != nil {
		return fmt.Errorf("add host route: %w", err)
	}

	// Find default gateway interface for FORWARD rules.
	defaultIface, err := getDefaultInterface()
	if err != nil {
		return fmt.Errorf("get default interface: %w", err)
	}

	// FORWARD rules: allow traffic between veth and default interface.
	if err := iptablesHost(
		"-A", "FORWARD",
		"-i", slot.VethName, "-o", defaultIface,
		"-j", "ACCEPT",
	); err != nil {
		return fmt.Errorf("add forward rule (out): %w", err)
	}
	if err := iptablesHost(
		"-A", "FORWARD",
		"-i", defaultIface, "-o", slot.VethName,
		"-j", "ACCEPT",
	); err != nil {
		return fmt.Errorf("add forward rule (in): %w", err)
	}

	// MASQUERADE for outbound traffic from sandbox.
	// After SNAT inside the namespace, outbound packets arrive on the host
	// with source = vpeerIP, so we match on that (not hostIP).
	if err := iptablesHost(
		"-t", "nat", "-A", "POSTROUTING",
		"-s", fmt.Sprintf("%s/32", slot.VpeerIP.String()),
		"-o", defaultIface,
		"-j", "MASQUERADE",
	); err != nil {
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
func RemoveNetwork(slot *Slot) error {
	defaultIface, _ := getDefaultInterface()

	// Remove host-side iptables rules (best effort).
	if defaultIface != "" {
		iptablesHost(
			"-D", "FORWARD",
			"-i", slot.VethName, "-o", defaultIface,
			"-j", "ACCEPT",
		)
		iptablesHost(
			"-D", "FORWARD",
			"-i", defaultIface, "-o", slot.VethName,
			"-j", "ACCEPT",
		)
		iptablesHost(
			"-t", "nat", "-D", "POSTROUTING",
			"-s", fmt.Sprintf("%s/32", slot.VpeerIP.String()),
			"-o", defaultIface,
			"-j", "MASQUERADE",
		)
	}

	// Remove host route.
	_, hostNet, _ := net.ParseCIDR(fmt.Sprintf("%s/32", slot.HostIP.String()))
	netlink.RouteDel(&netlink.Route{
		Dst: hostNet,
		Gw:  slot.VpeerIP,
	})

	// Delete veth (also destroys the peer in the namespace).
	if veth, err := netlink.LinkByName(slot.VethName); err == nil {
		netlink.LinkDel(veth)
	}

	// Delete the named namespace.
	netns.DeleteNamed(slot.NamespaceID)

	slog.Info("network removed", "ns", slot.NamespaceID)

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
