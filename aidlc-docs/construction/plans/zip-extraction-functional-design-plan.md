# Functional Design Plan — zip-extraction (UOW-SVC-12)

**Document Type**: Functional Design Plan (Part 1 — Planning)
**Project**: Zip Extraction Service (UOW-SVC-12)
**Unit**: `zip-extraction` (single unit)
**Phase**: CONSTRUCTION — Functional Design (Plan)
**Generated**: 2026-05-24
**Source Inputs**:
- `aidlc-docs/inception/requirements/requirements.md`
- `aidlc-docs/inception/application-design/application-design.md` (+ 4 detail docs)
- `aidlc-docs/inception/plans/execution-plan.md`

---

## Purpose

This is **Part 1 of Functional Design (Planning)**. It captures:

1. The checklist of detailed business-logic artefacts to be produced once questions are answered (Part 2 — Generation).
2. A targeted set of **business-logic** clarifying questions for decisions that are NOT already settled by the input spec, requirements verification (12 Q&A), or application design (8 Q&A). The intent is to lock the remaining behavioural choices before writing the algorithms.

Each question presents a **(Recommended)** option (option A) with rationale, alternatives, and an `[Answer]:` tag. Reply with a letter, or use the `X` option for a free-text answer.

---

## Part A — Execution Checklist (Part 2 Will Run After Answers Are Approved)

Once all answers are confirmed, these artefacts are produced under `aidlc-docs/construction/zip-extraction/functional-design/`:

- [x] **business-logic-model.md** — Full state machine for `extraction.Service.Process`, the 10-rule bomb-defence enforcement order, the per-entry processing pipeline, slipsheet write timing, and graceful-drain interaction with in-flight extractions
- [x] **business-rules.md** — Numbered business rules covering retry classification, idempotency conflict handling, partial-failure semantics, DLQ-bound conditions, heartbeat extension policy, and MIME labelling
- [x] **domain-entities.md** — Detailed entity definitions: `ClaimCheck`, `ArchiveMetadata`, `EntryInfo`, `EntryOutcome`, `PipelineFile`, `Slipsheet`, `ChildEntry`, and the typed-error hierarchy with all fields, value domains, and invariants
- [x] AWS error classification table — Concrete mapping from AWS SDK error codes (and HTTP status patterns) to `*TransientError` vs `*PermanentError` (referenced from `business-rules.md`)
- [x] PBT property refinement — Each property from `component-methods.md` "Testable Properties" tables is restated with a concrete `rapid` test sketch (generator names, invariant assertion, counter-example expectation) carried forward as input to Code Generation
- [x] Validate design against SECURITY-01…15 and PBT-01…10 (no new blocking findings)

---

## Part B — Clarifying Questions (Answer Required)

> **Format**: Each question has options A, B, C, … with option **A marked (Recommended)**. Reply by editing `[Answer]:` with the letter, or use `X: <free-text>` to override. **"Accept all recommendations"** locks every answer to A.

---

### Question 1 — Bomb-Defence Violation Mid-Extraction: Clean Up Partially-Uploaded Children?

If bomb-defence rule #2 (cumulative extracted size) or #3 (compression ratio) fires mid-extraction *after* some entries have already been uploaded to S3, what should happen to those orphaned `input/{execId}/*` objects?

A) **(Recommended)** **Leave them; do NOT delete.** Mark the pipeline execution FAILED in the slipsheet. The slipsheet lists every child that was uploaded before the bomb violation fired, with their status. S3 lifecycle policy on `input/` prefix (platform team configures a short TTL, e.g., 7 days) reaps them. **No in-band delete IAM permission needed** (least privilege per SECURITY-06).

B) **Best-effort cleanup**: after a bomb violation, fire-and-forget `DeleteObject` for each already-uploaded child. Failures during cleanup are logged but do not affect the FAILED status.

C) **Mandatory cleanup**: after a bomb violation, synchronously delete every already-uploaded child. Cleanup failures are themselves recorded in the slipsheet.

X) Other

[Answer]: A

