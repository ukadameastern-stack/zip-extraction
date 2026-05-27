package extraction_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
	mylog "github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/log"
)

// makeZip returns the bytes of an in-memory ZIP archive with the given entries.
func makeZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range entries {
		f, err := w.Create(name)
		require.NoError(t, err)
		_, err = f.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// --- Port fakes ---

type fakeDownloader struct {
	body []byte
	err  error
}

func (f *fakeDownloader) Download(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	return io.NopCloser(bytes.NewReader(f.body)), int64(len(f.body)), nil
}

type uploadCall struct {
	bucket, key, contentType string
	size                     int64
	bodyLen                  int
}

type fakeUploader struct {
	mu    sync.Mutex
	calls []uploadCall
	err   error
	// Per-key error: if set for the call's key, return that error instead of f.err.
	keyErr map[string]error
}

func (f *fakeUploader) Upload(ctx context.Context, bucket, key string, body io.Reader, sizeHint int64, contentType string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(body)
	f.calls = append(f.calls, uploadCall{bucket: bucket, key: key, contentType: contentType, size: sizeHint, bodyLen: len(b)})
	if f.keyErr != nil {
		if e, ok := f.keyErr[key]; ok {
			return e
		}
	}
	return f.err
}

type fakeRecorder struct {
	mu      sync.Mutex
	records []extraction.PipelineFile
	err     error
}

func (f *fakeRecorder) RecordEntry(ctx context.Context, rec extraction.PipelineFile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.records = append(f.records, rec)
	return nil
}

type fakeSlipsheetWriter struct {
	mu          sync.Mutex
	called      bool
	lastExec    string
	lastReason  string
	lastDetail  string
	lastStatus  extraction.Status
	lastEntries []extraction.EntryOutcome
	err         error
}

func (f *fakeSlipsheetWriter) Write(ctx context.Context, execID, source string, status extraction.Status, entries []extraction.EntryOutcome, reason, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = true
	f.lastExec = execID
	f.lastReason = reason
	f.lastDetail = detail
	f.lastStatus = status
	f.lastEntries = entries
	return f.err
}

// fakeBomb passes all rules and produces a pass-through LimitedReader.
type fakeBomb struct {
	preErr     error
	overlapErr error
	entryErr   error
	limit      int64 // if > 0, LimitedReader returns rule-2 error after this many bytes
}

func (f *fakeBomb) PreCheck(meta extraction.ArchiveMetadata) error     { return f.preErr }
func (f *fakeBomb) OverlapCheck(meta extraction.ArchiveMetadata) error { return f.overlapErr }
func (f *fakeBomb) EntryCheck(idx int, e extraction.EntryInfo) error   { return f.entryErr }
func (f *fakeBomb) NewLimitedReader(r io.Reader, compressedSize int64) io.Reader {
	if f.limit <= 0 {
		return r
	}
	return &cappedReader{r: r, cap: f.limit}
}

type cappedReader struct {
	r        io.Reader
	cap, got int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.got >= c.cap {
		return 0, &extraction.BombDefenceError{Rule: 2, Reason: "test cap"}
	}
	n, err := c.r.Read(p)
	c.got += int64(n)
	if c.got > c.cap {
		return 0, &extraction.BombDefenceError{Rule: 2, Reason: "test cap"}
	}
	return n, err
}

type fakePathValidator struct {
	err error
}

func (f *fakePathValidator) Sanitize(raw string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	// Trivial sanitisation: take last `/`-separated segment.
	if i := strings.LastIndex(raw, "/"); i >= 0 {
		return raw[i+1:], nil
	}
	return raw, nil
}

type passRetrier struct{}

func (passRetrier) Do(ctx context.Context, op func(ctx context.Context) error) error {
	return op(ctx)
}

type recordingMetrics struct {
	mu                    sync.Mutex
	entries               []string
	failures              []string
	bombRules             []int
	bytesExtracted        int64
	partialFailures       int
	redeliverySkips       int
	slipsheetFailures     int
	extractionDurations   int
	classificationSuccess []string
	classificationFailure []string
}

func (r *recordingMetrics) EntryProcessed(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, s)
}
func (r *recordingMetrics) ExtractionDuration(d time.Duration, outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.extractionDurations++
}
func (r *recordingMetrics) ExtractionFailure(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, reason)
}
func (r *recordingMetrics) BombRejection(rule int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bombRules = append(r.bombRules, rule)
}
func (r *recordingMetrics) BytesExtracted(n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bytesExtracted += n
}
func (r *recordingMetrics) PartialFailure() { r.mu.Lock(); defer r.mu.Unlock(); r.partialFailures++ }
func (r *recordingMetrics) RedeliverySkip() { r.mu.Lock(); defer r.mu.Unlock(); r.redeliverySkips++ }
func (r *recordingMetrics) SlipsheetWriteFailure() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.slipsheetFailures++
}
func (r *recordingMetrics) ClassificationSuccess(category string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.classificationSuccess = append(r.classificationSuccess, category)
}
func (r *recordingMetrics) ClassificationFailure(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.classificationFailure = append(r.classificationFailure, reason)
}

