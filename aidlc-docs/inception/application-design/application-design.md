# Application Design — Zip Extraction Service (UOW-SVC-12)

**Document Type**: Consolidated Application Design Overview
**Phase**: INCEPTION — Application Design (Part 2: Generation)
**Generated**: 2026-05-24
**Status**: Awaiting user approval

This document is the **consolidated, single-file view** of the application design. The detail documents that fed into it remain authoritative:

- `components.md` — full component inventory and public Go interfaces
- `component-methods.md` — full method signatures and PBT property tables per component
- `services.md` — service-layer lifecycles and sequence/state diagrams
- `component-dependency.md` — dependency matrix, layered view, and data-flow diagram

Each section below summarises the corresponding detail document and points to it for the full content.

---

## 1. Design Premise

| Topic | Decision |
|---|---|
| Project type | Greenfield single-component microservice (Go 1.24) |
| Repository root for code | `services/zip-extraction/` (per spec §1 / NFR-11) |
| Top-level architecture | One Go module with `cmd/zip-extraction` entry point and ~15 internal packages; one runtime pod with three in-process services (App / SQS consumer / HTTP operational server) |
| Extension constraints in force | SECURITY-01 … SECURITY-15 (Full, blocking) + PBT-01 … PBT-10 (Full, blocking) |
| Authoritative requirements | `aidlc-docs/inception/requirements/requirements.md` (16 FRs, 11 NFRs) |
| Question gates resolved | Requirement Q1–Q12 + Application Design Q1–Q8 (all answered with recommended options) |

---

## 2. Component Inventory (Summary of `components.md`)

Fifteen components — one per Go internal package plus `cmd/zip-extraction`. The two security-critical components (`bombdefence`, `validation`) are deliberately separate (Q8) and depend on nothing project-internal except shared error types.

