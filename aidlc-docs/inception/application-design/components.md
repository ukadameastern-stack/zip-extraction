# Components — Zip Extraction Service (UOW-SVC-12)

**Document Type**: Component Inventory
**Phase**: INCEPTION — Application Design (Part 2: Generation)
**Generated**: 2026-05-24

This document enumerates every logical component (one per Go internal package, plus `cmd/zip-extraction`). For each component it records: purpose, responsibilities, public interfaces (Go interface contracts), and the SECURITY / PBT rules whose enforcement it owns.

Per Q8 decision, `bombdefence` and `validation` remain **separate** components — both are security-critical but operate on disjoint inputs (aggregate archive metrics vs. entry-path strings) with disjoint properties.

---

## 1. `cmd/zip-extraction`

**Type**: Application entry point (`main` package)
**Purpose**: Process bootstrap — read config, construct AWS clients, wire components, run the App service until signalled.

**Responsibilities**:
- Parse environment variables (FR-14.1) and load YAML config (FR-14.2).
- Construct the structured logger (`internal/log`) using `LOG_FORMAT` (NFR-5.3).
- Construct AWS SDK v2 clients via `internal/awsclients` honouring `AWS_ENDPOINT_URL` (FR-15.1).
- Wire all adapters and inject them into `internal/app.Service`.
- Install SIGTERM/SIGINT handler that cancels the root context (Q7).
- Block on `app.Run(ctx)`; exit with code 0 on graceful shutdown, code 1 on fatal error.

**Public Interface**: `main()` — no exported symbols.

**Owns enforcement of**: SECURITY-09 (no default credentials at startup), SECURITY-15 (top-level error handler / recover).

---

## 2. `internal/app`

**Type**: Top-level orchestrator service.
**Purpose**: Coordinate the SQS consumer loop, the HTTP operational server, and graceful shutdown.

**Responsibilities**:
- Start the HTTP operational server (`/healthz/live`, `/healthz/ready`, `/metrics`) on the configured port (Q3 → 8080).
- Start the SQS receive-loop and worker pool (Q2).
- Mark `/healthz/ready` ready only after AWS clients pass startup health checks (SQS queue reachable, S3 bucket reachable, DynamoDB table reachable).
- On root-context cancellation, initiate graceful drain (Q7) — stop receiving new SQS messages, allow workers to complete (≤ `gracefulShutdownTimeoutSec`, default 250 s), then exit.

**Public Interface**:
```go
package app

type Service struct { /* unexported */ }

// New constructs a fully wired Service. All dependencies are injected (Q1).
func New(cfg Config, deps Dependencies) *Service

// Run starts all subsystems and blocks until ctx is cancelled or a fatal error occurs.
func (s *Service) Run(ctx context.Context) error

// Config holds runtime config (loaded from env + YAML by cmd/zip-extraction).
type Config struct { /* see internal/config */ }

// Dependencies is the dependency-injection root.
type Dependencies struct {
    Logger      Logger
    Metrics     Metrics
    Queue       MessageQueue
    Extractor   Extractor
    HealthGate  HealthGate
}
```

**Owns enforcement of**: SECURITY-11 (orchestrator centralises wiring; security-critical components remain isolated).

---

## 3. `internal/sqs` (Message Queue Adapter + Receive Loop)

**Type**: Adapter component implementing `app.MessageQueue` (Q1).
**Purpose**: Long-poll SQS, dispatch messages to the worker pool, manage per-message visibility heartbeats.

**Responsibilities**:
- Long-poll `ReceiveMessage` with `MaxNumberOfMessages=10`, `WaitTimeSeconds=20` (Q2).
- Dispatch each received message to a bounded worker pool (size = `sqs.maxInFlight`, default 5).
- Start a per-message heartbeat goroutine (FR-9, Q6) that calls `ChangeMessageVisibility` every 30 s with a context bound to the message lifecycle.
- Call `DeleteMessage` when the worker reports terminal completion (SUCCESS, PARTIAL_FAILED, or FAILED-after-bomb-defence). Leave the message for SQS native redrive on `TransientError` panics or DLQ-bound errors.
- On root-context cancellation, stop pulling new batches, drain in-flight workers (Q7).

