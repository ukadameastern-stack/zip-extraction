# Component Methods — Zip Extraction Service (UOW-SVC-12)

**Document Type**: Component Method Signatures
**Phase**: INCEPTION — Application Design (Part 2: Generation)
**Generated**: 2026-05-24

This document records **method signatures only** for every component identified in `components.md`. Detailed business rules and algorithm bodies are deferred to **Functional Design** (per-unit, CONSTRUCTION phase). Each component section ends with a **Testable Properties** subsection per PBT-01, which is the explicit hand-off carrying forward the PBT properties identified in `requirements.md` NFR-8.

Error types referenced (defined in `internal/extraction/errors.go` per Q4): `*BombDefenceError`, `*PathValidationError`, `*UnsupportedFeatureError`, `*TransientError`, `*PermanentError`.

---

## 1. `internal/app`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `New` | `func New(cfg Config, deps Dependencies) *Service` | Constructor — pure wiring; performs no I/O. |
| `Run` | `func (s *Service) Run(ctx context.Context) error` | Start all subsystems (HTTP server, SQS receive-loop, worker pool). Blocks until `ctx` done. Returns `nil` on graceful shutdown, error on fatal startup failure. |
| `startupHealthChecks` | `(unexported) func (s *Service) startupHealthChecks(ctx context.Context) error` | Probe SQS / S3 / DynamoDB reachability; flips `HealthGate.SetReady(true)` on success. |
| `gracefulDrain` | `(unexported) func (s *Service) gracefulDrain(ctx context.Context)` | On root-context cancellation, stop SQS receives, allow workers up to `gracefulShutdownTimeoutSec` (default 250 s) to finish, then return. Per Q7. |

### Testable Properties
**No PBT properties identified.** `app` is a thin wiring layer; behaviour is covered indirectly by Gate 2 (Testcontainers + LocalStack) and by unit tests of the ports it composes.

---

## 2. `internal/sqs`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `NewAdapter` | `func NewAdapter(client SQSClient, cfg Config, heart Heartbeater) *Adapter` | Constructor. |
| `Run` | `func (a *Adapter) Run(ctx context.Context, handler MessageHandler) error` | Long-poll receive-loop with bounded worker pool (Q2). Returns `nil` on graceful shutdown. |
| `dispatch` | `(unexported) func (a *Adapter) dispatch(ctx context.Context, msg *sqs.Message, handler MessageHandler)` | Worker-pool slot — runs `handler` with per-message heartbeat. |
| `parseMessage` | `(unexported) func parseMessage(raw *sqs.Message) (ClaimCheck, error)` | Validate JSON schema (FR-1.2). Returns `*extraction.PermanentError` on schema violation. |
| `Heartbeater.Start` | `func (h *heartbeater) Start(ctx context.Context, receiptHandle string) (cancel func())` | Spawn the per-message visibility-extension goroutine (FR-9). The returned `cancel` is invoked when the worker completes. |

### Testable Properties (PBT-01)

| Category | Property | Notes |
|---|---|---|
| Stateful (PBT-06) | For any sequence of `{ReceiveBatch, WorkerComplete, ShutdownSignal}` commands, the invariant `len(activeHeartbeats) == len(inFlightMessages)` holds after each step | Use a fake `SQSClient` + virtual clock; PBT generates command sequences. |
| Invariant (PBT-03) | After shutdown signal + ≤ `gracefulShutdownTimeoutSec`, `len(activeHeartbeats) == 0` and `len(inFlightMessages) == 0` regardless of starting state | Combines with the property above. |
| Invariant (PBT-03) | `parseMessage` rejects any JSON not matching the FR-1.2 schema with `*PermanentError` | Generator emits valid + invalid JSON; assert classification. |

---