func buildDeps(t *testing.T, body []byte) (extraction.Dependencies, *fakeUploader, *fakeRecorder, *fakeSlipsheetWriter, *recordingMetrics) {
	t.Helper()
	uploader := &fakeUploader{}
	recorder := &fakeRecorder{}
	slip := &fakeSlipsheetWriter{}
	m := &recordingMetrics{}
	deps := extraction.Dependencies{
		Downloader:      &fakeDownloader{body: body},
		Uploader:        uploader,
		Recorder:        recorder,
		SlipsheetWriter: slip,
		BombChecker:     &fakeBomb{},
		PathValidator:   &fakePathValidator{},
		Retrier:         passRetrier{},
		Metrics:         m,
		Logger:          mylog.NewDiscardLogger(),
		Clock:           extraction.SystemClock{},
		Config: extraction.ExtractionConfig{
			MaxExtractionDurationSec: 60,
			StagingBucket:            "staging",
		},
	}
	return deps, uploader, recorder, slip, m
}

func sampleMsg() extraction.ClaimCheck {
	return extraction.ClaimCheck{
		PipelineExecutionID: "exec-1",
		TenantID:            "t",
		DocumentID:          "d",
		SourceBucket:        "src-bucket",
		SourceKey:           "uploads/x.zip",
		CorrelationID:       "c",
	}
}

// --- Tests ---

func TestProcess_HappyPath_SUCCESS(t *testing.T) {
	zipBytes := makeZip(t, map[string][]byte{
		"a.txt":   []byte("hello"),
		"b/c.bin": []byte("world!!"),
	})
	deps, uploader, recorder, slip, m := buildDeps(t, zipBytes)
	svc := extraction.New(deps)

	out, err := svc.Process(context.Background(), sampleMsg())
	require.NoError(t, err)
	assert.Equal(t, extraction.StatusSuccess, out.Status)
	assert.Equal(t, 2, out.EntryCount)
	assert.Len(t, uploader.calls, 2)
	assert.Len(t, recorder.records, 2)
	assert.True(t, slip.called)
	assert.Equal(t, extraction.StatusSuccess, slip.lastStatus)
	assert.Empty(t, slip.lastReason)
	assert.Equal(t, []string{extraction.EntryStatusUploaded, extraction.EntryStatusUploaded}, m.entries)
}

func TestProcess_DownloadFailure_FAILED(t *testing.T) {
	deps, _, _, slip, _ := buildDeps(t, nil)
	deps.Downloader = &fakeDownloader{err: errors.New("NoSuchKey")}
	svc := extraction.New(deps)

	out, err := svc.Process(context.Background(), sampleMsg())
	require.NoError(t, err)
	assert.Equal(t, extraction.StatusFailed, out.Status)
	assert.NotEmpty(t, out.Reason)
	assert.True(t, slip.called)
	assert.Equal(t, extraction.StatusFailed, slip.lastStatus)
}

func TestProcess_CorruptZip_FAILED(t *testing.T) {
	deps, _, _, slip, _ := buildDeps(t, []byte("not a zip"))
	svc := extraction.New(deps)
	out, _ := svc.Process(context.Background(), sampleMsg())
	assert.Equal(t, extraction.StatusFailed, out.Status)
	assert.Contains(t, slip.lastReason, "corrupt-zip")
}

