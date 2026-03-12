package snapshot

import "github.com/google/uuid"

// CreateMapping converts a dirty-block bitset (represented as a []bool) into
// a sorted list of BuildMap entries. Consecutive dirty blocks are merged into
// a single entry. BuildStorageOffset tracks the sequential position in the
// compact diff file.
//
// Inspired by e2b's snapshot system (Apache 2.0, modified by Omukk).
func CreateMapping(buildID uuid.UUID, dirty []bool, blockSize int64) []*BuildMap {
	var mappings []*BuildMap
	var runStart int64 = -1
	var runLength int64
	var storageOffset uint64

	for i, set := range dirty {
		if !set {
			if runLength > 0 {
				mappings = append(mappings, &BuildMap{
					Offset:             uint64(runStart) * uint64(blockSize),
					Length:             uint64(runLength) * uint64(blockSize),
					BuildID:            buildID,
					BuildStorageOffset: storageOffset,
				})
				storageOffset += uint64(runLength) * uint64(blockSize)
				runLength = 0
			}
			runStart = -1
			continue
		}

		if runStart < 0 {
			runStart = int64(i)
			runLength = 1
		} else {
			runLength++
		}
	}

	if runLength > 0 {
		mappings = append(mappings, &BuildMap{
			Offset:             uint64(runStart) * uint64(blockSize),
			Length:             uint64(runLength) * uint64(blockSize),
			BuildID:            buildID,
			BuildStorageOffset: storageOffset,
		})
	}

	return mappings
}

// MergeMappings overlays diffMapping on top of baseMapping. Where they overlap,
// diff takes priority. The result covers the entire address space.
//
// Both inputs must be sorted by Offset. The base mapping should cover the full size.
//
// Inspired by e2b's snapshot system (Apache 2.0, modified by Omukk).
func MergeMappings(baseMapping, diffMapping []*BuildMap) []*BuildMap {
	if len(diffMapping) == 0 {
		return baseMapping
	}

	// Work on a copy of baseMapping to avoid mutating the original.
	baseCopy := make([]*BuildMap, len(baseMapping))
	for i, m := range baseMapping {
		cp := *m
		baseCopy[i] = &cp
	}

	var result []*BuildMap
	var bi, di int

	for bi < len(baseCopy) && di < len(diffMapping) {
		base := baseCopy[bi]
		diff := diffMapping[di]

		if base.Length == 0 {
			bi++
			continue
		}
		if diff.Length == 0 {
			di++
			continue
		}

		// No overlap: base entirely before diff.
		if base.Offset+base.Length <= diff.Offset {
			result = append(result, base)
			bi++
			continue
		}

		// No overlap: diff entirely before base.
		if diff.Offset+diff.Length <= base.Offset {
			result = append(result, diff)
			di++
			continue
		}

		// Base fully inside diff — skip base.
		if base.Offset >= diff.Offset && base.Offset+base.Length <= diff.Offset+diff.Length {
			bi++
			continue
		}

		// Diff fully inside base — split base around diff.
		if diff.Offset >= base.Offset && diff.Offset+diff.Length <= base.Offset+base.Length {
			leftLen := int64(diff.Offset) - int64(base.Offset)
			if leftLen > 0 {
				result = append(result, &BuildMap{
					Offset:             base.Offset,
					Length:             uint64(leftLen),
					BuildID:            base.BuildID,
					BuildStorageOffset: base.BuildStorageOffset,
				})
			}

			result = append(result, diff)
			di++

			rightShift := int64(diff.Offset) + int64(diff.Length) - int64(base.Offset)
			rightLen := int64(base.Length) - rightShift

			if rightLen > 0 {
				baseCopy[bi] = &BuildMap{
					Offset:             base.Offset + uint64(rightShift),
					Length:             uint64(rightLen),
					BuildID:            base.BuildID,
					BuildStorageOffset: base.BuildStorageOffset + uint64(rightShift),
				}
			} else {
				bi++
			}
			continue
		}

		// Base starts after diff with overlap — emit diff, trim base.
		if base.Offset > diff.Offset {
			result = append(result, diff)
			di++

			rightShift := int64(diff.Offset) + int64(diff.Length) - int64(base.Offset)
			rightLen := int64(base.Length) - rightShift

			if rightLen > 0 {
				baseCopy[bi] = &BuildMap{
					Offset:             base.Offset + uint64(rightShift),
					Length:             uint64(rightLen),
					BuildID:            base.BuildID,
					BuildStorageOffset: base.BuildStorageOffset + uint64(rightShift),
				}
			} else {
				bi++
			}
			continue
		}

		// Diff starts after base with overlap — emit left part of base.
		if diff.Offset > base.Offset {
			leftLen := int64(diff.Offset) - int64(base.Offset)
			if leftLen > 0 {
				result = append(result, &BuildMap{
					Offset:             base.Offset,
					Length:             uint64(leftLen),
					BuildID:            base.BuildID,
					BuildStorageOffset: base.BuildStorageOffset,
				})
			}
			bi++
			continue
		}
	}

	// Append remaining entries.
	result = append(result, baseCopy[bi:]...)
	result = append(result, diffMapping[di:]...)

	return result
}

// NormalizeMappings merges adjacent entries with the same BuildID.
func NormalizeMappings(mappings []*BuildMap) []*BuildMap {
	if len(mappings) == 0 {
		return nil
	}

	result := make([]*BuildMap, 0, len(mappings))
	current := &BuildMap{
		Offset:             mappings[0].Offset,
		Length:             mappings[0].Length,
		BuildID:            mappings[0].BuildID,
		BuildStorageOffset: mappings[0].BuildStorageOffset,
	}

	for i := 1; i < len(mappings); i++ {
		m := mappings[i]
		if m.BuildID == current.BuildID {
			current.Length += m.Length
		} else {
			result = append(result, current)
			current = &BuildMap{
				Offset:             m.Offset,
				Length:             m.Length,
				BuildID:            m.BuildID,
				BuildStorageOffset: m.BuildStorageOffset,
			}
		}
	}
	result = append(result, current)

	return result
}
