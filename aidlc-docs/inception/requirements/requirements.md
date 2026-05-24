# Requirements — Zip Extraction Service (UOW-SVC-12)

**Document Type**: Requirements Specification
**Project**: Zip Extraction Service (UOW-SVC-12)
**Phase**: INCEPTION — Requirements Analysis
**Depth**: Standard with comprehensive security & PBT coverage
**Generated**: 2026-05-24
**Source Input**: `zip-extraction-service-input.md`
**Verification Answers**: `aidlc-docs/inception/requirements/requirement-verification-questions.md`

---

## 1. Intent Analysis Summary

| Field | Value |
|---|---|
| User Request | "Using AI-DLC start building the application; use the details from zip-extraction-service-input.md" |
| Request Type | New Project (greenfield Go microservice) |
| Request Clarity | Clear — detailed 28-section input spec provided |
| Scope Estimate | Single Component (one service, multiple internal Go packages) |
| Complexity Estimate | Moderate-to-Complex — security-critical streaming extraction, AWS SDK integration, Kubernetes deployment, LocalStack support, multi-gate testing |
| Project Type | Greenfield |
| Workspace Root | `/home/ukadam/workspace/opus2/zip-extraction` |
| Target Code Directory | `services/zip-extraction/` |

---

## 2. Extension Configuration

Both opt-in extensions are **enabled** (see audit.md timestamps). Their full rules files have been loaded and apply as **blocking constraints** at every downstream stage.

| Extension | Status | Mode | Rule File |
|---|---|---|---|
| Security Baseline | Enabled | Full (all SECURITY-01 … SECURITY-15) | `extensions/security/baseline/security-baseline.md` |
| Property-Based Testing | Enabled | Full (all PBT-01 … PBT-10) | `extensions/testing/property-based/property-based-testing.md` |

---

## 3. Stakeholders and Personas

| Stakeholder | Role | Primary Concern |
|---|---|---|
| Document ingestion pipeline | Upstream producer | Reliable ZIP fan-out into downstream document processing |
| Downstream pipeline services | Consumers of extracted children | Each child must arrive as an independent S3 event (no in-band coupling) |
| Platform / SRE team | Operator | Liveness, readiness, metrics, alerts, log queryability |
| Security team | Reviewer / auditor | Bomb-defence, path-traversal protection, IAM least privilege, encrypted transit/at-rest, audit log integrity |
| Development team | Maintainer | Local-CI parity (LocalStack), readable structured logs, easy debug iteration |

---

## 4. Functional Requirements

### FR-1. SQS Queue Consumption
**Source**: §3, §6, §7

- FR-1.1 The service SHALL consume claim-check messages from SQS queue `zip-extraction-queue`.
- FR-1.2 The service SHALL accept messages matching the schema:
  ```json
  { "pipelineExecutionId", "tenantId", "documentId",
    "sourceBucket", "sourceKey", "correlationId" }
  ```
- FR-1.3 The queue SHALL be configured with a visibility timeout of **300s** and a DLQ with `maxReceiveCount = 3`.
- FR-1.4 Malformed messages (schema violation, missing required fields) SHALL be rejected with a structured log entry and routed to DLQ via SQS native redrive (i.e., NOT explicitly deleted by the service so that `maxReceiveCount` governs DLQ transition).

### FR-2. Archive Acquisition
**Source**: §6 step 2

- FR-2.1 The service SHALL download the archive identified by `(sourceBucket, sourceKey)` from S3.
- FR-2.2 The download SHALL be streamed (no full in-memory buffering) per FR-9.
- FR-2.3 S3 GetObject failures SHALL be retried per FR-12 (retry classification).

### FR-3. Streaming ZIP Extraction
**Source**: §6 steps 3–10, §8, §9

- FR-3.1 The service SHALL open the archive using Go's `archive/zip` stream API.
- FR-3.2 Entries SHALL be iterated **sequentially** (parallelism is achieved across SQS messages, NOT within an archive).
- FR-3.3 For each entry, the service SHALL:
  1. Validate the entry path (FR-6).
  2. Stream the decompressed contents to S3 (FR-4) via multipart upload when the entry size exceeds **5 MB**.
  3. Persist a per-entry record in DynamoDB (FR-5).
