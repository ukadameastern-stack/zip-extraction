// Package generators centralises pgregory.net/rapid generators for the
// zip-extraction service domain types per PBT-07 (generator quality).
//
// Generators in this package are exported as functions taking *rapid.T so each
// test creates fresh generators within its own t.Repeat / t.Sequence scope.
package generators

import (
	"fmt"
	"strings"

	"pgregory.net/rapid"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
)

// ClaimCheck returns a generator producing valid extraction.ClaimCheck values.
func ClaimCheck() *rapid.Generator[extraction.ClaimCheck] {
	return rapid.Custom(func(t *rapid.T) extraction.ClaimCheck {
		return extraction.ClaimCheck{
			PipelineExecutionID: rapid.StringMatching(`exec-[A-Za-z0-9_-]{1,32}`).Draw(t, "execId"),
			TenantID:            rapid.StringMatching(`tenant-[A-Za-z0-9_-]{1,16}`).Draw(t, "tenantId"),
			DocumentID:          rapid.StringMatching(`doc-[A-Za-z0-9_-]{1,32}`).Draw(t, "documentId"),
			SourceBucket:        rapid.StringMatching(`[a-z0-9][a-z0-9-]{1,40}`).Draw(t, "sourceBucket"),
			SourceKey:           "uploads/" + rapid.StringMatching(`[A-Za-z0-9_-]{1,32}\.zip`).Draw(t, "sourceKey"),
			CorrelationID:       rapid.StringMatching(`corr-[A-Za-z0-9_-]{1,32}`).Draw(t, "correlationId"),
		}
	})
}

// ArchiveMetadata returns a generator producing valid ArchiveMetadata.
// All numeric fields are bounded so the generator does not produce values that
// trivially blow up bomb-defence thresholds (use ArchiveMetadataBomb for that).
func ArchiveMetadata() *rapid.Generator[extraction.ArchiveMetadata] {
	return rapid.Custom(func(t *rapid.T) extraction.ArchiveMetadata {
		entryCount := rapid.IntRange(0, 5000).Draw(t, "entryCount")
		totalComp := rapid.Int64Range(0, 400*1024*1024).Draw(t, "totalCompressed")
		return extraction.ArchiveMetadata{
			EntryCount:                     entryCount,
			TotalCompressedBytes:           totalComp,
			TotalDeclaredUncompressedBytes: totalComp * 5,
			ZIP64:                          rapid.Bool().Draw(t, "zip64"),
		}
	})
}

// ArchiveMetadataBomb returns metadata that violates one of the pre-check rules
// (#1 compressed-size, #4 entry-count). Used by negative bomb-defence tests.
func ArchiveMetadataBomb(rule int) *rapid.Generator[extraction.ArchiveMetadata] {
	return rapid.Custom(func(t *rapid.T) extraction.ArchiveMetadata {
		switch rule {
		case 1:
			return extraction.ArchiveMetadata{
				EntryCount:           1,
				TotalCompressedBytes: rapid.Int64Range(600*1024*1024, 1<<40).Draw(t, "oversizedCompressed"),
			}
		case 4:
			return extraction.ArchiveMetadata{
				EntryCount:           rapid.IntRange(10001, 100000).Draw(t, "oversizedEntryCount"),
				TotalCompressedBytes: 100,
			}
		default:
			t.Fatalf("ArchiveMetadataBomb: unsupported rule %d (want 1 or 4)", rule)
			return extraction.ArchiveMetadata{}
		}
	})
}

// EntryInfo returns a valid entry generator.
func EntryInfo() *rapid.Generator[extraction.EntryInfo] {
	return rapid.Custom(func(t *rapid.T) extraction.EntryInfo {
		depth := rapid.IntRange(0, 5).Draw(t, "depth")
		name := buildEntryName(t, depth)
		comp := rapid.Int64Range(0, 50*1024*1024).Draw(t, "compressed")
		un := comp * rapid.Int64Range(1, 20).Draw(t, "ratio")
		return extraction.EntryInfo{
			Name:             name,
			Mode:             0o644,
			CompressedSize:   comp,
			UncompressedSize: un,
			Method:           8, // Deflate
			DirectoryDepth:   depth,
		}
	})
}

