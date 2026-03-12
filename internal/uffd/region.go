package uffd

import "fmt"

// Region is a mapping of guest memory to host virtual address space.
// Firecracker sends these as JSON when connecting to the UFFD socket.
// The JSON field names match Firecracker's UFFD protocol.
type Region struct {
	BaseHostVirtAddr uintptr `json:"base_host_virt_addr"`
	Size             uintptr `json:"size"`
	Offset           uintptr `json:"offset"`
	PageSize         uintptr `json:"page_size_kib"` // Actually in bytes despite the name.
}

// Mapping translates between host virtual addresses and logical memory offsets.
type Mapping struct {
	Regions []Region
}

// NewMapping creates a Mapping from a list of regions.
func NewMapping(regions []Region) *Mapping {
	return &Mapping{Regions: regions}
}

// GetOffset converts a host virtual address to a logical memory file offset
// and returns the page size. This is called on every UFFD page fault.
func (m *Mapping) GetOffset(hostVirtAddr uintptr) (int64, uintptr, error) {
	for _, r := range m.Regions {
		if hostVirtAddr >= r.BaseHostVirtAddr && hostVirtAddr < r.BaseHostVirtAddr+r.Size {
			offset := int64(hostVirtAddr-r.BaseHostVirtAddr) + int64(r.Offset)
			return offset, r.PageSize, nil
		}
	}
	return 0, 0, fmt.Errorf("address %#x not found in any memory region", hostVirtAddr)
}