- FR-3.4 The implementation SHALL satisfy all streaming constraints in NFR-3 (memory bounds, no full-disk extraction).
- FR-3.5 Nested archives SHALL be treated as opaque child entries — uploaded to S3 without further extraction (per Q4 decision). They re-enter the pipeline naturally via S3 event re-trigger.
- FR-3.6 Supported features: ZIP and ZIP64. Unsupported (must be rejected as FAILED): Encrypted ZIP, Multi-disk ZIP, Deflate64.

### FR-4. Child File Upload to S3
**Source**: §6 step 8, §13, §9

- FR-4.1 Each extracted child SHALL be uploaded to:
  ```
  s3://<staging-bucket>/input/{pipelineExecutionId}/{entryIndex:04d}-{safeFilename}
  ```
- FR-4.2 `safeFilename` SHALL be the path-validated, sanitised final-segment filename (no directory components).
- FR-4.3 Entries larger than **5 MB** SHALL use S3 multipart upload.
- FR-4.4 Each successful child upload SHALL trigger an S3 PutObject event consumed by downstream services (the service itself MUST NOT directly invoke downstream services per Non-Responsibility §4).

### FR-5. DynamoDB Per-Entry Record
**Source**: §14, §17

- FR-5.1 Table: `pipeline_files`.
- FR-5.2 Record schema (per child entry):
  ```json
  { "pk": "PIPELINE#<pipelineExecutionId>",
    "sk": "FILE#<entryIndex:04d>",
    "documentId": "<from-message>",
    "sourceArchive": "<sourceKey>",
    "childKey": "input/<pipelineExecutionId>/<entryIndex>-<safeFilename>",
    "mimeType": "<detected>",
    "status": "UPLOADED",
    "sizeBytes": <decompressed-size> }
  ```
- FR-5.3 Idempotency key SHALL be `pipelineExecutionId + entryIndex` (§17). Re-deliveries of the same SQS message SHALL NOT produce duplicate records (use conditional PutItem `attribute_not_exists(pk)` or equivalent).

### FR-6. Entry Path Validation (Path Safety)
**Source**: §6 step 7, §12, §11 rules 6–8

- FR-6.1 The service SHALL reject any entry path that:
  - contains `..` segments (e.g., `../../etc/passwd`);
  - is absolute (`/absolute/path/file.txt`, `C:\Windows\System32`);
  - is a symbolic link (zip entry mode flagged as symlink);
  - after normalisation, escapes the entry's intended staging prefix.
- FR-6.2 Rejection of an unsafe path SHALL fail the entire archive (status = FAILED) and emit a structured log entry classifying the violation.

### FR-7. 10-Point Zip Bomb Defence
**Source**: §11

The service SHALL enforce all ten defence rules. Default thresholds are the spec's recommended limits; each threshold SHALL be configurable via the YAML config file (NFR-7).

| # | Rule | Default Limit | Action on violation |
|---|------|---------------|---------------------|
| 1 | Max compressed archive size | 500 MB | Reject archive, FAILED |
| 2 | Max extracted size (cumulative) | 2 GB | Reject archive, FAILED |
| 3 | Max compression ratio | 1000× | Reject archive, FAILED |
| 4 | Max entry count | 10,000 | Reject archive, FAILED |
| 5 | Max directory nesting depth | 10 | Reject archive, FAILED |
| 6 | Reject symlinks | n/a | Reject archive, FAILED |
| 7 | Reject absolute paths | n/a | Reject archive, FAILED |
| 8 | Reject path traversal (`../`) | n/a | Reject archive, FAILED |
| 9 | Max single file size (decompressed) | 250 MB | Reject archive, FAILED |
| 10 | Max extraction duration | 240 s | Abort extraction, FAILED |

- FR-7.1 Bomb-defence violations SHALL NOT be retried (they are deterministic failures); the SQS message SHALL be deleted after recording the rejection in DynamoDB and emitting the `zip_bomb_rejections_total` metric.
- FR-7.2 Rule #2 (cumulative extracted size) and Rule #3 (compression ratio) SHALL be evaluated **incrementally** during streaming — extraction MUST abort as soon as a violation is detected, NOT after full decompression.

### FR-8. Parent Archive Slipsheet
**Source**: §15, Q7