**Recommendation rationale**: A is the **safest and most least-privilege** option. The S3 PutObject event for the orphaned objects has *already fired* (S3 events are at-least-once, not transactional with our process) — downstream pipelines may have already started consuming them. Deleting them after the fact creates a race condition with downstream consumers and adds an `s3:DeleteObject` permission to the IRSA role purely to handle bomb-defence cleanup — a least-privilege regression. Leaving them and relying on the platform-configured lifecycle policy keeps the IAM surface minimal and lets downstream consumers complete their own work cleanly. The slipsheet records the FAILED status authoritatively. Option B is messy (failed delete = no signal). Option C couples failure handling to a second, slower failure mode and inflates IAM scope.

---

### Question 2 — DynamoDB Conditional-Check-Failed: Skip the S3 Upload or Re-Upload?

When `RecordEntry` for `(pipelineExecutionId, entryIndex)` returns `ConditionalCheckFailedException` (a re-delivery), the DDB row already exists. Has the S3 upload for that entry already been written? Per the order in the per-entry pipeline (upload → record), yes — but only if the prior delivery reached the record step. What's the right behaviour?

A) **(Recommended)** **Order matters: ALWAYS upload to S3 first, THEN call `RecordEntry`.** A `ConditionalCheckFailedException` on the DDB write means the prior delivery had already uploaded AND recorded — so the redundant S3 PutObject just now is a same-content overwrite (S3 PutObject is idempotent for the same content). Continue silently — treat the entry as UPLOADED. Increment a `redelivery_skips_total` metric for visibility but do not log warn.

B) **Verify content matches** before treating as UPLOADED: HEAD the S3 object, compare ETag against the just-computed body checksum, only mark UPLOADED if equal.

C) **Skip the S3 upload entirely** if the DDB row exists. Requires checking DDB BEFORE uploading (extra round-trip per entry, expensive at scale).

X) Other

[Answer]: A

**Recommendation rationale**: A is the **simplest and idempotency-safe** option. Per FR-5.3 the idempotency contract is keyed by `(pipelineExecutionId, entryIndex)`, and since each entry's S3 key is deterministically derived from those same two values (FR-4.1), an S3 PutObject re-write is **content-identical**. S3 PutObject overwrites in place with the same ETag — there is no consistency problem to verify. Option B (ETag verification) adds latency and complexity for a verification that will essentially always succeed. Option C trades one DDB read per entry (≥ N round-trips per archive) for one DDB write per entry — same write cost, plus a read — net loss.

---

### Question 3 — Slipsheet Write Timing: End-Only or Incremental?

The slipsheet (FR-8) summarises all child entries. When is it written to S3?

A) **(Recommended)** **End-only.** Write the slipsheet once, after the per-entry loop completes, with the final SUCCESS / PARTIAL_FAILED / FAILED status. A single `PutObject` at `slipsheets/{execId}.json`. If extraction aborts mid-stream (bomb defence rule #10 hard timeout, panic), a slipsheet is still written in the `defer` block, recording the status known at that point.

B) **Incremental**: append a slipsheet entry to S3 per child upload. Requires either (i) one full PUT per entry rewriting the slipsheet object, or (ii) one micro-object per entry under `slipsheets/{execId}/{entryIndex}.json` and a final summary write.

C) **Per-pipeline-execution append log** in DynamoDB (separate SK pattern like `SLIP#{seq}`), aggregated into the slipsheet S3 object only at the end. Combines incremental durability + final-summary read pattern.

X) Other

[Answer]: A

**Recommendation rationale**: End-only matches the spec example in §15 (single JSON document with `childCount` + `children` array). Incremental writes (option B) inflate S3 PutObject costs N× per archive and create N temporary inconsistent states downstream consumers could read. Option C splits authority between DDB and S3 unnecessarily — the per-entry DDB record (FR-5) already provides the durable per-entry log; the slipsheet is meant to be the *summary* view. End-only also simplifies the FR-8.4 requirement (a FAILED archive with zero successful entries still produces a slipsheet) — it is just a single write.

---

### Question 4 — Per-Entry FAILED Row in DynamoDB: Written or Omitted?

When an entry fails after exhausting retries (FR-12.3), should we still write a DynamoDB row with `status: "FAILED"` and `failureReason`?

A) **(Recommended)** **Yes — write a FAILED row.** Schema becomes:
```json
{ "pk": "PIPELINE#<execId>",
  "sk": "FILE#<entryIndex>",
  "status": "UPLOADED" | "FAILED",
  "childKey": "<s3-key-if-uploaded-else-empty>",
  "failureReason": "<string-when-FAILED>",
  ... }
```
Gives operators a single-table view of the entire archive's per-entry fate. The conditional PutItem still applies (idempotent on `(execId, entryIndex)`).

B) **No — DynamoDB only records successful uploads.** Failures are recorded in the slipsheet only. Keeps the DynamoDB table semantically homogeneous ("if a row exists, the object is uploaded").

C) **Hybrid**: write a FAILED row only if `partial_failures_total > 0` for the execution; otherwise rely on the slipsheet.

X) Other

[Answer]: A

**Recommendation rationale**: A gives the operations team one consistent source of truth. The slipsheet is durable and authoritative, but operators investigating a specific entry use DDB's PK query (`PIPELINE#<execId>`) — getting only partial coverage there is a footgun. Adding a `status` field that can be `UPLOADED` or `FAILED` keeps the schema simple, costs one extra `failureReason` attribute on failure rows, and lets downstream services that read DDB (e.g., a future "redrive failed entries" tool) operate from a single source. Option B forces every operator and tool to consult both DDB and S3-slipsheet. Option C is over-clever — the schema differs based on the outcome of the same operation.

---

### Question 5 — AWS Error → Typed-Error Classification Table

Confirm the proposed AWS-SDK-error-code → typed-error mapping. (This is a review question, not a forced-choice — answer A if the table is correct, X with corrections otherwise.)

| AWS SDK condition | Typed error | Retryable? | Notes |
|---|---|---|---|
| `ProvisionedThroughputExceededException` (DDB) | `*TransientError{Class:"throttling"}` | Yes | DDB throttling |
| `RequestLimitExceeded`, `RequestThrottled` (any service) | `*TransientError{Class:"throttling"}` | Yes | API-level throttling |
| `SlowDown`, `503 Slow Down` (S3) | `*TransientError{Class:"throttling"}` | Yes | S3 prefix throttling |
| 5xx HTTP status (any AWS service) | `*TransientError{Class:"5xx"}` | Yes | Server-side error |
| `RequestTimeout`, `RequestTimeoutException` | `*TransientError{Class:"timeout"}` | Yes | AWS timeout |
| Net error (`net.OpError`, `*url.Error` with deadline exceeded NOT from extraction ctx) | `*TransientError{Class:"network"}` | Yes | Network blip |
| 4xx HTTP status (NoSuchKey, NoSuchBucket, AccessDenied, ValidationException, InvalidArgument, …) | `*PermanentError` | No | Client error |
| `ConditionalCheckFailedException` (DDB) | (special — handled as redelivery in Q2, NOT an error in retry-classifier sense) | N/A | See Q2 |
| `context.DeadlineExceeded` from the extraction context (240 s rule #10) | `*BombDefenceError{Rule:10}` | No | Extraction-level timeout |
| `context.Canceled` from root ctx (graceful drain) | propagate as-is; outer loop maps to PARTIAL_FAILED | No | Drain |
| ZIP parsing error (corrupt archive) | `*PermanentError` (wrapped) | No | Bad input |
| Encrypted ZIP detected | `*UnsupportedFeatureError{Feature:"encrypted-zip"}` | No | FR-3.6 |
| Multi-disk ZIP detected | `*UnsupportedFeatureError{Feature:"multi-disk"}` | No | FR-3.6 |
| Deflate64 entry detected | `*UnsupportedFeatureError{Feature:"deflate64"}` | No | FR-3.6 |

A) **(Recommended)** Accept the table as the canonical classification.

X) Modifications (list specific entries to change)

[Answer]: A

**Recommendation rationale**: This table covers the realistic AWS SDK error surface for SQS / S3 / DynamoDB. It splits **throttling** (which benefits from longer backoff) from **5xx** (which usually clears on retry) from **timeout** (which may need a shorter backoff) so that the same `Retrier` can apply distinct backoff multipliers per class in a future tuning iteration without changing the classifier logic. The 4xx blanket is correct AWS posture — 4xx means *the request is wrong*, not *the service is sad*. `ConditionalCheckFailedException` is intentionally classified as **not an error in the retry sense** because Q2 establishes it as the idempotency-redelivery signal.

