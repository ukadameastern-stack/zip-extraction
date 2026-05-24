# Application Design Plan — Zip Extraction Service (UOW-SVC-12)

**Document Type**: Application Design Plan (Part 1 — Planning)
**Project**: Zip Extraction Service (UOW-SVC-12)
**Phase**: INCEPTION — Application Design (Plan)
**Generated**: 2026-05-24
**Source Inputs**:
- `aidlc-docs/inception/requirements/requirements.md`
- `aidlc-docs/inception/plans/execution-plan.md`
- `zip-extraction-service-input.md`

---

## Purpose

This document is **Part 1 of Application Design (Planning)**. It captures:

1. The checklist of design artefacts to be produced once questions are answered (Part 2 — Generation).
2. A small set of **targeted clarifying questions** for design decisions that are NOT already settled by the input spec or by the 12 requirement-verification answers. The intent is to obtain user direction on the remaining ambiguities so that the generated design artefacts are unambiguous.

Each question presents a recommended option (first option, marked **(Recommended)**) with rationale, alternatives, and an `[Answer]:` tag for your input. Reply with the letter of your choice (or use the `X` option to describe a custom answer).

---

## Part A — Execution Checklist (Part 2 Will Run After Answers Are Approved)

Once all `[Answer]:` tags below are completed and the plan is approved, the following artefacts will be generated under `aidlc-docs/inception/application-design/`:

- [x] **components.md** — Component inventory (each Go internal package as a logical component), responsibilities, public interfaces
- [x] **component-methods.md** — Method signatures per component (input/output types only; detailed business rules deferred to Functional Design)
- [x] **services.md** — Service-layer definitions (top-level orchestrator `app.Service`, the SQS consumer loop, the HTTP operational server) and how they coordinate
- [x] **component-dependency.md** — Dependency matrix between components, communication patterns, data-flow diagram (Mermaid)
- [x] **application-design.md** — Consolidated single-doc design overview combining the above
- [x] Validate design completeness and consistency against SECURITY-11 (separation of concerns, defence in depth) and PBT-01 (property identification carried forward as a hand-off to Functional Design)

---

## Part B — Clarifying Questions (Answer Required)

> **Format**: Each question has options A, B, C, … with option **A marked (Recommended)**. The recommendation reflects the model's best judgement given the requirements; the rationale explains the trade-offs so you can override if you disagree. Reply by editing `[Answer]:` with the letter (or `X` for "Other" plus a free-text description).

---

### Question 1 — Interface Boundaries for Ports (Testability vs. Simplicity)

How should the AWS-adjacent dependencies (S3, DynamoDB, SQS, MIME detection, clock) be exposed inside `internal/extraction` to enable PBT and unit tests without hitting real AWS?

A) **(Recommended)** Define narrow Go interfaces ("ports") in `internal/extraction` (e.g., `S3Uploader`, `Recorder`, `MessageQueue`, `Clock`) — concrete adapter implementations live in `internal/storage`, `internal/dynamodb`, `internal/awsclients`, `internal/sqs`. Production wiring constructs adapters; tests inject fakes/mocks.

B) Use the AWS SDK client types (`s3.Client`, `dynamodb.Client`, `sqs.Client`) directly throughout the extraction package; rely on Testcontainers + LocalStack for all testing (no interfaces).

C) Use a third-party DI framework (e.g., `google/wire` or `uber-go/fx`) to generate construction code.

X) Other

[Answer]: A

**Recommendation rationale**: Narrow consumer-defined interfaces are idiomatic Go ("accept interfaces, return structs"), keep unit tests fast and deterministic (no LocalStack needed for property tests of `bombdefence`, `validation`, `slipsheet`, `retry`), and satisfy PBT-07 generator quality by letting tests inject deterministic fake clients. Option B forces every unit test to spin up Testcontainers, slowing iteration and conflicting with PBT-08 reproducibility (Testcontainers introduce non-determinism). Option C adds tooling complexity for a service with ~10 packages — manual constructor DI is cleaner and `wire`'s code generation is overkill at this scope.

---

### Question 2 — SQS Receive-Loop Concurrency Pattern

How should the service consume messages from `zip-extraction-queue` concurrently while staying within the 128 MiB pod memory budget?