- FR-8.1 At the end of a successful (or partially successful) extraction, the service SHALL write a parent slipsheet JSON document.
- FR-8.2 Storage location: `s3://<staging-bucket>/slipsheets/{pipelineExecutionId}.json` (separate prefix from `input/` per Q7 decision — prevents S3 event re-fanout misinterpreting the slipsheet as a child document).
- FR-8.3 Slipsheet schema:
  ```json
  { "type": "archive-container",
    "pipelineExecutionId": "<id>",
    "sourceArchive": "<sourceKey>",
    "childCount": <int>,
    "status": "SUCCESS|PARTIAL_FAILED|FAILED",
    "children": [
      { "entryIndex": <int>,
        "childKey": "<s3-key>",
        "status": "UPLOADED|FAILED",
        "failureReason": "<string-when-FAILED>" }
    ] }
  ```
- FR-8.4 A FAILED archive (e.g., bomb defence rejection before any extraction) SHALL still produce a slipsheet recording the failure reason; downstream consumers rely on the slipsheet as an authoritative summary.

### FR-9. SQS Visibility Heartbeat
**Source**: §18, Q6

- FR-9.1 For each in-flight message, the service SHALL spawn a per-message background goroutine that calls SQS `ChangeMessageVisibility` every **30 s**.
- FR-9.2 The heartbeat goroutine SHALL be bound to a `context.Context` cancelled on extraction completion (success, partial-failure, or hard timeout).
- FR-9.3 Heartbeat failures with error class "ReceiptHandleIsInvalid" or "MessageNotInflight" SHALL log at WARN and exit the goroutine without panicking the extraction.

### FR-10. Pipeline Status Reporting
**Source**: §16

- FR-10.1 The service SHALL emit one of three terminal statuses per pipeline execution:
  - **SUCCESS** — all entries uploaded successfully
  - **PARTIAL_FAILED** — at least one entry failed after exhausting retries; at least one succeeded
  - **FAILED** — archive rejected (bomb defence, unsupported feature) OR zero entries succeeded
- FR-10.2 Status SHALL be reflected in (a) the parent slipsheet, (b) DynamoDB per-entry records, and (c) emitted metrics.

### FR-11. Cleanup
**Source**: §6 step 13, §9

- FR-11.1 The service SHALL delete any ephemeral on-disk artefacts (multipart upload temp files, archive download buffers) after each message — success or failure.
- FR-11.2 Cleanup SHALL be implemented with `defer` blocks tied to function entry, ensuring it runs even on panic.

### FR-12. Partial-Failure & Retry Policy
**Source**: Q12

- FR-12.1 Per-entry transient failures (S3 throttling, 5xx, network timeouts) SHALL be retried up to **3 attempts** with exponential backoff (base 200 ms, factor 2, ±25 % jitter): ~200 ms / ~800 ms / ~3.2 s.
- FR-12.2 Retries SHALL be **classifier-driven**: ONLY throttling (`ProvisionedThroughputExceededException`, `SlowDown`), 5xx responses, and timeouts are retryable. 4xx client errors and bomb-defence violations are **never** retried.
- FR-12.3 After exhausting retries on a single entry, extraction SHALL continue with remaining entries; the pipeline execution status SHALL be **PARTIAL_FAILED** and the failing entry SHALL record its retry-exhaustion reason in DynamoDB and the slipsheet.
- FR-12.4 The SQS message SHALL be deleted after PARTIAL_FAILED to prevent re-delivery and double-upload; downstream remediation operates on the slipsheet.

### FR-13. Health and Metrics Endpoints
**Source**: §19, §20

- FR-13.1 The service SHALL expose HTTP endpoints:
  - `GET /healthz/live` — liveness (process responsive)
  - `GET /healthz/ready` — readiness (AWS clients initialised, config loaded, queue reachable)
  - `GET /metrics` — Prometheus exposition format
- FR-13.2 Prometheus metrics emitted:
  | Metric | Type | Labels |
  |---|---|---|
  | `zip_entries_total` | counter | `status` |
  | `zip_extraction_duration_seconds` | histogram | `outcome` |
  | `zip_extraction_failures_total` | counter | `reason` |
  | `zip_bomb_rejections_total` | counter | `rule` |
  | `extracted_bytes_total` | counter | — |
  | `partial_failures_total` | counter | — |

### FR-14. Configuration Ingestion
**Source**: Q8

