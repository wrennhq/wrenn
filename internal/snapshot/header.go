// Package snapshot implements snapshot storage, header-based memory mapping,
// and memory file processing for Firecracker VM snapshots.
//
// The header system implements a generational copy-on-write memory mapping.
// Each snapshot generation stores only the blocks that changed since the
// previous generation. A Header contains a sorted list of BuildMap entries
// that together cover the entire memory address space, with each entry
// pointing to a specific generation's diff file.
//
// Inspired by e2b's snapshot system (Apache 2.0, modified by Omukk).
package snapshot

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
)

const metadataVersion = 1

// Metadata is the fixed-size header prefix describing the snapshot memory layout.
// Binary layout (little-endian, 64 bytes total):
//
//	Version     uint64   (8 bytes)
//	BlockSize   uint64   (8 bytes)
//	Size        uint64   (8 bytes) — total memory size in bytes
//	Generation  uint64   (8 bytes)
//	BuildID     [16]byte (UUID)
//	BaseBuildID [16]byte (UUID)
type Metadata struct {
	Version     uint64
	BlockSize   uint64
	Size        uint64
	Generation  uint64
	BuildID     uuid.UUID
	BaseBuildID uuid.UUID
}

// NewMetadata creates metadata for a first-generation snapshot.
func NewMetadata(buildID uuid.UUID, blockSize, size uint64) *Metadata {
	return &Metadata{
		Version:     metadataVersion,
		Generation:  0,
		BlockSize:   blockSize,
		Size:        size,
		BuildID:     buildID,
		BaseBuildID: buildID,
	}
}

// NextGeneration creates metadata for the next generation in the chain.
func (m *Metadata) NextGeneration(buildID uuid.UUID) *Metadata {
	return &Metadata{
		Version:     m.Version,
		Generation:  m.Generation + 1,
		BlockSize:   m.BlockSize,
		Size:        m.Size,
		BuildID:     buildID,
		BaseBuildID: m.BaseBuildID,
	}
}

// BuildMap maps a contiguous range of the memory address space to a specific
// generation's diff file. Binary layout (little-endian, 40 bytes):
//
//	Offset             uint64   — byte offset in the virtual address space
//	Length             uint64   — byte count (multiple of BlockSize)
//	BuildID            [16]byte — which generation's diff file, uuid.Nil = zero-fill
//	BuildStorageOffset uint64   — byte offset within that generation's diff file
type BuildMap struct {
	Offset             uint64
	Length             uint64
	BuildID            uuid.UUID
	BuildStorageOffset uint64
}

// Header is the in-memory representation of a snapshot's memory mapping.
// It provides O(log N) lookup from any memory offset to the correct
// generation's diff file and offset within it.
type Header struct {
	Metadata *Metadata
	Mapping  []*BuildMap

	// blockStarts tracks which block indices start a new BuildMap entry.
	// startMap provides direct access from block index to the BuildMap.
	blockStarts []bool
	startMap    map[int64]*BuildMap
}

// NewHeader creates a Header from metadata and mapping entries.
// If mapping is nil/empty, a single entry covering the full size is created.
func NewHeader(metadata *Metadata, mapping []*BuildMap) (*Header, error) {
	if metadata.BlockSize == 0 {
		return nil, fmt.Errorf("block size cannot be zero")
	}

	if len(mapping) == 0 {
		mapping = []*BuildMap{{
			Offset:             0,
			Length:             metadata.Size,
			BuildID:            metadata.BuildID,
			BuildStorageOffset: 0,
		}}
	}

	blocks := TotalBlocks(int64(metadata.Size), int64(metadata.BlockSize))
	starts := make([]bool, blocks)
	startMap := make(map[int64]*BuildMap, len(mapping))

	for _, m := range mapping {
		idx := BlockIdx(int64(m.Offset), int64(metadata.BlockSize))
		if idx >= 0 && idx < blocks {
			starts[idx] = true
			startMap[idx] = m
		}
	}

	return &Header{
		Metadata:    metadata,
		Mapping:     mapping,
		blockStarts: starts,
		startMap:    startMap,
	}, nil
}

// GetShiftedMapping resolves a memory offset to the corresponding diff file
// offset, remaining length, and build ID. This is the hot path called for
// every UFFD page fault.
func (h *Header) GetShiftedMapping(_ context.Context, offset int64) (mappedOffset int64, mappedLength int64, buildID *uuid.UUID, err error) {
	if offset < 0 || offset >= int64(h.Metadata.Size) {
		return 0, 0, nil, fmt.Errorf("offset %d out of bounds (size: %d)", offset, h.Metadata.Size)
	}

	blockSize := int64(h.Metadata.BlockSize)
	block := BlockIdx(offset, blockSize)

	// Walk backwards to find the BuildMap that contains this block.
	start := block
	for start >= 0 {
		if h.blockStarts[start] {
			break
		}
		start--
	}
	if start < 0 {
		return 0, 0, nil, fmt.Errorf("no mapping found for offset %d", offset)
	}

	m, ok := h.startMap[start]
	if !ok {
		return 0, 0, nil, fmt.Errorf("no mapping at block %d", start)
	}

	shift := (block - start) * blockSize
	if shift >= int64(m.Length) {
		return 0, 0, nil, fmt.Errorf("offset %d beyond mapping end (mapping offset=%d, length=%d)", offset, m.Offset, m.Length)
	}

	return int64(m.BuildStorageOffset) + shift, int64(m.Length) - shift, &m.BuildID, nil
}

// Serialize writes metadata + mapping entries to binary (little-endian).
func Serialize(metadata *Metadata, mappings []*BuildMap) ([]byte, error) {
	var buf bytes.Buffer

	if err := binary.Write(&buf, binary.LittleEndian, metadata); err != nil {
		return nil, fmt.Errorf("write metadata: %w", err)
	}

	for _, m := range mappings {
		if err := binary.Write(&buf, binary.LittleEndian, m); err != nil {
			return nil, fmt.Errorf("write mapping: %w", err)
		}
	}

	return buf.Bytes(), nil
}

// Deserialize reads a header from binary data.
func Deserialize(data []byte) (*Header, error) {
	reader := bytes.NewReader(data)

	var metadata Metadata
	if err := binary.Read(reader, binary.LittleEndian, &metadata); err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}

	var mappings []*BuildMap
	for {
		var m BuildMap
		if err := binary.Read(reader, binary.LittleEndian, &m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read mapping: %w", err)
		}
		mappings = append(mappings, &m)
	}

	return NewHeader(&metadata, mappings)
}

// Block index helpers.

func TotalBlocks(size, blockSize int64) int64 {
	return (size + blockSize - 1) / blockSize
}

func BlockIdx(offset, blockSize int64) int64 {
	return offset / blockSize
}

func BlockOffset(idx, blockSize int64) int64 {
	return idx * blockSize
}