A) **(Recommended)** Single SQS long-poll goroutine pulling batches (max 10 messages per `ReceiveMessage` call, 20 s wait time) and dispatching each message to a bounded **worker pool** of size `sqs.maxInFlight` (default 5 per NFR-7 / FR-9). Workers handle full extraction + heartbeat + cleanup per message.

B) `N` receiver goroutines (N = `maxInFlight`), each calling `ReceiveMessage(MaxNumberOfMessages=1)` in its own loop and processing inline (no separate dispatcher).

C) Single goroutine, single message at a time (no concurrency within a pod; horizontal scaling only).

X) Other

[Answer]: A

**Recommendation rationale**: Single-receiver + worker-pool is the standard SDK pattern. Batched `ReceiveMessage(MaxNumberOfMessages=10)` halves SQS API costs and produces fewer empty-poll responses. The bounded worker pool gives a single tunable knob (`maxInFlight`) governing concurrent extractions and therefore memory pressure — crucial for the 128 MiB limit. Option B works but bills more API calls and complicates rate-limiting / circuit-breaking should we add them later. Option C is too conservative — it under-utilises the pod and inflates queue latency.

---

### Question 3 — HTTP Server Port Topology

The service must expose `/healthz/live`, `/healthz/ready`, and `/metrics`. How should these endpoints be served?

A) **(Recommended)** Single HTTP server on a single port (default **8080**) serving all three endpoints. Helm chart maps Service `targetPort: 8080`; Kubernetes liveness/readiness probes point at the same port.

B) Split: operational port for `/healthz/*` (default 8080) and a separate port for `/metrics` (default 9090) — common Prometheus convention.

C) UNIX socket for `/metrics` (used by a Prometheus sidecar); HTTP port for `/healthz/*`.

X) Other

[Answer]: A

**Recommendation rationale**: A single port keeps the Helm chart simple (one `containerPort`, one `Service`, one set of NetworkPolicy rules — see SECURITY-07). The endpoints have low contention with each other (probes are infrequent, metrics scraped every 15–30 s) so there is no operational benefit to splitting. Option B is a defensible convention but adds chart complexity for no real-world payoff at this scale. Option C overreaches.

---

### Question 4 — Error Classification Hierarchy

How should errors flow through the extraction pipeline so retry/non-retry decisions (FR-12) and status assignment (FR-10) are unambiguous?

A) **(Recommended)** A small typed-error hierarchy in `internal/extraction/errors.go`:
- `BombDefenceError{Rule int, Reason string}` — never retryable; archive → FAILED
- `PathValidationError{Path, Reason string}` — never retryable; archive → FAILED
- `UnsupportedFeatureError{Feature string}` — never retryable; archive → FAILED
- `TransientError{Cause error, Class string}` — retryable up to N attempts (FR-12)
- `PermanentError{Cause error}` — non-retryable per-entry failure; pipeline → PARTIAL_FAILED
The retry helper in `internal/retry` classifies AWS SDK errors (throttling, 5xx, timeouts → `TransientError`; 4xx, schema → `PermanentError`).

B) Use sentinel errors only (`ErrBombDefence`, `ErrPathInvalid`, etc.) and `errors.Is` for matching; no struct types.

C) Return AWS SDK error wrapping unchanged; classify at the top-level handler using `errors.As`.

X) Other

[Answer]: A

**Recommendation rationale**: Typed-error structs carry classification context (which bomb rule fired, which path was rejected, which retry class) — directly consumed by the slipsheet (FR-8.3 `failureReason`) and the metrics labels (FR-13.2 `zip_bomb_rejections_total{rule=…}`). This is also PBT-friendly: the path-validation invariant ("every rejected path produces a `PathValidationError` whose `.Reason` is one of the documented categories") is easy to state. Sentinel errors lose the context fields; raw SDK errors leak abstraction.

---

### Question 5 — Streaming Byte-Counting Pattern (Bomb Defence Rule #2 / #3)

Bomb-defence rules #2 (max extracted size) and #3 (max compression ratio) must be enforced incrementally during streaming. How is byte counting threaded into the read pipeline?

