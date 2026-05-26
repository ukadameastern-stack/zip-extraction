# Business Rules — zip-extraction (UOW-SVC-12)

**Document Type**: Numbered Business Rules + AWS Error Classification Table
**Phase**: CONSTRUCTION — Functional Design (Part 2: Generation)
**Generated**: 2026-05-24
**Unit**: `zip-extraction` (UOW-SVC-12)

This document enumerates the business rules that govern every algorithmic decision in the Zip Extraction Service. Every rule has a stable identifier (`BR-<category>-<NNN>`), a clear statement, a traceability link to its source (FR / NFR / Q&A), and a verification approach (unit test, PBT, or integration test).

---

## Rule Categories

| Prefix | Category |
|---|---|
| BR-BOMB | Bomb-defence (FR-7 rules #1 – #10) |
| BR-PATH | Path validation & sanitisation (FR-6 + FR-7 rules #7/#8) |
| BR-MIME | MIME-type detection & labelling |
| BR-RETRY | Retry classification & backoff (FR-12, Q5 of functional design) |
| BR-IDEMPOTENCY | Idempotency contract under SQS at-least-once delivery |
| BR-STATUS | Pipeline-execution status assignment (SUCCESS / PARTIAL_FAILED / FAILED) |
| BR-DDB | DynamoDB record schema and write semantics |
| BR-SLIP | Slipsheet schema, location, and write timing |
| BR-DLQ | DLQ-bound / SQS-redrive conditions |
| BR-HEARTBEAT | SQS visibility heartbeat policy |
| BR-DRAIN | Graceful-shutdown drain semantics |
| BR-LOG | Logging discipline (SECURITY-03) |
| BR-CLEAN | Cleanup discipline (FR-11) |

---

## BR-BOMB: Bomb Defence

### BR-BOMB-001 — Pre-check before opening any entry
**Statement**: After `archive/zip.NewReader` returns successfully, evaluate **rule #1** (compressed-archive-size ≤ `MaxCompressedSizeBytes`, default 500 MB) and **rule #4** (entry-count ≤ `MaxEntryCount`, default 10 000) using archive-level metadata. A violation aborts the archive with `*BombDefenceError{Rule: 1 | 4}` BEFORE iterating entries.
**Source**: FR-7
**Verification**: Unit + PBT-03 invariant property on `bombdefence.Checker.PreCheck`

### BR-BOMB-002 — Per-entry pre-stream check
**Statement**: For each entry, before opening its reader, evaluate **rule #5** (directory-nesting-depth ≤ `MaxDirectoryDepth`, default 10), **rule #6** (`entry.Mode().Type() != os.ModeSymlink`), and **rule #9** (`entry.UncompressedSize64 ≤ MaxSingleFileSizeBytes`, default 250 MB). A violation aborts the archive with `*BombDefenceError{Rule: 5 | 6 | 9}`.
**Source**: FR-7
**Verification**: Unit + PBT-03 invariant property on `bombdefence.Checker.EntryCheck`

### BR-BOMB-003 — Streaming cumulative-size check (rule #2)
**Statement**: The decompressed byte stream of every entry passes through `bombdefence.LimitedReader`, which maintains a **cumulative byte counter across all entries in the archive**. The moment cumulative bytes exceed `MaxExtractedSizeBytes` (default 2 GB), `Read` returns `(0, *BombDefenceError{Rule: 2})`.
**Source**: FR-7, Q5 of application design
**Verification**: PBT-03 strong invariant: `bytes-returned-by-LimitedReader ≤ cap` across all valid command sequences

### BR-BOMB-004 — Streaming compression-ratio check (rule #3)
**Statement**: At every `Read` call against the `LimitedReader`, the running ratio `cumulative-decompressed / cumulative-compressed` is computed. If the ratio exceeds `MaxCompressionRatio` (default **1000×**) **AND** cumulative compressed bytes ≥ 64 KiB (small-sample floor to avoid false positives on tiny streams), `Read` returns `(0, *BombDefenceError{Rule: 3})`. The 1000× cap was set after empirical observation that legitimate legacy Office documents (.doc OLE2 format with sparse zero-padding + repetitive text content) commonly compress at 100–300×; real-world zip bombs are universally above 1000× (the classic 42.zip = ~4.2 × 10⁹×). The absolute size caps (rules #2, #9) remain the primary defence against resource-exhaustion bombs; rule #3 is a fast-fail tripwire for the obvious cases.
**Source**: FR-7
**Verification**: PBT-03 invariant + PBT-05 oracle (formula match)

### BR-BOMB-005 — Extraction hard timeout (rule #10)
**Statement**: `extraction.Service.Process` derives its working context via `context.WithTimeout(parentCtx, cfg.MaxExtractionDurationSec * time.Second)` (default 240 s). When the context fires `DeadlineExceeded`, downstream calls return promptly. The outer handler maps this to `*BombDefenceError{Rule: 10}` (per BR-RETRY-012) and produces a `FAILED` outcome.
**Source**: FR-7 rule #10
**Verification**: Integration test with synthetic slow upload

### BR-BOMB-006 — No bomb-defence cleanup of already-uploaded children
**Statement**: When a bomb-defence violation fires mid-extraction (BR-BOMB-003 or BR-BOMB-004) after some entries have already been uploaded to S3, the service **does NOT delete** the already-uploaded objects. The slipsheet records them with `status: "UPLOADED"`. The S3 staging-bucket lifecycle policy (platform-team configured, expected ~7-day TTL on `input/` prefix) reaps the orphaned objects.
**Source**: Q1 of functional design
**Rationale**: Preserves SECURITY-06 least-privilege (no `s3:DeleteObject` on the IRSA role). Avoids race condition with downstream consumers that may already be processing the children via S3 PutObject events.
**Verification**: Integration test verifying no DeleteObject calls; IAM-policy unit test verifying no Delete action in the rendered IRSA template.

### BR-BOMB-007 — Bomb-defence violations are never retried
**Statement**: `*BombDefenceError` is **always** classified as non-transient. `internal/retry.Classify` returns `(transient: false, …)` for any wrapped `*BombDefenceError`. The error propagates immediately to the outer handler.
**Source**: FR-12.2, FR-7.1, Q5 of functional design
**Verification**: PBT-03 negative classification property on `retry.Classify`

### BR-BOMB-008 — Bomb-defence violations are deleted from SQS, not redriven
**Statement**: A bomb-defence FAILED outcome causes `DeleteMessage` (BR-DLQ-001) and is **not** routed to the DLQ. The violation is deterministic — redriving the same archive produces the same outcome. The platform-team operational dashboard observes bomb rejections via `zip_bomb_rejections_total{rule}` instead.
**Source**: Q8 of functional design, FR-7.1
**Verification**: Integration test on bomb-violation message lifecycle

### BR-BOMB-009 — Overlapping compressed-data ranges (rule #11, Fifield defence)
**Statement**: After `archive/zip.NewReader` succeeds and before iterating entries, the service calls `bombdefence.OverlapCheck` which collects each entry's compressed-data byte interval `[DataOffset, DataOffset + CompressedSize64)`, sorts by offset, and verifies that consecutive intervals do not overlap. A violation aborts the archive with `*BombDefenceError{Rule: 11}` BEFORE any decompression. Defends against the Fifield non-recursive bomb pattern: multiple central-directory records sharing the same compressed-data range so a single deflate stream gets decompressed multiple times under different "entry" identities, multiplying extracted size without a per-entry compression-ratio anomaly (so rule #3 alone cannot catch it; rule #2 catches the cumulative symptom but not the mechanism).
**Source**: FR-7 rule #11; defence-in-depth response to the academic "ZIP files: history, explanation, and implementation" Fifield-style construction.
**Verification**: Unit tests `TestOverlapCheckRule11_Rejects` (overlap detected), `TestOverlapCheckRule11_AdjacentOK` (boundary touch passes), `TestOverlapCheckRule11_SortsBeforeWalking` (order-invariant), `TestOverlapCheckRule11_FewerThanTwoEntries` (degenerate inputs safe).
**Operational note**: Tolerates `*zip.File.DataOffset()` errors on individual entries — surviving ranges are still pairwise-checked. A whole-archive DataOffset failure means zero ranges survive and OverlapCheck trivially passes; rule #1 / rule #2 still catch the resource-exhaustion symptom downstream.

---

## BR-PATH: Path Validation

### BR-PATH-001 — Sanitisation rejects traversal
**Statement**: `validation.Sanitize(rawPath)` rejects any input containing `..` segments — after URL-decoding, after backslash-to-slash normalisation, and after `filepath.Clean` — with `*PathValidationError{Reason: "path-traversal"}`. Includes encoded variants (`%2e%2e`, `..%2f`, `..\` …).
**Source**: FR-6, FR-7 rule #8
**Verification**: PBT-03 negative property — adversarial generator emits encoded `..` variants; assert rejection.

### BR-PATH-002 — Sanitisation rejects absolute paths
**Statement**: `validation.Sanitize(rawPath)` rejects any input starting with `/`, `\`, or matching `[A-Za-z]:` (drive-letter prefix) with `*PathValidationError{Reason: "absolute-path"}`.
**Source**: FR-6, FR-7 rule #7
**Verification**: PBT-03 negative property

### BR-PATH-003 — Sanitisation returns the base filename only
**Statement**: `validation.Sanitize` returns the **final path segment only** (no directory components). If the cleaned path is empty or `.`, returns `*PathValidationError{Reason: "empty-name"}`.
**Source**: FR-4 (S3 key construction uses `safeName` directly under `input/{execId}/<safeName>`)
**Verification**: PBT-03 invariant property — output contains no `/` or `\`.

### BR-PATH-004 — Filename length and character constraints
**Statement**: After sanitisation, `safeName` must satisfy: length ≤ 255 bytes; no control characters (`\x00`-`\x1f`, `\x7f`); no leading `.` followed by no other characters (i.e., not `.` or `..`). Violation → `*PathValidationError{Reason: "invalid-filename"}`.
**Source**: FR-6
**Verification**: PBT-03 invariant property on allowed-character regex

### BR-PATH-005 — Sanitisation is idempotent
**Statement**: For any input `x` where `Sanitize(x) → (safeName, nil)`, calling `Sanitize(safeName)` returns `(safeName, nil)` — i.e., the function is a closure under repeated application.
**Source**: PBT-04 idempotence requirement
**Verification**: PBT-04 idempotence property

### BR-PATH-006 — Path-validation failures fail the archive, not the entry
**Statement**: A `*PathValidationError` on **any** entry causes the **entire archive** to be marked FAILED, not just that one entry. Rationale: a malicious or malformed archive containing path-traversal attempts is treated as a security incident — best to halt the entire extraction and let an operator investigate.
**Source**: FR-6, FR-7 (defence-in-depth posture)
**Verification**: Integration test with crafted archive

---

## BR-MIME: MIME-Type Detection

### BR-MIME-001 — Hybrid detection algorithm
**Statement**: `storage.DetectMIME(peek []byte, fileName string) string` returns:
1. `result := http.DetectContentType(peek)`.
2. If `result == "application/octet-stream"` **AND** `fileName` has a known extension (`mime.TypeByExtension(filepath.Ext(fileName)) != ""`), return the extension-derived type.
3. Otherwise return `result` (may be `application/octet-stream` if both detection paths failed).
**Source**: Q6 of application design
**Verification**: PBT-05 oracle property (logical-OR equivalence)

### BR-MIME-002 — No special-casing of nested archives
**Statement**: Nested archives (entries whose decompressed content is itself a ZIP / TAR / GZIP / etc.) are MIME-labelled using the normal hybrid detector (BR-MIME-001). Typical result: `application/zip` (sniffed). This is **intentional** — downstream services can recognise nested archives via MIME and re-route through the Zip Extraction Service via the natural S3 PutObject event flow.
**Source**: Q7 of functional design, FR-3.5, Q4 of requirements verification
**Verification**: Unit test on nested-zip fixture

### BR-MIME-003 — MIME stored in DynamoDB and as S3 ContentType
**Statement**: The MIME determined by BR-MIME-001 is:
1. Stored as the `mimeType` attribute in the per-entry DynamoDB record (BR-DDB-001).
2. Set as the S3 object's `ContentType` header during `PutObject`.
**Source**: FR-5.2
**Verification**: Integration test verifying S3 `HEAD` ContentType matches DDB `mimeType`

---

## BR-RETRY: Retry Classification & Backoff

### BR-RETRY-001 — Retry framework selection
**Statement**: All retryable operations within `extraction.Service.processEntry` use the `internal/retry.Retrier.Do` wrapper. Direct SDK-level retry counts beyond AWS SDK defaults are NOT relied on for application-level retry decisions.
**Source**: FR-12, Q12 of requirements
**Verification**: Code-review checklist + grep for direct SDK retryer overrides

### BR-RETRY-002 — Maximum retry attempts
**Statement**: `Retrier.Do` invokes the operation at most `MaxAttempts` times (default 3 = 1 initial + 2 retries). After exhausting attempts, the last `*TransientError` is wrapped in `*PermanentError{Cause: lastErr}` and propagated.
**Source**: FR-12.1, Q12 of requirements
**Verification**: Stateful PBT-06 on `Retrier.Do`

### BR-RETRY-003 — Backoff formula (PBT oracle)
**Statement**: For attempt `n` (0-indexed), the wait duration is `BackoffFor(n) = BackoffBaseMillis * BackoffFactor^n * (1 + JitterFraction * r)` where `r ∈ [-1, 1]` is uniformly sampled. Default: 200 ms base, factor 2.0, jitter 0.25 → wait sequence ≈ {200ms ± 50ms, 400ms ± 100ms, 800ms ± 200ms}.

> NOTE: with `MaxAttempts=3`, only two backoff waits are observed (between attempt 1→2 and 2→3). Attempts beyond `MaxAttempts` are not made.
**Source**: FR-12.1
**Verification**: PBT-05 oracle — implementation must match closed-form within ±1 µs.

### BR-RETRY-004 — Classifier-driven retry: throttling
**Statement**: An operation error is `*TransientError{Class: "throttling"}` if its underlying cause is one of: `ProvisionedThroughputExceededException`, `ProvisionedThroughputExceeded`, `RequestLimitExceeded`, `RequestThrottled`, `Throttling`, `ThrottlingException`, or `SlowDown`, OR any error matching the AWS SDK middleware error class `RetryableError` with category `Throttling`.
**Source**: Q5 of functional design
**Verification**: Unit test with golden error fixtures from AWS SDK

### BR-RETRY-005 — Classifier-driven retry: 5xx
**Statement**: An operation error is `*TransientError{Class: "5xx"}` if the underlying error's HTTP status (via `smithy-go.HTTPResponseError`) is in `[500, 599]`.
**Source**: Q5 of functional design
**Verification**: Unit test with synthetic HTTP-500 fixture

### BR-RETRY-006 — Classifier-driven retry: timeout
**Statement**: An operation error is `*TransientError{Class: "timeout"}` if it matches `RequestTimeout`, `RequestTimeoutException`, OR is `context.DeadlineExceeded` **not** derived from the extraction-level context (i.e., it's a per-request timeout, not the 240 s extraction-wide rule #10).
**Source**: Q5 of functional design, BR-RETRY-012
**Verification**: Unit test with synthetic timeout fixtures

### BR-RETRY-007 — Classifier-driven retry: network
**Statement**: An operation error is `*TransientError{Class: "network"}` if it matches `net.OpError`, `*url.Error` (with `Err.Temporary() == true` OR `Err == io.EOF` mid-request), or DNS resolution errors.
**Source**: Q5 of functional design
**Verification**: Unit test with golden network-error fixtures

### BR-RETRY-008 — Non-retryable: 4xx client errors
**Statement**: Any error whose HTTP status is in `[400, 499]` is **not** retryable. Specific codes recognised: `NoSuchKey`, `NoSuchBucket`, `AccessDenied`, `InvalidAccessKeyId`, `ValidationException`, `InvalidArgument`, `MalformedQueryString`, `ResourceNotFoundException`. Wrapped as `*PermanentError`.
**Source**: Q5 of functional design
**Verification**: Unit test + PBT-03 negative classification

### BR-RETRY-009 — Non-retryable: bomb defence and path validation
**Statement**: `*BombDefenceError`, `*PathValidationError`, and `*UnsupportedFeatureError` are **always** non-retryable — they are deterministic outcomes.
**Source**: FR-7.1, FR-12.2, Q5 of functional design
**Verification**: PBT-03 negative property on `retry.Classify`

### BR-RETRY-010 — Non-retryable: `*PermanentError`
**Statement**: `*PermanentError` is by definition non-retryable. Nested unwrapping stops at the first `*PermanentError`.
**Source**: Q5 of functional design
**Verification**: Unit test

### BR-RETRY-011 — Idempotency conflict handled outside retry
**Statement**: `ConditionalCheckFailedException` from DynamoDB is **not** an error from the retry classifier's perspective. It is intercepted inside `dynamodb.Adapter.RecordEntry` and treated as a successful idempotent re-write (BR-IDEMPOTENCY-002). It never reaches `Retrier.Do`.
**Source**: Q5 of functional design, Q2 of functional design
**Verification**: Unit test on `RecordEntry`

### BR-RETRY-012 — Extraction-context `DeadlineExceeded` maps to rule #10
**Statement**: If a `context.DeadlineExceeded` error originates from the extraction context (240 s rule #10) — detected via `errors.Is(err, context.DeadlineExceeded) && ctx.Err() == context.DeadlineExceeded` at the outermost extraction frame — it is wrapped as `*BombDefenceError{Rule: 10, Reason: "extraction hard timeout"}` (NOT `*TransientError`). The archive is marked FAILED.
**Source**: Q5 of functional design, FR-7 rule #10
**Verification**: Integration test with synthetic slow upload

### BR-RETRY-013 — Root-context `Canceled` propagates as-is
**Statement**: If a `context.Canceled` error originates from the root context (graceful drain), it is propagated unwrapped. The outer handler maps it to PARTIAL_FAILED (if any entries succeeded) or FAILED (if none did) with `reason = "drain canceled"`.
**Source**: Q5 of functional design, Q7 of application design
**Verification**: Integration test with simulated SIGTERM

### BR-RETRY-014 — Retry classification AWS error table (canonical)

| AWS condition / error class | Classifier output | Retryable | Notes |
|---|---|---|---|
| `ProvisionedThroughputExceededException` (DDB) | `*TransientError{Class:"throttling"}` | yes | BR-RETRY-004 |
| `ProvisionedThroughputExceeded` | `*TransientError{Class:"throttling"}` | yes | BR-RETRY-004 |
| `RequestLimitExceeded` | `*TransientError{Class:"throttling"}` | yes | BR-RETRY-004 |
| `RequestThrottled` | `*TransientError{Class:"throttling"}` | yes | BR-RETRY-004 |
| `Throttling`, `ThrottlingException` | `*TransientError{Class:"throttling"}` | yes | BR-RETRY-004 |
| `SlowDown` (S3) | `*TransientError{Class:"throttling"}` | yes | BR-RETRY-004 |
| HTTP 5xx | `*TransientError{Class:"5xx"}` | yes | BR-RETRY-005 |
| `RequestTimeout`, `RequestTimeoutException` | `*TransientError{Class:"timeout"}` | yes | BR-RETRY-006 |
| per-request `context.DeadlineExceeded` (NOT extraction-level) | `*TransientError{Class:"timeout"}` | yes | BR-RETRY-006 |
| `net.OpError`, `*url.Error` (temp), DNS failures | `*TransientError{Class:"network"}` | yes | BR-RETRY-007 |
| HTTP 4xx (any) | `*PermanentError` | no | BR-RETRY-008 |
| `NoSuchKey`, `NoSuchBucket`, `AccessDenied`, … | `*PermanentError` | no | BR-RETRY-008 |
| `*BombDefenceError` | `*BombDefenceError` (propagated as-is) | no | BR-RETRY-009 / BR-BOMB-007 |
| `*PathValidationError` | `*PathValidationError` (propagated as-is) | no | BR-RETRY-009 |
| `*UnsupportedFeatureError` | `*UnsupportedFeatureError` (propagated as-is) | no | BR-RETRY-009 |
| `ConditionalCheckFailedException` (DDB) | not an error (BR-RETRY-011) | n/a | Idempotent-redelivery signal |
| extraction-level `context.DeadlineExceeded` | `*BombDefenceError{Rule:10}` | no | BR-RETRY-012 |
| root-ctx `context.Canceled` (drain) | propagate as-is | no | BR-RETRY-013 |
| corrupt ZIP parse error | `*PermanentError` | no | Bad input |
| encrypted ZIP detected | `*UnsupportedFeatureError{Feature:"encrypted-zip"}` | no | FR-3.6 |
| multi-disk ZIP detected | `*UnsupportedFeatureError{Feature:"multi-disk"}` | no | FR-3.6 |
| Deflate64 entry detected | `*UnsupportedFeatureError{Feature:"deflate64"}` | no | FR-3.6 |

---

## BR-IDEMPOTENCY: Idempotency Under At-Least-Once Delivery

### BR-IDEMPOTENCY-001 — Idempotency key
**Statement**: The idempotency contract is keyed on the pair `(pipelineExecutionId, entryIndex)`. The derived DDB primary key is `pk = "PIPELINE#" + pipelineExecutionId`, `sk = "FILE#" + fmt.Sprintf("%04d", entryIndex)`. The derived S3 key is `input/{pipelineExecutionId}/{entryIndex:04d}-{safeName}`.
**Source**: FR-5.3, FR-4.1, §17 of input spec
**Verification**: PBT-04 idempotence property

### BR-IDEMPOTENCY-002 — DDB conditional PutItem
**Statement**: `dynamodb.Adapter.RecordEntry` issues `PutItem` with `ConditionExpression: "attribute_not_exists(pk)"`. A `ConditionalCheckFailedException` is **not** propagated as an error — it indicates a redelivery and is mapped to `nil` (success).
**Source**: FR-5.3, Q2 of functional design
**Verification**: Unit test against fake DDB tracking conditional semantics

### BR-IDEMPOTENCY-003 — Upload-first-then-record ordering
**Statement**: Within `processEntry`, the S3 `Upload` call MUST precede the DDB `RecordEntry` call. A `ConditionalCheckFailedException` on `RecordEntry` therefore implies that the corresponding S3 object was uploaded on a prior delivery (and may also have been uploaded by this delivery — see BR-IDEMPOTENCY-004).
**Source**: Q2 of functional design
**Verification**: Code-review checklist; integration test verifies order via fake clients

### BR-IDEMPOTENCY-004 — Same-content S3 re-upload is safe
**Statement**: Because the S3 key is deterministically derived from `(pipelineExecutionId, entryIndex, safeName)` (BR-IDEMPOTENCY-001), and the source ZIP entry's decompressed bytes are deterministic given the source archive, an S3 `PutObject` performed during a re-delivery overwrites the prior object with byte-identical content. No `IfNoneMatch` precondition is needed. The service does NOT verify object ETags on re-write.
**Source**: Q2 of functional design
**Verification**: Integration test simulating SQS re-delivery; verifies same ETag pre/post

### BR-IDEMPOTENCY-005 — Slipsheet writes are not idempotency-checked
**Statement**: The slipsheet `PutObject` at `slipsheets/{execId}.json` is a same-key overwrite. The service does NOT use a `ConditionExpression` on slipsheet writes — each (final, possibly re-delivery) write produces an authoritative summary regardless of prior writes.
**Source**: BR-SLIP-001, Q2 of functional design
**Verification**: Integration test on re-delivery → slipsheet remains correct

### BR-IDEMPOTENCY-006 — Conditional-check-failed observability
**Statement**: Every `ConditionalCheckFailedException` increments a metric `redelivery_skips_total` (label: `entry`). No WARN-level log is emitted — these are expected under at-least-once delivery semantics and would be noisy.
**Source**: Q2 of functional design
**Verification**: Unit test on `dynamodb.Adapter.RecordEntry`

---

## BR-STATUS: Pipeline-Execution Status

### BR-STATUS-001 — Status decision function
**Statement**: `computeStatus(entryOutcomes []EntryOutcome, archiveErr error) Status` returns:

| Inputs | Output |
|---|---|
| `archiveErr != nil` AND no entries processed (pre-loop failure) | `StatusFailed` |
| `archiveErr != nil` AND ≥1 entries `UPLOADED` AND archive-level abort (bomb mid-stream, path-validation, rule #10) | `StatusFailed` (per BR-STATUS-002) |
| `archiveErr == nil` AND every entry `UPLOADED` | `StatusSuccess` |
| `archiveErr == nil` AND ≥1 entry `UPLOADED` AND ≥1 entry `FAILED` | `StatusPartialFailed` |
| `archiveErr == nil` AND every entry `FAILED` | `StatusFailed` (per BR-STATUS-003) |
| `archiveErr == nil` AND zero entries (empty archive after pre-check) | `StatusSuccess` (no entries to fail) |

**Source**: FR-10, FR-7.1, Q12 of requirements verification
**Verification**: Exhaustive truth-table unit test + PBT-06 stateful property

### BR-STATUS-002 — Archive-level abort overrides per-entry successes
**Statement**: When an archive-level error fires mid-extraction (bomb-defence rule #2/#3, path validation BR-PATH-006, rule #10), the pipeline status is **FAILED**, NOT PARTIAL_FAILED, even if some entries were uploaded successfully before the violation. The successfully-uploaded entries are recorded as `UPLOADED` in the slipsheet but the archive as a whole is FAILED.
**Source**: FR-7.1, BR-BOMB-006
**Rationale**: An archive that triggered a bomb-defence rule is treated as a malicious or malformed unit — the operator should not be told it "partially succeeded." Downstream consumers seeing the slipsheet's `status: "FAILED"` know to disregard the children.
**Verification**: Unit test on `computeStatus` with mid-extraction abort scenarios

### BR-STATUS-003 — Zero successes = FAILED, not PARTIAL_FAILED
**Statement**: If every entry in the archive fails (e.g., every entry fails after retries with `*PermanentError`), the pipeline status is **FAILED**, not PARTIAL_FAILED. PARTIAL_FAILED requires `≥1 UPLOADED` AND `≥1 FAILED`.
**Source**: FR-10
**Verification**: Unit test

### BR-STATUS-004 — Per-entry status values
**Statement**: An `EntryOutcome.Status` is one of: `"UPLOADED"` (S3 + DDB write succeeded) or `"FAILED"` (after retry-exhaustion OR archive aborted before this entry was processed). There are no intermediate states.
**Source**: FR-5.2, FR-10
**Verification**: Domain-entity test

---

## BR-DDB: DynamoDB Record Semantics

### BR-DDB-001 — Per-entry record schema
**Statement**: Every entry produces exactly one DynamoDB record (BR-DDB-002) with attributes:
```
pk             string  "PIPELINE#" + pipelineExecutionId   (partition key)
sk             string  "FILE#" + entryIndex (4-digit zero-padded)  (sort key)
documentId     string  copied from the SQS ClaimCheck
sourceArchive  string  copied from ClaimCheck.SourceKey
childKey       string  S3 key; empty string if status == FAILED
mimeType       string  per BR-MIME-001/003; empty if status == FAILED
status         string  "UPLOADED" | "FAILED"
sizeBytes      number  decompressed size; 0 if status == FAILED
failureReason  string  populated iff status == FAILED; e.g., "retries exhausted: throttling", "bomb-defence rule 9 violated"
recordedAt     string  RFC3339 timestamp from clock.Now()
```
**Source**: FR-5.2, Q4 of functional design
**Verification**: PBT-02 round-trip + PBT-03 invariant property

### BR-DDB-002 — One row per entry; no silent omissions
**Statement**: Every iteration of the per-entry loop produces exactly one `RecordEntry` call (either UPLOADED or FAILED). There are no skipped rows. Even an entry that fails before reaching the per-entry pipeline (e.g., the loop never started because of a pre-check failure) is **not** recorded in DDB — but that scenario has no entries to record, so no rows are expected.
**Source**: Q4 of functional design
**Verification**: Integration test counts DDB rows = entry count for in-bounds archives

### BR-DDB-003 — FAILED row's `childKey` is empty
**Statement**: For a FAILED entry, `childKey == ""` (no S3 object exists for that entry — or for orphaned bomb-defence cases, the orphan exists but is treated as "to be lifecycle-reaped" and not referenced by the canonical DDB record). `mimeType == ""`, `sizeBytes == 0`. The slipsheet may still include a `childKey` for orphans — see BR-SLIP-005.
**Source**: Q4 of functional design, BR-BOMB-006
**Verification**: Unit test

### BR-DDB-004 — Idempotent write (BR-IDEMPOTENCY-002)
See BR-IDEMPOTENCY-002.

### BR-DDB-005 — `failureReason` is a controlled vocabulary
**Statement**: `failureReason` values follow a controlled vocabulary so they are machine-parseable:
- `"bomb-defence rule N"` (N = 1..10)
- `"path-traversal" | "absolute-path" | "empty-name" | "invalid-filename"`
- `"unsupported: encrypted-zip" | "unsupported: multi-disk" | "unsupported: deflate64"`
- `"retries exhausted: throttling"`, `"retries exhausted: 5xx"`, `"retries exhausted: timeout"`, `"retries exhausted: network"`
- `"permanent: <aws-error-code>"` (e.g., `"permanent: AccessDenied"`)
- `"corrupt-zip: <details>"`
- `"drain canceled"`
- `"archive aborted: <bomb-defence rule N>"` (recorded on entries that were marked FAILED **due to** an earlier archive-level abort — distinguishes from individual-entry failures)
**Source**: BR-DDB-001, Q4 of functional design
**Verification**: Enum test + slipsheet round-trip PBT

### BR-DDB-006 — `recordedAt` uses injected `Clock`
**Statement**: `recordedAt` is sourced from `extraction.Service.Dependencies.Clock.Now()`, NOT `time.Now()` directly. This enables deterministic PBT and unit testing.
**Source**: PBT-08 reproducibility
**Verification**: Code-review checklist

---

## BR-SLIP: Slipsheet Semantics

### BR-SLIP-001 — Slipsheet location
**Statement**: Slipsheets are written to `s3://{StagingBucket}/slipsheets/{pipelineExecutionId}.json`. The `slipsheets/` prefix is **distinct** from the `input/` prefix so that S3 PutObject events on slipsheet writes do NOT trigger downstream pipeline executions.
**Source**: FR-8.2, Q7 of requirements verification
**Verification**: Unit test on `slipsheet.Writer.Write` target key

### BR-SLIP-002 — End-only write timing with defer coverage
**Statement**: The slipsheet is written exactly once per message processing — via a `defer` block in `extraction.Service.Process` — at the very end. The defer runs unconditionally on success, on archive-level failure, on early terminal failure (schema, source-missing, bomb-pre, unsupported, corrupt), and on panic.
**Source**: Q3 of functional design, FR-8.4
**Verification**: Unit test with synthetic panic

### BR-SLIP-003 — Stub slipsheet for early failures
**Statement**: When an early terminal failure (schema, source-missing, bomb-pre, unsupported, corrupt) prevents any per-entry processing, the slipsheet is still written with `childCount: 0`, `children: []`, and `status: "FAILED"` with `failureReason` populated.
**Source**: FR-8.4, Q3 of functional design
**Verification**: Integration test on each early-failure path

### BR-SLIP-004 — Slipsheet schema
**Statement**: Slipsheet JSON shape:
```json
{
  "type": "archive-container",
  "pipelineExecutionId": "<id>",
  "sourceArchive": "<sourceKey>",
  "childCount": <int>,
  "status": "SUCCESS" | "PARTIAL_FAILED" | "FAILED",
  "failureReason": "<string>",            // present iff status != SUCCESS
  "writtenAt": "<RFC3339-timestamp>",
  "children": [
    {
      "entryIndex": <int>,
      "childKey": "<s3-key-or-empty>",
      "status": "UPLOADED" | "FAILED",
      "failureReason": "<string>",        // present iff status == FAILED
      "sizeBytes": <int>
    }
  ]
}
```
**Source**: §15 input spec, FR-8, expanded for FR-10 / BR-STATUS / BR-DDB-005
**Verification**: PBT-02 round-trip property on `slipsheet.Marshal` / `Unmarshal`

### BR-SLIP-005 — Orphaned children retained in slipsheet
**Statement**: When a bomb-defence violation aborts the archive mid-stream, the slipsheet's `children[]` includes:
1. Each entry that completed (status=UPLOADED) with its actual `childKey` — these are the orphaned S3 objects per BR-BOMB-006.
2. The entry whose Read fired the bomb violation (status=FAILED, `failureReason: "bomb-defence rule N"`).
3. NO entries beyond the failing one (the loop short-circuits).
**Source**: BR-STATUS-002, BR-BOMB-006
**Verification**: Integration test with synthetic mid-stream bomb

### BR-SLIP-006 — Slipsheet write failure is non-fatal
**Statement**: If the slipsheet `PutObject` fails, the failure is logged at `Error` level and a `slipsheet_write_failures_total` metric is incremented. The pipeline's `archiveStatus` is **not** retroactively changed. The SQS message is still deleted (BR-DLQ-001). The per-entry DDB records remain authoritative; a future operational tool can rebuild missing slipsheets from `PIPELINE#<execId>` DDB queries.
**Source**: Q3 of functional design
**Verification**: Integration test with fake slipsheet writer returning error

---

## BR-DLQ: SQS Message Disposition

### BR-DLQ-001 — Delete on every terminal status
**Statement**: Upon `extraction.Service.Process` returning a terminal `Outcome` (SUCCESS, PARTIAL_FAILED, FAILED) — including all bomb-defence, path-validation, unsupported, corrupt-zip, drain-canceled, source-missing, and schema-violation reasons — the SQS receive-loop issues `DeleteMessage`. The message does NOT enter the redrive path.
**Source**: Q8 of functional design
**Verification**: Integration test on each terminal-reason class

### BR-DLQ-002 — Leave for redrive on unhandled panic
**Statement**: If a worker goroutine panics in a manner that escapes the per-worker `recover` block (e.g., a panic during the recover itself — pathological), the SQS message is left for native redrive. The receive-loop's top-level recover logs `FATAL` and exits the worker without deleting.
**Source**: Q8 of functional design
**Verification**: Unit test with synthetic panic

### BR-DLQ-003 — Leave for redrive on transient source-archive download failure
**Statement**: A `*TransientError` returned from the **top-level archive download** (`storage.Download` of the source ZIP) after retries are exhausted is propagated as `*PermanentError`. The outer handler maps this to FAILED with `failureReason: "permanent: source-download-failed"` and DELETES the message.
**Exception**: If `cfg.LeaveSourceDownloadFailuresForRedrive == true` (operator-tunable, default `false`), this specific path leaves the message instead. The default is to delete because re-downloading the same archive 3× during DLQ-bound retries is wasteful and unlikely to recover. Operators may enable redrive if their environment has intermittent S3 outages.
**Source**: Q8 of functional design, FR-2.3
**Verification**: Unit test on each setting

### BR-DLQ-004 — DLQ alerting hook
**Statement**: The chart README documents a recommended Prometheus alert on the SQS DLQ's `ApproximateNumberOfMessagesVisible` metric. The platform team operates the alert; this service does NOT generate DLQ alerts itself.
**Source**: SECURITY-14
**Verification**: README cross-reference check during Build & Test stage

---

## BR-HEARTBEAT: SQS Visibility Heartbeat

### BR-HEARTBEAT-001 — Per-message goroutine
**Statement**: For each in-flight SQS message, the dispatching worker calls `Heartbeater.Start(workerCtx, receiptHandle)`, spawning a goroutine that issues `ChangeMessageVisibility` every `HeartbeatIntervalSec` (default 30 s). The goroutine respects `workerCtx.Done()`.
**Source**: FR-9, Q6 of requirements verification
**Verification**: Stateful PBT-06 on `sqs.heartbeater`

### BR-HEARTBEAT-002 — Visibility extension value
**Statement**: Every heartbeat call sets the new visibility timeout to the queue's full configured visibility — by default 300 s (the value documented as the queue's visibility timeout). The value is NOT cumulative — each call sets the new horizon to "300 s from now."
**Source**: Q6 of functional design
**Verification**: Unit test asserts `ChangeMessageVisibility.VisibilityTimeout == 300`

### BR-HEARTBEAT-003 — Heartbeat survives drain
**Statement**: When the root context is cancelled (graceful drain), the heartbeat goroutine continues to operate against the per-worker context until the worker completes (per Q6 of functional design and Q7 of application design). Only `workerCtx` cancellation stops the heartbeat.
**Source**: Q6 of functional design
**Verification**: Integration test with drain scenario

### BR-HEARTBEAT-004 — Heartbeat error handling
**Statement**: A `ChangeMessageVisibility` failure with error code `ReceiptHandleIsInvalid` or `MessageNotInflight` is logged at `Warn` level and the goroutine exits cleanly (the message has already been deleted or expired — no further heartbeats are useful). Other errors are logged at `Error` level and the goroutine retries on the next tick.
**Source**: FR-9.3
**Verification**: Unit test against fake SDK returning each error class

---

## BR-DRAIN: Graceful Shutdown

### BR-DRAIN-001 — Drain timeout (250 s default)
**Statement**: On root-context cancellation, `app.Service.gracefulDrain` waits up to `GracefulShutdownTimeoutSec` (default 250 s) for in-flight workers to complete naturally.
**Source**: Q7 of application design
**Verification**: Integration test simulating SIGTERM with N in-flight workers

### BR-DRAIN-002 — Readiness flip on SIGTERM
**Statement**: Immediately on SIGTERM (before drain wait starts), `HealthGate.SetReady(false)` is invoked. Kubernetes' readiness-probe negative result removes the pod from the Service's endpoint list. This is purely good hygiene — there is little externally-routable traffic, but the Prometheus scraper stops, reducing noise during drain.
**Source**: Q7 of application design
**Verification**: Integration test on Kubernetes test rig

### BR-DRAIN-003 — Terminate after drain deadline
**Statement**: When the drain deadline fires, the process exits with code 0. Any still-in-flight messages remain at SQS visibility horizon `lastHeartbeat + 300 s`; SQS redelivers them after timeout. Idempotency contract (BR-IDEMPOTENCY-001 / 002) handles duplicates safely.
**Source**: Q7 of application design
**Verification**: Stress test with simulated stuck worker

### BR-DRAIN-004 — Terminate before drain deadline if pool is empty
**Statement**: If the worker pool becomes empty before the drain deadline, `gracefulDrain` returns immediately. The process does NOT wait for the full 250 s.
**Source**: Q7 of application design
**Verification**: Unit test on drain function with mock pool

---

## BR-LOG: Logging Discipline

### BR-LOG-001 — Mandatory fields per log entry
**Statement**: Every log entry SHALL include at minimum: `timestamp`, `level`, `message`, `service` (constant `"zip-extraction"`), `version`. Per-message handlers SHALL bind `pipelineExecutionId`, `correlationId`, `documentId` to the logger using `Logger.With(...)`.
**Source**: NFR-5.1, SECURITY-03
**Verification**: Linter rule + grep on test log fixtures

### BR-LOG-002 — Sensitive-field deny-list
**Statement**: The `Logger.With` and `Info/Warn/Error/Debug` methods filter outbound fields whose key matches (case-insensitive) any of: `password`, `passwd`, `secret`, `token`, `credential`, `aws_access_key_id`, `aws_secret_access_key`, `session_token`, `api_key`, `authorization`. Matched field values are replaced with `"[REDACTED]"` and the **filter event itself is logged at Warn level** (one-time per process startup or via rate-limited warning) so any accidental sensitive-field usage is observable.
**Source**: SECURITY-03, SECURITY-09
**Verification**: PBT-03 invariant: emitted JSON never contains a string matching the deny-list values

### BR-LOG-003 — No exception traces in production responses
**Statement**: HTTP responses on `/healthz/*` and `/metrics` MUST NOT include stack traces, panic details, or internal error messages. The body is a fixed minimal payload (`{"status":"ok"}` or `prom-exposition`). Errors from underlying systems are logged but not exposed via HTTP.
**Source**: SECURITY-09, SECURITY-15
**Verification**: Unit test on HTTP handlers' error paths

### BR-LOG-004 — Bomb-defence rejections logged at Warn with rule context
**Statement**: A bomb-defence rejection emits one log line at `Warn` level with fields: `event="bomb-rejection"`, `rule=<int>`, `reason=<string>`, `pipelineExecutionId`, `sourceArchive`. Additionally `metrics.BombRejection(rule)` is called.
**Source**: SECURITY-14, FR-7.1
**Verification**: Unit test verifying log + metric

---

## BR-CLEAN: Cleanup

### BR-CLEAN-001 — `defer`-based teardown
**Statement**: Every resource acquired during `Process` (S3 response body, ZIP reader, heartbeat goroutine, AWS client per-request context) is paired with a `defer <closer>()` immediately after acquisition. Cleanup runs in LIFO order at function exit.
**Source**: FR-11.2, SECURITY-15
**Verification**: Code-review checklist; lint rule `defer-after-acquire`

### BR-CLEAN-002 — Idempotent cleanup
**Statement**: All `defer` cleanup functions are safe to call on partial state (e.g., a `nil` `io.Closer`). Implementations check for nil before invoking `Close()`.
**Source**: FR-11.2
**Verification**: Unit test with partial-state cleanup

### BR-CLEAN-003 — Heartbeat cancellation precedes process exit
**Statement**: The `Heartbeater.Start` returned `cancel` function is invoked in a `defer` immediately after the start call (`defer cancel()`). This ensures the heartbeat goroutine is cancelled before the worker completes, regardless of how the worker exits (return, panic, ctx cancellation).
**Source**: Q6 of requirements, BR-HEARTBEAT-001
**Verification**: Stateful PBT-06 — count of active goroutines returns to zero post-worker

---

## Cross-Reference Matrix

| Business rule | Source FR/NFR/Q&A | PBT properties | SECURITY rules |
|---|---|---|---|
| BR-BOMB-001..008 | FR-7, Q1-FD | PBT-03, PBT-05 | SECURITY-05, SECURITY-06 |
| BR-PATH-001..006 | FR-6, FR-7 #7/#8 | PBT-03, PBT-04 | SECURITY-05 |
| BR-MIME-001..003 | Q6-AD, Q7-FD | PBT-05 | — |
| BR-RETRY-001..014 | FR-12, Q5-FD, Q12-Req | PBT-03, PBT-05, PBT-06 | SECURITY-15 |
| BR-IDEMPOTENCY-001..006 | FR-5.3, Q2-FD | PBT-02, PBT-04 | SECURITY-15 |
| BR-STATUS-001..004 | FR-10, FR-7.1 | PBT-06 | — |
| BR-DDB-001..006 | FR-5, Q4-FD | PBT-02, PBT-03 | SECURITY-01 |
| BR-SLIP-001..006 | FR-8, Q3-FD, Q7-Req | PBT-02 | — |
| BR-DLQ-001..004 | Q8-FD | — | SECURITY-14 |
| BR-HEARTBEAT-001..004 | FR-9, Q6-Req, Q6-FD | PBT-06 | — |
| BR-DRAIN-001..004 | Q7-AD | — | SECURITY-15 |
| BR-LOG-001..004 | NFR-5, SECURITY-03 | PBT-03 | SECURITY-03, SECURITY-09 |
| BR-CLEAN-001..003 | FR-11 | PBT-06 | SECURITY-15 |

---

## Compliance Summary

This rule set has been validated against:

- **SECURITY-01 … SECURITY-15**: every applicable rule maps to one or more business rules. The 3 N/A determinations from `requirements.md` NFR-6 remain N/A at this stage (no LB/gateway/CDN; no HTML endpoints; no human-facing routes).
- **PBT-01 … PBT-10**: every business rule with a property in its right-hand cross-reference column is testable via the framework selected (`pgregory.net/rapid`).

**No new blocking SECURITY or PBT findings at the Functional Design stage.**