- FR-14.1 **Infrastructure values** (queue URL, staging bucket name, DynamoDB table, AWS endpoint override, region, log format) SHALL be supplied via **environment variables**, injected by Helm / Kubernetes.
- FR-14.2 **Tunable limits** (all 7 numeric thresholds in FR-7, retry counts in FR-12, multipart threshold, heartbeat interval) SHALL be supplied via a **YAML config file** mounted from a ConfigMap.
- FR-14.3 The service SHALL fail-fast on startup if any required env var or YAML field is missing or malformed.
- FR-14.4 The YAML schema SHALL be validated at startup against a strict schema (no extra keys, no missing keys, types enforced).

### FR-15. LocalStack Compatibility
**Source**: §28, Q5

- FR-15.1 The service SHALL function identically when AWS SDK endpoints are pointed at LocalStack via the `AWS_ENDPOINT_URL` env var (validated services: SQS, S3, DynamoDB, STS).
- FR-15.2 A `docker-compose.yml` SHALL bring up LocalStack + the service container in one command (`make up`).
- FR-15.3 A `make bootstrap` target SHALL auto-provision the staging S3 bucket, the SQS queue + DLQ with redrive policy, and the `pipeline_files` DynamoDB table against LocalStack.
- FR-15.4 No production code path SHALL contain LocalStack-specific branching — environment configuration is the only difference between local and prod.

### FR-16. Deployment Artefacts
**Source**: §21, §22, §26, Q9

- FR-16.1 The repository SHALL ship:
  - Go application code under `services/zip-extraction/` per §26 layout.
  - A multi-stage `Dockerfile` producing a static, non-root, distroless or minimal-base image.
  - A `Makefile` with targets: `build`, `test`, `lint`, `up`, `down`, `bootstrap`, `run`, `docker`, `pbt-replay`.
  - A minimal Helm chart at `services/zip-extraction/chart/` containing: `Deployment`, `Service`, `ConfigMap`, `ServiceAccount`, `values.yaml`. NO `HPA`, `PDB`, `NetworkPolicy`, or `ServiceMonitor` (deferred to platform team).
- FR-16.2 The Dockerfile SHALL pin base image digests (no `latest` tags) per SECURITY-10.
- FR-16.3 The container SHALL run as a non-root UID with a read-only root filesystem and writable `/tmp` (emptyDir volume) per §25 and SECURITY-09.

---

## 5. Non-Functional Requirements

### NFR-1. Performance
- NFR-1.1 P95 extraction latency for a 100 MB archive with 100 entries SHALL be < **180 s** end-to-end (consistent with the 240 s extraction hard timeout and 300 s SQS visibility).
- NFR-1.2 The service SHALL sustain at least **5 concurrent SQS messages per pod** at default settings, with parallelism configurable.
- NFR-1.3 Per-entry processing SHALL achieve at minimum **5 MB/s** sustained upload to S3 (bounded by network and SDK retry behaviour).

### NFR-2. Memory Bounds
**Source**: §9

- NFR-2.1 The service SHALL operate within a **128 MiB** pod memory limit.
- NFR-2.2 Max in-memory buffer per in-flight entry SHALL be **4 MiB**.
- NFR-2.3 The service SHALL NOT buffer the full archive or full extracted contents in memory or on disk.
- NFR-2.4 Multipart upload SHALL be used for any entry larger than **5 MiB**.

### NFR-3. Streaming I/O Only
- NFR-3.1 All ZIP entry processing SHALL use streaming `io.Reader` chains end-to-end (zip reader → optional bomb-counting middleware → S3 multipart writer).
- NFR-3.2 No code path SHALL invoke `io.ReadAll`, `ioutil.ReadFile`, or equivalent on archive or entry streams.

### NFR-4. Reliability and Failure Handling
- NFR-4.1 The service SHALL be stateless (per §27); pod replacements SHALL be safe at any time.
- NFR-4.2 In-flight messages on pod termination SHALL be reclaimed by SQS after visibility timeout (graceful shutdown SHALL cancel heartbeat goroutines so visibility resets quickly).
- NFR-4.3 All external calls (SQS, S3, DynamoDB) SHALL have explicit timeouts and error handling per SECURITY-15.

