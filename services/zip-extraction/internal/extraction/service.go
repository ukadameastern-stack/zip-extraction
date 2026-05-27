package extraction

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Service is the per-message extraction orchestrator implementing the state
// machine documented in
// aidlc-docs/construction/zip-extraction/functional-design/business-logic-model.md §1.
type Service struct {
	deps Dependencies
}

// New constructs a Service.
func New(deps Dependencies) *Service { return &Service{deps: deps} }

// Process runs the full per-message lifecycle and returns the terminal Outcome.
// The accompanying error is non-nil only when the caller should NOT delete the
// SQS message (i.e., panic-equivalent failures). Per BR-DLQ-001 the common case
// is a non-nil Outcome + nil err = DeleteMessage.
//
//nolint:gocyclo // the state machine is intentionally explicit
func (s *Service) Process(ctx context.Context, msg ClaimCheck) (Outcome, error) {
	start := s.deps.Clock.Now()
	logger := s.deps.Logger.With(
		zap.String("pipelineExecutionId", msg.PipelineExecutionID),
		zap.String("correlationId", msg.CorrelationID),
		zap.String("documentId", msg.DocumentID),
	)

	// Per BR-BOMB-005 rule #10: extraction-context timeout.
	extractCtx, extractCancel := context.WithTimeout(
		ctx, time.Duration(s.deps.Config.MaxExtractionDurationSec)*time.Second,
	)
	defer extractCancel()

	// State held for the deferred slipsheet write (BR-SLIP-002).
	var (
		archiveStatus = StatusFailed
		archiveReason string
		archiveDetail string
		entries       []EntryOutcome
		successCount  int
	)

	// Deferred slipsheet write — runs on every exit path (success, error, panic).
	defer func() {
		// Use rootCtx (not extractCtx) so slipsheet write isn't cancelled by
		// rule-10 timeout. A 5s budget for slipsheet upload.
		sCtx, sCancel := context.WithTimeout(ctx, 5*time.Second)
		defer sCancel()
		// If only the reason is set, derive the human description from the controlled vocabulary.
		detail := archiveDetail
		if detail == "" && archiveReason != "" {
			detail = DescribeFailure(archiveReason, "")
		}
		if err := s.deps.SlipsheetWriter.Write(sCtx, msg.PipelineExecutionID, msg.SourceKey, archiveStatus, entries, archiveReason, detail); err != nil {
			logger.Error("slipsheet write failed", zap.Error(err))
			s.deps.Metrics.SlipsheetWriteFailure()
		}
		dur := s.deps.Clock.Now().Sub(start)
		s.deps.Metrics.ExtractionDuration(dur, archiveStatus.String())
	}()

	// 1. Download the source archive.
	body, size, err := s.deps.Downloader.Download(extractCtx, msg.SourceBucket, msg.SourceKey)
	if err != nil {
		archiveStatus, archiveReason = s.classifyArchiveErr(err)
		s.deps.Metrics.ExtractionFailure(failureReason(archiveStatus, archiveReason))
		return Outcome{Status: archiveStatus, Reason: archiveReason, DurationMs: ms(start, s.deps.Clock)}, nil
	}
	defer body.Close()

	// 2. Materialise to a temp file with size guard (rule #1) so archive/zip can
	// random-access. This is the only place a temp file lives; cleaned up via defer.
	tmpPath, err := spillToTemp(body, msg.PipelineExecutionID)
	if err != nil {
		archiveStatus = StatusFailed
		archiveReason = "spill: " + err.Error()
		s.deps.Metrics.ExtractionFailure("spill-failed")
		return Outcome{Status: archiveStatus, Reason: archiveReason, DurationMs: ms(start, s.deps.Clock)}, nil
	}
	defer os.Remove(tmpPath)

	// 3. Open ZIP and produce ArchiveMetadata.
	zr, zMeta, err := openZip(tmpPath, size)
	if err != nil {
		var ufe *UnsupportedFeatureError
		if errors.As(err, &ufe) {
			archiveStatus, archiveReason = StatusFailed, "unsupported: "+ufe.Feature
			archiveDetail = DescribeFailure(archiveReason, "")
			s.deps.Metrics.ExtractionFailure("unsupported")
		} else {
			archiveStatus, archiveReason = StatusFailed, "corrupt-zip: "+err.Error()
			archiveDetail = DescribeFailure(archiveReason, err.Error())
			s.deps.Metrics.ExtractionFailure("corrupt-zip")
		}
		return Outcome{Status: archiveStatus, Reason: archiveReason, DurationMs: ms(start, s.deps.Clock)}, nil
	}

	// 4a. Pre-check (BR-BOMB-001: rules #1, #4).
	// 4b. Overlap-check (BR-BOMB-009: rule #11 — Fifield non-recursive bombs).
	//     Both produce *BombDefenceError; we handle them with the same branch.
	preErr := s.deps.BombChecker.PreCheck(zMeta)
	if preErr == nil {
		preErr = s.deps.BombChecker.OverlapCheck(zMeta)
	}
	if err := preErr; err != nil {
		archiveStatus = StatusFailed
		if bde, ok := IsBombDefence(err); ok {
			archiveReason = fmt.Sprintf("bomb-defence rule %d", bde.Rule)
			archiveDetail = DescribeFailure(archiveReason, bde.Reason)
			s.deps.Metrics.BombRejection(bde.Rule)
			logger.Warn("bomb-rejection",
				zap.Int("rule", bde.Rule),
				zap.String("reason", bde.Reason),
				zap.String("sourceArchive", msg.SourceKey),
			)
		} else {
			archiveReason = err.Error()
			archiveDetail = err.Error()
		}
		s.deps.Metrics.ExtractionFailure(archiveReason)
		return Outcome{Status: archiveStatus, Reason: archiveReason, DurationMs: ms(start, s.deps.Clock)}, nil
	}

	// 5. Per-entry loop.
	for idx, file := range zr.File {
		if err := extractCtx.Err(); err != nil {
			// Rule #10 timeout OR drain canceled.
			if errors.Is(err, context.DeadlineExceeded) {
				archiveStatus = StatusFailed
				archiveReason = "bomb-defence rule 10"
				archiveDetail = DescribeFailure(archiveReason, fmt.Sprintf("aborted after %d seconds", s.deps.Config.MaxExtractionDurationSec))
				s.deps.Metrics.BombRejection(10)
			} else {
				if successCount > 0 {
					archiveStatus = StatusPartialFailed
					s.deps.Metrics.PartialFailure()
				} else {
					archiveStatus = StatusFailed
				}
				archiveReason = "drain canceled"
				archiveDetail = DescribeFailure(archiveReason, "")
			}
			s.deps.Metrics.ExtractionFailure(archiveReason)
			return Outcome{Status: archiveStatus, Reason: archiveReason, EntryCount: len(entries), DurationMs: ms(start, s.deps.Clock)}, nil
		}

		entryOutcome, archiveAbort, aReason, aDetail := s.processEntry(extractCtx, idx+1, file, msg, logger)
		entries = append(entries, entryOutcome)
		if entryOutcome.Status == EntryStatusUploaded {
			successCount++
		}
		s.deps.Metrics.EntryProcessed(entryOutcome.Status)
		if archiveAbort {
			archiveStatus = StatusFailed
			archiveReason = aReason
			archiveDetail = aDetail
			s.deps.Metrics.ExtractionFailure(aReason)
			return Outcome{Status: archiveStatus, Reason: archiveReason, EntryCount: len(entries), FailureCount: len(entries) - successCount, DurationMs: ms(start, s.deps.Clock)}, nil
		}
	}

	// 6. Compute terminal status (BR-STATUS-001).
	archiveStatus = computeStatus(entries)
	if archiveStatus == StatusPartialFailed {
		s.deps.Metrics.PartialFailure()
		archiveReason = ""
	}
	return Outcome{
		Status:       archiveStatus,
		EntryCount:   len(entries),
		FailureCount: len(entries) - successCount,
		DurationMs:   ms(start, s.deps.Clock),
	}, nil
}