| # | Package | Role |
|---|---|---|
| 1 | `cmd/zip-extraction` | Entry point — config load, AWS client construction, wiring, signal handling |
| 2 | `internal/app` | Top-level orchestrator coordinating the SQS consumer, HTTP server, and graceful shutdown |
| 3 | `internal/sqs` | SQS long-poll receive-loop + bounded worker pool + per-message visibility heartbeat (FR-9) |
| 4 | `internal/extraction` | Core domain orchestrator: download → bomb pre-check → per-entry loop → slipsheet → status. Defines all consumer-facing port interfaces and the typed-error hierarchy (Q4) |
| 5 | `internal/bombdefence` | 10-rule defence + short-circuiting `LimitedReader` (Q5) for cumulative-size + ratio rules |
| 6 | `internal/validation` | Entry path sanitisation (rules #7, #8) — pure, idempotent |
| 7 | `internal/storage` | S3 adapter (streaming `GetObject`, multipart `PutObject` >5 MiB, hybrid MIME detection per Q6) |
| 8 | `internal/dynamodb` | DynamoDB adapter — idempotent `PutItem` with `attribute_not_exists(pk)` (FR-5.3) |
| 9 | `internal/slipsheet` | Slipsheet builder + writer (FR-8, separate `slipsheets/` prefix) |
| 10 | `internal/retry` | Classifier-driven exponential-backoff retry (FR-12, Q12, PBT-05 oracle) |
| 11 | `internal/metrics` | Prometheus collectors for the 6 metrics in FR-13.2 |
| 12 | `internal/health` | HTTP server on a single port (Q3) serving `/healthz/{live,ready}` + `/metrics` |
| 13 | `internal/config` | Env + YAML loader with strict-decode validation (NFR-7) |
| 14 | `internal/awsclients` | AWS SDK v2 client factory; LocalStack endpoint override when `AWS_ENDPOINT_URL` set |
| 15 | `internal/log` | `zap`-backed logger factory; format selectable via `LOG_FORMAT` (Q10); deny-list for sensitive fields (SECURITY-03) |

**SECURITY ownership map**: SECURITY-01 → `awsclients`, `storage`, `dynamodb`; SECURITY-03 → `log`; SECURITY-05 → `extraction`, `validation`, `bombdefence`; SECURITY-09 → `health`, `cmd`; SECURITY-11 → `app` + isolation of `bombdefence` + `validation`; SECURITY-14 → `metrics`; SECURITY-15 → `extraction`, `cmd`.

See `components.md` for the full public interface listing per component.

---

## 3. Method Surface (Summary of `component-methods.md`)

Each component exposes a small, focused API. Highlights:

- **`extraction.Service.Process(ctx, msg) (Outcome, error)`** is the only domain entry point a worker invokes per SQS message.
- **All ports are consumer-defined** in `internal/extraction` (Q1) — `S3Downloader`, `S3Uploader`, `Recorder`, `SlipsheetWriter`, `BombChecker`, `PathValidator`, `Retrier`, `Metrics`, `Logger`, `Clock`. Adapters in `storage` / `dynamodb` / `slipsheet` / `bombdefence` / `validation` / `retry` / `metrics` / `log` implement these ports.
- **Typed-error hierarchy** (Q4): `BombDefenceError{Rule, Reason}`, `PathValidationError{Path, Reason}`, `UnsupportedFeatureError{Feature}`, `TransientError{Cause, Class}`, `PermanentError{Cause}`. `Is*` helper functions are provided so callers do not type-assert in handler code.
- **PBT-01 carry-forward**: every domain component has a "Testable Properties" subsection enumerating Round-trip / Invariant / Idempotence / Stateful / Oracle properties to be carried into Functional Design. Components without identifiable properties are explicitly marked "No PBT properties identified."

See `component-methods.md` for full signatures + the per-component property tables.

---

## 4. Service Layer (Summary of `services.md`)

Three runtime services run in a single pod:

### App Service (`internal/app`)
Coordinates lifecycle: construction → startup health checks → readiness flip → steady state → graceful drain → terminate.

### SQS Consumer Service (`internal/sqs`)
Per Q2 — **one** long-poll receiver goroutine pulling batches of up to 10 messages, dispatching each to a bounded worker pool sized by `sqs.maxInFlight` (default 5). Each worker spawns one heartbeat goroutine that extends visibility every 30 s. Total steady-state goroutines: `1 receiver + N workers + N heartbeats ≤ 11` for default `N=5`.

### HTTP Operational Server (`internal/health`)
Per Q3 — **one** HTTP server on port 8080 serving:
- `/healthz/live` → 200 OK once process running
- `/healthz/ready` → 200 OK iff `HealthGate.Ready() == true`
- `/metrics` → `promhttp.Handler()`

### Graceful shutdown (Q7)
On SIGTERM:
1. Cancel root context.
2. `HealthGate.SetReady(false)` — Kubernetes Service controller removes the pod from rotation.
3. SQS receive-loop stops calling `ReceiveMessage`; in-flight workers continue.
4. Heartbeat goroutines keep extending visibility during drain.
5. Wait up to `gracefulShutdownTimeoutSec` (default 250 s) for workers to complete.
6. Shut down HTTP server.
7. Exit process. Any still-in-flight messages are reclaimed by SQS visibility timeout — duplicate effect prevented by FR-5.3 idempotency.

`services.md` contains the Mermaid sequence diagrams for normal flow + shutdown, the startup-ordering flowchart, and the HTTP-server state diagram.

---

## 5. Dependencies and Data Flow (Summary of `component-dependency.md`)

### Layered architecture (top → bottom)

1. **Entry**: `cmd/zip-extraction` (universal importer; wires everything once).
2. **Orchestrator**: `internal/app`.
3. **Domain (pure / no I/O)**: `extraction`, `bombdefence`, `validation`, `retry`, `slipsheet`. Define ports.
4. **Adapters (I/O boundary)**: `sqs`, `storage`, `dynamodb`, `metrics`, `health`. Implement ports.
5. **Infra**: `awsclients`, `log`, `config`. Universally consumable leaves.

### No cycles, two pure-security leaves

`bombdefence` and `validation` depend on nothing except stdlib + `extraction`'s typed-error symbols. This makes them maximally testable with PBT (no mocks needed) and aligns with SECURITY-11.

### Communication patterns

- In-process synchronous calls through interface dispatch (domain ↔ adapter)
- Goroutines + cancellation contexts (receive-loop, workers, heartbeats, HTTP server)
- Bounded semaphore (worker-pool size = `sqs.maxInFlight`)
- HTTPS for all AWS SDK egress (LocalStack endpoint override is the only legitimate non-HTTPS scheme and is local-only)

See `component-dependency.md` for the full matrix, layered Mermaid view, per-message data-flow sequence diagram, and the local-vs-production boundary table.

---

## 6. Local-Production Parity

All design decisions preserve the parity principle the user established in Requirement Verification Q1 (Security extension opt-in for "same code, local + prod"). The dependency graph is **identical** in both environments; the only legitimate per-environment differences are:

| Per-env? | What | Where |
|---|---|---|
| Yes | `AWS_ENDPOINT_URL` (LocalStack vs AWS) | Env var |
| Yes | `LOG_FORMAT` (`console` vs `json`) | Env var |
| Yes | `cfg.MaxInFlight`, retry counts, bomb thresholds | YAML ConfigMap / local file |
| Yes | Container runtime (Compose vs Deployment) | Deployment artefacts |
| No | All code paths in `cmd/zip-extraction` and `internal/*` | Same binary |

This means Gate 2 (Testcontainers + LocalStack) exercises the exact same logic that runs in production — including the goroutine topology, error hierarchy, retry classifier, and drain sequence.

---

## 7. SECURITY / PBT Compliance Summary

### SECURITY Compliance (extension-baseline rules)

| Rule | Status | Where addressed |
|---|---|---|
| SECURITY-01 Encryption at rest & in transit | Compliant | `awsclients` (TLS-by-default), `storage`/`dynamodb` use HTTPS; bucket SSE + non-TLS-deny policy documented for platform team |
| SECURITY-02 Access logging on network intermediaries | N/A | No LB/API gateway/CDN in service scope |
| SECURITY-03 Application-level logging | Compliant | `internal/log` with structured fields + sensitive-field deny-list |
| SECURITY-04 HTTP security headers | N/A | No HTML-serving endpoints |
| SECURITY-05 Input validation on API parameters | Compliant | `sqs.parseMessage` (schema validation), `validation.Sanitize` (path), `bombdefence.PreCheck/EntryCheck` (archive metadata) |
| SECURITY-06 Least-privilege access policies | Compliant | IRSA role shape documented in `services.md` and the Helm chart README; no wildcards |
| SECURITY-07 Restrictive network configuration | Compliant (partial) | Service has no inbound external traffic; egress allowlist documented for platform-team NetworkPolicy |
| SECURITY-08 Application-level access control | N/A | No human-facing routes |
| SECURITY-09 Hardening & misconfiguration | Compliant | Non-root, RO root FS, no debug routes, generic error responses |
| SECURITY-10 Software supply chain | Compliant | go.sum lockfile; CI vuln-scan + SBOM steps documented (Build & Test stage) |
| SECURITY-11 Secure design | Compliant | `bombdefence` + `validation` isolated; defence in depth (path + bomb + IAM + bucket policy); design documents misuse cases |
| SECURITY-12 Authentication & credential management | Compliant (partial) | IRSA in prod; LocalStack dummy creds for local only |
| SECURITY-13 Software & data integrity | Compliant | Stdlib `archive/zip`; ZIP treated as untrusted and defended; CI/CD posture documented for platform team |
| SECURITY-14 Alerting & monitoring | Compliant | Metrics + recommended alert rules documented for platform integration |
| SECURITY-15 Exception handling & fail-safe | Compliant | Typed errors, top-level recover, `defer` cleanup, fail-closed semantics |

**No blocking SECURITY findings** at the Application Design stage.

### PBT Compliance (extension-property-based-testing rules)

| Rule | Status | Where addressed |
|---|---|---|
| PBT-01 Property identification during design | Compliant | Every domain component in `component-methods.md` has a "Testable Properties" subsection. Components with no identifiable properties are explicitly marked "No PBT properties identified" |
| PBT-02 Round-trip properties | Compliant (design) | Identified for `slipsheet` JSON, `dynamodb` marshal/unmarshal, `storage` S3 key parse/format, `config` YAML, `extraction.Process` re-delivery idempotency |
| PBT-03 Invariant properties | Compliant (design) | Identified for `bombdefence` size/ratio bounds, `validation` post-conditions, `extraction.computeStatus`, `metrics` registered names, `config.Validate` |
| PBT-04 Idempotency properties | Compliant (design) | Identified for `validation.Sanitize`, `dynamodb.RecordEntry` under at-least-once delivery, `retry.Do` composability |
| PBT-05 Oracle and model-based testing | Compliant (design) | `retry.BackoffFor` closed-form formula as oracle; `bombdefence.EntryCheck` depth via stdlib oracle |
| PBT-06 Stateful property testing | Compliant (design) | Identified for `sqs.Adapter` heartbeat lifecycle, `retry.Retrier` command sequences, `extraction` status transitions |
| PBT-07 Generator quality | Compliant (design) | Domain generators for `ClaimCheck`, `PipelineFile`, `Slipsheet`, `ArchiveMetadata`, `EntryInfo` will be centralised in `test/generators` (specified in `component-dependency.md` Section 7 hand-off) |
| PBT-08 Shrinking and reproducibility | Compliant (design) | `pgregory.net/rapid` selected (PBT-09); enables shrinking + seed reproducibility natively; CI seed logging will be wired in the Build & Test stage |
| PBT-09 Framework selection | Compliant | `pgregory.net/rapid` chosen per §23 of input spec |
| PBT-10 Complementary testing strategy | Compliant (design) | Test layout will separate `*_test.go` (example-based) from `*_prop_test.go` (PBT) — formalised in the Functional Design + Code Generation stages |

**No blocking PBT findings** at the Application Design stage.

---

## 8. Open Items Carried to Functional Design

These items are intentionally left for the per-unit **Functional Design** stage (CONSTRUCTION phase) where business-logic detail is the focus:

1. Full state-transition table for `extraction.Service.Process` (per-entry success / fail / bomb / unsupported transitions).
2. Concrete `rapid` test plan per property listed in `component-methods.md` Testable Properties tables.
3. Concrete error-classification table mapping every AWS SDK error code to `*TransientError` vs `*PermanentError`.
4. Detailed drain-deadline analysis: worst-case scenarios where the default 250 s drain window may be insufficient and the documented fallback behaviour.
5. Domain generator definitions in `test/generators` (PBT-07).
6. Decision on whether the FR-3.5 "nested archives uploaded opaquely" path needs any special MIME labelling (sniff vs. mark as `application/zip`).

---

## 9. Approval Gate

This design has been validated against:

- **Requirements completeness**: every FR-1 … FR-16 and NFR-1 … NFR-11 maps to one or more components above.
- **SECURITY rule coverage**: 12 applicable rules addressed with non-N/A components; 3 N/A determinations carried forward with documented rationale.
- **PBT rule coverage**: all 10 rules addressed; framework selected; property tables populated.
- **Q&A consistency**: all 8 design Q&A answers (Q1=A through Q8=A) reflected in the artefacts.
- **Local-production parity**: dependency graph identical in both environments; only legitimate variation is in injected runtime values (env vars + YAML).

Once approved, the workflow advances to **CONSTRUCTION → Functional Design** for the single unit UOW-SVC-12.
