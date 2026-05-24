# Per-Unit NFR Requirements — zip-extraction (UOW-SVC-12)

**Document Type**: Non-Functional Requirements (Unit-level)
**Phase**: CONSTRUCTION — NFR Requirements (Part 2: Generation)
**Generated**: 2026-05-24
**Unit**: `zip-extraction` (UOW-SVC-12)

This document refines the project-level NFRs from `aidlc-docs/inception/requirements/requirements.md` (NFR-1 … NFR-11) into **unit-level, measurable, testable requirements** labelled `NFR-Z-NNN` (Z = the zip-extraction unit). Every project-level NFR maps to one or more unit-level entries (see §10 cross-reference matrix).

---

## 1. Scalability

### NFR-Z-001 — Per-pod concurrency
**Statement**: The service SHALL process up to `sqs.maxInFlight` SQS messages concurrently per pod. Default value: **5**. Configurable via YAML config (NFR-7 in `requirements.md`).
**Source**: Q1 of NFR plan, FR-9
**Verification**: Integration test asserting 5 concurrent extractions; memory stays ≤ 128 MiB
**Acceptance**: Aggregate per-pod throughput ≥ **50 archives/min** for the typical mix (small + 100 MB archives)

### NFR-Z-002 — Horizontal autoscaling
**Statement**: The service SHALL be horizontally scalable. The recommended autoscaling configuration (consumed by the platform team's HPA / KEDA manifest):
- **Min replicas**: 2
- **Max replicas**: 10
- **Scaling trigger**: SQS `ApproximateNumberOfMessagesVisible` ÷ `cfg.MaxInFlight` (target ≈ 1 — one visible message per per-pod-in-flight slot)
**Source**: Q2 of NFR plan
**Verification**: Chart README contains the recommendation; integration test against KEDA in sandbox EKS (deferred — Gate 3 per Q11 of requirements)
**Acceptance**: Aggregate throughput up to **500 archives/min** per cluster at max scale (10 pods × 50 archives/min)

### NFR-Z-003 — Pod resource requests
**Statement**: Helm chart `values.yaml` documents recommended resources:
```yaml
resources:
  requests:
    cpu: 250m       # no CPU limit — avoids CFS throttling stutter
    memory: 96Mi    # below the 128Mi limit, leaves GC headroom
  limits:
    memory: 128Mi   # matches input-spec §1; OOM-kill on streaming-invariant violation
```
**Source**: Q3 of NFR plan, §1 input spec
**Verification**: Helm chart linter check; Gate 2 LocalStack test under load
**Acceptance**: Service operates within these limits across the SLO test workload

### NFR-Z-004 — Multi-AZ pod distribution
**Statement**: Chart README documents `topologySpreadConstraints` with `maxSkew: 1` across the cluster's available AZs. Service is stateless (per §27 + NFR-4.1 of requirements), making AZ-aware spreading penalty-free.
**Source**: Q5 of NFR plan
**Verification**: Chart README cross-reference check during Build & Test
**Acceptance**: An AZ outage leaves ≥ 1 replica running (given min-2-replica configuration in NFR-Z-002)

---

## 2. Performance — SLOs / SLIs

### NFR-Z-010 — Success-rate SLO
**Statement**:
- **Objective**: ≥ **99.5%** of received SQS messages reach a terminal status of SUCCESS or PARTIAL_FAILED (i.e., NOT FAILED) over a **28-day rolling window**.
- **Numerator exclusions**: bomb-defence-rejected archives (`failureReason` starting with `"bomb-defence rule"`) and unsupported-feature-rejected archives (`failureReason` starting with `"unsupported:"`) are **excluded from BOTH the numerator AND the denominator**. These are deterministic outcomes attributable to upstream input quality, not service quality.
- **Error budget**: ~3.5 hours of zero-success time per 28-day window.
**Source**: Q4 of NFR plan, FR-10
**Verification**: SLI defined as `sum(rate(zip_extraction_failures_total{reason!~"bomb-defence.*|unsupported.*"}[28d])) / sum(rate(zip_entries_total[28d]))`; Grafana dashboard documented in chart README

### NFR-Z-011 — P95 latency SLO
**Statement**: For archives ≤ 100 MB compressed AND ≤ 100 entries, P95 end-to-end processing latency ≤ **180 s** over a 28-day rolling window.
**Source**: Q4 of NFR plan, NFR-1.1 of requirements
**Verification**: SLI defined as `histogram_quantile(0.95, sum(rate(zip_extraction_duration_seconds_bucket{archive_size_class="small_medium"}[28d])) by (le))`
**Acceptance**: P95 stays below 180 s in Gate 2 LocalStack load tests

### NFR-Z-012 — P99 latency ceiling
**Statement**: For archives ≤ 100 MB / ≤ 100 entries, P99 end-to-end processing latency ≤ **220 s**. Stays below the 240 s extraction hard timeout (BR-BOMB-005 rule #10) with comfortable margin.
**Source**: Q4 of NFR plan, FR-7 rule #10
**Verification**: SLI defined as P99 histogram quantile; alert if P99 > 230 s for 1 hour

### NFR-Z-013 — Sustained upload throughput
**Statement**: Per-entry upload throughput ≥ **5 MB/s** sustained (driven by S3 SDK + network — service must not add > 10% overhead).
**Source**: NFR-1.3 of requirements
**Verification**: Gate 2 LocalStack benchmark with 100 MB single-entry archive

### NFR-Z-014 — Streaming memory ceiling
**Statement**:
- Per in-flight entry memory: ≤ **4 MiB** for streaming buffer (NFR-2.2 of requirements; matches `cfg.MaxInMemoryBufferBytes`).
- Per-pod steady-state memory: ≤ **128 MiB** under `maxInFlight=5` concurrent extractions of mixed-size archives.
- No code path SHALL invoke `io.ReadAll`, `os.ReadFile`, `ioutil.ReadAll`, or `bytes.Buffer.Write` of an entire archive or entry payload.
**Source**: NFR-2, NFR-3 of requirements
**Verification**: golangci-lint custom rule + Gate 2 memory-profile assertion

---

## 3. Availability

### NFR-Z-020 — Pod-failure availability
**Statement**: A single pod failure SHALL NOT cause loss of in-flight messages. Mechanism: SQS visibility timeout reclaim; idempotency contract (FR-5.3) ensures re-processed messages produce identical end-state.
**Source**: NFR-4.1, NFR-4.2 of requirements
**Verification**: Chaos test in Gate 2 — kill a worker mid-extraction; assert no duplicate effects on retry

### NFR-Z-021 — Rolling-update availability
**Statement**: With min-2-replicas (NFR-Z-002) and `topologySpreadConstraints` (NFR-Z-004), a rolling update SHALL maintain ≥ 1 ready replica throughout the rollout. Helm chart configures `strategy.type: RollingUpdate` with `maxUnavailable: 1`, `maxSurge: 1`.
**Source**: Q5 of NFR plan
**Verification**: Helm template render check

### NFR-Z-022 — Graceful drain
**Statement**: On SIGTERM, the service SHALL drain in-flight workers up to `gracefulShutdownTimeoutSec` (default 250 s) before exiting. Heartbeats continue during drain. Per-entry idempotency provides safe re-delivery if drain deadline exceeded.
**Source**: Q7 of application design, BR-DRAIN-001..004
**Verification**: Integration test in Gate 2

### NFR-Z-023 — AZ-failure tolerance
**Statement**: An AZ outage SHALL NOT cause complete service unavailability when min-2-replica + `topologySpreadConstraints` is enforced.
**Source**: Q5 of NFR plan, NFR-Z-002, NFR-Z-004
**Verification**: Documented in chart README as platform-team responsibility

---

## 4. Reliability

### NFR-Z-030 — Bounded retry
**Statement**: Transient operation failures (FR-12.1, FR-12.2, BR-RETRY-002) SHALL be retried up to 3 attempts with exponential backoff (base 200 ms, factor 2.0, jitter ±25 %).
**Source**: FR-12, Q12 of requirements, BR-RETRY-003
**Verification**: PBT-05 oracle property on `retry.BackoffFor`

### NFR-Z-031 — Classifier-driven retry
**Statement**: Retry SHALL be limited to AWS-SDK errors classifiable as `*TransientError` (throttling, 5xx, timeout, network). Bomb defence, path validation, unsupported features, and 4xx client errors are NEVER retried (BR-RETRY-009 / BR-RETRY-008).
**Source**: FR-12.2, BR-RETRY-004..010
**Verification**: PBT-03 invariant property on `retry.Classify`

### NFR-Z-032 — Partial-failure tolerance
**Statement**: A single per-entry failure (after retries exhausted) SHALL NOT abort the entire archive. The pipeline status becomes PARTIAL_FAILED (FR-10 + BR-STATUS-001) with a per-entry FAILED row in DynamoDB (BR-DDB-002).
**Source**: FR-12.3, FR-10
**Verification**: Integration test injecting per-entry failure

### NFR-Z-033 — Idempotent re-delivery
**Statement**: An SQS re-delivery of an already-processed message SHALL NOT produce duplicate DynamoDB rows or duplicate-content S3 objects. Mechanism: conditional PutItem (BR-IDEMPOTENCY-002) + deterministic S3 keys (BR-IDEMPOTENCY-001).
**Source**: FR-5.3, BR-IDEMPOTENCY-*
**Verification**: PBT-04 idempotence property + Gate 2 redelivery test

### NFR-Z-034 — Heartbeat liveness
**Statement**: For every in-flight SQS message, `ChangeMessageVisibility(300s)` SHALL be invoked every 30 s until the message reaches a terminal state (BR-HEARTBEAT-002).
**Source**: FR-9, Q6 of NFR plan
**Verification**: Stateful PBT-06 + Gate 2 long-running-extraction test

---

## 5. Security (unit-level mapping of SECURITY-01 … SECURITY-15)

### NFR-Z-040 — Encryption in transit
**Statement**: All AWS-service calls (SQS, S3, DynamoDB) SHALL use HTTPS / TLS 1.2+. No code path SHALL pass `WithDisableSSL` to the AWS SDK. LocalStack's `http://localstack:4566` is the only documented exception (local-only).
**Source**: SECURITY-01
**Verification**: Code-review checklist + grep for `DisableSSL`

### NFR-Z-041 — Encryption at rest delegated to platform
**Statement**: S3 staging bucket (with SSE), SQS queue (with SSE), and DynamoDB table (default encryption) SHALL be provisioned by the platform team with encryption at rest enabled. Service code does NOT require KMS key references — it consumes whatever the IRSA role permits.
**Source**: SECURITY-01
**Verification**: Chart README cross-reference; IRSA policy template documents KMS-related permissions if SSE-KMS is used

### NFR-Z-042 — Structured logging
**Statement**: All log output SHALL be JSON in production via `zap`. Every log entry includes `timestamp`, `level`, `message`, `service`, `version`. Per-message handlers bind `pipelineExecutionId`, `correlationId`, `documentId`. No sensitive fields are emitted (BR-LOG-002 deny-list).
**Source**: SECURITY-03, BR-LOG-001..004
**Verification**: PBT-03 invariant property — emitted JSON contains no deny-list values

### NFR-Z-043 — Input validation
**Statement**: Every external input is validated: SQS message body schema (FR-1.2, BR-LOG-001 + section 6 of business-logic-model.md); ZIP entry paths (BR-PATH-001..006); ZIP archive metadata (BR-BOMB-001).
**Source**: SECURITY-05
**Verification**: Unit tests + PBT-03 invariant properties

### NFR-Z-044 — Least-privilege IAM (IRSA)
**Statement**: The Kubernetes ServiceAccount's IRSA role policy SHALL contain ONLY the following actions, each scoped to the specific resource ARN (no wildcards):
- `sqs:ReceiveMessage`, `sqs:DeleteMessage`, `sqs:ChangeMessageVisibility` on `arn:aws:sqs:eu-west-1:<account>:zip-extraction-queue`
- `s3:GetObject` on `arn:aws:s3:::<source-bucket>/uploads/*`
- `s3:PutObject` on `arn:aws:s3:::<staging-bucket>/input/*` AND `arn:aws:s3:::<staging-bucket>/slipsheets/*`
- `dynamodb:PutItem` on `arn:aws:dynamodb:eu-west-1:<account>:table/pipeline_files`
- (optionally, if SSE-KMS with CMK: `kms:Decrypt` + `kms:GenerateDataKey` on the specific KMS key ARN)
**Source**: SECURITY-06, NFR-6 of requirements
**Verification**: Helm chart IRSA template + a CI check that the rendered policy contains no `*` resource or action

### NFR-Z-045 — Restrictive network egress
**Statement**: Chart README documents the required egress allowlist for the platform-team `NetworkPolicy`: AWS service endpoints in `eu-west-1` only (SQS, S3, DynamoDB, STS endpoint hosts). No general internet egress.
**Source**: SECURITY-07
**Verification**: Chart README cross-reference

### NFR-Z-046 — Hardening
**Statement**: The container image SHALL:
- Run as non-root UID (e.g., `10001`)
- Use a read-only root filesystem
- Mount `/tmp` as `emptyDir` (writable, for any transient AWS SDK temp files)
- Use a pinned base-image digest (no `latest` tag)
- Expose ONLY port 8080 (no debug / pprof / sample endpoints)
**Source**: SECURITY-09, §25 of input spec
**Verification**: Helm template render check + Dockerfile review

### NFR-Z-047 — Supply chain
**Statement**: The repository SHALL include:
- `go.mod` + `go.sum` committed (lockfile)
- `govulncheck` step in CI (NFR-Z-070)
- SBOM generation via `syft` in CI (NFR-Z-071)
- Dockerfile base image pinned by digest
- Reusable GitHub Actions pinned by full SHA (not by tag)
**Source**: SECURITY-10, Q6 of NFR plan, Q8 of NFR plan
**Verification**: CI workflow inspection during Build & Test

### NFR-Z-048 — Secure design (separation of concerns)
**Statement**: Security-critical logic is isolated in dedicated packages: `internal/bombdefence` (10-rule defence + LimitedReader) and `internal/validation` (path safety). They are leaf packages depending only on `internal/extraction` typed-error types and stdlib (per `component-dependency.md`).
**Source**: SECURITY-11
**Verification**: Import-graph lint rule (`internal/bombdefence` MUST NOT import AWS SDK or `internal/sqs|storage|dynamodb`)

### NFR-Z-049 — Credential management
**Statement**: NO static AWS credentials in source code, Helm values, or container image. Authentication SHALL be via IRSA (`AWS_ROLE_ARN` + `AWS_WEB_IDENTITY_TOKEN_FILE` set by EKS Pod Identity webhook). LocalStack uses well-known dummy credentials (`test`/`test`), documented in `docker-compose.yml` only.
**Source**: SECURITY-12
**Verification**: CI grep for credential patterns; chart values.yaml review

### NFR-Z-050 — Fail-safe error handling
**Statement**: Every external call SHALL have explicit error handling. A top-level `recover` is installed at the worker entry point (`internal/sqs.Adapter.dispatch`). Uncovered panics propagate to the receive-loop's `recover` which logs FATAL and lets visibility lapse for SQS redrive (BR-DLQ-002).
**Source**: SECURITY-15, BR-CLEAN-001..003
**Verification**: Code-review checklist + unit test with synthetic panic

---

## 6. Observability

### NFR-Z-060 — Prometheus metrics
**Statement**: The service SHALL expose `/metrics` (in Prometheus exposition format) with the 6 metrics defined in FR-13.2:
- `zip_entries_total{status}` — counter
- `zip_extraction_duration_seconds{outcome}` — histogram (buckets: 1, 5, 15, 30, 60, 120, 180, 220, 240 s)
- `zip_extraction_failures_total{reason}` — counter
- `zip_bomb_rejections_total{rule}` — counter
- `extracted_bytes_total` — counter
- `partial_failures_total` — counter
Plus 2 additional operational metrics:
- `redelivery_skips_total` — counter (BR-IDEMPOTENCY-006)
- `slipsheet_write_failures_total` — counter (BR-SLIP-006)
**Source**: FR-13.2, BR-LOG / BR-IDEMPOTENCY / BR-SLIP
**Verification**: Unit test on metrics registration + Gate 2 scrape test

### NFR-Z-061 — Health endpoints
**Statement**: `/healthz/live` (always 200 after startup) and `/healthz/ready` (200 iff startup health checks passed AND no graceful-drain in progress) SHALL be served on port 8080 (Q3 of application design).
**Source**: FR-13.1
**Verification**: Helm chart probe configuration + integration test

### NFR-Z-062 — Recommended alert rules
**Statement**: Chart README documents recommended Prometheus alert rules for platform-team consumption:
- **AlertZipExtractionSloViolation**: `rate(zip_extraction_failures_total{reason!~"bomb-defence.*|unsupported.*"}[1h]) > 0.005 * rate(zip_entries_total[1h])` for 30 min
- **AlertZipExtractionLatencyP99**: `histogram_quantile(0.99, sum(rate(zip_extraction_duration_seconds_bucket[10m])) by (le)) > 230`
- **AlertZipDLQDepth**: `aws_sqs_approximate_number_of_messages_visible{queue=~".*zip-extraction-dlq"} > 5`
- **AlertZipBombSpike**: `rate(zip_bomb_rejections_total[5m]) > 0.01 * rate(zip_entries_total[5m])` (suspicious if > 1% of arriving archives are rejected as bombs)
- **AlertZipRedeliverySpike**: `rate(redelivery_skips_total[5m]) > 0.05 * rate(zip_entries_total[5m])` (high redelivery suggests downstream consumer or SQS infrastructure issue)
**Source**: SECURITY-14, NFR-5.5 of requirements
**Verification**: Chart README cross-reference

### NFR-Z-063 — Distributed tracing (not implemented in this unit)
**Statement**: This unit does NOT emit OpenTelemetry / Jaeger traces in v1. Trace correlation is achieved via the `correlationId` field in logs (BR-LOG-001). Future enhancement: add OTel SDK + Jaeger exporter — out of scope.
**Source**: NFR-5.1 of requirements
**Verification**: N/A (scope decision documented)

---

## 7. Maintainability

### NFR-Z-070 — Vulnerability scanning in CI
**Statement**: CI SHALL run `govulncheck ./...` on every push. Build FAILS on any HIGH or CRITICAL vulnerability in the dependency tree.
**Source**: SECURITY-10, Q6 of NFR plan
**Verification**: CI workflow inspection

### NFR-Z-071 — SBOM generation
**Statement**: CI SHALL generate a CycloneDX SBOM via `syft .` on each release tag. SBOM is uploaded as a GitHub Release asset (Q8 of NFR plan).
**Source**: SECURITY-10
**Verification**: CI workflow inspection

### NFR-Z-072 — Linting
**Statement**: `golangci-lint run` SHALL pass on every push with the curated rule set documented in `.golangci.yml`:
- `errcheck`, `govet`, `staticcheck`, `ineffassign`, `gocritic`, `gosec`, `unused`, `unparam`, `unconvert`, `gofmt`, `goimports`
**Source**: Q6 of NFR plan
**Verification**: CI workflow inspection

### NFR-Z-073 — Test coverage
**Statement**: Unit test coverage ≥ **80 %** of statements (`go test -cover ./...`). PBT properties are part of the test corpus.
**Source**: NFR-9.1 of requirements
**Verification**: CI threshold gate

### NFR-Z-074 — Code formatting & imports
**Statement**: `gofmt -s` and `goimports` MUST be clean on every push. Enforced by `pre-commit` hook + CI.
**Source**: Q6 of NFR plan
**Verification**: CI workflow inspection

### NFR-Z-075 — Documentation
**Statement**: The repository SHALL ship:
- `README.md` at `services/zip-extraction/` covering: purpose, local dev quick-start (`make up && make bootstrap`), env vars table, YAML config schema, observability summary, deployment summary
- `services/zip-extraction/chart/README.md` covering: IRSA setup, NetworkPolicy egress allowlist, recommended HPA / topologySpread / Prometheus alert rules
- Godoc on every exported symbol (enforced by `golangci-lint` rule `revive` set to `exported`)
**Source**: maintainability + Q9 of requirements
**Verification**: Build & Test stage check

---

## 8. Reproducibility & PBT

### NFR-Z-080 — PBT framework
**Statement**: Property-based tests SHALL use `pgregory.net/rapid`. Framework supports shrinking + seed-based reproducibility.
**Source**: PBT-09, §23 input spec, Q7 of NFR plan
**Verification**: go.mod pin

### NFR-Z-081 — Seed logging
**Statement**: Every PBT run SHALL log the seed on failure (built into `rapid`'s default test output). CI workflow MAY pin a seed for deterministic runs via `RAPID_SEED` env var; default is randomised seed with seed logged.
**Source**: PBT-08
**Verification**: CI workflow inspection + sample failure run

### NFR-Z-082 — PBT in CI
**Statement**: PBT runs in the same CI job as example-based tests via `go test ./...`. PBT failures fail CI. No skipping / quarantine of failing PBT properties.
**Source**: PBT-08, PBT-10
**Verification**: CI workflow inspection

---

## 9. Local-Production Parity (Cross-Cutting)

### NFR-Z-090 — Single binary, env-driven configuration
**Statement**: One binary serves both LocalStack-based local dev and production EKS. The only legitimate per-environment differences are:
- Env vars (`AWS_ENDPOINT_URL`, `LOG_FORMAT`, queue URL, bucket name, table name, region)
- Tunable YAML values (`maxInFlight`, retry counts, bomb thresholds)
NO code paths branch on environment.
**Source**: Q1 of requirements (security parity), local-prod analysis in `services.md`
**Verification**: Code-review checklist + grep for `os.Getenv("ENV")` or equivalent

### NFR-Z-091 — Identical dependency graph in both environments
**Statement**: The Go-package-level dependency graph (per `component-dependency.md`) is IDENTICAL in local and production. Only the runtime values passed to `awsclients.Build(ctx, cfg.Infra)` differ.
**Source**: §6 of `application-design.md`
**Verification**: Dependency graph generation + diff (no env-conditional imports)

---

## 10. Cross-Reference Matrix — Project NFR-1..11 → Unit NFR-Z-NNN

| Project NFR (requirements.md) | Unit NFR-Z entries |
|---|---|
| **NFR-1 Performance** | NFR-Z-010, NFR-Z-011, NFR-Z-012, NFR-Z-013 |
| **NFR-2 Memory bounds** | NFR-Z-014, NFR-Z-003 |
| **NFR-3 Streaming I/O only** | NFR-Z-014 |
| **NFR-4 Reliability** | NFR-Z-020, NFR-Z-021, NFR-Z-022, NFR-Z-030, NFR-Z-031, NFR-Z-032, NFR-Z-033, NFR-Z-034 |
| **NFR-5 Observability** | NFR-Z-060, NFR-Z-061, NFR-Z-062, NFR-Z-063, NFR-Z-042 |
| **NFR-6 Security (SECURITY-01..15)** | NFR-Z-040 … NFR-Z-050 |
| **NFR-7 Config schema** | (covered by FR-14 + tech-stack-decisions.md) |
| **NFR-8 PBT** | NFR-Z-080, NFR-Z-081, NFR-Z-082 |
| **NFR-9 Testing gates** | NFR-Z-073, NFR-Z-082 |
| **NFR-10 Build & deployment** | NFR-Z-070, NFR-Z-071, NFR-Z-046, NFR-Z-047 |
| **NFR-11 Repo structure** | NFR-Z-075 |

---

## 11. SLI Definitions (for Platform Team's Dashboard Wiring)

| SLI | PromQL |
|---|---|
| Success rate (28 d, SLO denominator) | `1 - (sum(rate(zip_extraction_failures_total{reason!~"bomb-defence.*\|unsupported.*"}[28d])) / sum(rate(zip_entries_total[28d])))` |
| P95 latency (small/medium archives) | `histogram_quantile(0.95, sum(rate(zip_extraction_duration_seconds_bucket[28d])) by (le))` |
| P99 latency | `histogram_quantile(0.99, sum(rate(zip_extraction_duration_seconds_bucket[28d])) by (le))` |
| Bomb-rejection rate | `sum(rate(zip_bomb_rejections_total[1h])) / sum(rate(zip_entries_total[1h]))` |
| Redelivery rate | `sum(rate(redelivery_skips_total[1h])) / sum(rate(zip_entries_total[1h]))` |
| Slipsheet-write failure rate | `sum(rate(slipsheet_write_failures_total[1h])) / sum(rate(zip_entries_total[1h]))` |

---

## 12. Compliance Summary

- **SECURITY-01 … SECURITY-15**: every applicable rule maps to one or more NFR-Z entries (NFR-Z-040 … NFR-Z-050 cluster). The 3 N/A determinations from `requirements.md` (SECURITY-02, SECURITY-04, SECURITY-08) remain N/A unchanged.
- **PBT-01 … PBT-10**: NFR-Z-080..082 establish framework selection (PBT-09), seed reproducibility (PBT-08), and CI integration (PBT-08, PBT-10). The property-identification work itself lives in `component-methods.md` (PBT-01) and `business-rules.md` cross-references.
- **All 8 NFR-plan Q&A** (Q1 throughput, Q2 HPA, Q3 resources, Q4 SLOs, Q5 multi-AZ, Q6 tools, Q7 libraries, Q8 CI) are reflected in the catalogue above.

**No blocking SECURITY or PBT findings at the NFR Requirements stage.**