**Public Interface**:
```go
package sqs

// MessageQueue is the port consumed by app.Service.
type MessageQueue interface {
    Run(ctx context.Context, handler MessageHandler) error
}

// MessageHandler is invoked once per received message.
// A returned error of nil → DeleteMessage; non-nil → leave for SQS redrive.
type MessageHandler func(ctx context.Context, msg ClaimCheck) error

// Adapter wraps the AWS SQS SDK client and implements MessageQueue.
type Adapter struct { /* unexported */ }
func NewAdapter(client SQSClient, cfg Config, heart Heartbeater) *Adapter
func (a *Adapter) Run(ctx context.Context, handler MessageHandler) error

// SQSClient is the minimum SDK surface used (testable seam — Q1).
type SQSClient interface {
    ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
    DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
    ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

// Heartbeater encapsulates the per-message ChangeMessageVisibility goroutine.
type Heartbeater interface {
    Start(ctx context.Context, receiptHandle string) (cancel func())
}
```

**Owns enforcement of**: FR-1 (queue consumption + schema), FR-9 (heartbeat), Q7 (graceful drain).

---

## 4. `internal/extraction` (Orchestrator)

**Type**: Core domain component — does NOT touch AWS SDK directly; consumes port interfaces.
**Purpose**: For one SQS message, drive the end-to-end extraction lifecycle: download → open ZIP → bomb-defence pre-checks → per-entry loop (validate, stream upload, record) → slipsheet → status assignment.

