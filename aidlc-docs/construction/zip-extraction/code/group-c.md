# Group C Summary — Steps 12–18 (Domain Layer; No I/O)

| Step | File | Highlights |
|---|---|---|
| 12 | `internal/extraction/types.go` | Status enum (SUCCESS / PARTIAL_FAILED / FAILED) + `ClaimCheck`, `ArchiveMetadata`, `EntryInfo`, `EntryOutcome`, `PipelineFile`, `Outcome` structs; `String()` method on Status; vocabulary constants for unsupported features and entry status. |
| 13 | `internal/extraction/errors.go` | Typed-error hierarchy (`BombDefenceError`, `PathValidationError`, `UnsupportedFeatureError`, `TransientError`, `PermanentError`) + `Is*` classifier helpers using `errors.As`; controlled-vocabulary constants for `PathReason*` and `TransientClass*`. |
| 14 | `internal/extraction/ports.go` | Consumer-defined port interfaces (`S3Downloader`, `S3Uploader`, `Recorder`, `SlipsheetWriter`, `BombChecker`, `PathValidator`, `Retrier`, `Metrics`, `Logger`, `Clock`); `Dependencies` DI root; `SystemClock` production implementation. |
| 15 | `internal/validation/validator.go` | `PathValidator.Sanitize`: URL-decode → backslash normalise → drive-letter check → leading-`/` check → `..` segment check → `filepath.Clean` → post-Clean traversal re-check → base-filename extraction → control-char + UTF-8 + length validation. Returns base filename only; idempotent (BR-PATH-005). |
| 16 | `internal/bombdefence/checker.go` + `format.go` | `Checker.PreCheck` (rules #1, #4), `EntryCheck` (#5, #6, #9), `NewLimitedReader` (#2, #3 short-circuiting). LimitedReader maintains sticky-error state — once tripped, subsequent reads return same error. Small-sample floor (64 KiB compressed) prevents false-positive ratio violations on tiny streams. |
| 17 | `internal/retry/retry.go` | `Retrier.Do` with bounded 3-attempt classifier-driven retry; `BackoffFor` exported as PBT-05 oracle (`baseMs * factor^n * (1 + jitter*r)`); `Classify` distinguishes throttling / 5xx / timeout / network / 4xx using smithy-go `APIError` interface + HTTP status interface + `net.OpError` + `url.Error`; `AsTransient` helper for adapters to wrap classified errors. |
| 18 | `internal/slipsheet/slipsheet.go` + `util.go` | `Slipsheet` + `ChildEntry` JSON structs; `Build()` constructs deterministic-order summary; `Marshal/Unmarshal` round-trip helpers; `Writer.Write` persists to `<prefix><execId>.json` via `extraction.S3Uploader` port. |

Compliance:
- SECURITY-05: input validation in validation + bombdefence + parseMessage (sqs in Group D).
- SECURITY-11: bombdefence + validation are leaf packages depending only on `internal/extraction` typed errors.
- SECURITY-15: typed errors propagate fail-closed; no silent success on edge cases.
- PBT-02: round-trip helpers in slipsheet + (Group D: dynamodb).
- PBT-03: invariants enforced by validator + checker + LimitedReader sticky-error.
- PBT-04: `Sanitize` idempotence.
- PBT-05: `retry.BackoffFor` exported oracle.
