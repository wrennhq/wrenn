// SPDX-License-Identifier: Apache-2.0

package port

import (
	"slices"
)

type ScannerFilter struct {
	State string
	IPs   []string
}

func (sf *ScannerFilter) Match(conn *ConnStat) bool {
	// Filter is an empty struct.
	if sf.State == "" && len(sf.IPs) == 0 {
		return false
	}

	ipMatch := slices.Contains(sf.IPs, conn.LocalIP)

	if ipMatch && sf.State == conn.Status {
		return true
	}

	return false
}