// processEntry handles one ZIP entry per the pipeline in business-logic-model.md §3.
// Returns (entry outcome, archive-level abort flag, archive-level reason, archive-level detail).
func (s *Service) processEntry(
	ctx context.Context,
	idx int,
	file *zip.File,
	msg ClaimCheck,
	logger Logger,
) (EntryOutcome, bool, string, string) {
	now := s.deps.Clock.Now()
	out := EntryOutcome{Index: idx, RecordedAt: now, Status: EntryStatusFailed}

	// Path validation (BR-PATH-* + FR-6 rules #7/#8 — archive-level abort on violation per BR-PATH-006).
	safe, err := s.deps.PathValidator.Sanitize(file.Name)
	if err != nil {
		var pve *PathValidationError
		if errors.As(err, &pve) {
			logger.Warn("entry path rejected",
				zap.String("rawPath", truncate(file.Name, 128)),
				zap.String("reason", pve.Reason),
			)
			out.FailureReason = pve.Reason
			out.FailureDetail = DescribeFailure(pve.Reason, fmt.Sprintf("rejected path: %q", truncate(file.Name, 128)))
			s.deps.Metrics.ExtractionFailure("path-validation")
			s.recordFailedEntry(ctx, msg, out, idx) // BR-DDB-002
			return out, true, pve.Reason, out.FailureDetail
		}
		out.FailureReason = err.Error()
		out.FailureDetail = err.Error()
		s.recordFailedEntry(ctx, msg, out, idx)
		return out, true, err.Error(), err.Error()
	}
	out.SafeName = safe

	entry := EntryInfo{
		Name:             file.Name,
		Mode:             uint32(file.Mode()),
		CompressedSize:   int64(file.CompressedSize64),
		UncompressedSize: int64(file.UncompressedSize64),
		Method:           file.Method,
		DirectoryDepth:   strings.Count(filepath.ToSlash(filepath.Clean(file.Name)), "/"),
	}

	// Per-entry bomb check (rules #5, #6, #9).
	if err := s.deps.BombChecker.EntryCheck(idx, entry); err != nil {
		if bde, ok := IsBombDefence(err); ok {
			logger.Warn("entry bomb-rejection",
				zap.Int("rule", bde.Rule),
				zap.String("reason", bde.Reason),
			)
			out.FailureReason = fmt.Sprintf("bomb-defence rule %d", bde.Rule)
			out.FailureDetail = DescribeFailure(out.FailureReason, bde.Reason)
			s.deps.Metrics.BombRejection(bde.Rule)
			s.recordFailedEntry(ctx, msg, out, idx) // BR-DDB-002: every entry produces exactly one row
			return out, true, out.FailureReason, out.FailureDetail
		}
		out.FailureReason = err.Error()
		out.FailureDetail = err.Error()
		s.recordFailedEntry(ctx, msg, out, idx)
		return out, true, err.Error(), err.Error()
	}

	// Open entry reader.
	rc, err := file.Open()
	if err != nil {
		out.FailureReason = "open-entry: " + err.Error()
		out.FailureDetail = "entry could not be opened by archive/zip: " + err.Error()
		return out, false, "", "" // single-entry failure; archive continues
	}
	defer rc.Close()

	// Wrap in bomb LimitedReader (rules #2, #3).
	limited := s.deps.BombChecker.NewLimitedReader(rc, entry.CompressedSize)

	// Peek for MIME sniff.
	const peekBytes = 512
	peeked, err := peekReader(limited, peekBytes)
	if err != nil {
		// Peek itself may surface the bomb-defence error if the LimitedReader
		// short-circuits on the very first read (rare but possible for tiny entries
		// well above ratio cap). Treat as archive-level abort.
		if bde, ok := IsBombDefence(err); ok {
			logger.Warn("peek bomb-rejection",
				zap.Int("rule", bde.Rule),
				zap.String("reason", bde.Reason),
			)
			out.FailureReason = fmt.Sprintf("bomb-defence rule %d", bde.Rule)
			out.FailureDetail = DescribeFailure(out.FailureReason, bde.Reason)
			s.deps.Metrics.BombRejection(bde.Rule)
			s.recordFailedEntry(ctx, msg, out, idx)
			return out, true, out.FailureReason, out.FailureDetail
		}
		out.FailureReason = "peek: " + err.Error()
		out.FailureDetail = "could not buffer first 512 bytes for MIME sniff: " + err.Error()
		return out, false, "", ""
	}

	mimeType := detectMimeShim(peeked.Peek, safe)
	out.MimeType = mimeType

	childKey := buildChildKey(msg.PipelineExecutionID, idx, safe)
	out.ChildKey = childKey

	// Upload via retrier (BR-RETRY-002).
	uploadErr := s.deps.Retrier.Do(ctx, func(c context.Context) error {
		// Note: the stream is consumed; for retries on the SAME entry the retrier
		// would need a fresh reader. Per BR-RETRY-004..010, throttling/5xx errors
		// from S3 are typically returned BEFORE bytes are consumed, so the SDK
		// internal retry handles single-call retries. The application-level
		// retrier here protects against repeated transient failures that escape
		// the SDK. For deeply consumed streams the retry will receive a fresh
		// error wrapped as *PermanentError after exhaustion.
		return s.deps.Uploader.Upload(c, s.deps.Config.StagingBucket, childKey, peeked.Rebuilt, entry.UncompressedSize, mimeType)
	})
	if uploadErr != nil {
		// Bomb-defence violation can fire mid-stream — archive-level abort.
		if bde, ok := IsBombDefence(uploadErr); ok {
			logger.Warn("mid-stream bomb-rejection",
				zap.Int("rule", bde.Rule),
				zap.String("reason", bde.Reason),
			)
			out.FailureReason = fmt.Sprintf("bomb-defence rule %d", bde.Rule)
			out.FailureDetail = DescribeFailure(out.FailureReason, bde.Reason)
			s.deps.Metrics.BombRejection(bde.Rule)
			s.recordFailedEntry(ctx, msg, out, idx) // BR-DDB-002: every entry produces exactly one row
			return out, true, out.FailureReason, out.FailureDetail
		}
		out.FailureReason = classifyEntryFailure(uploadErr)
		out.FailureDetail = DescribeFailure(out.FailureReason, uploadErr.Error())
		s.recordFailedEntry(ctx, msg, out, idx)
		return out, false, "", ""
	}

	// Record DDB row (BR-DDB-001 + BR-IDEMPOTENCY-003: after S3 upload).
	out.Status = EntryStatusUploaded
	out.SizeBytes = entry.UncompressedSize
	recordErr := s.deps.Retrier.Do(ctx, func(c context.Context) error {
		return s.deps.Recorder.RecordEntry(c, PipelineFile{
			PK:            "PIPELINE#" + msg.PipelineExecutionID,
			SK:            fmt.Sprintf("FILE#%04d", idx),
			DocumentID:    msg.DocumentID,
			SourceArchive: msg.SourceKey,
			ChildKey:      childKey,
			MimeType:      mimeType,
			Status:        EntryStatusUploaded,
			SizeBytes:     entry.UncompressedSize,
			RecordedAt:    now,
		})
	})
	if recordErr != nil {
		// Record failed but S3 succeeded — mark FAILED but with the upload reason.
		out.Status = EntryStatusFailed
		out.FailureReason = "record: " + classifyEntryFailure(recordErr)
		out.FailureDetail = "S3 upload succeeded but DynamoDB write failed: " + recordErr.Error()
		s.recordFailedEntry(ctx, msg, out, idx)
		return out, false, "", ""
	}

	s.deps.Metrics.BytesExtracted(entry.UncompressedSize)

	// Optional classification hop. Best-effort: failure is logged + metric but
	// does not affect the entry's UPLOADED status or the parent archive outcome.
	if s.deps.Classifier != nil {
		s.classifyChild(ctx, msg, &out, logger)
	}

	return out, false, "", ""
}