func TestProcess_PreBombViolation_FAILED(t *testing.T) {
	zipBytes := makeZip(t, map[string][]byte{"a.txt": []byte("hi")})
	deps, uploader, recorder, slip, m := buildDeps(t, zipBytes)
	deps.BombChecker = &fakeBomb{preErr: &extraction.BombDefenceError{Rule: 1, Reason: "test"}}
	svc := extraction.New(deps)

	out, _ := svc.Process(context.Background(), sampleMsg())
	assert.Equal(t, extraction.StatusFailed, out.Status)
	assert.Equal(t, "bomb-defence rule 1", slip.lastReason)
	assert.Equal(t, []int{1}, m.bombRules)
	assert.Empty(t, uploader.calls)
	assert.Empty(t, recorder.records)
}

func TestProcess_PerEntryBombViolation_AbortsArchive(t *testing.T) {
	zipBytes := makeZip(t, map[string][]byte{"a.txt": []byte("ok")})
	deps, _, _, slip, m := buildDeps(t, zipBytes)
	deps.BombChecker = &fakeBomb{entryErr: &extraction.BombDefenceError{Rule: 9, Reason: "too big"}}
	svc := extraction.New(deps)
	out, _ := svc.Process(context.Background(), sampleMsg())
	assert.Equal(t, extraction.StatusFailed, out.Status)
	assert.Equal(t, []int{9}, m.bombRules)
	assert.Equal(t, "bomb-defence rule 9", slip.lastReason)
}

func TestProcess_PathValidationFails_ArchiveFAILED(t *testing.T) {
	zipBytes := makeZip(t, map[string][]byte{"a.txt": []byte("ok")})
	deps, _, _, slip, _ := buildDeps(t, zipBytes)
	deps.PathValidator = &fakePathValidator{err: &extraction.PathValidationError{Path: "x", Reason: extraction.PathReasonTraversal}}
	svc := extraction.New(deps)
	out, _ := svc.Process(context.Background(), sampleMsg())
	assert.Equal(t, extraction.StatusFailed, out.Status)
	assert.Equal(t, extraction.PathReasonTraversal, slip.lastReason)
}

func TestProcess_UploadFailure_PARTIAL_FAILED(t *testing.T) {
	zipBytes := makeZip(t, map[string][]byte{
		"a.txt": []byte("ok"),
		"b.txt": []byte("ok"),
	})
	deps, uploader, recorder, slip, m := buildDeps(t, zipBytes)
	// Fail the second entry's upload (key contains "b.txt"). Determine actual key prefix.
	uploader.keyErr = map[string]error{}
	// We don't know the order; force-fail any key ending with "b.txt".
	uploader.err = nil
	deps.Uploader = &keyMatchingUploader{base: uploader, failSuffix: "-b.txt"}
	svc := extraction.New(deps)

	out, _ := svc.Process(context.Background(), sampleMsg())
	if out.Status == extraction.StatusSuccess {
		t.Fatalf("expected PARTIAL_FAILED or similar, got SUCCESS (%+v)", out)
	}
	require.NotNil(t, slip)
	assert.True(t, slip.called)
	// At least one uploaded record + at least one failed record expected.
	hasUploaded, hasFailed := false, false
	for _, r := range recorder.records {
		if r.Status == extraction.EntryStatusUploaded {
			hasUploaded = true
		}
		if r.Status == extraction.EntryStatusFailed {
			hasFailed = true
		}
	}
	assert.True(t, hasUploaded, "expected ≥1 UPLOADED record")
	assert.True(t, hasFailed, "expected ≥1 FAILED record")
	if hasUploaded && hasFailed {
		assert.Equal(t, extraction.StatusPartialFailed, out.Status)
		assert.GreaterOrEqual(t, m.partialFailures, 1)
	}
}

// keyMatchingUploader fails uploads where the key ends with failSuffix; otherwise delegates.
type keyMatchingUploader struct {
	base       *fakeUploader
	failSuffix string
}

func (k *keyMatchingUploader) Upload(ctx context.Context, bucket, key string, body io.Reader, sizeHint int64, contentType string) error {
	if strings.HasSuffix(key, k.failSuffix) {
		_, _ = io.Copy(io.Discard, body) // consume so the stream completes
		return &extraction.PermanentError{Cause: errors.New("AccessDenied")}
	}
	return k.base.Upload(ctx, bucket, key, body, sizeHint, contentType)
}

