package extraction

import (
	"context"
	"io"
	"time"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/log"
)

// S3Downloader is the port consumed for reading the source archive from S3.
// Implementations live in internal/storage.
type S3Downloader interface {
	Download(ctx context.Context, bucket, key string) (body io.ReadCloser, size int64, err error)
}

// S3Uploader is the port consumed for writing extracted children + slipsheets to S3.
type S3Uploader interface {
	Upload(ctx context.Context, bucket, key string, body io.Reader, sizeHint int64, contentType string) error
}

// Recorder is the port for persisting per-entry DynamoDB rows (BR-DDB-001).
type Recorder interface {
	RecordEntry(ctx context.Context, rec PipelineFile) error
}

// SlipsheetWriter is the port for persisting the parent slipsheet to S3.
//
// reason is the machine-readable controlled-vocabulary failure reason
// (empty for SUCCESS); detail is the human-readable elaboration (empty for
// SUCCESS or when there is no extra context to add).
type SlipsheetWriter interface {
	Write(ctx context.Context, execID, sourceArchive string, status Status, entries []EntryOutcome, reason, detail string) error
}

// BombChecker is the port for the 10-rule defence + LimitedReader factory.
type BombChecker interface {
	PreCheck(meta ArchiveMetadata) error
	EntryCheck(idx int, entry EntryInfo) error
	NewLimitedReader(r io.Reader, compressedSize int64) io.Reader
}

// PathValidator is the port for FR-6 entry-path sanitisation.
type PathValidator interface {
	Sanitize(rawPath string) (safeName string, err error)
}

// Retrier is the port for the classifier-driven retry helper (FR-12).
type Retrier interface {
	Do(ctx context.Context, op func(ctx context.Context) error) error
}

// Metrics is the port for emitting the FR-13.2 + operational metrics.
type Metrics interface {
	EntryProcessed(status string)
	ExtractionDuration(d time.Duration, outcome string)
	ExtractionFailure(reason string)
	BombRejection(rule int)
	BytesExtracted(n int64)
	PartialFailure()
	RedeliverySkip()
	SlipsheetWriteFailure()
}

// Logger is re-exported to keep extraction's consumer-defined-port surface
// self-contained.
type Logger = log.Logger

// Clock returns the current wall-clock time. Injected so tests can use a
// controlled clock — supports PBT-08 reproducibility.
type Clock interface {
	Now() time.Time
}

// Dependencies is the dependency-injection root for extraction.Service.
type Dependencies struct {
	Downloader      S3Downloader
	Uploader        S3Uploader
	Recorder        Recorder
	SlipsheetWriter SlipsheetWriter
	BombChecker     BombChecker
	PathValidator   PathValidator
	Retrier         Retrier
	Metrics         Metrics
	Logger          Logger
	Clock           Clock
	Config          ExtractionConfig
}

// ExtractionConfig holds tunables that the orchestrator consumes directly
// (not via ports).
type ExtractionConfig struct {
	MaxExtractionDurationSec int
	StagingBucket            string
	SSEMode                  string
	SSEKMSKeyID              string
}

// SystemClock is the production Clock implementation.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }
