# Code Generation Plan — zip-extraction (UOW-SVC-12)

**Document Type**: Code Generation Plan (Part 1 — Planning)
**Project**: Zip Extraction Service (UOW-SVC-12)
**Unit**: `zip-extraction` (single unit)
**Phase**: CONSTRUCTION — Code Generation (Plan)
**Generated**: 2026-05-24
**Source Inputs**: All prior INCEPTION + CONSTRUCTION artefacts (requirements, application-design, functional-design, NFR-requirements, NFR-design, infrastructure-design)
**Target Code Location**: `services/zip-extraction/` (workspace root sub-path per `aidlc-state.md` + §1/§26 of input spec)

---

## Purpose

This document is the **authoritative, sequential step list** for generating all artefacts of the Zip Extraction Service. After user approval of this plan, **Part 2 (Generation)** executes each step in order. Every step has a checkbox `[ ]` that is flipped to `[x]` immediately after completion. **No deviation from the plan is permitted during execution** per workflow rules.

---

## Unit Context

| Property | Value |
|---|---|
| Unit ID | UOW-SVC-12 |
| Service Name | Zip Extraction Service |
| Language | Go 1.24 |
| Project Type | Greenfield single unit |
| Target Directory | `services/zip-extraction/` |
| Documentation Output | `aidlc-docs/construction/zip-extraction/code/` |
| Story Mapping | N/A (User Stories stage skipped — see execution-plan.md; the FR/NFR catalogue in `requirements.md` is the authoritative requirement source) |
| Dependencies on other units | None (single-unit service) |
| Public surface | SQS consumer (input), S3 PutObject events on `input/` prefix (output), HTTP `/healthz/{live,ready}` + `/metrics` (operational) |

---

## Code Location Rules (Reaffirmed)

- **Application code** → `services/zip-extraction/...` in workspace root. **NEVER** under `aidlc-docs/`.
- **Documentation summaries** → `aidlc-docs/construction/zip-extraction/code/` (markdown only).
- **Build/config files** (Dockerfile, Makefile, .golangci.yml, .github/) → workspace root or `services/zip-extraction/` as appropriate.

---

## Plan Group Index

| Group | Topic | Steps |
|---|---|---|
| A | Project skeleton + build infrastructure | 1–8 |
| B | Configuration / logging / AWS clients | 9–11 |
| C | Domain layer (no I/O) | 12–18 |
| D | Adapter layer (I/O) | 19–22 |
| E | Orchestrator + entry point | 23–25 |
| F | Test infrastructure (PBT generators) | 26 |
| G | Unit tests per package | 27–37 |
| H | Gate 2 integration tests | 38 |
| I | Local-dev deploy artefacts | 39–41 |
| J | Helm chart | 42–49 |
| K | CI workflows + dependency management | 50–52 |
| L | Documentation | 53–55 |

---

## Plan Steps (Part 2 Will Execute These In Order)

### Group A — Project Skeleton + Build Infrastructure

- [x] **Step 1 — Workspace skeleton & `go.mod`**
  - Create `services/zip-extraction/` directory tree (`cmd/zip-extraction/`, `internal/{app,sqs,extraction,bombdefence,validation,storage,dynamodb,slipsheet,retry,metrics,health,config,awsclients,log}/`, `test/{e2e,generators,testdata}/`, `chart/`, `deploy/`, `tools/`, `.github/workflows/`).
  - Create `go.mod` with module path (`github.com/<org>/doc-uploader/services/zip-extraction` placeholder — operator can rename) and `go 1.24` directive.
  - Add initial dependencies: `aws-sdk-go-v2` (core + sqs + s3 + dynamodb + manager), `go.uber.org/zap`, `prometheus/client_golang`, `pgregory.net/rapid`, `gopkg.in/yaml.v3`, `stretchr/testify`, `testcontainers/testcontainers-go`.

- [x] **Step 2 — `.gitignore` + `.dockerignore`**
  - `.gitignore`: bin/, *.pprof, coverage.out, sbom.cyclonedx.json, .idea/, .vscode/, *.local.yaml
  - `.dockerignore`: .git/, aidlc-docs/, *_test.go, test/, .github/, README*.md, Makefile, .golangci.yml

- [x] **Step 3 — `Dockerfile`**
  - Multi-stage: `golang:1.24-bookworm@sha256:<digest>` builder → `gcr.io/distroless/static-debian12:nonroot@sha256:<digest>` final.
  - Multi-arch ready (`--platform=$BUILDPLATFORM` + `ARG TARGETOS TARGETARCH`).
  - Static build with `-trimpath -ldflags="-s -w"`, `CGO_ENABLED=0`.
  - `USER 65532:65532`; `ENTRYPOINT ["/app/zip-extraction"]`.
  - Digest values left as `<digest>` placeholders with comments instructing operators to pin via `docker manifest inspect`.

- [x] **Step 4 — `Makefile`**
  - Targets: `build`, `test`, `lint`, `vuln`, `sbom`, `up`, `down`, `bootstrap`, `run`, `docker`, `docker-multiarch`, `helm-template`, `pbt-replay`, `clean`.
  - Variables: `IMG_REPO`, `IMG_TAG`, `PLATFORMS`, `GO`, `GOLANGCI`, `GOVULN`, `SYFT`, `COSIGN`, `DOCKER`, `COMPOSE`, `HELM`.

- [x] **Step 5 — `.golangci.yml`**
  - Enable: errcheck, govet, staticcheck, ineffassign, gocritic, gosec, unused, unparam, unconvert, gofmt, goimports, revive (with `exported` rule).
  - Run-mode: timeout 5m; tests included.
  - Linter-specific configs: gosec excludes G104 in test files; revive exports-rule.

- [x] **Step 6 — `tools/go.mod` (pinned CLI tools)**
  - Tool-tracker pattern: separate go.mod imports `golang.org/x/vuln/cmd/govulncheck` and `github.com/golangci/golangci-lint/cmd/golangci-lint` at pinned versions.
  - Makefile `vuln` target invokes via `go run -modfile=tools/go.mod ...`.

- [x] **Step 7 — `dependabot.yml` + `renovate.json`**
  - `.github/dependabot.yml`: weekly Go modules + Dockerfile + GitHub Actions updates.
  - `renovate.json`: equivalent ruleset for cross-tool compatibility.

- [x] **Step 8 — Code-location summary doc**
  - `aidlc-docs/construction/zip-extraction/code/project-skeleton.md`: file-by-file summary of Group A artefacts (paths, purposes, key configuration choices).

---

### Group B — Configuration / Logging / AWS Clients

- [x] **Step 9 — `internal/log` package**
  - `logger.go`: `Logger` interface (`With`, `Info`, `Warn`, `Error`, `Debug`) backed by `*zap.Logger`.
  - `Field` type alias to `zap.Field`.
  - `New(cfg config.LoggingConfig, version string)` constructor.
  - Sensitive-field deny-list filter (BR-LOG-002).
  - `aidlc-docs/construction/zip-extraction/code/log.md` summary.

- [x] **Step 10 — `internal/config` package**
  - `config.go`: `Config`, `InfraConfig`, `StreamingConfig`, `SQSConfig`, `HTTPConfig`, `LoggingConfig`, `SSEConfig` structs.
  - `Load()`: read env vars + parse YAML via `yaml.NewDecoder(r).KnownFields(true)`.
  - `Validate()`: range / consistency checks; fail-fast on violation.
  - `aidlc-docs/construction/zip-extraction/code/config.md` summary.

- [x] **Step 11 — `internal/awsclients` package**
  - `awsclients.go`: `Set` struct (SQS, S3, DDB, S3Uploader); `Build(ctx, InfraConfig)` constructor.
  - Endpoint resolver factory for LocalStack endpoint override.
  - `aws.RetryModeAdaptive` enabled.
  - `aidlc-docs/construction/zip-extraction/code/awsclients.md` summary.

