# NFR Design Patterns — zip-extraction (UOW-SVC-12)

**Document Type**: NFR Implementation Patterns
**Phase**: CONSTRUCTION — NFR Design (Part 2: Generation)
**Generated**: 2026-05-24
**Unit**: `zip-extraction` (UOW-SVC-12)

This document records the **concrete implementation patterns** the service uses to satisfy each non-functional requirement (`NFR-Z-NNN`). Patterns are grouped by quality attribute (Resilience / Scalability / Performance / Security / Observability). For each pattern, the document records:

- **Pattern name** and one-line summary
- **NFR-Z source** the pattern satisfies
- **Implementation locus** (Go package + key types / functions; SDK option; Helm value)
- **Configurable parameters** (env vars / YAML keys / Helm values)
- **Anti-patterns avoided** (what we explicitly do NOT do, with rationale)
- **Cross-references** (related patterns, business rules, SECURITY / PBT)

---

## 1. Resilience Patterns

### 1.1 Classifier-driven retry with exponential backoff + jitter
**Summary**: AWS SDK errors are classified into typed errors; only `*TransientError` is retried, up to 3 attempts with exponential backoff and uniform jitter.
**NFR-Z source**: NFR-Z-030, NFR-Z-031
**Implementation locus**: `internal/retry.Retrier.Do` + `internal/retry.Classify`. Wraps every per-entry S3/DDB call inside `extraction.Service.processEntry`.
**Configurable parameters** (YAML `retry:`):
- `maxAttempts: 3`
- `backoffBaseMillis: 200`
- `backoffFactor: 2.0`
- `jitterFraction: 0.25`
**Anti-patterns avoided**:
- ❌ Retry every error blindly — bombs would be re-attempted (waste + DLQ pollution).
- ❌ Sleep without jitter — synchronised retry storms across pods.
- ❌ Unbounded retry — workers can starve other in-flight messages indefinitely.
**Cross-references**: BR-RETRY-001..014, PBT-05 oracle (`BackoffFor`).

### 1.2 Per-message SQS visibility heartbeat
**Summary**: Each in-flight message has a dedicated goroutine extending visibility every 30 s to 300 s (BR-HEARTBEAT-002).
**NFR-Z source**: NFR-Z-034
**Implementation locus**: `internal/sqs.heartbeater.Start(ctx, receiptHandle)` returning a `cancel func()`.
**Configurable parameters** (YAML `sqs.heartbeatIntervalSec: 30`).
**Anti-patterns avoided**:
- ❌ Single shared ticker for all messages — coupling failures across unrelated workers.
- ❌ Cumulative-add visibility — risk of unbounded growth on cancellation bugs.
- ❌ No heartbeat — 60 s margin between 240 s extraction limit and 300 s visibility evaporates under GC pauses.
**Cross-references**: FR-9, Q6 of requirements, BR-HEARTBEAT-*.

### 1.3 Graceful drain with idempotent fallback
**Summary**: SIGTERM cancels root context; receive-loop stops, in-flight workers complete within 250 s; deadline-exceeded messages fall back to SQS visibility expiry + idempotent redelivery (no data loss).
**NFR-Z source**: NFR-Z-022, NFR-Z-033
**Implementation locus**: `internal/app.Service.gracefulDrain`. Idempotency contract: `internal/dynamodb.Adapter.RecordEntry` conditional PutItem; deterministic S3 keys from `(pipelineExecutionId, entryIndex, safeName)`.
**Configurable parameters** (YAML `sqs.gracefulShutdownTimeoutSec: 250`; Helm `terminationGracePeriodSeconds: 270`).
**Anti-patterns avoided**:
- ❌ Hard-cancel on SIGTERM — causes avoidable rework.
- ❌ Visibility-reset-to-zero on drain — duplicate processing.
- ❌ No idempotency contract — drain-deadline-hit messages would corrupt state on redelivery.
**Cross-references**: Q7 of application design, BR-DRAIN-*, BR-IDEMPOTENCY-*.