### NFR-5. Observability
- NFR-5.1 Structured logging via `zap` per SECURITY-03; every log entry SHALL include timestamp, log level, message, `pipelineExecutionId`, `correlationId`, and `documentId` where available.
- NFR-5.2 No log entry SHALL contain secrets, tokens, PII, or full file contents (SECURITY-03 / SECURITY-09).
- NFR-5.3 Log output format SHALL be runtime-selected: **JSON in production** (default), **console in local development** via `LOG_FORMAT=console` (per Q10).
- NFR-5.4 Logs SHALL be routed to stdout; centralized aggregation (CloudWatch / Loki) is the platform team's responsibility (SECURITY-03 satisfied by stdout + EKS log driver).
- NFR-5.5 Prometheus metrics SHALL be exposed per FR-13.2; alerting on `zip_extraction_failures_total` rate, `zip_bomb_rejections_total` rate, and authentication / authorization failures (SECURITY-14) SHALL be documented in the Helm chart README as platform-team integration points.

### NFR-6. Security (cross-cutting; SECURITY-01 … SECURITY-15)

Every SECURITY rule from `extensions/security/baseline/security-baseline.md` is enforced as a blocking constraint. The applicability and approach per rule is mapped below:

| Rule | Applicability | Compliance Approach |
|---|---|---|
| SECURITY-01 Encryption at rest & in transit | **Applicable** | S3 staging bucket: SSE-S3 or SSE-KMS enabled + bucket policy denying non-TLS requests. DynamoDB: encryption at rest enabled by default. SQS: SSE enabled. All AWS SDK calls use HTTPS (TLS 1.2+) by default. |
| SECURITY-02 Access logging on network intermediaries | **N/A** | The service exposes only an in-cluster `/metrics` / `/healthz` endpoint on a ClusterIP Service. No external LB, API gateway, or CDN in this service's scope (those are platform-owned). |
| SECURITY-03 Application-level logging | **Applicable** | `zap` structured logger with required fields. No PII / secret logging. Output to stdout. |
| SECURITY-04 HTTP security headers | **N/A** | The service serves only operational endpoints (`/healthz`, `/metrics`) — not HTML — and is not internet-exposed. |
| SECURITY-05 Input validation on API parameters | **Applicable** | SQS message schema validation; ZIP entry path validation; archive metadata validation (FR-6, FR-7, FR-1.4). All "API surface" is the SQS message, plus operational endpoints (no user input). |
| SECURITY-06 Least-privilege IAM | **Applicable** | Service IRSA role MUST have only: `sqs:ReceiveMessage`/`DeleteMessage`/`ChangeMessageVisibility` scoped to the specific queue ARN; `s3:GetObject` scoped to upload prefix; `s3:PutObject` scoped to `input/*` and `slipsheets/*` prefixes of the staging bucket; `dynamodb:PutItem`/`UpdateItem` scoped to the `pipeline_files` table. No wildcards. |
| SECURITY-07 Restrictive network configuration | **Applicable** (partial) | Service has no inbound external traffic; egress restricted via the (platform-team-owned) cluster `NetworkPolicy` to AWS service endpoints only. The Helm chart README SHALL document the required egress allowlist. |
| SECURITY-08 Application-level access control | **N/A** | No human-facing routes. The SQS-consumer pattern delegates authentication to AWS IAM (IRSA-bound role). |
| SECURITY-09 Hardening & misconfiguration prevention | **Applicable** | Non-root container; read-only root filesystem; no default credentials; production error responses generic; no sample / debug routes; pinned base image; supported runtime (Go 1.24). |
| SECURITY-10 Software supply chain | **Applicable** | `go.sum` committed (Go lockfile); `govulncheck` (or `osv-scanner`) step in CI; Dockerfile pins base image digest (no `latest`); SBOM generation via `syft` documented in build instructions. |
| SECURITY-11 Secure design | **Applicable** | Security-critical logic (`bombdefence`, `validation`) isolated in dedicated packages; defence-in-depth (path validation + bomb defence + IAM scoping + bucket policy); rate-limiting concern handled implicitly by SQS concurrency cap; misuse cases documented in design (zip bomb, path traversal, symlink, slipsheet re-fanout). |
| SECURITY-12 Authentication & credential management | **Applicable** (partial) | No human users; AWS authentication via IRSA (no static credentials, no env-var keys in production). LocalStack profile uses well-known dummy credentials (`test`/`test`) which is acceptable for local development per LocalStack convention. No hardcoded credentials in production code paths or Helm values. |
| SECURITY-13 Software & data integrity | **Applicable** | ZIP parsing is via stdlib `archive/zip` (no untrusted-deserialisation surface beyond ZIP itself, which we treat as untrusted and defend per FR-7). Build pipeline access controls are platform-team concerns; documented in the Helm chart README. |
| SECURITY-14 Alerting & monitoring | **Applicable** | Metric definitions in FR-13.2 plus alert recommendations (high `zip_bomb_rejections_total` rate, sustained `zip_extraction_failures_total` rate) documented for platform integration. Log retention (≥90 days) is a platform-cluster concern documented in the chart README; the service role MUST NOT have permission to delete its own CloudWatch log streams. |
| SECURITY-15 Exception handling & fail-safe defaults | **Applicable** | All external calls have explicit error handling; global recover handler at goroutine entry points; fail-closed semantics (any uncertainty → reject archive, never silently accept); resources cleaned via `defer`. |