---

### Group C — Domain Layer (No I/O)

- [x] **Step 12 — `internal/extraction/types.go`**
  - Define domain types: `ClaimCheck`, `ArchiveMetadata`, `EntryInfo`, `EntryOutcome`, `PipelineFile`, `Outcome`, `Status` (+ `String()` method).

- [x] **Step 13 — `internal/extraction/errors.go`**
  - Define typed-error hierarchy: `BombDefenceError`, `PathValidationError`, `UnsupportedFeatureError`, `TransientError`, `PermanentError` with `Error()` + `Unwrap()` methods.
  - Define `Is*` classifier helper functions.

- [x] **Step 14 — `internal/extraction/ports.go`**
  - Define consumer-defined port interfaces: `S3Downloader`, `S3Uploader`, `Recorder`, `SlipsheetWriter`, `BombChecker`, `PathValidator`, `Retrier`, `Metrics`, `Logger`, `Clock`.
  - `aidlc-docs/construction/zip-extraction/code/extraction-types-errors-ports.md` summary.

- [x] **Step 15 — `internal/validation` package**
  - `validator.go`: `PathValidator` struct; `Sanitize(rawPath string) (safeName string, err error)`.
  - Path-traversal / absolute / drive-letter / control-char / empty / invalid-filename rejection per BR-PATH-001..004.
  - Idempotence guarantee.
  - `aidlc-docs/construction/zip-extraction/code/validation.md` summary.

- [x] **Step 16 — `internal/bombdefence` package**
  - `checker.go`: `Config` struct; `Checker` struct; `New(cfg) *Checker`; `PreCheck(meta)`; `EntryCheck(idx, EntryInfo)`.
  - `limited_reader.go`: `NewLimitedReader(r, compressedSize) io.Reader` returning a short-circuiting implementation enforcing rules #2 + #3 per BR-BOMB-003 / 004.
  - `aidlc-docs/construction/zip-extraction/code/bombdefence.md` summary.

- [x] **Step 17 — `internal/retry` package**
  - `retry.go`: `Config` struct; `Retrier` struct; `New(cfg, clock, rng, logger) *Retrier`; `Do(ctx, op)`.
  - `BackoffFor(attempt, cfg, jitter)` exported for PBT-05 oracle.
  - `Classify(err) (transient bool, class string)` per BR-RETRY-004..010 + BR-RETRY-014 table.
  - `aidlc-docs/construction/zip-extraction/code/retry.md` summary.

- [x] **Step 18 — `internal/slipsheet` package**
  - `slipsheet.go`: `Slipsheet`, `ChildEntry` structs; `Build(execID, sourceArchive, status, entries, archiveReason) Slipsheet`.
  - `marshal.go`: `Marshal(ss)` / `Unmarshal(b)` round-trip helpers (PBT-02).
  - `writer.go`: `Writer` struct; `NewWriter(uploader, cfg)`; `Write(ctx, ss)`.
  - `aidlc-docs/construction/zip-extraction/code/slipsheet.md` summary.

---

### Group D — Adapter Layer (I/O)

- [x] **Step 19 — `internal/storage` package** (S3 adapter)
  - `adapter.go`: `Adapter` struct; `NewAdapter(client, uploader, cfg)`; `Download(ctx, bucket, key)`; `Upload(ctx, bucket, key, body, sizeHint)`.
  - `mime.go`: `DetectMIME(peek, fileName)` hybrid implementation (BR-MIME-001).
  - `peek.go`: `peekReader(r, n)` returning `(peek, rebuilt, err)` without extra read pass.
  - `aidlc-docs/construction/zip-extraction/code/storage.md` summary.

- [x] **Step 20 — `internal/dynamodb` package**
  - `adapter.go`: `Adapter` struct; `NewAdapter(client, cfg)`; `RecordEntry(ctx, rec)`.
  - `marshal.go`: `Marshal(rec)` / `Unmarshal(av)` round-trip helpers (PBT-02).
  - Conditional PutItem with `attribute_not_exists(pk)` per BR-IDEMPOTENCY-002.
  - `ConditionalCheckFailedException` mapped to `nil` (idempotency); metric `redelivery_skips_total` incremented.
  - `aidlc-docs/construction/zip-extraction/code/dynamodb.md` summary.

- [x] **Step 21 — `internal/sqs` package**
  - `adapter.go`: `Adapter` struct; `NewAdapter(client, cfg, heart)`; `Run(ctx, handler)`.
  - `heartbeater.go`: `Heartbeater` interface; `heartbeater` struct; `Start(ctx, receiptHandle) cancel`.
  - `parse.go`: `parseMessage(raw) (ClaimCheck, error)` with schema validation.
  - Bounded worker-pool via semaphore channel.
  - Graceful drain on root-context cancellation.
  - SQS message disposition table per BR-DLQ-001..003.
  - `aidlc-docs/construction/zip-extraction/code/sqs.md` summary.

- [x] **Step 22 — `internal/metrics` + `internal/health` packages**
  - `metrics.go`: `Metrics` struct with 8 typed methods (FR-13.2 + redelivery + slipsheet-write-failures); registration on `prometheus.DefaultRegisterer`.
  - `health/gate.go`: `Gate` struct with atomic readiness flag.
  - `health/server.go`: `Server` struct; `NewServer(port, gate)`; `Start(ctx)`; `Shutdown(ctx)`.
  - HTTP handlers for `/healthz/live`, `/healthz/ready`, `/metrics`.
  - `aidlc-docs/construction/zip-extraction/code/metrics-health.md` summary.

---

### Group E — Orchestrator + Entry Point

- [x] **Step 23 — `internal/extraction` orchestrator (Service.Process)**
  - `service.go`: `Service` struct + `Dependencies` struct; `New(deps)`; `Process(ctx, msg)`.
  - State machine implementation per `business-logic-model.md` §1.
  - Per-entry pipeline per `business-logic-model.md` §3 (path validation → bomb entry-check → wrap stream → upload → record).
  - End-only slipsheet write via `defer` (BR-SLIP-002 / 003).
  - Cleanup via `defer` (BR-CLEAN-001..003).
  - Extraction-context with `WithTimeout(MaxExtractionDurationSec)` per BR-BOMB-005 rule #10.
  - `aidlc-docs/construction/zip-extraction/code/extraction.md` summary.

- [x] **Step 24 — `internal/app` orchestrator**
  - `service.go`: `Service` struct + `Config` + `Dependencies`; `New(cfg, deps)`; `Run(ctx)`.
  - Startup health checks (SQS / S3 / DDB reachability); flip `HealthGate.SetReady(true)` on success.
  - Graceful drain (Q7 of application design) with `gracefulShutdownTimeoutSec` deadline.
  - `aidlc-docs/construction/zip-extraction/code/app.md` summary.

- [x] **Step 25 — `cmd/zip-extraction/main.go`**
  - Process bootstrap per DI wiring sketch in `logical-components.md` §3.
  - SIGTERM / SIGINT root-context cancellation.
  - SIGUSR1 → `runtime/pprof.WriteHeapProfile(...)` to `/tmp/heap-<RFC3339>.pprof` (per Q4 of NFR design).
  - Top-level `defer recover()` for fail-loud panic handling.
  - `aidlc-docs/construction/zip-extraction/code/main.md` summary.

---

### Group F — Test Infrastructure

