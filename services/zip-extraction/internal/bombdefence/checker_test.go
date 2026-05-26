package bombdefence_test

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/bombdefence"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/config"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/test/generators"
)

func defaultCfg() config.BombDefenceConfig {
	return config.BombDefenceConfig{
		MaxCompressedSizeBytes:            500 * 1024 * 1024,
		MaxExtractedSizeBytes:             2 * 1024 * 1024 * 1024,
		MaxCompressionRatio:               100,
		MaxEntryCount:                     10000,
		MaxDirectoryDepth:                 10,
		MaxSingleFileSizeBytes:            250 * 1024 * 1024,
		MaxExtractionDurationSec:          240,
		MaxTotalDeclaredUncompressedBytes: 50 * 1024 * 1024 * 1024, // 50 GB
	}
}

// BR-BOMB-001 — rule #1: compressed-size limit fires.
func TestPreCheckRule1Rejects(t *testing.T) {
	c := bombdefence.New(defaultCfg())
	err := c.PreCheck(extraction.ArchiveMetadata{
		EntryCount:           1,
		TotalCompressedBytes: 600 * 1024 * 1024,
	})
	require.Error(t, err)
	bde, ok := extraction.IsBombDefence(err)
	require.True(t, ok)
	assert.Equal(t, 1, bde.Rule)
}

// BR-BOMB-001 — rule #4: entry-count limit fires.
func TestPreCheckRule4Rejects(t *testing.T) {
	c := bombdefence.New(defaultCfg())
	err := c.PreCheck(extraction.ArchiveMetadata{
		EntryCount:           20000,
		TotalCompressedBytes: 100,
	})
	require.Error(t, err)
	bde, _ := extraction.IsBombDefence(err)
	assert.Equal(t, 4, bde.Rule)
}

// BR-BOMB-010 — rule #12: sum-of-declared-uncompressed cap fires for honestly-
// declared bombs. Untrusted input, but cheap to reject pre-stream.
func TestPreCheckRule12Rejects(t *testing.T) {
	cfg := defaultCfg()
	cfg.MaxTotalDeclaredUncompressedBytes = 1024
	c := bombdefence.New(cfg)
	err := c.PreCheck(extraction.ArchiveMetadata{
		EntryCount:                     1,
		TotalCompressedBytes:           100,
		TotalDeclaredUncompressedBytes: 2048,
	})
	require.Error(t, err)
	bde, _ := extraction.IsBombDefence(err)
	assert.Equal(t, 12, bde.Rule)
	assert.Contains(t, bde.Reason, "total declared uncompressed size")
}

// Rule #12 accepts archives exactly at the cap (boundary inclusive).
func TestPreCheckRule12AcceptsAtCap(t *testing.T) {
	cfg := defaultCfg()
	cfg.MaxTotalDeclaredUncompressedBytes = 1024
	c := bombdefence.New(cfg)
	require.NoError(t, c.PreCheck(extraction.ArchiveMetadata{
		EntryCount:                     1,
		TotalCompressedBytes:           100,
		TotalDeclaredUncompressedBytes: 1024,
	}))
}

// Rule #12 is disabled (skipped) when the cap is 0. Defensive: lets operators
// opt out without breaking the pre-check flow.
func TestPreCheckRule12DisabledWhenZero(t *testing.T) {
	cfg := defaultCfg()
	cfg.MaxTotalDeclaredUncompressedBytes = 0
	c := bombdefence.New(cfg)
	require.NoError(t, c.PreCheck(extraction.ArchiveMetadata{
		EntryCount:                     1,
		TotalCompressedBytes:           100,
		TotalDeclaredUncompressedBytes: 1 << 50, // 1 PB — astronomically over, but cap is off
	}))
}

// BR-BOMB-003 — strong invariant: bytes returned by LimitedReader ≤ cap.
func TestPropertyLimitedReaderInvariant(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := defaultCfg()
		cfg.MaxExtractedSizeBytes = 4096 // tight cap so the property is exercised
		c := bombdefence.New(cfg)

		payloadSize := rapid.IntRange(0, 8192).Draw(t, "payloadSize")
		body := bytes.NewReader(bytes.Repeat([]byte{'a'}, payloadSize))
		lr := c.NewLimitedReader(body, int64(payloadSize))

		var got int64
		buf := make([]byte, 256)
		for {
			n, err := lr.Read(buf)
			got += int64(n)
			require.LessOrEqualf(t, got, cfg.MaxExtractedSizeBytes,
				"observed bytes %d > cap %d", got, cfg.MaxExtractedSizeBytes)
			if err == io.EOF {
				return
			}
			if err != nil {
				_, ok := extraction.IsBombDefence(err)
				require.True(t, ok)
				return
			}
		}
	})
}