## 3. `internal/extraction`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `New` | `func New(deps Dependencies) *Service` | Constructor — wires ports. |
| `Process` | `func (s *Service) Process(ctx context.Context, msg ClaimCheck) (Outcome, error)` | Single-message extraction lifecycle. Returns `Outcome.Status ∈ {SUCCESS, PARTIAL_FAILED, FAILED}` plus a `reason` when FAILED. |
| `downloadArchive` | `(unexported) func (s *Service) downloadArchive(ctx context.Context, msg ClaimCheck) (io.ReadCloser, int64, error)` | Wraps `Downloader.Download` with the 240 s extraction context. |
| `openZip` | `(unexported) func openZip(r io.ReaderAt, size int64) (*zip.Reader, ArchiveMetadata, error)` | Opens ZIP, extracts metadata (entry count, total compressed size). Returns `*UnsupportedFeatureError` for encrypted / multi-disk / Deflate64. |
| `processEntries` | `(unexported) func (s *Service) processEntries(ctx context.Context, zr *zip.Reader, msg ClaimCheck) ([]EntryOutcome, error)` | Sequential per-entry loop. |
| `processEntry` | `(unexported) func (s *Service) processEntry(ctx context.Context, idx int, entry *zip.File, msg ClaimCheck) (EntryOutcome, error)` | Validate path → sanity-check size → wrap stream in `bombdefence.LimitedReader` → upload via `Retrier.Do(... Uploader.Upload ...)` → record DynamoDB row. |
| `computeStatus` | `(unexported) func computeStatus(entries []EntryOutcome, archiveErr error) Status` | SUCCESS if all entries UPLOADED; PARTIAL_FAILED if some FAILED but ≥1 UPLOADED; FAILED if archive-level error or zero entries succeeded. |
| `cleanup` | `(unexported) func (s *Service) cleanup(tmpPath string)` | Called via `defer` (FR-11.2). Idempotent — safe on partial state. |

### Error type methods (Q4)

```go
func (e *BombDefenceError) Error() string
func (e *PathValidationError) Error() string
func (e *UnsupportedFeatureError) Error() string
func (e *TransientError) Error() string
func (e *TransientError) Unwrap() error
func (e *PermanentError) Error() string
func (e *PermanentError) Unwrap() error

// Helpers for callers to classify errors without type-asserting everywhere.
func IsBombDefence(err error) (*BombDefenceError, bool)
func IsPathValidation(err error) (*PathValidationError, bool)
func IsUnsupportedFeature(err error) (*UnsupportedFeatureError, bool)
func IsTransient(err error) (*TransientError, bool)
func IsPermanent(err error) (*PermanentError, bool)
```

### Testable Properties (PBT-01)

| Category | Property | Notes |
|---|---|---|
| Invariant (PBT-03) | For any extraction that returns `Outcome.Status == SUCCESS`, every `EntryOutcome` in the result has `Status == UPLOADED` | Generator: synthetic zip + fake S3/DDB. |
| Invariant (PBT-03) | For any extraction that returns `Outcome.Status == FAILED`, the slipsheet `failureReason` is non-empty | Property holds across all bomb-defence and unsupported-feature paths. |
| Stateful (PBT-06) | Status transitions: any valid sequence of `{EntryUploaded, EntryFailed, BombViolation}` events maps deterministically to one of `{SUCCESS, PARTIAL_FAILED, FAILED}` via `computeStatus` | Generator emits random sequences. |
| Invariant (PBT-03) | For any cumulative compressed input ≤ `cfg.MaxCompressedSizeBytes` AND entry count ≤ `cfg.MaxEntryCount`, `PreCheck` returns nil | Cross-checks the `Checker.PreCheck` contract from `bombdefence`. |
| Round-trip (PBT-02) | `Process(msg)` is idempotent under SQS re-delivery: running `Process` twice with the same `msg` produces the same DynamoDB state and the same final S3 object set | Driven by the `Recorder` conditional PutItem in `internal/dynamodb`. |

---