**Responsibilities**:
- Download the archive from S3 via the `S3Downloader` port (streaming, no full buffer per NFR-3.1).
- Apply pre-extraction bomb defence (rules #1, #4 from archive metadata).
- Iterate ZIP entries sequentially (FR-3.2). For each entry:
  - Reject symlinks (rule #6), absolute paths (rule #7), traversal paths (rule #8) via `validation` port.
  - Apply per-entry limits (single-file size rule #9; depth rule #5).
  - Stream the decompressed reader through `bombdefence.LimitedReader` (Q5) for cumulative rule #2 + ratio rule #3 enforcement.
  - Upload via `S3Uploader` port; record DynamoDB row via `Recorder` port (FR-5, idempotent per FR-5.3).
  - Apply retry policy (FR-12) via `Retrier` port — classifier-driven, 3 attempts max.
- After loop: compute status (SUCCESS / PARTIAL_FAILED / FAILED), write slipsheet via `SlipsheetWriter` port (FR-8.2 `slipsheets/` prefix).
- Enforce extraction hard timeout 240 s (rule #10) via `context.WithTimeout`.

**Public Interface**:
```go
package extraction

// Extractor is the port consumed by app.Service (driven by the SQS handler).
type Extractor interface {
    Process(ctx context.Context, msg ClaimCheck) (Outcome, error)
}

// Service implements Extractor.
type Service struct { /* unexported */ }
func New(deps Dependencies) *Service
func (s *Service) Process(ctx context.Context, msg ClaimCheck) (Outcome, error)

// Dependencies is the port injection point (Q1).
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
    Config          Config
}

// Ports — all consumer-defined interfaces (Q1).
type S3Downloader     interface { Download(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error) }
type S3Uploader       interface { Upload(ctx context.Context, bucket, key string, body io.Reader, sizeHint int64) error }
type Recorder         interface { RecordEntry(ctx context.Context, rec PipelineFile) error }
type SlipsheetWriter  interface { Write(ctx context.Context, ss Slipsheet) error }
type BombChecker      interface {
    PreCheck(meta ArchiveMetadata) error
    EntryCheck(entryIndex int, name string, decompressedSize int64) error
    NewLimitedReader(r io.Reader, capBytes int64, ratio float64) io.Reader
}
type PathValidator    interface { Sanitize(rawPath string) (safeName string, err error) }
type Retrier          interface { Do(ctx context.Context, op func(ctx context.Context) error) error }
type Clock            interface { Now() time.Time }

// Domain types
type ClaimCheck struct {
    PipelineExecutionID string
    TenantID            string
    DocumentID          string
    SourceBucket        string
    SourceKey           string
    CorrelationID       string
}
type Outcome struct {
    Status      Status // SUCCESS | PARTIAL_FAILED | FAILED
    EntryCount  int
    FailureCount int
    Reason      string // populated when FAILED
}
type Status int
const (
    StatusSuccess Status = iota
    StatusPartialFailed
    StatusFailed
)

// Error hierarchy (Q4)
type BombDefenceError struct {
    Rule   int    // 1..10
    Reason string
}
func (e *BombDefenceError) Error() string

type PathValidationError struct {
    Path   string
    Reason string
}
func (e *PathValidationError) Error() string

type UnsupportedFeatureError struct {
    Feature string // "encrypted-zip" | "multi-disk" | "deflate64"
}
func (e *UnsupportedFeatureError) Error() string

type TransientError struct {
    Cause error
    Class string // "throttling" | "5xx" | "timeout" | "network"
}
func (e *TransientError) Error() string
func (e *TransientError) Unwrap() error

type PermanentError struct {
    Cause error
}
func (e *PermanentError) Error() string
func (e *PermanentError) Unwrap() error
```

**Owns enforcement of**: FR-2, FR-3, FR-4 (orchestration), FR-10 (status), FR-11 (cleanup via `defer`), FR-12 (retry orchestration), SECURITY-05 (input validation flow), SECURITY-11 (security-critical orchestration centralised), SECURITY-15 (`defer` cleanup, fail-closed).

---

## 5. `internal/bombdefence`

**Type**: Pure security component — no I/O, no external dependencies.
**Purpose**: Implement the 10-point zip-bomb defence (FR-7) using deterministic checks plus the streaming `LimitedReader` (Q5).

**Responsibilities**:
- `PreCheck(meta)` — apply rules #1 (compressed-size) and #4 (entry-count) using archive metadata only.
- `EntryCheck(idx, name, decompressedSize)` — apply rules #5 (depth), #6 (symlink — caller signals via `name`-mode flag), #9 (single-file-size). Note rules #7 (absolute) and #8 (traversal) are delegated to `internal/validation` per Q8 separation.
- `NewLimitedReader(r, cap, ratio)` — returns an `io.Reader` that short-circuits with `*BombDefenceError{Rule: 2}` or `{Rule: 3}` the moment cumulative bytes exceed `cap` or the running compression ratio exceeds `ratio`.

**Public Interface**:
```go
package bombdefence

type Config struct {
    MaxCompressedSizeBytes   int64
    MaxExtractedSizeBytes    int64
    MaxCompressionRatio      float64
    MaxEntryCount            int
    MaxDirectoryDepth        int
    MaxSingleFileSizeBytes   int64
    MaxExtractionDurationSec int
}

type Checker struct { /* unexported */ }
func New(cfg Config) *Checker
func (c *Checker) PreCheck(meta extraction.ArchiveMetadata) error
func (c *Checker) EntryCheck(entryIndex int, name string, decompressedSize int64) error
func (c *Checker) NewLimitedReader(r io.Reader, compressedSize int64) io.Reader
```

**Owns enforcement of**: FR-7 (all 10 rules except #7, #8), Q5 (short-circuiting limiter).

---

## 6. `internal/validation`

**Type**: Pure security component — no I/O, no external dependencies (kept separate from `bombdefence` per Q8 / SECURITY-11).
**Purpose**: Entry-path safety and filename sanitisation (FR-6, rules #7 + #8 from FR-7).

**Responsibilities**:
- `Sanitize(rawPath)` — normalise the ZIP entry path, reject `..` segments, leading `/`, drive letters, control characters. Return the **base filename only** for use in the S3 key.
- Idempotent: `Sanitize(Sanitize(x)) == Sanitize(x)` for any valid `x` (PBT-04).

**Public Interface**:
```go
package validation

type PathValidator struct{ /* unexported */ }
func New() *PathValidator
// Sanitize returns the cleaned base filename or *extraction.PathValidationError.
func (v *PathValidator) Sanitize(rawPath string) (safeName string, err error)
```

**Owns enforcement of**: FR-6, FR-7 rules #7 + #8.

---

## 7. `internal/storage` (S3 Adapter)

**Type**: Adapter implementing `S3Downloader` and `S3Uploader` ports defined in `internal/extraction`.
**Purpose**: All S3 interactions — streaming `GetObject`, streaming `PutObject` with multipart for entries > 5 MiB (FR-4.3), MIME-type detection (Q6).

**Responsibilities**:
- `Download(ctx, bucket, key)` — return `io.ReadCloser` backed by `GetObject`. Caller is responsible for `Close`.
- `Upload(ctx, bucket, key, body, sizeHint)`:
  - For `sizeHint ≤ multipartThresholdBytes` (5 MiB default), use single `PutObject`.
  - For larger or unknown sizes, use `s3manager.Uploader` for automatic multipart (NFR-2.4).
  - Sniff MIME via `bufio.Peek(512)` + extension fallback (Q6) and attach as `ContentType`.
- All calls go through the configured `S3Client` interface (testable).

**Public Interface**:
```go
package storage

type Adapter struct{ /* unexported */ }
func NewAdapter(client S3Client, uploader S3Uploader, cfg Config) *Adapter

func (a *Adapter) Download(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error)
func (a *Adapter) Upload(ctx context.Context, bucket, key string, body io.Reader, sizeHint int64) error

type S3Client interface {
    GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
    PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}
type S3Uploader interface {
    Upload(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3manager.Uploader)) (*s3manager.UploadOutput, error)
}

type Config struct {
    MultipartThresholdBytes int64
}

// DetectMIME is exported for unit + property testing.
func DetectMIME(peek []byte, fileName string) string
```

**Owns enforcement of**: FR-4, FR-5 (mime field), Q6 (MIME detection).

---

## 8. `internal/dynamodb` (DynamoDB Adapter)

**Type**: Adapter implementing the `Recorder` port.
**Purpose**: Idempotent per-entry record writes to `pipeline_files`.

**Responsibilities**:
- `RecordEntry(ctx, rec)` — `PutItem` with `ConditionExpression: attribute_not_exists(pk)` for idempotency (FR-5.3).
- A conflict (`ConditionalCheckFailedException`) is NOT an error — the record already exists (re-delivery). Returns `nil`.
- Round-trip marshal/unmarshal via attribute-value mappers (PBT-02 round-trip property).

**Public Interface**:
```go
package dynamodb

type Adapter struct{ /* unexported */ }
func NewAdapter(client DDBClient, cfg Config) *Adapter
func (a *Adapter) RecordEntry(ctx context.Context, rec extraction.PipelineFile) error

type DDBClient interface {
    PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

type Config struct {
    TableName string
}

// Marshal/Unmarshal exported for round-trip PBT.
func Marshal(rec extraction.PipelineFile) (map[string]types.AttributeValue, error)
func Unmarshal(av map[string]types.AttributeValue) (extraction.PipelineFile, error)
```

**Owns enforcement of**: FR-5 (DynamoDB record), FR-5.3 (idempotency), PBT-02 (round-trip), PBT-04 (idempotence under at-least-once delivery).

---

## 9. `internal/slipsheet`

**Type**: Domain component — generates the slipsheet JSON; relies on the `S3Uploader` port to write it.
**Purpose**: Build the parent archive slipsheet (FR-8) and persist it at `slipsheets/{pipelineExecutionId}.json` (FR-8.2 / Q7 of requirements).

**Responsibilities**:
- `Build(execID, sourceArchive, status, entries)` — return a `Slipsheet` struct.
- `Write(ctx, ss)` — JSON-marshal and upload to `slipsheets/{ss.PipelineExecutionID}.json`.
- Round-trip safety: `Unmarshal(Marshal(s)) == s` (PBT-02).

**Public Interface**:
```go
package slipsheet

type Slipsheet struct {
    Type                string
    PipelineExecutionID string
    SourceArchive       string
    ChildCount          int
    Status              string // "SUCCESS" | "PARTIAL_FAILED" | "FAILED"
    Children            []ChildEntry
}
type ChildEntry struct {
    EntryIndex    int
    ChildKey      string
    Status        string  // "UPLOADED" | "FAILED"
    FailureReason string  // populated when FAILED
}

type Writer struct{ /* unexported */ }
func NewWriter(uploader extraction.S3Uploader, cfg Config) *Writer
func (w *Writer) Build(execID, sourceArchive string, status extraction.Status, entries []ChildEntry) Slipsheet
func (w *Writer) Write(ctx context.Context, ss Slipsheet) error

type Config struct {
    BucketName   string
    KeyPrefix    string // "slipsheets/"
}

// Round-trip helpers exported for PBT-02.
func Marshal(ss Slipsheet) ([]byte, error)
func Unmarshal(b []byte) (Slipsheet, error)
```

**Owns enforcement of**: FR-8 (slipsheet), PBT-02.

---

## 10. `internal/retry`

**Type**: Pure helper component — no I/O.
**Purpose**: Classifier-driven retry with exponential backoff + jitter (FR-12, Q12, PBT-05 oracle property).

**Responsibilities**:
- `Do(ctx, op)` — invoke `op`; on returned `*extraction.TransientError`, wait `base * factor^n * (1 ± jitter)` and retry up to `maxAttempts`.
- Never retries on `*BombDefenceError`, `*PathValidationError`, `*UnsupportedFeatureError`, `*PermanentError`, or `ctx.Done`.
- Backoff sequence is deterministic for a fixed seed — testable via PBT-05 oracle (closed-form formula).

**Public Interface**:
```go
package retry

type Config struct {
    MaxAttempts       int
    BackoffBaseMillis int
    BackoffFactor     float64
    JitterFraction    float64
}

type Retrier struct{ /* unexported */ }
func New(cfg Config, clock extraction.Clock, rng *rand.Rand, logger extraction.Logger) *Retrier
func (r *Retrier) Do(ctx context.Context, op func(ctx context.Context) error) error

// BackoffFor is exported for PBT-05 oracle comparison.
func BackoffFor(attempt int, cfg Config, jitter float64) time.Duration

// Classify is exported for unit testing of error classification.
func Classify(err error) (transient bool, class string)
```

**Owns enforcement of**: FR-12, Q12, PBT-05.

---

## 11. `internal/metrics`

**Type**: Adapter implementing the `Metrics` port.
**Purpose**: Construct and register Prometheus collectors per FR-13.2.

**Responsibilities**:
- Register all 6 metrics from FR-13.2 on the default `prometheus.Registerer`.
- Expose a typed API to the rest of the codebase (no string-keyed metric lookups).

**Public Interface**:
```go
package metrics

type Metrics struct{ /* unexported */ }
func New(reg prometheus.Registerer) *Metrics

func (m *Metrics) EntryProcessed(status string)              // zip_entries_total{status}
func (m *Metrics) ExtractionDuration(d time.Duration, outcome string) // zip_extraction_duration_seconds{outcome}
func (m *Metrics) ExtractionFailure(reason string)           // zip_extraction_failures_total{reason}
func (m *Metrics) BombRejection(rule int)                    // zip_bomb_rejections_total{rule}
func (m *Metrics) BytesExtracted(n int64)                    // extracted_bytes_total
func (m *Metrics) PartialFailure()                           // partial_failures_total
```

**Owns enforcement of**: FR-13.2, SECURITY-14 (alerting hooks).

---

## 12. `internal/health` (HTTP Operational Server)

**Type**: Adapter component.
**Purpose**: Serve `/healthz/live`, `/healthz/ready`, and `/metrics` on a single port (Q3 → 8080).

**Responsibilities**:
- `/healthz/live` — always 200 OK once the process is up.
- `/healthz/ready` — 200 OK only once `HealthGate.Ready()` returns true (set by `app.Service` after AWS clients pass startup health checks).
- `/metrics` — Prometheus exposition format via `promhttp.Handler()`.
- Graceful shutdown via `http.Server.Shutdown(ctx)` on root-context cancellation.

**Public Interface**:
```go
package health

type Server struct{ /* unexported */ }
func NewServer(port int, gate HealthGate) *Server
func (s *Server) Start(ctx context.Context) error  // blocks until ctx done
func (s *Server) Shutdown(ctx context.Context) error

type HealthGate interface {
    SetReady(ready bool)
    Ready() bool
}

type Gate struct{ /* unexported */ }
func NewGate() *Gate
```

**Owns enforcement of**: FR-13.1, FR-13.2, SECURITY-09 (no debug routes, no stack-trace leakage).

---

## 13. `internal/config`

**Type**: Pure infrastructure component — no AWS dependencies.
**Purpose**: Load + validate env + YAML configuration (FR-14, NFR-7).

**Responsibilities**:
- `Load()` — read env vars (queue URL, bucket name, table name, region, endpoint override, log format, http port) AND parse the YAML file at `CONFIG_PATH`.
- Validate: required env vars present, YAML schema strict (no extra keys, all keys typed correctly), limits within sane bounds (e.g., `maxAttempts > 0`).
- Fail-fast on any validation error — `main` exits non-zero (NFR-2.x of SECURITY-15 fail-closed).

**Public Interface**:
```go
package config

type Config struct {
    Infra        InfraConfig
    BombDefence  bombdefence.Config
    Streaming    StreamingConfig
    Retry        retry.Config
    SQS          SQSConfig
    HTTP         HTTPConfig
    Logging      LoggingConfig
}
type InfraConfig struct {
    QueueURL          string
    StagingBucket     string
    DynamoTable       string
    AWSRegion         string
    AWSEndpointURL    string // empty in prod; "http://localstack:4566" locally
}
type StreamingConfig struct {
    MaxInMemoryBufferBytes  int64
    MultipartThresholdBytes int64
}
type SQSConfig struct {
    HeartbeatIntervalSec   int
    MaxInFlight            int
    GracefulShutdownTimeoutSec int  // Q7: drain timeout, default 250
}
type HTTPConfig struct {
    Port int
}
type LoggingConfig struct {
    Format string // "json" | "console"
    Level  string // "info" | "debug" | "warn" | "error"
}

func Load() (Config, error)
func (c Config) Validate() error
```

**Owns enforcement of**: FR-14, NFR-7, SECURITY-15 (fail-closed validation).

---

## 14. `internal/awsclients`

**Type**: Adapter factory.
**Purpose**: Construct AWS SDK v2 clients (SQS, S3, DynamoDB) with consistent configuration: TLS, region, endpoint override, default retry mode set to `aws.RetryModeAdaptive` (the SDK retries are then layered under `internal/retry`'s extraction-level classifier).

**Responsibilities**:
- Build a single `aws.Config` from env vars; apply `WithEndpointResolverWithOptions` if `AWS_ENDPOINT_URL` is set (FR-15.1).
- Provide typed constructors for each client.

**Public Interface**:
```go
package awsclients

type Set struct {
    SQS *sqs.Client
    S3  *s3.Client
    DDB *dynamodb.Client
    S3Uploader *s3manager.Uploader
}

func Build(ctx context.Context, cfg config.InfraConfig) (Set, error)
```

**Owns enforcement of**: FR-15.1 (LocalStack endpoint override), SECURITY-01 (TLS — default SDK behaviour with no insecure override).

---

## 15. `internal/log`

**Type**: Logger factory.
**Purpose**: Construct the `*zap.Logger` honouring `LOG_FORMAT` (Q10).

**Responsibilities**:
- `LOG_FORMAT=json` → `zap.NewProductionConfig()`.
- `LOG_FORMAT=console` → `zap.NewDevelopmentConfig()` with colour and human-readable timestamps.
- Apply a global field set: `service=zip-extraction`, `version=$(GIT_SHA)`.
- Provide a `WithFields(ctx, fields...)` helper so handlers append `pipelineExecutionId` / `correlationId` / `documentId` consistently (NFR-5.1).
- Implement a no-secret-logging policy — the helper rejects field keys matching a deny-list (`password`, `token`, `credential`, `aws_access_key_id`, …) at compile time via a `cmd/lint` check + at runtime via a logger wrapper.

**Public Interface**:
```go
package log

// Logger is the consumer-defined port (Q1) consumed by extraction, sqs, etc.
type Logger interface {
    Info(msg string, fields ...Field)
    Warn(msg string, fields ...Field)
    Error(msg string, fields ...Field)
    Debug(msg string, fields ...Field)
    With(fields ...Field) Logger
}

type Field = zap.Field

func New(cfg config.LoggingConfig, version string) (Logger, error)
```

**Owns enforcement of**: NFR-5.1, NFR-5.3, SECURITY-03 (no secrets / PII in logs).

---

## Component Inventory Summary

| # | Package | Type | SECURITY rules owned | PBT rules owned |
|---|---|---|---|---|
| 1 | `cmd/zip-extraction` | Entry point | 09, 15 | — |
| 2 | `internal/app` | Orchestrator | 11 | — |
| 3 | `internal/sqs` | Adapter | — | 06 (heartbeat stateful) |
| 4 | `internal/extraction` | Orchestrator | 05, 11, 15 | 01, 06, 10 |
| 5 | `internal/bombdefence` | Security pure | 11 | 03 (invariants), 05 (oracle) |
| 6 | `internal/validation` | Security pure | 11 | 03, 04 (idempotence) |
| 7 | `internal/storage` | Adapter | 01 (TLS) | — |
| 8 | `internal/dynamodb` | Adapter | 01 | 02 (round-trip), 04 (idempotence) |
| 9 | `internal/slipsheet` | Domain | — | 02 (round-trip) |
| 10 | `internal/retry` | Pure helper | — | 05 (oracle backoff) |
| 11 | `internal/metrics` | Adapter | 14 | — |
| 12 | `internal/health` | Adapter | 09 | — |
| 13 | `internal/config` | Pure infra | 15 | — |
| 14 | `internal/awsclients` | Adapter factory | 01 (TLS) | — |
| 15 | `internal/log` | Logger factory | 03 | — |

Detailed method signatures appear in `component-methods.md`.
Service-layer orchestration and shutdown sequence appear in `services.md`.
Dependency relationships and data-flow appear in `component-dependency.md`.