### NFR-7. Configuration Schema (Tunable Limits YAML)

The mounted ConfigMap YAML SHALL match this schema (validated at startup):

```yaml
bombDefence:
  maxCompressedSizeBytes:     524288000     # 500 MB
  maxExtractedSizeBytes:      2147483648    # 2 GB
  maxCompressionRatio:        1000     # bumped from 100 after a real-world 102 MB .doc OLE2 archive (114× ratio) tripped the original 100× cap; the 1000× cap still trips on any practical zip bomb (classic 42.zip = ~4.2e9×) while accommodating legitimate Office documents
  maxEntryCount:              10000
  maxDirectoryDepth:          10
  maxSingleFileSizeBytes:     262144000     # 250 MB
  maxExtractionDurationSec:   240
streaming:
  maxInMemoryBufferBytes:     4194304       # 4 MB
  multipartThresholdBytes:    5242880       # 5 MB
retry:
  maxAttempts:                3
  backoffBaseMillis:          200
  backoffFactor:              2.0
  jitterFraction:             0.25
sqs:
  heartbeatIntervalSec:       30
  maxInFlight:                5
```

### NFR-8. Property-Based Testing (cross-cutting; PBT-01 … PBT-10)

Every PBT rule from `extensions/testing/property-based/property-based-testing.md` is enforced as a blocking constraint. PBT framework: **`pgregory.net/rapid`** per §23. Identified properties to be carried forward into Functional Design (PBT-01):

| Category | Property | Component |
|---|---|---|
| Round-trip | Slipsheet JSON marshal / unmarshal = identity | `internal/slipsheet` |
| Round-trip | DynamoDB record marshal / unmarshal = identity | `internal/dynamodb` |
| Round-trip | S3 key parse / format = identity for valid inputs | `internal/storage` |
| Invariant | Path validation: every accepted path, after normalization, has no `..` segments, no leading `/`, no drive letter | `internal/validation` |
| Invariant | Bomb defence: for any input archive metadata, accepted → all 10 thresholds hold (no false negatives) | `internal/bombdefence` |
| Invariant | Bomb defence: total decompressed bytes ≤ maxExtractedSizeBytes for any accepted archive | `internal/bombdefence` |
| Idempotence | Path sanitisation: `sanitize(sanitize(x)) == sanitize(x)` | `internal/validation` |
| Idempotence | DynamoDB write (conditional PutItem with `(pipelineExecutionId, entryIndex)`) idempotent under at-least-once SQS delivery | `internal/dynamodb` |
| Stateful (PBT-06) | SQS heartbeat lifecycle: any valid command sequence on (start, complete, fail, hard-timeout) preserves the invariant "heartbeat goroutine count == active extractions" | `internal/extraction` |
| Stateful (PBT-06) | Status transitions: any valid sequence of (entry success, entry fail, archive abort) maps deterministically to one of {SUCCESS, PARTIAL_FAILED, FAILED} | `internal/extraction` |
| Oracle | Backoff sequence: generated delays match the closed-form formula `base × factor^n × (1 ± jitter)` | `internal/retry` |

PBT outputs MUST be reproducible — seed logged on every failure; framework shrinking enabled; CI integration mandatory.

### NFR-9. Testing Gates
**Source**: §24, Q11