## 4. `internal/bombdefence`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `New` | `func New(cfg Config) *Checker` | Constructor. |
| `PreCheck` | `func (c *Checker) PreCheck(meta extraction.ArchiveMetadata) error` | Rules #1 (compressed-size) + #4 (entry-count). Returns `*BombDefenceError` on violation. |
| `EntryCheck` | `func (c *Checker) EntryCheck(entryIndex int, entry EntryInfo) error` | Rules #5 (depth) + #6 (symlink) + #9 (single-file-size). |
| `NewLimitedReader` | `func (c *Checker) NewLimitedReader(r io.Reader, compressedSize int64) io.Reader` | Q5 — short-circuiting wrapper enforcing rules #2 (cumulative extracted size) and #3 (compression ratio). |
| `EntryInfo` | `type EntryInfo struct { Name string; Mode os.FileMode; CompressedSize, UncompressedSize int64 }` | Subset of `*zip.File` needed for checks (testable without the full zip type). |

The internal limited-reader type implements `io.Reader`:

```go
type limitedReader struct { /* unexported */ }
func (lr *limitedReader) Read(p []byte) (n int, err error)
```

It returns `(0, *BombDefenceError{Rule: 2 | 3})` the moment a threshold is crossed.

### Testable Properties (PBT-01)

| Category | Property | Notes |
|---|---|---|
| Invariant (PBT-03) | For any archive metadata where compressed-size > cap OR entry-count > cap, `PreCheck` returns `*BombDefenceError` | Generator: random metadata bounded above/below caps. |
| Invariant (PBT-03) | For any limited-reader run, the number of bytes ever returned satisfies `read ≤ cap`. There is NO sequence of reads that crosses the cap | Strong streaming invariant (Q5 short-circuit). |
| Invariant (PBT-03) | For any input stream, the runtime ratio `extracted / compressed` measured by the limited-reader at any point is ≤ `MaxCompressionRatio + ε` (ε accounts for the last partial block) | Generator: synthetic streams with controlled expansion. |
| Oracle (PBT-05) | `EntryCheck`'s depth count equals `strings.Count(filepath.Clean(name), "/")` for any normalised name (oracle = stdlib) | Cross-implementation check. |

---

## 5. `internal/validation`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `New` | `func New() *PathValidator` | Constructor (no state). |
| `Sanitize` | `func (v *PathValidator) Sanitize(rawPath string) (safeName string, err error)` | Normalise ZIP entry path; reject traversal / absolute / drive-letter / control-char inputs. Returns base filename only. |

### Testable Properties (PBT-01)

