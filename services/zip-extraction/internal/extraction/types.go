// Package extraction is the core domain orchestrator for the Zip Extraction
// Service. It defines the consumer-defined port interfaces (see ports.go),
// the typed-error hierarchy (see errors.go), and the per-message processing
// service (service.go).
//
// The package is the central authority for the FR-3 (streaming extraction)
// + FR-10 (status assignment) state machine described in
// aidlc-docs/construction/zip-extraction/functional-design/business-logic-model.md.
package extraction

import (
	"time"
)

// Status is the terminal status of a pipeline execution per FR-10.
type Status int

const (
	// StatusSuccess — every entry was uploaded successfully.
	StatusSuccess Status = iota
	// StatusPartialFailed — at least one entry failed after retries; at least one succeeded.
	StatusPartialFailed
	// StatusFailed — archive rejected or zero entries succeeded.
	StatusFailed
)

// String returns the wire-format string used in DynamoDB rows and slipsheets.
func (s Status) String() string {
	switch s {
	case StatusSuccess:
		return "SUCCESS"
	case StatusPartialFailed:
		return "PARTIAL_FAILED"
	case StatusFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// ClaimCheck is the SQS message body — a claim-check pointer to the source archive.
// All fields are required per FR-1.2.
type ClaimCheck struct {
	PipelineExecutionID string `json:"pipelineExecutionId"`
	TenantID            string `json:"tenantId"`
	DocumentID          string `json:"documentId"`
	SourceBucket        string `json:"sourceBucket"`
	SourceKey           string `json:"sourceKey"`
	CorrelationID       string `json:"correlationId"`
}

// ArchiveMetadata is the aggregate metadata produced after opening a ZIP and
// before iterating its entries. Used by bombdefence.Checker.PreCheck (BR-BOMB-001).
type ArchiveMetadata struct {
	EntryCount                     int
	TotalCompressedBytes           int64
	TotalDeclaredUncompressedBytes int64
	ZIP64                          bool
	Encrypted                      bool
	MultiDisk                      bool
	HasDeflate64Entries            bool
	// EntryDataRanges holds each entry's compressed-data byte interval
	// [Start, End) in the archive. Populated by openZip and consumed by
	// bombdefence.OverlapCheck (BR-BOMB-009, FR-7 rule #11) to detect Fifield
	// non-recursive bombs that reuse the same compressed bytes across multiple
	// central-directory records.
	EntryDataRanges []EntryDataRange
}

// EntryDataRange is one entry's compressed-data byte interval in the archive.
type EntryDataRange struct {
	EntryIndex int   // 0-based index into zip.Reader.File (used in error messages)
	Start      int64 // inclusive
	End        int64 // exclusive
}

// EntryInfo is the subset of *zip.File the bombdefence + validation packages
// need. Decoupling from the SDK type makes the checks PBT-testable without
// constructing real ZIP archives.
type EntryInfo struct {
	Name             string
	Mode             uint32 // os.FileMode bits; uint32 to avoid os import in domain layer
	CompressedSize   int64
	UncompressedSize int64
	Method           uint16
	DirectoryDepth   int
}

// EntryOutcome is the result of processing one ZIP entry.
//
// FailureReason is the machine-readable controlled-vocabulary string used as
// the Prometheus failure-reason label (e.g. "bomb-defence rule 3").
// FailureDetail is the human-readable explanation, often carrying dynamic
// context such as the actual measured ratio. Both are empty for UPLOADED entries.
//
// Classification is non-nil only when the optional classification step ran
// and succeeded for this entry. Stamped onto the slipsheet so the downstream
// pipeline can consume it without a second classifier hop.
type EntryOutcome struct {
	Index          int
	SafeName       string
	ChildKey       string
	MimeType       string
	SizeBytes      int64
	Status         string // "UPLOADED" | "FAILED"
	FailureReason  string
	FailureDetail  string
	RecordedAt     time.Time
	Classification *Classification
}

// Classification is the slipsheet-friendly subset of the classification
// service's /api/classify response. Mirrors classification.Result so the
// extraction layer doesn't depend on the classification package's HTTP types.
type Classification struct {
	Format            string  `json:"format"`
	Category          string  `json:"category"`
	SubCategory       string  `json:"subCategory,omitempty"`
	ConfidenceScore   float64 `json:"confidenceScore"`
	DetectionTier     string  `json:"detectionTier"`
	IsForcedSlipsheet bool    `json:"isForcedSlipsheet"`
	SlipsheetReason   string  `json:"slipsheetReason,omitempty"`
	ContentHash       string  `json:"contentHash,omitempty"`
	IsDuplicate       bool    `json:"isDuplicate,omitempty"`
	PolicyVersion     string  `json:"policyVersion,omitempty"`
	ElapsedMs         int     `json:"elapsedMs,omitempty"`
}

// PipelineFile is the DynamoDB row schema per BR-DDB-001 (FR-5.2).
type PipelineFile struct {
	PK            string    `dynamodbav:"pk"`
	SK            string    `dynamodbav:"sk"`
	DocumentID    string    `dynamodbav:"documentId"`
	SourceArchive string    `dynamodbav:"sourceArchive"`
	ChildKey      string    `dynamodbav:"childKey"`
	MimeType      string    `dynamodbav:"mimeType"`
	Status        string    `dynamodbav:"status"`
	SizeBytes     int64     `dynamodbav:"sizeBytes"`
	FailureReason string    `dynamodbav:"failureReason,omitempty"`
	FailureDetail string    `dynamodbav:"failureDetail,omitempty"`
	RecordedAt    time.Time `dynamodbav:"recordedAt"`
}

// Outcome is the return value of Service.Process. It maps directly to the
// SQS message disposition (BR-DLQ-001) and the slipsheet status.
type Outcome struct {
	Status       Status
	Reason       string
	EntryCount   int
	FailureCount int
	DurationMs   int64
}

// Entry-outcome status constants (BR-STATUS-004).
const (
	EntryStatusUploaded = "UPLOADED"
	EntryStatusFailed   = "FAILED"
)

// Unsupported-feature names (BR-DDB-005 controlled vocabulary).
const (
	FeatureEncryptedZIP = "encrypted-zip"
	FeatureMultiDisk    = "multi-disk"
	FeatureDeflate64    = "deflate64"
)