- [x] **Step 26 — `test/generators/` (PBT generator catalogue per PBT-07)**
  - `claimcheck.go`: `gens.ClaimCheck()` + variants (Filter, Invalid).
  - `archive.go`: `gens.ArchiveMetadata()` + `.Bomb(rule)`.
  - `entry.go`: `gens.EntryInfo()` + `.Bomb(rule)`.
  - `paths.go`: `gens.RawPath()` + `.Traversal()` + `.Absolute()`.
  - `outcome.go`: `gens.EntryOutcome()`.
  - `pipelinefile.go`: `gens.PipelineFile()`.
  - `slipsheet.go`: `gens.Slipsheet()`.
  - `aws_errors.go`: `gens.SDKError(class)`.
  - `aidlc-docs/construction/zip-extraction/code/generators.md` summary.

---

### Group G — Unit Tests per Package

- [x] **Step 27 — `internal/validation` tests (unit + PBT)** — BR-PATH-001..006 properties.
- [x] **Step 28 — `internal/bombdefence` tests (unit + PBT)** — BR-BOMB-001..008 invariants, including LimitedReader short-circuit property.
- [x] **Step 29 — `internal/retry` tests (unit + PBT)** — BR-RETRY-014 classifier table + PBT-05 oracle + stateful PBT-06.
- [x] **Step 30 — `internal/slipsheet` tests (unit + PBT)** — round-trip property + Build invariants.
- [x] **Step 31 — `internal/dynamodb` tests (unit + PBT)** — marshal round-trip + idempotency.
- [x] **Step 32 — `internal/storage` tests (unit + PBT)** — `DetectMIME` oracle property + multipart routing assertion.
- [x] **Step 33 — `internal/sqs` tests (unit + stateful PBT)** — heartbeat lifecycle invariant + parseMessage schema rejection.
- [x] **Step 34 — `internal/extraction` tests (unit + stateful PBT)** — `computeStatus` truth table + state-machine generated sequences + re-delivery idempotency.
- [x] **Step 35 — `internal/config` tests (unit + PBT)** — YAML round-trip + `Validate` negative cases.
- [x] **Step 36 — `internal/log` tests (unit + PBT)** — sensitive-field redaction invariant.
- [x] **Step 37 — `internal/{health,metrics,awsclients,app}` tests (unit)** — example-based HTTP / collector registration / endpoint-resolver / lifecycle smoke tests.

---

### Group H — Gate 2 Integration Tests

- [x] **Step 38 — `test/e2e/` (Testcontainers + LocalStack)**
  - `localstack_test.go`: Testcontainers harness spinning up LocalStack with `SERVICES=sqs,s3,dynamodb,sts`.
  - Helper functions: `provisionBucket`, `provisionQueueWithDLQ`, `provisionTable` mirroring `make bootstrap` logic.
  - Tests: happy-path SUCCESS, bomb-defence FAILED, path-traversal FAILED, unsupported-feature FAILED, transient-then-retry PARTIAL_FAILED, redelivery idempotency.
  - `aidlc-docs/construction/zip-extraction/code/e2e-tests.md` summary.

---

### Group I — Local-Dev Deploy Artefacts

- [x] **Step 39 — `deploy/docker-compose.yml`**
  - Services: `localstack` (pinned digest, ports 4566 exposed), `zip-extraction` (built from local Dockerfile, depends_on localstack, env vars pointing at LocalStack endpoint, stop_grace_period 260s).
- [x] **Step 40 — `deploy/bootstrap-localstack.sh`**
  - Bash script (idempotent) that runs `aws --endpoint-url=http://localhost:4566 ...` to create staging bucket, source bucket, SQS main+DLQ with redrive, DDB table with the correct schema.
- [x] **Step 41 — `deploy/config-local.yaml`**
  - YAML config file mirroring `chart/values.yaml` `bombDefence/streaming/retry/sqs` keys; consumed by `make run`.

---

### Group J — Helm Chart