// EntryInfoBomb returns an entry violating one of the per-entry rules (#5, #6, #9).
func EntryInfoBomb(rule int) *rapid.Generator[extraction.EntryInfo] {
	return rapid.Custom(func(t *rapid.T) extraction.EntryInfo {
		switch rule {
		case 5:
			depth := rapid.IntRange(11, 50).Draw(t, "depthOver")
			return extraction.EntryInfo{
				Name:           strings.Repeat("a/", depth) + "file.txt",
				Mode:           0o644,
				DirectoryDepth: depth,
			}
		case 6:
			return extraction.EntryInfo{
				Name: "link",
				// os.ModeSymlink = 1 << 27 — see os.FileMode constants.
				Mode: 1 << 27,
			}
		case 9:
			return extraction.EntryInfo{
				Name:             "huge.bin",
				Mode:             0o644,
				CompressedSize:   1024,
				UncompressedSize: 300 * 1024 * 1024, // > 250 MB default
			}
		default:
			t.Fatalf("EntryInfoBomb: unsupported rule %d (want 5, 6, or 9)", rule)
			return extraction.EntryInfo{}
		}
	})
}

func buildEntryName(t *rapid.T, depth int) string {
	parts := make([]string, depth+1)
	for i := range parts {
		parts[i] = rapid.StringMatching(`[a-z0-9_]{1,12}`).Draw(t, fmt.Sprintf("segment-%d", i))
	}
	return strings.Join(parts, "/")
}

// RawPath returns paths that should sanitize successfully.
func RawPath() *rapid.Generator[string] {
	return rapid.StringMatching(`[a-zA-Z0-9_]{1,32}(\.[a-zA-Z]{1,5})?`)
}

// RawPathTraversal returns paths containing `..` segments.
func RawPathTraversal() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		variant := rapid.IntRange(0, 4).Draw(t, "variant")
		base := rapid.StringMatching(`[a-z]{1,10}`).Draw(t, "base")
		switch variant {
		case 0:
			return "../" + base
		case 1:
			return "a/../" + base
		case 2:
			return "..\\" + base
		case 3:
			return "%2e%2e/" + base
		default:
			return "../../" + base
		}
	})
}

// RawPathAbsolute returns absolute / drive-letter paths.
func RawPathAbsolute() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		variant := rapid.IntRange(0, 2).Draw(t, "variant")
		base := rapid.StringMatching(`[a-z]{1,10}`).Draw(t, "base")
		switch variant {
		case 0:
			return "/" + base
		case 1:
			return "C:\\" + base
		default:
			return "/etc/" + base
		}
	})
}

// EntryOutcome returns a generator producing valid EntryOutcome values.
// The bias toward UPLOADED is configurable via the failureProb parameter (0.0–1.0).
func EntryOutcome(failureProb float64) *rapid.Generator[extraction.EntryOutcome] {
	return rapid.Custom(func(t *rapid.T) extraction.EntryOutcome {
		idx := rapid.IntRange(1, 9999).Draw(t, "idx")
		safe := rapid.StringMatching(`[a-z0-9_-]{1,32}\.[a-z]{1,4}`).Draw(t, "safe")
		isFailed := rapid.Float64Range(0, 1).Draw(t, "rand") < failureProb
		out := extraction.EntryOutcome{
			Index:    idx,
			SafeName: safe,
			Status:   extraction.EntryStatusUploaded,
		}
		if isFailed {
			out.Status = extraction.EntryStatusFailed
			out.FailureReason = rapid.SampledFrom([]string{
				"retries exhausted: throttling",
				"retries exhausted: 5xx",
				"retries exhausted: timeout",
				"permanent: AccessDenied",
			}).Draw(t, "failureReason")
		} else {
			out.ChildKey = fmt.Sprintf("input/exec-x/%04d-%s", idx, safe)
			out.MimeType = "application/octet-stream"
			out.SizeBytes = rapid.Int64Range(1, 1024*1024).Draw(t, "sizeBytes")
		}
		return out
	})
}

// PipelineFile returns a generator producing valid PipelineFile DDB rows.
func PipelineFile() *rapid.Generator[extraction.PipelineFile] {
	return rapid.Custom(func(t *rapid.T) extraction.PipelineFile {
		execID := rapid.StringMatching(`exec-[A-Za-z0-9_-]{1,16}`).Draw(t, "execId")
		idx := rapid.IntRange(1, 9999).Draw(t, "idx")
		status := rapid.SampledFrom([]string{extraction.EntryStatusUploaded, extraction.EntryStatusFailed}).Draw(t, "status")
		pf := extraction.PipelineFile{
			PK:            "PIPELINE#" + execID,
			SK:            fmt.Sprintf("FILE#%04d", idx),
			DocumentID:    rapid.StringMatching(`doc-[A-Za-z0-9_-]{1,12}`).Draw(t, "doc"),
			SourceArchive: "uploads/x.zip",
			Status:        status,
			SizeBytes:     rapid.Int64Range(0, 1024*1024).Draw(t, "size"),
		}
		if status == extraction.EntryStatusUploaded {
			pf.ChildKey = fmt.Sprintf("input/%s/%04d-x.bin", execID, idx)
			pf.MimeType = "application/octet-stream"
		} else {
			pf.FailureReason = "retries exhausted: throttling"
		}
		return pf
	})
}