func TestProcess_EmptyArchive_SUCCESS(t *testing.T) {
	zipBytes := makeZip(t, map[string][]byte{})
	deps, uploader, recorder, slip, _ := buildDeps(t, zipBytes)
	svc := extraction.New(deps)
	out, _ := svc.Process(context.Background(), sampleMsg())
	assert.Equal(t, extraction.StatusSuccess, out.Status)
	assert.Empty(t, uploader.calls)
	assert.Empty(t, recorder.records)
	assert.True(t, slip.called)
	assert.Equal(t, 0, len(slip.lastEntries))
}

func TestProcess_ContextCancelBetweenEntries(t *testing.T) {
	// Use many entries with a tight extraction context so cancellation fires mid-loop.
	entries := map[string][]byte{}
	for i := 0; i < 50; i++ {
		entries["entry-"+string(rune('a'+i%26))+string(rune('0'+i%10))] = []byte("payload")
	}
	zipBytes := makeZip(t, entries)
	deps, _, _, slip, _ := buildDeps(t, zipBytes)
	deps.Config.MaxExtractionDurationSec = 1 // tight bound

	// Slow uploader so the deadline trips.
	deps.Uploader = &slowUploader{delay: 100 * time.Millisecond}
	svc := extraction.New(deps)
	out, _ := svc.Process(context.Background(), sampleMsg())
	// Either rule-10 timeout OR drain canceled OR PARTIAL_FAILED depending on timing.
	assert.NotEqual(t, extraction.StatusSuccess, out.Status)
	assert.True(t, slip.called)
}

type slowUploader struct {
	delay time.Duration
}

func (s *slowUploader) Upload(ctx context.Context, _ string, _ string, body io.Reader, _ int64, _ string) error {
	_, _ = io.Copy(io.Discard, body)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.delay):
		return nil
	}
}

// --- Lower-level: computeStatus + buildChildKey via integration through Process ---

func TestProcess_StatusBytesAccounting(t *testing.T) {
	zipBytes := makeZip(t, map[string][]byte{"a.bin": bytes.Repeat([]byte{'x'}, 1024)})
	deps, _, _, _, m := buildDeps(t, zipBytes)
	svc := extraction.New(deps)
	_, _ = svc.Process(context.Background(), sampleMsg())
	assert.Equal(t, int64(1024), m.bytesExtracted)
}

// --- Classification hop tests ---

// fakeClassifier records calls and returns canned outputs / errors per call index.
type fakeClassifier struct {
	mu      sync.Mutex
	calls   []extraction.ClassifyRequest
	results []*extraction.Classification // index aligned with calls
	errs    []error                      // index aligned with calls
}

func (f *fakeClassifier) Classify(ctx context.Context, req extraction.ClassifyRequest) (*extraction.Classification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Drain the body to mirror real client semantics (otherwise the io.Reader could leak).
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
	idx := len(f.calls)
	f.calls = append(f.calls, req)
	var res *extraction.Classification
	if idx < len(f.results) {
		res = f.results[idx]
	}
	var err error
	if idx < len(f.errs) {
		err = f.errs[idx]
	}
	return res, err
}

func TestProcess_Classification_StampedOnSuccess(t *testing.T) {
	zipBytes := makeZip(t, map[string][]byte{"a.txt": []byte("hello")})
	deps, _, _, slip, m := buildDeps(t, zipBytes)

	fc := &fakeClassifier{
		results: []*extraction.Classification{
			{Format: "txt", Category: "ocr-direct", ConfidenceScore: 0.99, DetectionTier: "extension-fallback", PolicyVersion: "v1"},
		},
	}
	deps.Classifier = fc
	svc := extraction.New(deps)

	out, err := svc.Process(context.Background(), sampleMsg())
	require.NoError(t, err)
	assert.Equal(t, extraction.StatusSuccess, out.Status)

	require.Len(t, fc.calls, 1)
	assert.Equal(t, "t", fc.calls[0].WorkspaceID) // from sampleMsg().TenantID
	assert.Equal(t, "a.txt", fc.calls[0].Filename)
	assert.Equal(t, 1, fc.calls[0].ParentArchiveDepth)

	require.Len(t, slip.lastEntries, 1)
	require.NotNil(t, slip.lastEntries[0].Classification, "classification should be stamped on EntryOutcome")
	assert.Equal(t, "txt", slip.lastEntries[0].Classification.Format)
	assert.Equal(t, "ocr-direct", slip.lastEntries[0].Classification.Category)

	assert.Equal(t, []string{"ocr-direct"}, m.classificationSuccess)
	assert.Empty(t, m.classificationFailure)
}