// classifyChild streams the child back from staging and POSTs it to the
// classifier. The result is stamped onto out.Classification on success;
// errors are logged and counted but never propagated.
func (s *Service) classifyChild(ctx context.Context, msg ClaimCheck, out *EntryOutcome, logger Logger) {
	workspaceID := msg.TenantID
	if workspaceID == "" {
		workspaceID = s.deps.Config.ClassificationFallbackWorkspace
	}
	if workspaceID == "" {
		logger.Warn("classify: skipped — no workspaceId on message and no fallback configured",
			zap.Int("entryIndex", out.Index),
		)
		s.deps.Metrics.ClassificationFailure("no-workspace")
		return
	}

	body, _, err := s.deps.Downloader.Download(ctx, s.deps.Config.StagingBucket, out.ChildKey)
	if err != nil {
		logger.Warn("classify: download child failed",
			zap.Int("entryIndex", out.Index),
			zap.String("childKey", out.ChildKey),
			zap.Error(err),
		)
		s.deps.Metrics.ClassificationFailure("download")
		return
	}
	defer body.Close()

	result, err := s.deps.Classifier.Classify(ctx, ClassifyRequest{
		WorkspaceID:        workspaceID,
		Filename:           out.SafeName,
		ContentType:        out.MimeType,
		ParentArchiveDepth: 1,
		Body:               body,
	})
	if err != nil {
		logger.Warn("classify: call failed",
			zap.Int("entryIndex", out.Index),
			zap.String("childKey", out.ChildKey),
			zap.Error(err),
		)
		s.deps.Metrics.ClassificationFailure("http")
		return
	}
	out.Classification = result
	if result != nil {
		s.deps.Metrics.ClassificationSuccess(result.Category)
	}
}