// PBT-03 invariant: PreCheck accepts metadata within bounds.
func TestPropertyPreCheckAcceptsInBounds(t *testing.T) {
	cfg := defaultCfg()
	c := bombdefence.New(cfg)
	rapid.Check(t, func(t *rapid.T) {
		meta := generators.ArchiveMetadata().Draw(t, "meta")
		err := c.PreCheck(meta)
		if meta.TotalCompressedBytes <= cfg.MaxCompressedSizeBytes && meta.EntryCount <= cfg.MaxEntryCount {
			require.NoError(t, err)
		}
	})
}

// PBT-03 negative: bomb-shaped pre-check rejects.
func TestPropertyPreCheckRejectsBomb(t *testing.T) {
	c := bombdefence.New(defaultCfg())
	rapid.Check(t, func(t *rapid.T) {
		rule := rapid.SampledFrom([]int{1, 4}).Draw(t, "rule")
		meta := generators.ArchiveMetadataBomb(rule).Draw(t, "meta")
		err := c.PreCheck(meta)
		require.Error(t, err)
		bde, _ := extraction.IsBombDefence(err)
		require.Equal(t, rule, bde.Rule)
	})
}

// Sanity check the empty-archive accept case (BR-STATUS-001 last row).
func TestPreCheckEmptyArchive(t *testing.T) {
	c := bombdefence.New(defaultCfg())
	require.NoError(t, c.PreCheck(extraction.ArchiveMetadata{}))
}

func TestEntryCheckRule5_Depth(t *testing.T) {
	c := bombdefence.New(defaultCfg())
	err := c.EntryCheck(1, extraction.EntryInfo{Name: strings.Repeat("a/", 12) + "x.txt", DirectoryDepth: 12})
	require.Error(t, err)
	bde, _ := extraction.IsBombDefence(err)
	assert.Equal(t, 5, bde.Rule)
}

func TestEntryCheckRule6_Symlink(t *testing.T) {
	c := bombdefence.New(defaultCfg())
	err := c.EntryCheck(1, extraction.EntryInfo{Name: "link", Mode: uint32(os.ModeSymlink)})
	require.Error(t, err)
	bde, _ := extraction.IsBombDefence(err)
	assert.Equal(t, 6, bde.Rule)
}

// BR-BOMB-004 rule #3 — ratio rule fires above the small-sample floor.
func TestLimitedReader_RatioRule(t *testing.T) {
	cfg := defaultCfg()
	cfg.MaxExtractedSizeBytes = 1 << 30 // effectively unlimited so rule #3 fires before rule #2
	cfg.MaxCompressionRatio = 10
	c := bombdefence.New(cfg)

	// 1 MB compressed (above 64 KiB floor) → ratio cap 10× means cap at 10 MB extracted.
	compressed := int64(1 * 1024 * 1024)
	body := bytes.NewReader(bytes.Repeat([]byte{'a'}, int(compressed*15))) // 15:1 ratio
	lr := c.NewLimitedReader(body, compressed)

	buf := make([]byte, 1024*1024)
	var totalRead int64
	for {
		n, err := lr.Read(buf)
		totalRead += int64(n)
		if err != nil {
			bde, ok := extraction.IsBombDefence(err)
			require.True(t, ok)
			assert.Equal(t, 3, bde.Rule)
			return
		}
	}
}