func TestProcess_Classification_FailureDoesNotAffectOutcome(t *testing.T) {
	zipBytes := makeZip(t, map[string][]byte{"a.txt": []byte("hello")})
	deps, _, _, slip, m := buildDeps(t, zipBytes)

	fc := &fakeClassifier{errs: []error{errors.New("classifier exploded")}}
	deps.Classifier = fc
	svc := extraction.New(deps)

	out, err := svc.Process(context.Background(), sampleMsg())
	require.NoError(t, err)
	assert.Equal(t, extraction.StatusSuccess, out.Status, "classification failure must not change archive outcome")

	require.Len(t, slip.lastEntries, 1)
	assert.Equal(t, extraction.EntryStatusUploaded, slip.lastEntries[0].Status)
	assert.Nil(t, slip.lastEntries[0].Classification)

	assert.Empty(t, m.classificationSuccess)
	assert.Equal(t, []string{"http"}, m.classificationFailure)
}

func TestProcess_Classification_FallbackWorkspace(t *testing.T) {
	zipBytes := makeZip(t, map[string][]byte{"a.txt": []byte("hi")})
	deps, _, _, _, _ := buildDeps(t, zipBytes)
	deps.Config.ClassificationFallbackWorkspace = "wks-default"
	fc := &fakeClassifier{results: []*extraction.Classification{{Format: "txt"}}}
	deps.Classifier = fc
	svc := extraction.New(deps)

	msg := sampleMsg()
	msg.TenantID = "" // empty → must fall back to configured default
	_, err := svc.Process(context.Background(), msg)
	require.NoError(t, err)

	require.Len(t, fc.calls, 1)
	assert.Equal(t, "wks-default", fc.calls[0].WorkspaceID)
}

func TestProcess_Classification_NoWorkspaceSkipped(t *testing.T) {
	zipBytes := makeZip(t, map[string][]byte{"a.txt": []byte("hi")})
	deps, _, _, _, m := buildDeps(t, zipBytes)
	fc := &fakeClassifier{}
	deps.Classifier = fc
	svc := extraction.New(deps)

	msg := sampleMsg()
	msg.TenantID = "" // no fallback configured → skipped
	_, err := svc.Process(context.Background(), msg)
	require.NoError(t, err)

	assert.Empty(t, fc.calls, "classifier should not be called without a workspaceId")
	assert.Equal(t, []string{"no-workspace"}, m.classificationFailure)
}

func TestProcess_Classification_NilClassifier_NoCall(t *testing.T) {
	zipBytes := makeZip(t, map[string][]byte{"a.txt": []byte("hi")})
	deps, _, _, slip, _ := buildDeps(t, zipBytes)
	deps.Classifier = nil
	svc := extraction.New(deps)
	_, err := svc.Process(context.Background(), sampleMsg())
	require.NoError(t, err)
	assert.Nil(t, slip.lastEntries[0].Classification)
}

func TestProcess_Classification_NotCalledOnFailedEntry(t *testing.T) {
	zipBytes := makeZip(t, map[string][]byte{"a.txt": []byte("hi")})
	deps, _, _, _, _ := buildDeps(t, zipBytes)
	// Per-key upload failure on the only entry.
	deps.Uploader = &fakeUploader{keyErr: map[string]error{"input/exec-1/0001-a.txt": errors.New("S3 boom")}}
	fc := &fakeClassifier{}
	deps.Classifier = fc
	svc := extraction.New(deps)
	_, err := svc.Process(context.Background(), sampleMsg())
	require.NoError(t, err)

	assert.Empty(t, fc.calls, "classifier must not be called for failed entries")
}