| Category | Property | Notes |
|---|---|---|
| Invariant (PBT-03) | For any accepted path, `safeName` contains no `..`, no `/`, no `\`, no `:`, no leading `.` other than legitimate filenames | Strict allowlist post-condition. |
| Invariant (PBT-03) | `safeName` length ≤ 255 bytes (filesystem-safe even though S3 has higher limit) | Bound on output domain. |
| Idempotence (PBT-04) | `Sanitize(Sanitize(x)) == Sanitize(x)` for any valid input `x` (where the first call succeeded) | Pure-function idempotence. |
| Invariant (PBT-03) | For any input containing `..` (even URL-encoded or normalised), `Sanitize` returns `*PathValidationError` | Negative property — adversarial generator. |
| Invariant (PBT-03) | For any input starting with `/`, `\`, or `[A-Z]:`, `Sanitize` returns `*PathValidationError` | Negative property. |

---

## 6. `internal/storage`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `NewAdapter` | `func NewAdapter(client S3Client, uploader S3Uploader, cfg Config) *Adapter` | Constructor. |
| `Download` | `func (a *Adapter) Download(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error)` | `GetObject`. Returns the body as `io.ReadCloser` plus `ContentLength`. |
| `Upload` | `func (a *Adapter) Upload(ctx context.Context, bucket, key string, body io.Reader, sizeHint int64) error` | Single `PutObject` for ≤ 5 MiB; otherwise multipart via `s3manager.Uploader`. Sets `ContentType` via `DetectMIME`. |
| `DetectMIME` | `func DetectMIME(peek []byte, fileName string) string` | Q6 hybrid: try `net/http.DetectContentType(peek)`; if result is `application/octet-stream`, fall back to `mime.TypeByExtension(filepath.Ext(fileName))`; final fallback is `application/octet-stream`. |
| `peekReader` | `(unexported) func peekReader(r io.Reader, n int) (peek []byte, rebuilt io.Reader, err error)` | `bufio.NewReader.Peek(n)` then `io.MultiReader(bytes.NewReader(peek), bufio.Reader)` — no extra read pass. |

### Testable Properties (PBT-01)

| Category | Property | Notes |
|---|---|---|
| Round-trip (PBT-02) | For any `(bucket, key)` pair, S3 key derivation `Format → Parse → Format` yields the same string | Helper functions exported for PBT. |
| Invariant (PBT-03) | `Upload` with `sizeHint > MultipartThresholdBytes` routes to `s3manager.Uploader` (assert via fake) | Mock-based property. |
| Oracle (PBT-05) | `DetectMIME(peek, name) == "application/octet-stream"` iff (`http.DetectContentType(peek) == "application/octet-stream"` AND `mime.TypeByExtension(filepath.Ext(name)) == ""`) | Exhaustive logical-or check. |

---

## 7. `internal/dynamodb`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `NewAdapter` | `func NewAdapter(client DDBClient, cfg Config) *Adapter` | Constructor. |
| `RecordEntry` | `func (a *Adapter) RecordEntry(ctx context.Context, rec extraction.PipelineFile) error` | `PutItem` with `ConditionExpression: attribute_not_exists(pk)`. `ConditionalCheckFailedException` → nil (idempotency). |
| `Marshal` | `func Marshal(rec extraction.PipelineFile) (map[string]types.AttributeValue, error)` | Round-trip helper — exported for PBT-02. |
| `Unmarshal` | `func Unmarshal(av map[string]types.AttributeValue) (extraction.PipelineFile, error)` | Round-trip helper. |

### Testable Properties (PBT-01)

| Category | Property | Notes |
|---|---|---|
| Round-trip (PBT-02) | `Unmarshal(Marshal(rec)) == rec` for any valid `rec` | Generator emits random valid `PipelineFile`. |
| Idempotence (PBT-04) | Running `RecordEntry(rec)` twice (fake DDB tracks PK uniqueness) results in the same final state as running it once | At-least-once delivery property. |
| Invariant (PBT-03) | For any `rec`, `Marshal(rec)` always produces an item containing required keys `pk`, `sk`, `documentId`, `sourceArchive`, `childKey`, `mimeType`, `status`, `sizeBytes` | Schema invariant. |

---

## 8. `internal/slipsheet`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `NewWriter` | `func NewWriter(uploader extraction.S3Uploader, cfg Config) *Writer` | Constructor. |
| `Build` | `func (w *Writer) Build(execID, sourceArchive string, status extraction.Status, entries []ChildEntry) Slipsheet` | Pure builder. |
| `Write` | `func (w *Writer) Write(ctx context.Context, ss Slipsheet) error` | Marshal + upload to `slipsheets/{ss.PipelineExecutionID}.json` (Q7 of requirements). |
| `Marshal` | `func Marshal(ss Slipsheet) ([]byte, error)` | Round-trip helper. |
| `Unmarshal` | `func Unmarshal(b []byte) (Slipsheet, error)` | Round-trip helper. |

### Testable Properties (PBT-01)

| Category | Property | Notes |
|---|---|---|
| Round-trip (PBT-02) | `Unmarshal(Marshal(ss)) == ss` for any valid `Slipsheet` | Generator emits random valid `Slipsheet`. |
| Invariant (PBT-03) | `Build(_, _, FAILED, _).Status == "FAILED"` AND `len(.Children) == 0` whenever the archive itself failed pre-extraction | Generator: pre-extraction-failed scenarios. |
| Invariant (PBT-03) | `Build(_, _, PARTIAL_FAILED, entries).Status == "PARTIAL_FAILED"` iff at least one child has `Status == "FAILED"` AND at least one has `Status == "UPLOADED"` | Cross-property with `extraction.computeStatus`. |

---

## 9. `internal/retry`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `New` | `func New(cfg Config, clock extraction.Clock, rng *rand.Rand, logger extraction.Logger) *Retrier` | Constructor. `rng` is exposed for deterministic testing. |
| `Do` | `func (r *Retrier) Do(ctx context.Context, op func(ctx context.Context) error) error` | Invoke `op`, retry only on `*extraction.TransientError`, up to `MaxAttempts`. |
| `BackoffFor` | `func BackoffFor(attempt int, cfg Config, jitter float64) time.Duration` | PBT-05 oracle: closed-form `base * factor^attempt * (1 + jitter*r)` with `r ∈ [-1, 1]`. |
| `Classify` | `func Classify(err error) (transient bool, class string)` | Inspects AWS SDK error codes / HTTP statuses; returns transient classification for throttling, 5xx, timeout, network. |

### Testable Properties (PBT-01)

| Category | Property | Notes |
|---|---|---|
| Oracle (PBT-05) | For any `(attempt, cfg, jitterFraction)`, the implementation's delay equals `BackoffFor(attempt, cfg, jitter)` within ±1 µs | Reference formula matches generated values. |
| Invariant (PBT-03) | `Classify` returns `transient == false` for `*BombDefenceError`, `*PathValidationError`, `*UnsupportedFeatureError`, `*PermanentError`, and any `4xx` AWS error | Negative classification property. |
| Invariant (PBT-03) | `Classify` returns `transient == true` for throttling (`ProvisionedThroughputExceeded`, `SlowDown`, `RequestThrottled`), 5xx, and `context.DeadlineExceeded` not derived from the parent extraction ctx | Positive classification property. |
| Stateful (PBT-06) | For any sequence of operation outcomes (`success` | `transient_n` for n = 1..3 | `permanent`), `Do` calls `op` at most `min(1 + transient-count, MaxAttempts)` times | Stateful PBT with command generator. |
| Idempotence (PBT-04) | If `op` is idempotent and eventually succeeds, `Do(op)` produces the same final external state as a single successful `op` | Composability property. |

---

## 10. `internal/metrics`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `New` | `func New(reg prometheus.Registerer) *Metrics` | Construct + register all 6 collectors (FR-13.2). |
| `EntryProcessed` | `func (m *Metrics) EntryProcessed(status string)` | `zip_entries_total{status}` |
| `ExtractionDuration` | `func (m *Metrics) ExtractionDuration(d time.Duration, outcome string)` | `zip_extraction_duration_seconds{outcome}` (histogram) |
| `ExtractionFailure` | `func (m *Metrics) ExtractionFailure(reason string)` | `zip_extraction_failures_total{reason}` |
| `BombRejection` | `func (m *Metrics) BombRejection(rule int)` | `zip_bomb_rejections_total{rule}` |
| `BytesExtracted` | `func (m *Metrics) BytesExtracted(n int64)` | `extracted_bytes_total` |
| `PartialFailure` | `func (m *Metrics) PartialFailure()` | `partial_failures_total` |

### Testable Properties (PBT-01)

**No PBT properties identified beyond simple unit tests** — this component is a thin Prometheus collector wrapper. Behaviour is verified via example-based tests that assert the registered collectors' names, label sets, and types match FR-13.2.

---

## 11. `internal/health`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `NewGate` | `func NewGate() *Gate` | Construct readiness gate (default not-ready). |
| `Gate.SetReady` | `func (g *Gate) SetReady(ready bool)` | Flip readiness atomically. |
| `Gate.Ready` | `func (g *Gate) Ready() bool` | Atomic read. |
| `NewServer` | `func NewServer(port int, gate HealthGate) *Server` | Wire routes; do not start. |
| `Server.Start` | `func (s *Server) Start(ctx context.Context) error` | Bind + serve; respects `ctx.Done`. |
| `Server.Shutdown` | `func (s *Server) Shutdown(ctx context.Context) error` | Graceful `http.Server.Shutdown`. |
| HTTP handlers | `(unexported)` | `/healthz/live` → 200; `/healthz/ready` → 200 iff `gate.Ready()`; `/metrics` → `promhttp.Handler()`. |

### Testable Properties (PBT-01)

**No PBT properties identified.** Behaviour verified via example-based HTTP tests (httptest).

---

## 12. `internal/config`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `Load` | `func Load() (Config, error)` | Read env vars + parse YAML at `CONFIG_PATH`. Apply schema strict-decode (`yaml.Decoder.KnownFields(true)`). |
| `Validate` | `func (c Config) Validate() error` | Range and consistency checks. |
| `loadEnv` | `(unexported) func loadEnv() (InfraConfig, LoggingConfig, HTTPConfig, error)` | Env-var parsing. |
| `loadYAML` | `(unexported) func loadYAML(path string) (bombdefence.Config, StreamingConfig, retry.Config, SQSConfig, error)` | YAML parsing. |

### Testable Properties (PBT-01)

| Category | Property | Notes |
|---|---|---|
| Invariant (PBT-03) | A `Config` returned by `Load` always passes `Validate` (round-trip via temp files for PBT generators) | Round-trip property combined with invariant. |
| Round-trip (PBT-02) | YAML marshal(Config.YAML-section) → Unmarshal yields the original section | Generator emits random valid configs. |
| Invariant (PBT-03) | `Validate` rejects any config with `MaxAttempts ≤ 0`, `MaxExtractionDurationSec ≤ 0`, `MultipartThresholdBytes < 5 MiB`, or `MaxInFlight ≤ 0` | Negative property. |

---

## 13. `internal/awsclients`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `Build` | `func Build(ctx context.Context, cfg config.InfraConfig) (Set, error)` | Construct SQS, S3, DynamoDB clients + `s3manager.Uploader`. Applies endpoint override if `cfg.AWSEndpointURL != ""`. |
| `endpointResolver` | `(unexported) func endpointResolver(endpoint string) aws.EndpointResolverWithOptionsFunc` | LocalStack endpoint resolver factory. |

### Testable Properties (PBT-01)

**No PBT properties identified.** Behaviour is verified via Gate 2 (Testcontainers + LocalStack) E2E.

---

## 14. `internal/log`

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `New` | `func New(cfg config.LoggingConfig, version string) (Logger, error)` | Construct `*zap.Logger` with format from `cfg.Format` ("json"/"console") per Q10. |
| `Logger.With` | `func (l *zapLogger) With(fields ...Field) Logger` | Returns a child logger with bound fields. |
| `Logger.Info/Warn/Error/Debug` | `func (l *zapLogger) <Level>(msg string, fields ...Field)` | Standard log methods. |
| `denySensitiveKeys` | `(unexported) func denySensitiveKeys(fields []Field) []Field` | Deny-list filter for fields whose key matches `password`, `token`, `credential`, `aws_access_key_id`, `secret`, etc. Replaces value with `[REDACTED]` and emits a warn. |

### Testable Properties (PBT-01)

| Category | Property | Notes |
|---|---|---|
| Invariant (PBT-03) | For any input field set, the emitted JSON entry contains NO substring matching the deny-list values (regex on stdout buffer) | Generator emits fields with sensitive keys; assert redaction. |

---

## Summary — PBT-01 Carry-Forward to Functional Design

Per **PBT-01** ("Property Identification During Design"), the table below summarises which property categories apply per component. **Functional Design** (CONSTRUCTION phase) will refine the property statements into concrete `rapid` test plans.

| Component | Round-trip | Invariant | Idempotence | Stateful | Oracle |
|---|---|---|---|---|---|
| `app` | — | — | — | — | — |
| `sqs` | — | ✓ | — | ✓ | — |
| `extraction` | ✓ | ✓ | — | ✓ | — |
| `bombdefence` | — | ✓ | — | — | ✓ |
| `validation` | — | ✓ | ✓ | — | — |
| `storage` | ✓ | ✓ | — | — | ✓ |
| `dynamodb` | ✓ | ✓ | ✓ | — | — |
| `slipsheet` | ✓ | ✓ | — | — | — |
| `retry` | — | ✓ | ✓ | ✓ | ✓ |
| `metrics` | — | — | — | — | — |
| `health` | — | — | — | — | — |
| `config` | ✓ | ✓ | — | — | — |
| `awsclients` | — | — | — | — | — |
| `log` | — | ✓ | — | — | — |

Components with no PBT properties identified are marked **"No PBT properties identified"** explicitly per PBT-01 verification requirement.
