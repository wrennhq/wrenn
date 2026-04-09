// SPDX-License-Identifier: Apache-2.0

package port

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ConnStat represents a single TCP connection read from /proc/net/tcp(6).
// It contains only the fields needed by the port scanner and forwarder.
type ConnStat struct {
	LocalIP   string
	LocalPort uint32
	Status    string
	Family    uint32 // syscall.AF_INET or syscall.AF_INET6
	Inode     uint64 // socket inode, unique per connection
}

// tcpStates maps the hex state values from /proc/net/tcp to string names
// matching the gopsutil convention used by ScannerFilter.
var tcpStates = map[string]string{
	"01": "ESTABLISHED",
	"02": "SYN_SENT",
	"03": "SYN_RECV",
	"04": "FIN_WAIT1",
	"05": "FIN_WAIT2",
	"06": "TIME_WAIT",
	"07": "CLOSE",
	"08": "CLOSE_WAIT",
	"09": "LAST_ACK",
	"0A": "LISTEN",
	"0B": "CLOSING",
}

// ReadTCPConnections reads /proc/net/tcp and /proc/net/tcp6 and returns
// all TCP connections. This avoids the /proc/{pid}/fd walk that gopsutil
// performs, which is unsafe across Firecracker snapshot/restore boundaries.
func ReadTCPConnections() ([]ConnStat, error) {
	var conns []ConnStat

	tcp4, err := parseProcNetTCP("/proc/net/tcp", syscall.AF_INET)
	if err != nil {
		return nil, fmt.Errorf("parse /proc/net/tcp: %w", err)
	}
	conns = append(conns, tcp4...)

	tcp6, err := parseProcNetTCP("/proc/net/tcp6", syscall.AF_INET6)
	if err != nil {
		return nil, fmt.Errorf("parse /proc/net/tcp6: %w", err)
	}
	conns = append(conns, tcp6...)

	return conns, nil
}

// parseProcNetTCP reads a single /proc/net/tcp or /proc/net/tcp6 file.
//
// Format (fields are whitespace-separated):
//
//	sl  local_address rem_address   st tx_queue:rx_queue tr:tm->when retrnsmt   uid  timeout inode
//	0:  0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000   1000 0       12345
func parseProcNetTCP(path string, family uint32) ([]ConnStat, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var conns []ConnStat
	scanner := bufio.NewScanner(f)

	// Skip header line.
	scanner.Scan()

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		// fields[1] = local_address (hex_ip:hex_port)
		ip, port, err := parseHexAddr(fields[1], family)
		if err != nil {
			continue
		}

		// fields[3] = state (hex)
		state, ok := tcpStates[fields[3]]
		if !ok {
			state = "UNKNOWN"
		}

		// fields[9] = inode
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}

		conns = append(conns, ConnStat{
			LocalIP:   ip,
			LocalPort: port,
			Status:    state,
			Family:    family,
			Inode:     inode,
		})
	}

	return conns, scanner.Err()
}

// parseHexAddr parses "HEXIP:HEXPORT" from /proc/net/tcp.
// IPv4 addresses are 8 hex chars (4 bytes, little-endian per 32-bit word).
// IPv6 addresses are 32 hex chars (16 bytes, little-endian per 32-bit word).
func parseHexAddr(s string, family uint32) (string, uint32, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid address: %s", s)
	}

	port64, err := strconv.ParseUint(parts[1], 16, 32)
	if err != nil {
		return "", 0, err
	}

	ipHex := parts[0]
	ipBytes, err := hex.DecodeString(ipHex)
	if err != nil {
		return "", 0, err
	}

	var ip net.IP
	if family == syscall.AF_INET {
		if len(ipBytes) != 4 {
			return "", 0, fmt.Errorf("invalid IPv4 length: %d", len(ipBytes))
		}
		// /proc/net/tcp stores IPv4 as a single little-endian 32-bit word.
		ip = net.IPv4(ipBytes[3], ipBytes[2], ipBytes[1], ipBytes[0])
	} else {
		if len(ipBytes) != 16 {
			return "", 0, fmt.Errorf("invalid IPv6 length: %d", len(ipBytes))
		}
		// /proc/net/tcp6 stores IPv6 as four little-endian 32-bit words.
		ip = make(net.IP, 16)
		for i := 0; i < 4; i++ {
			ip[i*4+0] = ipBytes[i*4+3]
			ip[i*4+1] = ipBytes[i*4+2]
			ip[i*4+2] = ipBytes[i*4+1]
			ip[i*4+3] = ipBytes[i*4+0]
		}
	}

	return ip.String(), uint32(port64), nil
}