### 1.4 Deterministic idempotency key
**Summary**: The idempotency key is the pair `(pipelineExecutionId, entryIndex)`. The DDB sort key, the S3 object key, and the slipsheet ChildEntry order are all deterministic functions of this pair.
**NFR-Z source**: NFR-Z-033
**Implementation locus**: `internal/extraction.processEntry` constructs the S3 key as `input/{execId}/{idx:04d}-{safeName}`. `internal/dynamodb.Adapter.RecordEntry` constructs the SK as `FILE#{idx:04d}`.
**Configurable parameters**: none — derived from inputs only.
**Anti-patterns avoided**:
- ❌ Random-UUID-per-attempt keys — would not deduplicate under redelivery.
- ❌ Pre-attempt content hash — requires reading the entry twice (violates streaming).
- ❌ Time-based keys — non-deterministic; breaks redelivery deduplication.
**Cross-references**: FR-5.3, Q2 of functional design, BR-IDEMPOTENCY-*.

### 1.5 Fail-closed exception handling
**Summary**: Every external call has explicit error handling. Every typed error has a defined classification (retry / non-retry / archive-abort / drain). Worker top-level `recover` logs FATAL and lets SQS redeliver. NO error path silently succeeds.
**NFR-Z source**: NFR-Z-050
**Implementation locus**: `internal/extraction` typed-error hierarchy; `internal/retry.Classify`; `cmd/zip-extraction/main.go` top-level `defer recover()`; `internal/sqs.Adapter.dispatch` per-worker recover.
**Configurable parameters**: none.
**Anti-patterns avoided**:
- ❌ `_ = client.PutObject(...)` — silent failures hide bugs.
- ❌ Bare `panic` propagation across goroutines — process crash without context.
- ❌ Default "success on unknown error" semantics — security regression.
**Cross-references**: SECURITY-15, BR-RETRY-*, BR-CLEAN-*.

### 1.6 No explicit circuit breaker (Q1 decision)
**Summary**: AWS SDK v2 adaptive retry (`aws.RetryModeAdaptive`) + application-level classifier-driven 3-attempt retry + worker-pool semaphore bounding provide sufficient backpressure under chronic AWS service failures. No additional circuit-breaker library is wired in.
**NFR-Z source**: NFR-Z-030 (sufficient via existing mechanisms)
**Implementation locus**: SDK option set in `internal/awsclients.Build`: `aws.NewConfig().WithRetryMode(aws.RetryModeAdaptive)`.
**Configurable parameters**: `aws.NewConfig().WithRetryMaxAttempts(3)` — same value as application retry.
**Anti-patterns avoided**:
- ❌ Adding a separate breaker on top of SDK adaptive retry — two mechanisms competing for the same job (coordination hazard).
- ❌ Breaker without state monitoring — silent open-state = silent outage.
**Cross-references**: Q1 of NFR design plan.

---

## 2. Scalability Patterns

### 2.1 Bounded worker pool (semaphore backpressure)
**Summary**: A single SQS long-poll receiver dispatches messages into a buffered channel of size `maxInFlight`. When the channel is full, the next `ReceiveMessage` call doesn't issue until a worker completes.
**NFR-Z source**: NFR-Z-001, NFR-Z-014
**Implementation locus**: `internal/sqs.Adapter.Run`. Channel: `slots := make(chan struct{}, cfg.MaxInFlight)`. Pattern: `slots <- struct{}{}` before dispatch, `<-slots` on worker completion.
**Configurable parameters** (YAML `sqs.maxInFlight: 5`).
**Anti-patterns avoided**:
- ❌ Unbounded goroutine spawning — instant OOM under burst.
- ❌ Spawn N receiver goroutines (each pulling 1 message) — wastes SQS API calls, complicates rate-limiting.
- ❌ Single-threaded sequential — under-utilises pod, inflates queue latency.
**Cross-references**: Q2 of application design, Q5 of NFR design.