// Small-sample floor: ratio check does NOT fire under the 64 KiB compressed threshold.
func TestLimitedReader_SmallSampleFloorSkipsRatio(t *testing.T) {
	cfg := defaultCfg()
	cfg.MaxCompressionRatio = 2 // very tight; would normally fire
	c := bombdefence.New(cfg)

	compressed := int64(100) // below 64 KiB floor
	body := bytes.NewReader(bytes.Repeat([]byte{'a'}, 10000)) // 100:1 ratio
	lr := c.NewLimitedReader(body, compressed)
	buf := make([]byte, 1024)
	for {
		_, err := lr.Read(buf)
		if err == io.EOF {
			return // expected — rule #3 didn't fire
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

// Sticky-error: once tripped, subsequent reads return the same error.
func TestLimitedReader_StickyError(t *testing.T) {
	cfg := defaultCfg()
	cfg.MaxExtractedSizeBytes = 100
	c := bombdefence.New(cfg)
	body := bytes.NewReader(bytes.Repeat([]byte{'a'}, 500))
	lr := c.NewLimitedReader(body, 500)
	buf := make([]byte, 200)
	_, err1 := lr.Read(buf)
	require.Error(t, err1)
	_, err2 := lr.Read(buf)
	require.Equal(t, err1, err2, "sticky-error contract")
}

func TestPreCheck_AcceptsEmptyArchiveExplicitly(t *testing.T) {
	c := bombdefence.New(defaultCfg())
	require.NoError(t, c.PreCheck(extraction.ArchiveMetadata{}))
}

func TestEntryCheck_RejectsRule5DepthExactlyOverLimit(t *testing.T) {
	cfg := defaultCfg()
	cfg.MaxDirectoryDepth = 5
	c := bombdefence.New(cfg)
	err := c.EntryCheck(1, extraction.EntryInfo{Name: "a/b/c/d/e/f", DirectoryDepth: 6})
	require.Error(t, err)
	bde, _ := extraction.IsBombDefence(err)
	assert.Equal(t, 5, bde.Rule)
}

func TestEntryCheckRule9_SingleFileSize(t *testing.T) {
	c := bombdefence.New(defaultCfg())
	err := c.EntryCheck(1, extraction.EntryInfo{
		Name:             "big.bin",
		Mode:             0o644,
		UncompressedSize: 300 * 1024 * 1024,
	})
	require.Error(t, err)
	bde, _ := extraction.IsBombDefence(err)
	assert.Equal(t, 9, bde.Rule)
}

// BR-BOMB-009 — rule #11: overlapping compressed-data ranges fire.
// Fifield non-recursive bomb pattern: two central-directory records point to
// the same compressed bytes [100, 200), so one decoded stream is "extracted"
// twice and total extracted size doubles without per-entry ratio anomaly.
func TestOverlapCheckRule11_Rejects(t *testing.T) {
	c := bombdefence.New(defaultCfg())
	err := c.OverlapCheck(extraction.ArchiveMetadata{
		EntryCount: 2,
		EntryDataRanges: []extraction.EntryDataRange{
			{EntryIndex: 0, Start: 100, End: 200},
			{EntryIndex: 1, Start: 150, End: 250}, // overlaps entry 0
		},
	})
	require.Error(t, err)
	bde, ok := extraction.IsBombDefence(err)
	require.True(t, ok)
	assert.Equal(t, 11, bde.Rule)
	assert.Contains(t, bde.Reason, "overlapping")
}

// BR-BOMB-009 — non-overlapping ranges (even when adjacent at the boundary) pass.
func TestOverlapCheckRule11_AdjacentOK(t *testing.T) {
	c := bombdefence.New(defaultCfg())
	err := c.OverlapCheck(extraction.ArchiveMetadata{
		EntryCount: 3,
		EntryDataRanges: []extraction.EntryDataRange{
			{EntryIndex: 0, Start: 100, End: 200},
			{EntryIndex: 1, Start: 200, End: 300}, // touches but does not overlap
			{EntryIndex: 2, Start: 350, End: 450},
		},
	})
	assert.NoError(t, err)
}

// BR-BOMB-009 — checker is order-invariant: it sorts the input copy.
func TestOverlapCheckRule11_SortsBeforeWalking(t *testing.T) {
	c := bombdefence.New(defaultCfg())
	// Same overlapping pair, presented out of order.
	err := c.OverlapCheck(extraction.ArchiveMetadata{
		EntryCount: 2,
		EntryDataRanges: []extraction.EntryDataRange{
			{EntryIndex: 1, Start: 150, End: 250},
			{EntryIndex: 0, Start: 100, End: 200},
		},
	})
	require.Error(t, err)
	bde, _ := extraction.IsBombDefence(err)
	assert.Equal(t, 11, bde.Rule)
}

// Single entry trivially passes; empty too. Avoids n-1 underflow.
func TestOverlapCheckRule11_FewerThanTwoEntries(t *testing.T) {
	c := bombdefence.New(defaultCfg())
	assert.NoError(t, c.OverlapCheck(extraction.ArchiveMetadata{EntryCount: 0}))
	assert.NoError(t, c.OverlapCheck(extraction.ArchiveMetadata{
		EntryCount:      1,
		EntryDataRanges: []extraction.EntryDataRange{{EntryIndex: 0, Start: 30, End: 130}},
	}))
}