- NFR-9.1 **Gate 1 — Unit tests**: every package SHALL have unit tests covering happy paths, error paths, and PBT properties from NFR-8. Coverage target: ≥80 % statements.
- NFR-9.2 **Gate 2 — Local E2E (Testcontainers + LocalStack)**: end-to-end test SHALL run the service container against LocalStack with seeded SQS queue, S3 bucket, DynamoDB table; verify full flow including S3 PutObject event observation, idempotency under message re-delivery, and bomb-defence rejection paths.
- NFR-9.3 **Gate 3 — Sandbox EKS E2E**: **deferred** per Q11. The repository SHALL NOT generate Gate 3 harness scaffolding; a follow-up task will add it once the platform team provisions a sandbox EKS environment with real IRSA credentials.

### NFR-10. Build & Deployment
- NFR-10.1 Go toolchain: **1.24** (per Q3).
- NFR-10.2 Build outputs: static linked binary, multi-stage Docker image with non-root user and read-only root filesystem.
- NFR-10.3 Image registry path: `537462380503.dkr.ecr.eu-west-1.amazonaws.com/doc-uploader-sandbox/zip-extraction`.
- NFR-10.4 Deployment: `helm upgrade --install` per §21.

### NFR-11. Repository Structure
**Source**: §26

```
services/zip-extraction/
├── cmd/
│   └── zip-extraction/main.go
├── internal/
│   ├── extraction/        # ProcessMessage, ExtractEntries
│   ├── bombdefence/       # CheckBombDefence (10 rules)
│   ├── storage/           # S3 upload (multipart-aware)
│   ├── dynamodb/          # CreatePipelineRecord (idempotent)
│   ├── slipsheet/         # GenerateSlipSheet
│   ├── metrics/           # Prometheus collectors
│   ├── validation/        # ValidateEntryPath + sanitisation
│   ├── retry/             # Classifier-driven retry helper
│   ├── config/            # YAML + env-var loading & validation
│   └── awsclients/        # SDK construction (endpoint override aware)
├── chart/
│   ├── Chart.yaml
│   ├── values.yaml
│   └── templates/
│       ├── deployment.yaml
│       ├── service.yaml
│       ├── configmap.yaml
│       └── serviceaccount.yaml
├── deploy/
│   └── docker-compose.yml
├── test/
│   ├── e2e/               # Gate 2 Testcontainers + LocalStack tests
│   └── testdata/          # Golden archives (incl. crafted bomb fixtures)
├── Dockerfile
├── Makefile
├── go.mod
├── go.sum
└── README.md
```

---

## 6. Constraints

| Constraint | Source |
|---|---|
| Language: Go 1.24 | Q3 |
| Runtime memory limit: 128 MiB | §1 |
| Queue visibility timeout: 300 s | §1, §18 |
| Extraction hard timeout: 240 s | §18 |
| Heartbeat interval: 30 s | §18 |
| AWS region: eu-west-1 | §1 |
| Deployment target: DEV05-EKS-CLUSTER | §1 |
| Required libraries: `archive/zip`, `aws-sdk-go-v2`, `zap`, `prometheus/client_golang`, `testcontainers-go`, `rapid` | §23 |
| Nested archives: opaque upload only (no recursion) | Q4 |
| Slipsheet location: `slipsheets/` prefix (separate from `input/`) | Q7 |
| Config: env vars (infra) + YAML (limits) | Q8 |
| Helm scope: minimal skeleton (5 templates) | Q9 |
| Logs: JSON in prod, console in local | Q10 |
| Gates: Gates 1 + 2 only; Gate 3 deferred | Q11 |
| Retry: 3 attempts, exponential backoff, classifier-driven | Q12 |
| Heartbeat: per-message goroutine, context-cancelled | Q6 |

---

## 7. Out of Scope

- Downstream pipeline orchestration (handled via S3 event fan-out, not in-band)
- Content conversion, OCR, or classification of extracted files
- Encrypted, multi-disk, or Deflate64 ZIPs (explicitly rejected — FAILED status)
- Recursive nested-archive extraction (opaque upload only per Q4)
- HPA, PodDisruptionBudget, NetworkPolicy, ServiceMonitor templates (platform-team scope per Q9)
- Gate 3 sandbox-EKS E2E test harness (deferred per Q11)
- Bucket / queue / table provisioning in production (operated by platform IaC outside this repo)

---

## 8. Open Questions / Deferred Decisions