---

### Question 6 — Heartbeat `ChangeMessageVisibility` Extension Value

Every 30 s during in-flight extraction, the heartbeat goroutine extends SQS visibility (FR-9). To what value?

A) **(Recommended)** **Reset to the queue's full visibility timeout (300 s) on every heartbeat.** Simple, leaves a comfortable safety margin even if a heartbeat is missed (one missed 30 s tick still leaves ~270 s of remaining visibility before SQS reclaims the message).

B) **Match remaining extraction-context deadline + 60 s buffer.** Heartbeat-on-30s computes `min(300, remaining_extraction_seconds + 60)` and sets that. Tight coupling to the in-flight deadline; less wasted visibility margin if extraction is about to complete.

C) **Add 30 s on each tick** (no reset; cumulative add). Risks unbounded growth if heartbeat fails to be cancelled.

X) Other

[Answer]: A

**Recommendation rationale**: A is the operationally safest. SQS only changes message visibility relative to the **call time** (the value passed is the new visibility duration starting from now), so resetting to 300 s = "300 s from now" — there's no cumulative growth risk. Option B adds complexity (needs to read remaining deadline + add buffer) for no real benefit; a missed heartbeat under option B with a near-deadline extraction could let visibility lapse if the buffer is too tight. Option C is wrong — `ChangeMessageVisibility(+30s)` is not a thing in the SQS API; the parameter sets the new visibility, it doesn't increment.

---

### Question 7 — MIME Labelling for Nested Archive Entries

Per FR-3.5 (and Q4 of requirements verification), nested archives (entries that are themselves ZIP/TAR/etc.) are uploaded **opaquely** — no recursive extraction. When the hybrid MIME detection (Q6 of application design) encounters such an entry, what is the recorded MIME?

A) **(Recommended)** **Whatever the hybrid detector returns naturally** — typically `application/zip` (sniffed) or `application/x-tar` (extension fallback). No special-casing. Downstream consumers seeing `application/zip` know the child is a nested archive and can re-route through this service via the same S3-PutObject-event mechanism.

B) **Always label nested archives as `application/octet-stream`** to hide their archive nature from downstream MIME-based routing.

C) **Tag with a custom `x-archive-container` MIME or S3 object metadata key** so downstream can branch on it explicitly.

X) Other

[Answer]: A

**Recommendation rationale**: A composes naturally with the event-driven re-trigger pattern from requirements Q4: the upstream Document Uploader uses MIME `application/zip` to route to this service via SQS; that exact same routing applies recursively when an extracted child is itself a ZIP. No new abstraction is needed. Option B (octet-stream) **breaks** the recursive routing — downstream sees a generic blob, loses the archive signal, can't route it back to ZIP extraction. Option C adds a new contract that downstream services have to learn for the marginal benefit of a one-bit hint already conveyed by the MIME.

---

### Question 8 — DLQ-Bound Conditions: Which Terminal States Leave the Message for SQS Redrive?

After `extraction.Service.Process` completes (or panics), should the SQS message be **deleted** (workflow terminal — no redrive) or **left** (SQS native redrive → DLQ after `maxReceiveCount=3`)?

A) **(Recommended)** **DELETE on every terminal status** (SUCCESS, PARTIAL_FAILED, FAILED-bomb-defence, FAILED-unsupported, FAILED-zero-entries-succeeded, FAILED-corrupt-zip, FAILED-source-not-found). **LEAVE only on unhandled panic that escaped the worker's recover handler** (the receive-loop catches it, logs FATAL, lets visibility lapse → SQS redrive → DLQ after 3 attempts). LEAVE also on `*TransientError` from the **top-level archive download** (FR-2.3) — i.e., GetObject of the source ZIP itself fails permanently — because that may resolve on retry from SQS redrive after visibility timeout.

B) **DELETE only on SUCCESS**; PARTIAL_FAILED and FAILED leave the message for SQS redrive.

C) **DELETE on SUCCESS, PARTIAL_FAILED, and FAILED-bomb-defence**; everything else LEAVE for redrive.

X) Other

[Answer]: A

