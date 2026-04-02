// SPDX-License-Identifier: Apache-2.0
// Modifications by M/S Omukk

package snapshot

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"
)

const (
	// DefaultBlockSize is 4KB — standard page size for Firecracker.
	DefaultBlockSize int64 = 4096
)

// ProcessMemfile reads a full memory file produced by Firecracker's
// PUT /snapshot/create, identifies non-zero blocks, and writes only those
// blocks to a compact diff file. Returns the Header describing the mapping.
//
// The output diff file contains non-zero blocks written sequentially.
// The header maps each block in the full address space to either:
//   - A position in the diff file (for non-zero blocks)
//   - uuid.Nil (for zero/empty blocks, served as zeros without I/O)
//
// buildID identifies this snapshot generation in the header chain.
func ProcessMemfile(memfilePath, diffPath, headerPath string, buildID uuid.UUID) (*Header, error) {
	src, err := os.Open(memfilePath)
	if err != nil {
		return nil, fmt.Errorf("open memfile: %w", err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat memfile: %w", err)
	}
	memSize := info.Size()

	dst, err := os.Create(diffPath)
	if err != nil {
		return nil, fmt.Errorf("create diff file: %w", err)
	}
	defer dst.Close()

	totalBlocks := TotalBlocks(memSize, DefaultBlockSize)
	dirty := make([]bool, totalBlocks)
	empty := make([]bool, totalBlocks)
	buf := make([]byte, DefaultBlockSize)

	for i := int64(0); i < totalBlocks; i++ {
		n, err := io.ReadFull(src, buf)
		if err != nil && err != io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("read block %d: %w", i, err)
		}

		// Zero-pad the last block if it's short.
		if int64(n) < DefaultBlockSize {
			for j := n; j < int(DefaultBlockSize); j++ {
				buf[j] = 0
			}
		}

		if isZeroBlock(buf) {
			empty[i] = true
			continue
		}

		dirty[i] = true
		if _, err := dst.Write(buf); err != nil {
			return nil, fmt.Errorf("write diff block %d: %w", i, err)
		}
	}

	// Build header.
	dirtyMappings := CreateMapping(buildID, dirty, DefaultBlockSize)
	emptyMappings := CreateMapping(uuid.Nil, empty, DefaultBlockSize)
	merged := MergeMappings(dirtyMappings, emptyMappings)
	normalized := NormalizeMappings(merged)

	metadata := NewMetadata(buildID, uint64(DefaultBlockSize), uint64(memSize))
	header, err := NewHeader(metadata, normalized)
	if err != nil {
		return nil, fmt.Errorf("create header: %w", err)
	}

	// Write header to disk.
	headerData, err := Serialize(metadata, normalized)
	if err != nil {
		return nil, fmt.Errorf("serialize header: %w", err)
	}
	if err := os.WriteFile(headerPath, headerData, 0644); err != nil {
		return nil, fmt.Errorf("write header: %w", err)
	}

	return header, nil
}

// ProcessMemfileWithParent processes a memory file as a new generation on top
// of an existing parent header. The new diff file contains only blocks that
// differ from what the parent header maps. This is used for re-pause of a
// sandbox that was restored from a snapshot.
func ProcessMemfileWithParent(memfilePath, diffPath, headerPath string, parentHeader *Header, buildID uuid.UUID) (*Header, error) {
	src, err := os.Open(memfilePath)
	if err != nil {
		return nil, fmt.Errorf("open memfile: %w", err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat memfile: %w", err)
	}
	memSize := info.Size()

	dst, err := os.Create(diffPath)
	if err != nil {
		return nil, fmt.Errorf("create diff file: %w", err)
	}
	defer dst.Close()

	totalBlocks := TotalBlocks(memSize, DefaultBlockSize)
	dirty := make([]bool, totalBlocks)
	buf := make([]byte, DefaultBlockSize)

	for i := int64(0); i < totalBlocks; i++ {
		n, err := io.ReadFull(src, buf)
		if err != nil && err != io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("read block %d: %w", i, err)
		}

		if int64(n) < DefaultBlockSize {
			for j := n; j < int(DefaultBlockSize); j++ {
				buf[j] = 0
			}
		}

		if isZeroBlock(buf) {
			// For a diff memfile, zero blocks mean "not dirtied since resume" —
			// they should inherit the parent's mapping, not be zero-filled.
			continue
		}

		dirty[i] = true
		if _, err := dst.Write(buf); err != nil {
			return nil, fmt.Errorf("write diff block %d: %w", i, err)
		}
	}

	// Only dirty blocks go into the diff overlay; MergeMappings preserves the
	// parent's mapping for everything else.
	dirtyMappings := CreateMapping(buildID, dirty, DefaultBlockSize)
	merged := MergeMappings(parentHeader.Mapping, dirtyMappings)
	normalized := NormalizeMappings(merged)

	metadata := parentHeader.Metadata.NextGeneration(buildID)
	header, err := NewHeader(metadata, normalized)
	if err != nil {
		return nil, fmt.Errorf("create header: %w", err)
	}

	headerData, err := Serialize(metadata, normalized)
	if err != nil {
		return nil, fmt.Errorf("serialize header: %w", err)
	}
	if err := os.WriteFile(headerPath, headerData, 0644); err != nil {
		return nil, fmt.Errorf("write header: %w", err)
	}

	return header, nil
}