A) **(Recommended)** A reusable `LimitedReader` wrapper in `internal/bombdefence` that wraps `io.Reader` and returns a sentinel `BombDefenceError` the moment the configured cap is exceeded. The extraction loop wraps each entry's decompressed stream and the cumulative archive byte counter so violations abort within a single Read call.

B) Use `io.TeeReader` to a `bytes.Buffer` and check after each chunk.

C) Decompress fully into a counting writer, then check at the end.

X) Other

[Answer]: A

**Recommendation rationale**: A short-circuiting wrapper aborts the moment the limit is crossed — a bomb that would decompress 100 GB stops within a few KB of overspill. Option B re-buffers data (wastes memory and violates NFR-2.3 "no full archive in memory"). Option C is catastrophically wrong for bomb defence (decompresses the entire payload before checking, defeating the purpose).

---

### Question 6 — MIME-Type Detection Strategy

The DynamoDB record schema (FR-5.2) includes a `mimeType` field. How is the MIME type determined for an extracted entry?

A) **(Recommended)** **Hybrid**: detect by content sniffing the first 512 bytes via `net/http.DetectContentType`, falling back to `mime.TypeByExtension` if sniffing returns `application/octet-stream`. Detection is performed during the streaming upload (using `bufio.Reader.Peek(512)`) — no extra read passes.

B) **Extension-only** via `mime.TypeByExtension` on `safeFilename`.

C) **Sniff-only** via `net/http.DetectContentType` on the first 512 bytes.

X) Other

[Answer]: A

**Recommendation rationale**: Hybrid gives the best signal: sniffing handles files where the extension is missing or wrong (common for adversarial uploads), and the extension fallback handles content types that sniffing identifies only as `application/octet-stream` (e.g., `.docx`, which is a ZIP container — sniffing returns `application/zip`; an extension hint to `application/vnd.openxmlformats-officedocument.wordprocessingml.document` is more useful downstream). Both stdlib functions are pure / deterministic — PBT-friendly. No additional read pass is needed because `bufio.Peek(512)` runs at the start of the upload stream.

---

### Question 7 — Graceful Shutdown Coordination

When the pod receives SIGTERM (rolling update, scale-down), how should in-flight work be handled to avoid duplicate processing and meet the 240 s extraction hard timeout?

A) **(Recommended)** Signal handler cancels a top-level `context.Context`. The SQS receive-loop **stops calling `ReceiveMessage`** but lets in-flight workers complete (up to a configurable `gracefulShutdownTimeoutSec`, default 250 s — slightly above extraction hard timeout). Heartbeat goroutines continue to extend visibility during this drain. Workers that finish flush their DynamoDB+slipsheet writes and then `DeleteMessage`. After the timeout, the process exits — any still-in-flight messages will be reclaimed by SQS visibility timeout (no data loss because writes are idempotent per FR-5.3).

B) Hard-cancel all in-flight extractions on SIGTERM; let SQS reclaim everything.

C) Refuse new work AND immediately call `ChangeMessageVisibility` to 0 for all in-flight messages so SQS reclaims them instantly (no drain).

X) Other

[Answer]: A

**Recommendation rationale**: Graceful drain preserves work-in-progress, avoids amplifying load on the replacement pod, and leverages the idempotency guarantee (FR-5.3) as a safety net. Kubernetes' default `terminationGracePeriodSeconds` should be set to ≥260 s in the Helm chart `values.yaml` (250 s drain + 10 s buffer). Option B causes avoidable rework. Option C is unsafe — instant visibility reset on a still-running extraction causes duplicate processing.

---

### Question 8 — Should `bombdefence` and `validation` Be Merged?

NFR-11 lists `bombdefence` (10-point rules) and `validation` (entry-path safety + sanitisation) as separate packages. SECURITY-11 mandates security-critical logic isolation. Should they be merged into a single security package?

A) **(Recommended)** **Keep separate.** `bombdefence` operates on aggregate archive metrics + per-entry size; `validation` operates on entry paths (string-domain logic with different PBT properties — round-trip idempotence vs. invariant size bounds). Separate packages give each a tight test surface and make the security-review boundary explicit.

B) Merge into a single `internal/security` package.

C) Move both into a single `internal/safety` package alongside any future safety controls.

X) Other

[Answer]: A