func (s *Service) recordFailedEntry(ctx context.Context, msg ClaimCheck, out EntryOutcome, idx int) {
	rec := PipelineFile{
		PK:            "PIPELINE#" + msg.PipelineExecutionID,
		SK:            fmt.Sprintf("FILE#%04d", idx),
		DocumentID:    msg.DocumentID,
		SourceArchive: msg.SourceKey,
		Status:        EntryStatusFailed,
		FailureReason: out.FailureReason,
		FailureDetail: out.FailureDetail,
		RecordedAt:    out.RecordedAt,
	}
	// Best-effort failure record — don't propagate.
	if err := s.deps.Recorder.RecordEntry(ctx, rec); err != nil {
		s.deps.Logger.Warn("recording FAILED entry failed",
			zap.Int("index", idx),
			zap.Error(err),
		)
	}
}

// classifyArchiveErr categorises top-level download failures.
func (s *Service) classifyArchiveErr(err error) (Status, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusFailed, "bomb-defence rule 10"
	}
	if errors.Is(err, context.Canceled) {
		return StatusFailed, "drain canceled"
	}
	if _, ok := IsPermanent(err); ok {
		return StatusFailed, "permanent: source-download-failed"
	}
	if _, ok := IsTransient(err); ok {
		return StatusFailed, "transient: source-download-failed"
	}
	return StatusFailed, "source-download-failed: " + err.Error()
}

