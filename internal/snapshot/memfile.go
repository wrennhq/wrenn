package snapshot

import (
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
	empty := make([]bool, totalBlocks)
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
			empty[i] = true
			continue
		}

		dirty[i] = true
		if _, err := dst.Write(buf); err != nil {
			return nil, fmt.Errorf("write diff block %d: %w", i, err)
		}
	}

	// Build new generation header merged with parent.
	dirtyMappings := CreateMapping(buildID, dirty, DefaultBlockSize)
	emptyMappings := CreateMapping(uuid.Nil, empty, DefaultBlockSize)
	diffMapping := MergeMappings(dirtyMappings, emptyMappings)
	merged := MergeMappings(parentHeader.Mapping, diffMapping)
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