- [x] **Step 42 — `chart/Chart.yaml`** — chart metadata.
- [x] **Step 43 — `chart/values.yaml`** — canonical defaults per `deployment-architecture.md` §2.2.
- [x] **Step 44 — `chart/values-sandbox.yaml`** + `values-staging.yaml` + `values-prod.yaml` overlay files.
- [x] **Step 45 — `chart/templates/_helpers.tpl`** — standard Helm helpers (name, fullname, labels, selectorLabels, serviceAccountName).
- [x] **Step 46 — `chart/templates/deployment.yaml`** — Deployment manifest per `deployment-architecture.md` §2.4.
- [x] **Step 47 — `chart/templates/service.yaml` + `configmap.yaml`** — ClusterIP Service + ConfigMap rendering the YAML config.
- [x] **Step 48 — `chart/templates/serviceaccount.yaml`** — SA with IRSA annotation.
- [x] **Step 49 — `chart/README.md`** — platform-team integration guide (HPA / NetworkPolicy / VPC endpoints / IRSA policy / alerts / SLI dashboard / S3 bucket policy / SQS redrive).

---

### Group K — CI Workflows + Dependency Management

- [x] **Step 50 — `.github/workflows/ci.yml`** — push/PR workflow: lint + test + coverage gate + govulncheck.
- [x] **Step 51 — `.github/workflows/release.yml`** — tag-triggered: multi-arch buildx + SBOM + cosign keyless OIDC sign + in-toto attestation + bot PR to bump sandbox digest.
- [x] **Step 52 — `aidlc-docs/construction/zip-extraction/code/ci-workflows.md`** — CI summary doc.

---

### Group L — Documentation

- [x] **Step 53 — `services/zip-extraction/README.md`** — service-level overview: purpose, architecture summary, env-var table, YAML config schema, local-dev quick-start, deployment summary, observability notes.
- [x] **Step 54 — Per-package Godoc**
  - Ensure every exported symbol has Godoc comment (enforced by `golangci-lint` `revive`'s `exported` rule). Already included in steps 9–25 — Step 54 is a final sweep to verify nothing is missing.
- [x] **Step 55 — `aidlc-docs/construction/zip-extraction/code/index.md`**
  - Master index linking all per-package code summary docs (~20 files), the project-skeleton summary, the generators summary, e2e-tests summary, and ci-workflows summary.

---

## Story Traceability

Since User Stories stage was SKIPPED (see `execution-plan.md`), traceability is via FR/NFR and business-rule (BR-*) identifiers from `requirements.md`, `nfr-requirements.md`, and `business-rules.md`. Each Go file's Godoc header references the FR / NFR / BR identifiers it implements. The per-package code summary docs (in `aidlc-docs/construction/zip-extraction/code/`) maintain the mapping table.

---

## Execution Notes for Part 2

- **Sequential by step**. No parallel steps. Each completed step's `[ ]` becomes `[x]` immediately.
- **No deviation**. If a discovered constraint forces a deviation, surface it to the user (Step 12 of code-generation.md: "Mark completed step + update aidlc-state.md") before continuing.
- **File-modification semantics**. This is greenfield — every file is a new creation. No in-place modification of existing application code.
- **Documentation in lockstep**. Each package code-generation step is paired with its summary doc under `aidlc-docs/construction/zip-extraction/code/`.
- **Tests after code**. Group G unit tests come after Groups B–E source code generation. This is intentional — generating tests against not-yet-existing types is impractical even though tests reference the types.
- **Total files expected**: ~80 Go files (incl. tests + generators) + 5 chart templates + 3 chart values overlays + ~4 deploy/helper files + 2 CI workflows + Dockerfile + Makefile + .golangci.yml + go.mod + go.sum (generated) + 2 dep-bot configs + 1 service README + ~20 code summary docs.

---

## Approval

After plan approval, Part 2 executes the 55 steps above sequentially. Plan-level checkbox progress is tracked **in this file**; project-level progress is tracked in `aidlc-docs/aidlc-state.md` per the workflow rule "Two-Level Checkbox Tracking System".

> **Approval options for the user:**
>
> 🔧 **Request Changes** — Modify the plan structure, add/remove steps, change order, scope down.
> ✅ **Approve & Begin Generation** — Lock the 55 steps as-is and proceed to Part 2 execution.
