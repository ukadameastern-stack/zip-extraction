// Package bombdefence implements the 11-rule zip-bomb defence per FR-7 +
// BR-BOMB-001..009. PreCheck handles aggregate archive rules (#1, #4),
// EntryCheck handles per-entry pre-stream rules (#5, #6, #9),
// NewLimitedReader handles streaming rules (#2, #3) with short-circuit
// behaviour per Q5 of application design / BR-BOMB-003/004, and OverlapCheck
// handles rule #11 (BR-BOMB-009) — overlapping compressed-data ranges that
// produce Fifield non-recursive bombs.
//
// Rule #10 (extraction hard timeout) is enforced at the orchestrator level via
// context.WithTimeout — not by this package.
// Rules #7, #8 (path safety) are delegated to internal/validation per Q8 of
// application design (SECURITY-11 separation of concerns).
package bombdefence

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/config"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
)

// Checker applies the bomb-defence rules.
type Checker struct {
	cfg config.BombDefenceConfig
}

// New constructs a Checker with the given configuration.
func New(cfg config.BombDefenceConfig) *Checker { return &Checker{cfg: cfg} }

// PreCheck applies rules #1 (max compressed archive size) and #4 (max entry
// count) using archive metadata only. Called immediately after archive/zip
// successfully parses the central directory.
func (c *Checker) PreCheck(meta extraction.ArchiveMetadata) error {
	if meta.TotalCompressedBytes > c.cfg.MaxCompressedSizeBytes {
		return &extraction.BombDefenceError{
			Rule:   1,
			Reason: formatExceeded("compressed size", meta.TotalCompressedBytes, c.cfg.MaxCompressedSizeBytes),
		}
	}
	if meta.EntryCount > c.cfg.MaxEntryCount {
		return &extraction.BombDefenceError{
			Rule:   4,
			Reason: formatExceededInt("entry count", meta.EntryCount, c.cfg.MaxEntryCount),
		}
	}
	return nil
}

// EntryCheck applies rules #5 (max directory depth), #6 (symlink rejection),
// and #9 (max single-file decompressed size using the declared header value).
// Rule #9 is re-checked indirectly by the streaming limiter (rule #2) — this
// is defence in depth (the declared header value is untrusted but cheap to
// check up front).
func (c *Checker) EntryCheck(idx int, e extraction.EntryInfo) error {
	if e.DirectoryDepth > c.cfg.MaxDirectoryDepth {
		return &extraction.BombDefenceError{
			Rule:   5,
			Reason: formatExceededInt("directory depth", e.DirectoryDepth, c.cfg.MaxDirectoryDepth),
		}
	}
	if (os.FileMode(e.Mode) & os.ModeSymlink) != 0 {
		return &extraction.BombDefenceError{
			Rule:   6,
			Reason: "symlink entries are rejected",
		}
	}
	if e.UncompressedSize > c.cfg.MaxSingleFileSizeBytes {
		return &extraction.BombDefenceError{
			Rule:   9,
			Reason: formatExceeded("single-file size", e.UncompressedSize, c.cfg.MaxSingleFileSizeBytes),
		}
	}
	return nil
}

// OverlapCheck applies rule #11 (BR-BOMB-009): no two entries' compressed-data
// byte intervals may overlap within the archive. Defends against Fifield
// non-recursive bombs that point multiple central-directory records at the
// same compressed bytes, so each "entry" decompresses the same stream and the
// total extracted size multiplies without an obvious per-entry compression
// ratio signature.
//
// Algorithm: sort the entry data-ranges by Start, then walk linearly. Each
// entry's Start must be ≥ the previous entry's End. O(n log n) time, O(1)
// extra space (the sort is in-place on a fresh slice copy).
//
// Tolerates archives where DataOffset() failed for some entries — those are
// simply absent from the slice; surviving ranges are still checked.
func (c *Checker) OverlapCheck(meta extraction.ArchiveMetadata) error {
	ranges := meta.EntryDataRanges
	if len(ranges) < 2 {
		return nil
	}
	// Copy to avoid mutating caller's slice ordering.
	sorted := make([]extraction.EntryDataRange, len(ranges))
	copy(sorted, ranges)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })

	for i := 1; i < len(sorted); i++ {
		prev, cur := sorted[i-1], sorted[i]
		if cur.Start < prev.End {
			return &extraction.BombDefenceError{
				Rule: 11,
				Reason: fmt.Sprintf(
					"overlapping compressed data: entry %d [%d,%d) overlaps entry %d [%d,%d)",
					prev.EntryIndex, prev.Start, prev.End,
					cur.EntryIndex, cur.Start, cur.End,
				),
			}
		}
	}
	return nil
}

// NewLimitedReader wraps r in a short-circuiting reader that enforces
// rule #2 (cumulative extracted size) and rule #3 (compression ratio). The
// returned reader maintains state across reads — instances MUST NOT be shared
// across goroutines.
//
// compressedSize is the declared compressed size of the current entry, used as
// the denominator for the ratio check. Pass the cumulative compressed-bytes-so-far
// if checking ratio over the whole archive; pass 0 to disable ratio checking.
func (c *Checker) NewLimitedReader(r io.Reader, compressedSize int64) io.Reader {
	return &limitedReader{
		r:                  r,
		maxExtractedBytes:  c.cfg.MaxExtractedSizeBytes,
		maxRatio:           c.cfg.MaxCompressionRatio,
		entryCompressedSz:  compressedSize,
		smallSampleFloorSz: smallSampleFloorBytes,
	}
}

// smallSampleFloorBytes is the minimum cumulative compressed-byte count required
// before the ratio check fires. Avoids false positives on tiny streams where
// the ratio is mathematically extreme but harmless. Matches BR-BOMB-004.
const smallSampleFloorBytes = 64 * 1024

// limitedReader is the io.Reader implementation returned by NewLimitedReader.
type limitedReader struct {
	r                  io.Reader
	maxExtractedBytes  int64
	maxRatio           float64
	entryCompressedSz  int64
	smallSampleFloorSz int64

	extracted    int64
	cumulativeCS int64 // cumulative compressed bytes (accumulated externally; see Note)
	errSticky    error
}

// Read implements io.Reader. On a bomb-defence violation, Read returns
// (0, *extraction.BombDefenceError{Rule:2 | 3}) and remembers the error for
// subsequent calls (sticky).
func (lr *limitedReader) Read(p []byte) (int, error) {
	if lr.errSticky != nil {
		return 0, lr.errSticky
	}
	n, err := lr.r.Read(p)
	if n > 0 {
		lr.extracted += int64(n)
		if lr.maxExtractedBytes > 0 && lr.extracted > lr.maxExtractedBytes {
			lr.errSticky = &extraction.BombDefenceError{
				Rule:   2,
				Reason: formatExceeded("cumulative extracted size", lr.extracted, lr.maxExtractedBytes),
			}
			return 0, lr.errSticky
		}
		// Ratio check. Use the entry's declared compressed size as the denominator
		// when available; otherwise skip. Apply a small-sample floor to avoid false
		// positives.
		if lr.maxRatio > 0 && lr.entryCompressedSz > 0 && lr.entryCompressedSz >= lr.smallSampleFloorSz {
			ratio := float64(lr.extracted) / float64(lr.entryCompressedSz)
			if ratio > lr.maxRatio {
				lr.errSticky = &extraction.BombDefenceError{
					Rule:   3,
					Reason: formatRatioExceeded(ratio, lr.maxRatio),
				}
				return 0, lr.errSticky
			}
		}
	}
	return n, err
}
