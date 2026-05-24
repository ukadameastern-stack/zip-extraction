// Package slipsheet implements the parent archive slipsheet per FR-8 +
// BR-SLIP-001..006. The slipsheet is written exactly once per message via a
// `defer` block in extraction.Service.Process (BR-SLIP-002) to
// s3://<staging-bucket>/slipsheets/<execId>.json — a separate prefix from
// input/ to prevent S3 PutObject events from re-triggering the downstream
// pipeline (BR-SLIP-001).
package slipsheet

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
)

// Slipsheet is the JSON document written to S3.
//
// FailureReason is machine-readable (used for metric labels);
// FailureDetail is the human-readable explanation with dynamic context
// (the actual measured ratio, retry class, etc.). Both are empty for SUCCESS.
type Slipsheet struct {
	Type                string       `json:"type"` // constant "archive-container"
	PipelineExecutionID string       `json:"pipelineExecutionId"`
	SourceArchive       string       `json:"sourceArchive"`
	ChildCount          int          `json:"childCount"`
	Status              string       `json:"status"` // "SUCCESS"|"PARTIAL_FAILED"|"FAILED"
	FailureReason       string       `json:"failureReason,omitempty"`
	FailureDetail       string       `json:"failureDetail,omitempty"`
	WrittenAt           time.Time    `json:"writtenAt"`
	Children            []ChildEntry `json:"children"`
}

// ChildEntry is one row in Slipsheet.Children.
type ChildEntry struct {
	EntryIndex    int    `json:"entryIndex"`
	ChildKey      string `json:"childKey"`
	Status        string `json:"status"` // "UPLOADED" | "FAILED"
	FailureReason string `json:"failureReason,omitempty"`
	FailureDetail string `json:"failureDetail,omitempty"`
	SizeBytes     int64  `json:"sizeBytes"`
}

// SlipsheetType is the constant value for Slipsheet.Type.
const SlipsheetType = "archive-container"

// Build assembles a Slipsheet from per-entry outcomes. The Children slice is
// returned sorted by EntryIndex ascending so consumers see a deterministic order.
// archiveReason populates FailureReason when the archive failed for an
// archive-level reason (bomb defence, path traversal, rule #10, etc.);
// archiveDetail populates FailureDetail with the human-readable elaboration.
func Build(
	execID, sourceArchive string,
	status extraction.Status,
	entries []extraction.EntryOutcome,
	archiveReason, archiveDetail string,
	now time.Time,
) Slipsheet {
	ss := Slipsheet{
		Type:                SlipsheetType,
		PipelineExecutionID: execID,
		SourceArchive:       sourceArchive,
		Status:              status.String(),
		FailureReason:       archiveReason,
		FailureDetail:       archiveDetail,
		WrittenAt:           now,
		Children:            make([]ChildEntry, 0, len(entries)),
	}
	for _, e := range entries {
		ss.Children = append(ss.Children, ChildEntry{
			EntryIndex:    e.Index,
			ChildKey:      e.ChildKey,
			Status:        e.Status,
			FailureReason: e.FailureReason,
			FailureDetail: e.FailureDetail,
			SizeBytes:     e.SizeBytes,
		})
	}
	sort.Slice(ss.Children, func(i, j int) bool {
		return ss.Children[i].EntryIndex < ss.Children[j].EntryIndex
	})
	ss.ChildCount = len(ss.Children)
	return ss
}

// Marshal is the round-trip helper exported for PBT-02 round-trip property.
func Marshal(ss Slipsheet) ([]byte, error) {
	b, err := json.Marshal(ss)
	if err != nil {
		return nil, fmt.Errorf("slipsheet: marshal: %w", err)
	}
	return b, nil
}

// Unmarshal is the inverse of Marshal.
func Unmarshal(b []byte) (Slipsheet, error) {
	var ss Slipsheet
	if err := json.Unmarshal(b, &ss); err != nil {
		return Slipsheet{}, fmt.Errorf("slipsheet: unmarshal: %w", err)
	}
	return ss, nil
}

// Writer persists slipsheets to S3 via the extraction.S3Uploader port.
type Writer struct {
	uploader extraction.S3Uploader
	bucket   string
	prefix   string
}

// NewWriter constructs a Writer. prefix MUST end with "/" — defaults to
// "slipsheets/" when empty.
func NewWriter(uploader extraction.S3Uploader, bucket, prefix string) *Writer {
	if prefix == "" {
		prefix = "slipsheets/"
	}
	return &Writer{uploader: uploader, bucket: bucket, prefix: prefix}
}

// Write marshals ss and uploads it to s3://<bucket>/<prefix><execId>.json.
// Implements extraction.SlipsheetWriter.
func (w *Writer) Write(
	ctx context.Context,
	execID, sourceArchive string,
	status extraction.Status,
	entries []extraction.EntryOutcome,
	reason, detail string,
) error {
	ss := Build(execID, sourceArchive, status, entries, reason, detail, time.Now().UTC())
	b, err := Marshal(ss)
	if err != nil {
		return err
	}
	key := w.prefix + execID + ".json"
	return w.uploader.Upload(ctx, w.bucket, key, bytesReader(b), int64(len(b)), "application/json")
}