### 2.2 HPA on SQS depth (KEDA scaler)
**Summary**: Pod count scales between 2 and 10 driven by `ApproximateNumberOfMessagesVisible / maxInFlight` target ≈ 1. Documented in chart README; platform team renders KEDA `ScaledObject` or equivalent.
**NFR-Z source**: NFR-Z-002
**Implementation locus**: Chart README (this repo doesn't render the KEDA manifest per Q9 of requirements — platform team scope).
**Configurable parameters** (chart README guidance): `minReplicaCount: 2`, `maxReplicaCount: 10`, `scaleTargetRef: zip-extraction Deployment`, `triggers: aws-sqs-queue`.
**Anti-patterns avoided**:
- ❌ HPA on CPU only — meaningless for an I/O-bound consumer.
- ❌ Manual replica management — operational toil and lagged response to load.
**Cross-references**: Q2 of NFR plan, NFR-Z-002.

### 2.3 SDK adaptive retry (rate limiting delegated to SDK)
**Summary**: `aws.RetryModeAdaptive` provides client-side rate limiting based on observed throttling. No additional application-level rate limiter is wired in (Q2 decision).
**NFR-Z source**: implicit via NFR-Z-031 (no own-side rate limiter)
**Implementation locus**: `internal/awsclients.Build` sets `aws.RetryModeAdaptive` on the config.
**Configurable parameters**: SDK-side; not exposed in service config.
**Anti-patterns avoided**:
- ❌ Token-bucket limiter on top of SDK adaptive — duplicate mechanism.
- ❌ No rate limiting at all + aggressive retry — risk of throttle-amplification cascade.
**Cross-references**: Q2 of NFR design plan.

### 2.4 Stateless service + multi-AZ pod spread
**Summary**: The pod stores no persistent state. `topologySpreadConstraints` (chart README guidance) distributes replicas across the cluster's AZs with `maxSkew: 1`.
**NFR-Z source**: NFR-Z-004, NFR-Z-020, NFR-Z-023
**Implementation locus**: Chart `values.yaml` `topologySpreadConstraints` block (commented as platform-team-tunable).
**Configurable parameters**: `topologyKey: topology.kubernetes.io/zone`, `whenUnsatisfiable: ScheduleAnyway`.
**Anti-patterns avoided**:
- ❌ Pod affinity to specific node — fragility under node-failure.
- ❌ Strict anti-affinity required — scheduling friction at small cluster sizes.
**Cross-references**: Q5 of NFR plan, §27 input spec.

### 2.5 No bulkhead / uniform pool (Q7 decision)
**Summary**: Single worker pool processes all archive sizes uniformly. Worst-case starvation bounded by bomb-defence rule #10 (240 s extraction timeout) — a "stuck" archive can hold one slot for at most 240 s.
**NFR-Z source**: implicit via NFR-Z-001 (sufficient with current concurrency tier)
**Implementation locus**: `internal/sqs.Adapter` uses single `slots` channel.
**Configurable parameters**: none (would require code changes to partition).
**Anti-patterns avoided**:
- ❌ Per-size-class pools — requires routing decision before metadata read; complex bookkeeping; no measurable starvation problem at our scale.
**Cross-references**: Q7 of NFR design plan.

---

## 3. Performance Patterns

### 3.1 Streaming I/O end-to-end
**Summary**: The decompressed entry stream flows through `io.Reader` composition: `*zip.File → zip.Open() → bombdefence.LimitedReader → bufio.Reader(peek 512) → io.MultiReader → s3manager.Uploader`. No code path materialises the full entry in memory or on disk.
**NFR-Z source**: NFR-Z-014, NFR-Z-013
**Implementation locus**: `internal/extraction.processEntry`. Linter check forbids `io.ReadAll` / `os.ReadFile` / `ioutil.ReadAll` / `bytes.Buffer.ReadFrom` of archive or entry streams.
**Configurable parameters** (YAML `streaming.maxInMemoryBufferBytes: 4194304` (4 MiB)).
**Anti-patterns avoided**:
- ❌ Full archive decode to memory — instant OOM on large archives (memory budget is 128 MiB).
- ❌ Disk staging — violates §9 streaming constraint + adds I/O latency + invites cleanup bugs.
- ❌ Per-entry `bytes.Buffer` materialisation — defeats streaming benefit on individual entries.
**Cross-references**: NFR-3 of requirements, §9 input spec.

### 3.2 Short-circuiting LimitedReader (bomb defence + invariant)
**Summary**: A custom `io.Reader` wrapper maintains cumulative byte counters and returns `(0, *BombDefenceError{Rule: 2 | 3})` the **moment** a threshold is exceeded — i.e., even mid-block reads can return an error.
**NFR-Z source**: NFR-Z-014; BR-BOMB-003 / BR-BOMB-004
**Implementation locus**: `internal/bombdefence.NewLimitedReader(r, compressedSize)` returns an `io.Reader` implementation.
**Configurable parameters** (YAML `bombDefence.maxExtractedSizeBytes: 2147483648` (2 GB), `bombDefence.maxCompressionRatio: 1000`).
**Anti-patterns avoided**:
- ❌ Post-hoc check after full decompress — defeats bomb-defence purpose.
- ❌ `io.TeeReader → bytes.Buffer` — wastes memory + still post-hoc.
**Cross-references**: Q5 of application design, BR-BOMB-003 / 004 (PBT-03 invariant property).

### 3.3 S3 multipart-aware upload
**Summary**: Single `PutObject` for entries ≤ 5 MiB; `s3manager.Uploader` for larger or unknown size. Multipart parallelism and part size are SDK defaults (concurrency 5 within the uploader, 5 MiB part size).
**NFR-Z source**: NFR-Z-013, FR-4.3
**Implementation locus**: `internal/storage.Adapter.Upload` branches on `sizeHint` vs `MultipartThresholdBytes`.
**Configurable parameters** (YAML `streaming.multipartThresholdBytes: 5242880` (5 MiB)).
**Anti-patterns avoided**:
- ❌ Always single `PutObject` — fails for entries > 5 GiB (S3 single-object limit; though we cap entries at 250 MB so technically fine, multipart is still faster).
- ❌ Always multipart — overhead for tiny entries.
**Cross-references**: FR-4.3, NFR-2.4.

### 3.4 Singleton AWS clients (connection-pool reuse)
**Summary**: One `sqs.Client`, one `s3.Client`, one `dynamodb.Client`, one `s3manager.Uploader` per pod, constructed once in `main` and injected everywhere. Shares the underlying HTTPS connection pool across all goroutines.
**NFR-Z source**: NFR-Z-013 (sustained throughput target)
**Implementation locus**: `internal/awsclients.Build(ctx, cfg.Infra) → Set{SQS, S3, DDB, S3Uploader}`. Set is injected into adapters via `cmd/zip-extraction/main.go` wiring.
**Configurable parameters**: none.
**Anti-patterns avoided**:
- ❌ Per-worker clients — wastes TLS handshake cost; underutilises connection pool.
- ❌ Per-request client construction — catastrophic latency.
**Cross-references**: Q3 of NFR design plan.

### 3.5 Hybrid MIME detection (no extra read pass)
**Summary**: MIME is detected via `bufio.NewReader(r).Peek(512)` rebuilt back into the stream via `io.MultiReader(bytes.NewReader(peek), bufReader)`. Falls back to `mime.TypeByExtension(filepath.Ext(name))` when sniff returns `application/octet-stream`.
**NFR-Z source**: NFR-Z-013 (no read-pass overhead)
**Implementation locus**: `internal/storage.DetectMIME(peek, fileName)` + `internal/storage.peekReader(r, 512)`.
**Configurable parameters**: none.
**Anti-patterns avoided**:
- ❌ Re-read from S3 to sniff — doubles network cost.
- ❌ Sniff via `bytes.Buffer` — adds memory pressure for very-small entries.
**Cross-references**: Q6 of application design, BR-MIME-001.

---

## 4. Security Patterns

### 4.1 IRSA + least-privilege IAM
**Summary**: Pod authenticates to AWS via IRSA (`AWS_ROLE_ARN` + `AWS_WEB_IDENTITY_TOKEN_FILE` injected by EKS Pod Identity webhook). The IRSA role policy is **scoped to specific resource ARNs with no wildcards**.
**NFR-Z source**: NFR-Z-044, NFR-Z-049
**Implementation locus**: Helm chart `serviceaccount.yaml` with annotation `eks.amazonaws.com/role-arn: <role-arn>`. Service code does no auth handling — SDK auto-discovers IRSA credentials.
**Configurable parameters** (Helm `serviceAccount.roleArn`, `serviceAccount.create: true`).
**Anti-patterns avoided**:
- ❌ Static AWS access keys in Helm values or env vars — credential leakage risk.
- ❌ Wildcard IAM resources (`s3:*` on `*`) — broad blast radius.
- ❌ Inline policies in code — opaque, hard to audit.
**Cross-references**: SECURITY-06, SECURITY-12, NFR-Z-044 explicit action list.

### 4.2 SSE-S3 default + SSE-KMS opt-in (Q6 decision)
**Summary**: Server-side encryption mode selected via Helm `values.yaml`:
```yaml
sse:
  mode: "SSE-S3"                                # default
  # mode: "SSE-KMS"
  # kmsKeyId: "arn:aws:kms:eu-west-1:<acct>:key/<id>"
```
The Helm chart renders the IRSA policy with `kms:Decrypt` + `kms:GenerateDataKey` actions **only** when `sse.mode == "SSE-KMS"`. Service code passes `ServerSideEncryption: aws.String("aws:kms")` and `SSEKMSKeyId: aws.String(cfg.KMSKeyID)` only in KMS mode.
**NFR-Z source**: NFR-Z-040, NFR-Z-041
**Implementation locus**: Helm chart `templates/serviceaccount.yaml` conditional; `internal/storage.Adapter.Upload` reads `cfg.SSE`.
**Configurable parameters**: Helm `sse.mode`, `sse.kmsKeyId`.
**Anti-patterns avoided**:
- ❌ Hard-code SSE-S3 only — no path to SSE-KMS without code change.
- ❌ Always include `kms:` in IRSA — least-privilege violation in SSE-S3 environments.
**Cross-references**: Q6 of NFR design plan, SECURITY-01.

### 4.3 TLS 1.2+ via SDK defaults
**Summary**: AWS SDK v2 enforces HTTPS by default. No code path passes `WithDisableSSL`. The only legitimate `http://` endpoint is LocalStack (`AWS_ENDPOINT_URL=http://localstack:4566`) for local dev.
**NFR-Z source**: NFR-Z-040
**Implementation locus**: `internal/awsclients.Build` — no `WithDisableSSL` option set.
**Configurable parameters**: none.
**Anti-patterns avoided**:
- ❌ `WithDisableSSL` to "work around" TLS issues — masks misconfigurations.
- ❌ Custom HTTPS transport bypassing SDK middleware — error-prone.
**Cross-references**: SECURITY-01.

### 4.4 Structured logging with sensitive-field redaction
**Summary**: All logging goes through `internal/log.Logger`, a thin wrapper around `*zap.Logger` that filters outbound fields whose key matches (case-insensitive) the deny-list (`password`, `secret`, `token`, `credential`, `aws_access_key_id`, …). Matched fields' values are replaced with `[REDACTED]`.
**NFR-Z source**: NFR-Z-042
**Implementation locus**: `internal/log.zapLogger.With/Info/Warn/Error/Debug`.
**Configurable parameters**: deny-list is hardcoded; extensible via constant array.
**Anti-patterns avoided**:
- ❌ Logging `cfg` struct as a whole (would log every key including future secrets).
- ❌ Inline `fmt.Sprintf("%+v", obj)` — bypasses deny-list filter.
**Cross-references**: SECURITY-03, BR-LOG-002 (PBT-03 invariant property).

### 4.5 Distroless non-root container with read-only root FS
**Summary**: Multi-stage Dockerfile produces a final image of `gcr.io/distroless/static-debian12:nonroot@sha256:<digest>` running as UID 65532. Root filesystem mounted read-only; `/tmp` mounted as `emptyDir`.
**NFR-Z source**: NFR-Z-046
**Implementation locus**: `Dockerfile` (multi-stage); Helm chart `templates/deployment.yaml` with `securityContext.readOnlyRootFilesystem: true` + `emptyDir` for `/tmp`.
**Configurable parameters**: Helm `image.repository`, `image.digest`. Both pinned by digest.
**Anti-patterns avoided**:
- ❌ Alpine / Ubuntu base — larger attack surface.
- ❌ Root user — privilege escalation risk.
- ❌ Writable root FS — malware persistence surface.
- ❌ `image.tag: latest` — supply-chain integrity violation (SECURITY-10).
**Cross-references**: SECURITY-09, SECURITY-10, §25 input spec.

### 4.6 No pprof endpoint; SIGUSR1 → heap-dump file (Q4 decision)
**Summary**: No `net/http/pprof` route is registered. Emergency profiling is via a SIGUSR1 handler that calls `runtime/pprof.WriteHeapProfile(file)` to `/tmp/heap-<RFC3339>.pprof`. Operator retrieves the file via `kubectl cp` after triggering with `kubectl exec -- kill -USR1 1`.
**NFR-Z source**: NFR-Z-046, NFR-Z-090 (parity)
**Implementation locus**: `cmd/zip-extraction/main.go` installs `signal.Notify(c, syscall.SIGUSR1)` handler.
**Configurable parameters**: none (always on; produces file only on signal).
**Anti-patterns avoided**:
- ❌ `/debug/pprof/*` routes — SECURITY-09 hardening violation.
- ❌ Env-gated pprof — branches code on environment (NFR-Z-090 parity violation).
- ❌ Off-by-default pprof that ops "just enables temporarily" — muscle-memory risk.
**Cross-references**: Q4 of NFR design plan, SECURITY-09.

### 4.7 Schema-validated config + strict YAML decode
**Summary**: Env vars and YAML config loaded by `internal/config.Load()` go through `yaml.NewDecoder(r).KnownFields(true)` (rejects unknown keys) and a `Config.Validate()` method that range-checks every numeric (e.g., `MaxAttempts > 0`, `MaxExtractionDurationSec > 0`, `MultipartThresholdBytes >= 5 MiB`). Any failure → fail-fast exit non-zero.
**NFR-Z source**: NFR-Z-050, NFR-Z-014
**Implementation locus**: `internal/config.Load + Validate`. Called by `cmd/zip-extraction/main.go` at startup.
**Configurable parameters**: schema is the configuration; bounds are constants in `config.go`.
**Anti-patterns avoided**:
- ❌ Permissive YAML decoder — accepts typos like `maxAttempt` silently.
- ❌ Validation only at first use — late failures vs fail-fast startup.
**Cross-references**: FR-14.3, FR-14.4, SECURITY-15.

---

## 5. Observability Patterns

### 5.1 Prometheus collector taxonomy
**Summary**: Six business metrics from FR-13.2 plus two operational counters (`redelivery_skips_total`, `slipsheet_write_failures_total`), all registered on `prometheus.DefaultRegisterer` at startup and served via `promhttp.Handler()` on `/metrics`.
**NFR-Z source**: NFR-Z-060
**Implementation locus**: `internal/metrics.New(reg) → *Metrics` with typed methods (`EntryProcessed`, `ExtractionDuration`, `ExtractionFailure`, `BombRejection`, `BytesExtracted`, `PartialFailure`, `RedeliverySkip`, `SlipsheetWriteFailure`).
**Configurable parameters**: histogram buckets for `zip_extraction_duration_seconds`: `[1, 5, 15, 30, 60, 120, 180, 220, 240]` seconds.
**Anti-patterns avoided**:
- ❌ Free-form metric names looked up by string — typos hidden until production.
- ❌ Unbounded label cardinality (e.g., `pipelineExecutionId` as a label) — Prometheus OOM.
**Cross-references**: FR-13.2, SECURITY-14, NFR-Z-060.

### 5.2 Structured log field discipline
**Summary**: Every log entry binds `service`, `version`, `timestamp`, `level`, `message`. Per-message handlers `Logger.With(zap.String("pipelineExecutionId", …), zap.String("correlationId", …), zap.String("documentId", …))` so all entries within a message processing share consistent fields for cross-event correlation.
**NFR-Z source**: NFR-Z-042, NFR-Z-063 (trace-correlation-via-correlationId)
**Implementation locus**: `internal/log.Logger.With` returns a child logger with bound fields. Used at every handler entry.
**Configurable parameters** (YAML / env `LOG_FORMAT: "json" | "console"`, `LOG_LEVEL: "info" | "debug" | "warn" | "error"`).
**Anti-patterns avoided**:
- ❌ `log.Printf` with format strings — defeats grep / query / alert.
- ❌ Inconsistent field names across packages (`exec_id` vs `executionId` vs `eid`).
**Cross-references**: SECURITY-03, NFR-5.1 of requirements.

### 5.3 Health-probe semantics: ready=false during drain
**Summary**: `/healthz/live` returns 200 OK once the process is alive. `/healthz/ready` returns 200 only if `HealthGate.Ready() == true`, which is flipped to `true` after startup health checks pass AND flipped back to `false` immediately on SIGTERM (before drain wait).
**NFR-Z source**: NFR-Z-061, NFR-Z-022
**Implementation locus**: `internal/health.Gate.SetReady/Ready` (atomic); HTTP handlers in `internal/health.Server`.
**Configurable parameters**: none.
**Anti-patterns avoided**:
- ❌ Liveness probe that checks readiness — Kubernetes restarts pods that just need to drain.
- ❌ Static "always ready" probes — failed startup goes undetected.
- ❌ Same probe for liveness and readiness — defeats Kubernetes' two-mode probe model.
**Cross-references**: FR-13.1, Q3 of application design.

### 5.4 Recommended Prometheus alert rules
**Summary**: Chart README documents 5 platform-team-consumable alert rules covering SLO violation, P99 latency, DLQ depth, bomb-rejection spike, and redelivery spike. The repository does NOT render the rules itself (per Q9 of requirements — platform team scope) but documents the PromQL.
**NFR-Z source**: NFR-Z-062
**Implementation locus**: `services/zip-extraction/chart/README.md` recommendations section.
**Configurable parameters**: thresholds documented as starting values; platform team tunes per environment.
**Anti-patterns avoided**:
- ❌ No documented alerts — operational dark spot.
- ❌ Hard-coded alert rules in the chart — overrides platform-team conventions.
**Cross-references**: SECURITY-14, NFR-Z-062.

### 5.5 No distributed tracing in v1 (scope decision)
**Summary**: This unit does NOT emit OpenTelemetry traces. Cross-service correlation is achieved via the `correlationId` field in structured logs. Future enhancement (out of scope): OTel SDK + Jaeger exporter.
**NFR-Z source**: NFR-Z-063
**Implementation locus**: N/A.
**Configurable parameters**: N/A.
**Anti-patterns avoided**:
- ❌ Half-implemented OTel that emits no spans — wasted dependency.
**Cross-references**: NFR-Z-063 explicit scope.

---

## 6. Maintainability Patterns

### 6.1 Pinned-dependency build chain
**Summary**: Every external dependency pinned at the strongest layer available — Go modules via `go.sum`, Docker base images by digest, GitHub Actions by full SHA, Helm chart version-locked, LocalStack image digest-pinned, CLI tools (golangci-lint, govulncheck, syft) version-pinned in CI workflow or `tools/go.mod`.
**NFR-Z source**: NFR-Z-047
**Implementation locus**: `go.mod` + `go.sum`, `Dockerfile`, `.github/workflows/ci.yml`, `Chart.yaml`, `dependabot.yml`, `renovate.json`.
**Configurable parameters**: dependabot config defines update cadence; humans review each PR.
**Anti-patterns avoided**:
- ❌ `image: golang:1.24` — floats over re-tagged digests.
- ❌ `uses: actions/checkout@v4` — float over re-tagged actions.
- ❌ Unsigned dependencies — supply-chain integrity gap.
**Cross-references**: SECURITY-10, SECURITY-13.

### 6.2 CI quality gates
**Summary**: CI runs (in order) `go build`, `golangci-lint run`, `go test -race -cover ./...`, `govulncheck ./...`, then `syft .` on release tags. Build fails if any gate fails (no soft-fail).
**NFR-Z source**: NFR-Z-070..074, NFR-Z-082
**Implementation locus**: `.github/workflows/ci.yml` jobs.
**Configurable parameters**: coverage threshold (default 80 %) configurable via workflow input.
**Anti-patterns avoided**:
- ❌ Tests in a separate optional workflow — failures hidden from PR review.
- ❌ Suppressed lint findings without rationale — debt accumulation.
**Cross-references**: SECURITY-10, NFR-9.1 of requirements.

### 6.3 Linter rule set (curated)
**Summary**: `.golangci.yml` enables: `errcheck`, `govet`, `staticcheck`, `ineffassign`, `gocritic`, `gosec`, `unused`, `unparam`, `unconvert`, `gofmt`, `goimports`, `revive` (with `exported` rule for Godoc enforcement).
**NFR-Z source**: NFR-Z-072
**Implementation locus**: `.golangci.yml` at repo root.
**Configurable parameters**: per-rule severity in the YAML.
**Anti-patterns avoided**:
- ❌ Default config (enables noise checks like `funlen`).
- ❌ No `gosec` — security findings invisible to CI.
**Cross-references**: SECURITY-10, NFR-Z-072.

---

## 7. Cross-Reference Matrix — Pattern → NFR-Z

| Pattern | Primary NFR-Z |
|---|---|
| 1.1 Classifier retry | NFR-Z-030, NFR-Z-031 |
| 1.2 Heartbeat | NFR-Z-034 |
| 1.3 Graceful drain | NFR-Z-022, NFR-Z-033 |
| 1.4 Deterministic idempotency | NFR-Z-033 |
| 1.5 Fail-closed | NFR-Z-050 |
| 1.6 No circuit breaker | NFR-Z-030 |
| 2.1 Bounded worker pool | NFR-Z-001, NFR-Z-014 |
| 2.2 HPA / KEDA | NFR-Z-002 |
| 2.3 SDK adaptive | NFR-Z-031 |
| 2.4 Multi-AZ spread | NFR-Z-004, NFR-Z-020, NFR-Z-023 |
| 2.5 Uniform pool | NFR-Z-001 |
| 3.1 Streaming I/O | NFR-Z-014, NFR-Z-013 |
| 3.2 LimitedReader | NFR-Z-014 |
| 3.3 Multipart upload | NFR-Z-013 |
| 3.4 Singleton clients | NFR-Z-013 |
| 3.5 Hybrid MIME | NFR-Z-013 |
| 4.1 IRSA | NFR-Z-044, NFR-Z-049 |
| 4.2 SSE mode | NFR-Z-040, NFR-Z-041 |
| 4.3 TLS via SDK | NFR-Z-040 |
| 4.4 Log redaction | NFR-Z-042 |
| 4.5 Distroless | NFR-Z-046 |
| 4.6 No pprof | NFR-Z-046, NFR-Z-090 |
| 4.7 Strict YAML decode | NFR-Z-050 |
| 5.1 Metrics taxonomy | NFR-Z-060 |
| 5.2 Log discipline | NFR-Z-042, NFR-Z-063 |
| 5.3 Health probes | NFR-Z-061, NFR-Z-022 |
| 5.4 Alert rules | NFR-Z-062 |
| 5.5 No tracing v1 | NFR-Z-063 |
| 6.1 Pinned deps | NFR-Z-047 |
| 6.2 CI gates | NFR-Z-070..074, NFR-Z-082 |
| 6.3 Linter set | NFR-Z-072 |

**Coverage**: every NFR-Z entry from `nfr-requirements.md` maps to at least one pattern above.

---

## 8. Compliance Summary

| SECURITY Rule | Pattern coverage |
|---|---|
| SECURITY-01 | 4.3 (TLS), 4.2 (SSE) |
| SECURITY-03 | 4.4 (log redaction), 5.2 (log discipline) |
| SECURITY-05 | 4.7 (config validation), 3.2 (bomb defence), validation pkg (path) |
| SECURITY-06 | 4.1 (IRSA least-privilege) |
| SECURITY-09 | 4.5 (distroless), 4.6 (no pprof), 4.7 (no debug routes) |
| SECURITY-10 | 6.1 (pinned deps), 6.2 (CI gates) |
| SECURITY-11 | 1.5 (separation of concerns; bombdefence + validation isolation reaffirmed) |
| SECURITY-12 | 4.1 (no static creds via IRSA) |
| SECURITY-13 | 6.1 (pinned actions/digests) |
| SECURITY-14 | 5.4 (alert rules), 5.1 (metrics) |
| SECURITY-15 | 1.5 (fail-closed), 4.7 (fail-fast config) |

| PBT Rule | Pattern coverage |
|---|---|
| PBT-02 | 1.4 (idempotency round-trip), 5.x (slipsheet/DDB round-trip) |
| PBT-03 | 3.2 (LimitedReader invariant), 4.4 (log invariant), validation pkg |
| PBT-04 | 1.4 (idempotence under redelivery), validation idempotence |
| PBT-05 | 1.1 (BackoffFor oracle) |
| PBT-06 | 1.2 (heartbeat lifecycle stateful), 1.1 (retry sequence stateful) |
| PBT-07 | (generator catalogue centralised — see `domain-entities.md` §8) |
| PBT-08 | 6.2 (CI gates: seed logging) |
| PBT-09 | (`rapid` framework — see `tech-stack-decisions.md`) |
| PBT-10 | 6.2 (PBT + example-based both run in CI) |

**No blocking SECURITY or PBT findings at the NFR Design stage.**