**Recommendation rationale**: Option A makes redrive behaviour predictable and operator-meaningful. PARTIAL_FAILED and the various FAILED reasons are **terminal business outcomes** — they have been recorded in DDB and the slipsheet; retrying the same archive will produce the same outcome (deterministic). Letting SQS redrive them 3× wastes compute and inflates DLQ noise. The DLQ should signal **infrastructure / bug failures** (panics, transient archive-download failures), not deterministic business outcomes. Option B causes the worst case: every PARTIAL_FAILED archive is processed 3 times before hitting DLQ — that's at minimum 3× S3 PutObject duplicates per failed-entry's retries-exhausted path. Option C is closer but still includes some deterministic FAILED states in the redrive path.

---

## Part C — Notes for Part 2 (Generation)

After answers are confirmed, Part 2 will produce these business-logic artefacts:

1. **business-logic-model.md**:
   - Full state machine for `extraction.Service.Process` (states: Receiving, Downloading, Opening, PreChecking, Iterating, Uploading, Recording, Slipsheet, Cleanup; transitions: success, transient, permanent, bomb, unsupported, canceled). Mermaid state diagram.
   - 10-rule bomb-defence enforcement order with rationale (which rules check at archive-open time vs. mid-stream).
   - Per-entry processing pipeline (path validation → bomb entry-check → wrap stream → upload → record).
   - Slipsheet write timing decision (end-only per Q3) with defer-block coverage of abort paths.
   - Graceful-drain interaction (Q7 of application design): in-flight extractions complete naturally up to the drain deadline.
2. **business-rules.md**:
   - BR-001 … BR-NNN numbered rules.
   - Retry classification table (Q5 of functional design) as the authoritative BR-RETRY-*.
   - Idempotency conflict handling (Q2 of functional design) as BR-IDEMPOTENCY-*.
   - Partial-failure semantics (FR-10 + Q12 of requirements + Q4 of functional design).
   - DLQ-bound conditions (Q8 of functional design) as BR-DLQ-*.
   - Heartbeat policy (Q6 of functional design) as BR-HEARTBEAT-*.
   - MIME labelling (Q7 of functional design).
   - Bomb-defence cleanup policy (Q1 of functional design).
3. **domain-entities.md**:
   - Full struct definitions for `ClaimCheck`, `ArchiveMetadata`, `EntryInfo`, `EntryOutcome`, `PipelineFile`, `Slipsheet`, `ChildEntry`, `Status`.
   - Typed-error hierarchy (Q4 of application design) with field-level invariants.
   - Value-domain tables for every enum/discriminator (e.g., `failureReason` allowed values).
4. **PBT property refinement**:
   - Each property from `component-methods.md` "Testable Properties" tables restated with concrete `rapid` test sketches (generator names, invariant assertion, expected shrunk counter-example).

---

## Part D — How to Respond

1. Edit `[Answer]:` tags in this file with a letter, or `X: <free-text>` for overrides.
2. Or reply inline (e.g., "Q1=A, Q2=A, …").
3. **"Accept all recommendations"** locks all 8 answers to option A.

Once answers are confirmed, Part 2 generates the 3 business-logic artefact files + AWS error classification table + PBT property refinement.

---

## Part E — User Answers (Confirmed)

**Confirmed 2026-05-24T12:45:00Z** — user reply: **"Accept all recommendations"**.

| Question | Answer | Decision |
|---|---|---|
| Q1 — Bomb-defence cleanup | A | Leave orphaned children; S3 lifecycle policy reaps |
| Q2 — DDB conflict handling | A | Upload-first-then-record order; conflict means safe content-identical re-write |
| Q3 — Slipsheet timing | A | End-only, with `defer`-block coverage of abort paths |
| Q4 — Per-entry FAILED row | A | Yes — single source of truth; `status` field + `failureReason` attribute |
| Q5 — AWS error classification | A | Accept proposed table (14 conditions mapped) |
| Q6 — Heartbeat extension value | A | Reset to 300 s on every tick |
| Q7 — Nested-archive MIME | A | Natural hybrid-detector result; preserves recursive routing via S3 events |
| Q8 — DLQ-bound conditions | A | DELETE on all terminal statuses; LEAVE only on unhandled panic or transient source-archive download failure |

**Ambiguity analysis**: All 8 answers are unambiguous letter selections. No follow-up questions required. Proceeding to Part 2 (Generation).