**Recommendation rationale**: The two concerns share a security category but have **disjoint logic, disjoint inputs, and disjoint properties** (per the PBT properties table in NFR-8). Merging would couple unrelated change reasons (loosening the audit boundary and complicating code review by mixing string-sanitisation diffs with size-counter diffs). Keeping them separate aligns with SECURITY-11 ("Separation of concerns: security-critical logic isolated in dedicated modules") and with NFR-11 as already drafted. The merged options offer no operational benefit.

---

## Part C — Notes for Part 2 (Generation)

After answers are confirmed, the artefact generation in Part 2 will:

1. Translate Q1's port interfaces into Go interface stubs in `components.md` and `component-methods.md`.
2. Document the worker-pool concurrency model (Q2) in `services.md` with a Mermaid sequence diagram for the receive→worker→complete flow.
3. Specify the single-port HTTP topology (Q3) and the routes table in `services.md`.
4. Define the typed-error hierarchy (Q4) in `component-methods.md` under the `internal/extraction` component.
5. Capture the `LimitedReader` streaming pattern (Q5) in `component-methods.md` under `internal/bombdefence`.
6. Capture the hybrid MIME-detection function (Q6) in `component-methods.md` under `internal/storage`.
7. Document the graceful-shutdown sequence (Q7) in `services.md` with a Mermaid state diagram covering normal, draining, and terminated states.
8. Reflect the separate `bombdefence` + `validation` packages (Q8) consistently across all artefacts.
9. Carry the PBT-01 property identifications (from requirements NFR-8) forward into `component-methods.md` as a "Testable Properties" subsection per component (this is mandatory PBT-01 compliance and the explicit hand-off to Functional Design).

---

## Part D — How to Respond

1. Edit this file (`aidlc-docs/inception/plans/application-design-plan.md`) and fill in each `[Answer]:` tag with a letter (or `X: <free-text>`).
2. Or, reply with the letters inline (e.g., "Q1=A, Q2=A, Q3=B, …"). The model will update the file for you and reflect the answers in the audit log.
3. Use **"Accept all recommendations"** as a shorthand to lock every answer to the (Recommended) option (`A` for all 8 questions).

Once answers are confirmed, the model will execute Part 2 (Generation) and produce the design artefacts listed in Part A.

---

## Part E — User Answers (Confirmed)

**Confirmed 2026-05-24T12:30:00Z** — user reply: **"Accept all recommendations"**.

| Question | Answer | Decision |
|---|---|---|
| Q1 — Interface boundaries | A | Narrow consumer-defined ports (`S3Uploader`, `Recorder`, `MessageQueue`, `Clock`); SDK clients live behind adapters |
| Q2 — SQS concurrency | A | Single long-poll receiver + bounded worker pool (size = `sqs.maxInFlight`, default 5) |
| Q3 — HTTP port topology | A | Single port 8080 serving `/healthz/live`, `/healthz/ready`, `/metrics` |
| Q4 — Error hierarchy | A | Typed-error structs: `BombDefenceError`, `PathValidationError`, `UnsupportedFeatureError`, `TransientError`, `PermanentError` |
| Q5 — Byte-counting pattern | A | Short-circuiting `LimitedReader` in `internal/bombdefence` |
| Q6 — MIME-type detection | A | Hybrid — sniff first 512 bytes via `net/http.DetectContentType`, fall back to `mime.TypeByExtension` |
| Q7 — Graceful shutdown | A | Drain in-flight workers up to 250 s; heartbeat continues during drain; idempotent writes safe-net |
| Q8 — bombdefence / validation merge | A | Keep separate (SECURITY-11 separation of concerns) |

**Local-vs-production parity confirmation**: User explicitly raised parity concern; model confirmed all 8 answers are environment-agnostic by design — same binary and code paths in LocalStack-based local dev and EKS production. Only legitimate per-environment differences are (a) environment variables for infrastructure values (per Q8 of requirements verification) and (b) tunable values inside the YAML ConfigMap (`maxInFlight`, retry counts, bomb thresholds — same schema, different values per env). No code branches on environment.

**Ambiguity analysis**: All 8 answers are unambiguous letter selections (A on each). No follow-up questions required. Proceeding to Part 2 (Generation).