// MergeDiffs consolidates multiple generation diff files into a single diff
// file and resets the generation counter to 0. This is a pure file-level
// operation — no Firecracker involvement.
//
// It reads each non-nil block from the appropriate diff file (as mapped by
// the header), writes them all sequentially into a single new diff file,
// and produces a fresh header pointing only at that file.
//
// diffFiles maps build ID (string) → open file path for each generation's diff.
func MergeDiffs(header *Header, diffFiles map[string]string, mergedDiffPath, headerPath string) (*Header, error) {
	blockSize := int64(header.Metadata.BlockSize)
	mergedBuildID := uuid.New()

	// Open all source diff files.
	sources := make(map[string]*os.File, len(diffFiles))
	for id, path := range diffFiles {
		f, err := os.Open(path)
		if err != nil {
			// Close already opened files.
			for _, sf := range sources {
				sf.Close()
			}
			return nil, fmt.Errorf("open diff file for build %s: %w", id, err)
		}
		sources[id] = f
	}
	defer func() {
		for _, f := range sources {
			f.Close()
		}
	}()

	dst, err := os.Create(mergedDiffPath)
	if err != nil {
		return nil, fmt.Errorf("create merged diff file: %w", err)
	}
	defer dst.Close()

	totalBlocks := TotalBlocks(int64(header.Metadata.Size), blockSize)
	dirty := make([]bool, totalBlocks)
	empty := make([]bool, totalBlocks)
	buf := make([]byte, blockSize)

	for i := int64(0); i < totalBlocks; i++ {
		offset := i * blockSize
		mappedOffset, _, buildID, err := header.GetShiftedMapping(context.Background(), offset)
		if err != nil {
			return nil, fmt.Errorf("lookup block %d: %w", i, err)
		}

		if *buildID == uuid.Nil {
			empty[i] = true
			continue
		}

		src, ok := sources[buildID.String()]
		if !ok {
			return nil, fmt.Errorf("no diff file for build %s (block %d)", buildID, i)
		}

		if _, err := src.ReadAt(buf, mappedOffset); err != nil {
			return nil, fmt.Errorf("read block %d from build %s: %w", i, buildID, err)
		}

		dirty[i] = true
		if _, err := dst.Write(buf); err != nil {
			return nil, fmt.Errorf("write merged block %d: %w", i, err)
		}
	}

	// Build fresh header with generation 0.
	dirtyMappings := CreateMapping(mergedBuildID, dirty, blockSize)
	emptyMappings := CreateMapping(uuid.Nil, empty, blockSize)
	merged := MergeMappings(dirtyMappings, emptyMappings)
	normalized := NormalizeMappings(merged)

	metadata := NewMetadata(mergedBuildID, uint64(blockSize), header.Metadata.Size)
	newHeader, err := NewHeader(metadata, normalized)
	if err != nil {
		return nil, fmt.Errorf("create merged header: %w", err)
	}

	headerData, err := Serialize(metadata, normalized)
	if err != nil {
		return nil, fmt.Errorf("serialize merged header: %w", err)
	}
	if err := os.WriteFile(headerPath, headerData, 0644); err != nil {
		return nil, fmt.Errorf("write merged header: %w", err)
	}

	return newHeader, nil
}

// isZeroBlock checks if a block is entirely zero bytes.
func isZeroBlock(block []byte) bool {
	// Fast path: compare 8 bytes at a time.
	for i := 0; i+8 <= len(block); i += 8 {
		if block[i] != 0 || block[i+1] != 0 || block[i+2] != 0 || block[i+3] != 0 ||
			block[i+4] != 0 || block[i+5] != 0 || block[i+6] != 0 || block[i+7] != 0 {
			return false
		}
	}
	// Tail bytes.
	for i := len(block) &^ 7; i < len(block); i++ {
		if block[i] != 0 {
			return false
		}
	}
	return true
}