| Topic | Status | Resolution path |
|---|---|---|
| Sandbox EKS E2E (Gate 3) harness | Deferred per Q11 | Follow-up after platform-team provisions sandbox + IRSA credentials |
| KMS key choice (SSE-S3 vs SSE-KMS with CMK) for staging bucket | Defers to platform-team bucket-level decision; service supports either | Resolved at infrastructure deploy time |
| HPA / PDB / NetworkPolicy template authoring | Out of scope per Q9 | Owned by platform team |
| Log retention duration in CloudWatch / Loki | Out of service scope; documented in chart README as platform integration point | Resolved by platform configuration (SECURITY-14 requires ≥90 days minimum) |

---

## 9. Traceability Matrix (Requirements → Spec Sources)

| Requirement | Spec Section / Q&A |
|---|---|
| FR-1 SQS consumption | §3, §6, §7 |
| FR-2 Archive acquisition | §6 |
| FR-3 Streaming extraction | §6, §8, §9, §10, Q4 |
| FR-4 S3 child upload | §6, §13, §9 |
| FR-5 DynamoDB record | §14, §17 |
| FR-6 Path validation | §6, §11(6–8), §12 |
| FR-7 Bomb defence | §11 |
| FR-8 Slipsheet | §15, Q7 |
| FR-9 Heartbeat | §18, Q6 |
| FR-10 Status reporting | §16 |
| FR-11 Cleanup | §6, §9 |
| FR-12 Retry policy | Q12 |
| FR-13 Health / metrics | §19, §20 |
| FR-14 Configuration | Q8 |
| FR-15 LocalStack | §28, Q5 |
| FR-16 Deployment artefacts | §21, §22, §26, Q9 |
| NFR-1 Performance | §1, §18 |
| NFR-2 Memory bounds | §9 |
| NFR-3 Streaming I/O | §9 |
| NFR-4 Reliability | §27 |
| NFR-5 Observability | §19, §20, Q10, SECURITY-03 |
| NFR-6 Security | §25, SECURITY-01…15 |
| NFR-7 Config schema | Q8 |
| NFR-8 PBT | §23, PBT-01…10 |
| NFR-9 Testing gates | §24, Q11 |
| NFR-10 Build & deployment | §21, §22, Q3 |
| NFR-11 Repo structure | §26 |

---

## 10. Summary of Key Requirements

- **Service shape**: Stateless Go 1.24 SQS consumer running in EKS, streaming ZIP→S3 with DynamoDB metadata.
- **Security posture**: All 15 SECURITY rules enforced (encrypted storage, TLS, least-privilege IAM via IRSA, structured logging, hardening, supply-chain pinning, fail-closed error handling). Bomb-defence and path-traversal protection are first-class blocking concerns evaluated incrementally during streaming.
- **Testing posture**: PBT (rapid) on every package with identifiable properties (round-trip, invariant, idempotence, stateful, oracle) plus example-based tests for business-critical scenarios — covers PBT-01…10. Gates 1 + 2 (unit + LocalStack Testcontainers) in scope; Gate 3 deferred.
- **Configuration**: env vars for infrastructure (12-factor) + YAML ConfigMap for tunable limits — gives auditability of security boundaries.
- **Deployment**: Application code + Dockerfile + Makefile + minimal Helm chart (5 templates). HPA/PDB/NetworkPolicy/ServiceMonitor deferred to platform team.
- **Observability**: zap structured logs (JSON prod / console local), Prometheus metrics on `/metrics`, liveness/readiness probes on `/healthz/{live,ready}`.
- **Failure handling**: PARTIAL_FAILED semantics with bounded classifier-driven retries (3 attempts, exponential backoff). Bomb-defence violations and 4xx errors fail fast and are never retried.

---

## 11. Compliance Summary (Stage Exit Gate)

### Security Compliance (SECURITY-01 … SECURITY-15)
All rules addressed at the requirements level — see NFR-6 mapping table. Items marked **N/A** (SECURITY-02, SECURITY-04, SECURITY-08) reflect the service's architectural shape (no public network intermediary, no HTML-serving endpoint, no human-facing routes); these N/A determinations are not blocking findings.

### PBT Compliance (PBT-01 … PBT-10)
All rules addressed at the requirements level — see NFR-8 properties table. PBT-09 (framework selection) is satisfied by selecting `rapid`. PBT-05 (oracle) is satisfied at minimum by the backoff-sequence oracle property; additional oracle opportunities will be evaluated during Functional Design.

**No blocking SECURITY or PBT findings outstanding at the Requirements Analysis stage.**