// classifyEntryFailure converts an entry-processing error into a controlled-vocabulary reason.
func classifyEntryFailure(err error) string {
	if pe, ok := IsPermanent(err); ok {
		_ = pe
		return "retries exhausted: permanent"
	}
	if te, ok := IsTransient(err); ok {
		return "retries exhausted: " + te.Class
	}
	return err.Error()
}

// computeStatus returns the terminal Status per BR-STATUS-001.
func computeStatus(entries []EntryOutcome) Status {
	if len(entries) == 0 {
		return StatusSuccess // empty archive passing pre-check is a valid SUCCESS
	}
	var ok, fail int
	for _, e := range entries {
		if e.Status == EntryStatusUploaded {
			ok++
		} else {
			fail++
		}
	}
	switch {
	case ok == len(entries):
		return StatusSuccess
	case ok > 0 && fail > 0:
		return StatusPartialFailed
	default:
		return StatusFailed
	}
}

// buildChildKey deterministically constructs the S3 key (BR-IDEMPOTENCY-001).
func buildChildKey(execID string, idx int, safeName string) string {
	return fmt.Sprintf("input/%s/%04d-%s", execID, idx, safeName)
}

func ms(start time.Time, clock Clock) int64 {
	return clock.Now().Sub(start).Milliseconds()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func failureReason(status Status, reason string) string {
	if reason != "" {
		return reason
	}
	return status.String()
}
