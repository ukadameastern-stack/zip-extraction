# Domain Entities — zip-extraction (UOW-SVC-12)

**Document Type**: Domain Entity Definitions & Value Domains
**Phase**: CONSTRUCTION — Functional Design (Part 2: Generation)
**Generated**: 2026-05-24
**Unit**: `zip-extraction` (UOW-SVC-12)

This document records the **detailed domain entities** that flow through the Zip Extraction Service. Each entity is presented as a Go struct sketch (for unambiguity), followed by:

- **Field-level documentation** with value-domain constraints and invariants
- **Equality and hashing semantics** where they affect idempotency
- **PBT generator notes** to be carried forward into Code Generation

The entities are grouped into 6 categories:

1. **Message contract** (input from SQS)
2. **Archive metadata** (extracted from the source ZIP)
3. **Per-entry domain** (entry-level state)
4. **Persistence** (DynamoDB row + S3 slipsheet)
5. **Outcome** (status reporting back to the SQS handler)
6. **Typed-error hierarchy** (defined in `internal/extraction`)

---

## 1. Message Contract

### `ClaimCheck`

Inbound SQS message body — the *claim-check* pointer to the source archive in S3.

```go
type ClaimCheck struct {
    PipelineExecutionID string  `json:"pipelineExecutionId"`
    TenantID            string  `json:"tenantId"`
    DocumentID          string  `json:"documentId"`
    SourceBucket        string  `json:"sourceBucket"`
    SourceKey           string  `json:"sourceKey"`
    CorrelationID       string  `json:"correlationId"`
}
```

| Field | Constraints | Invariants |
|---|---|---|
| `PipelineExecutionID` | non-empty; ≤ 128 chars; `[A-Za-z0-9_-]+` | Used as DDB PK prefix (`PIPELINE#…`) AND as S3 prefix (`input/{id}/`). Mismatch in either consumer-side parsing is a bug. |
| `TenantID` | non-empty; ≤ 64 chars | Carried in logs and DDB row for multi-tenant audit |
| `DocumentID` | non-empty; ≤ 128 chars | Carried as `documentId` attribute in DDB rows |
| `SourceBucket` | non-empty; valid S3 bucket name | S3 GetObject target |
| `SourceKey` | non-empty; ≤ 1024 chars | S3 GetObject key |
| `CorrelationID` | non-empty; ≤ 64 chars | Cross-service tracing field |

**Equality**: by `PipelineExecutionID` alone (the idempotency anchor). Two `ClaimCheck` values with the same `PipelineExecutionID` but different `SourceKey` are a delivery anomaly — the second is ignored if the first has already begun (in practice this would only occur from a producer bug; not a redelivery case).

**PBT generator** (`gens.ClaimCheck()`):
- `PipelineExecutionID`: `rapid.StringMatching("[A-Za-z0-9_-]{1,128}")`
- `SourceKey`: `rapid.StringMatching("uploads/[A-Za-z0-9_/.-]{1,255}\\.zip")`
- Other strings: bounded ASCII generators

---

## 2. Archive Metadata

### `ArchiveMetadata`

Aggregate metadata extracted from the source ZIP **immediately after opening**, used for the bomb-defence pre-check (BR-BOMB-001) and for diagnostics.

```go
type ArchiveMetadata struct {
    EntryCount             int
    TotalCompressedBytes   int64
    TotalDeclaredUncompressedBytes int64
    ZIP64                  bool
    Encrypted              bool
    MultiDisk              bool
    HasDeflate64Entries    bool
}
```

| Field | Constraints | Source |
|---|---|---|
| `EntryCount` | ≥ 0; bounded by FR-7 rule #4 default 10 000 | `len(zipReader.File)` |
| `TotalCompressedBytes` | ≥ 0; bounded by FR-7 rule #1 default 500 MB | Sum of `*zip.File.CompressedSize64` |
| `TotalDeclaredUncompressedBytes` | ≥ 0 (advisory only; untrusted) | Sum of `*zip.File.UncompressedSize64` — used for sanity logging, NOT for bomb decisions (streaming counter is authoritative) |
| `ZIP64` | bool | Detected via `*zip.File.UncompressedSize64 > math.MaxUint32` or Zip64 extra-field presence |
| `Encrypted` | bool | Any entry has `Flags & 0x1 != 0` → reject as `*UnsupportedFeatureError{Feature:"encrypted-zip"}` per FR-3.6 |
| `MultiDisk` | bool | Central-directory `NumberOfThisDisk != 0` or `NumberOfEntriesOnDisk != TotalEntries` → reject `*UnsupportedFeatureError{Feature:"multi-disk"}` per FR-3.6 |
| `HasDeflate64Entries` | bool | Any entry has `Method == 9` (Deflate64) → reject `*UnsupportedFeatureError{Feature:"deflate64"}` per FR-3.6 |

**Invariants**:
- If `Encrypted || MultiDisk || HasDeflate64Entries`, the archive is **never** iterated — `*UnsupportedFeatureError` is raised before BR-BOMB-001 evaluates.
- `EntryCount > 0` is NOT required for SUCCESS — an empty archive that passes all pre-checks is a valid SUCCESS with `childCount: 0` (BR-STATUS-001 last row).

**PBT generator**: synthetic metadata generator covers (a) in-bounds, (b) one-rule-violated, (c) multiple-rule-violated, and (d) unsupported-feature cases.

---

## 3. Per-Entry Domain

### `EntryInfo`

A *subset* of `*zip.File` containing only the fields the domain code needs. Decoupling from the SDK type makes `bombdefence.EntryCheck` PBT-testable without constructing real ZIP archives.

```go
type EntryInfo struct {
    Name             string       // raw entry name from ZIP central directory
    Mode             os.FileMode  // entry file mode bits
    CompressedSize   int64        // bytes on disk in the archive
    UncompressedSize int64        // declared decompressed bytes (UNTRUSTED — see below)
    Method           uint16       // compression method (8=Deflate, 0=Store, etc.)
    DirectoryDepth   int          // computed from filepath.Clean(Name)
}
```

| Field | Constraints | Notes |
|---|---|---|
| `Name` | raw string; not yet validated | Input to `validation.Sanitize` (BR-PATH-*) |
| `Mode` | full `os.FileMode` | Symlink check (BR-BOMB-002 rule #6): `Mode().Type() & os.ModeSymlink != 0` |
| `CompressedSize` | ≥ 0 | Contributes to `LimitedReader` compression-ratio denominator |
| `UncompressedSize` | ≥ 0; UNTRUSTED | Used for rule #9 (declared single-file size); the streaming `LimitedReader` (BR-BOMB-003) is the authoritative ceiling regardless of what the header claims |
| `Method` | uint16; supported = 0, 8; unsupported = 9 (Deflate64) | Deflate64 caught at `ArchiveMetadata.HasDeflate64Entries` |
| `DirectoryDepth` | ≥ 0 | `strings.Count(filepath.Clean(Name), "/")` per BR-BOMB-002 rule #5 |

### `EntryOutcome`

Result of processing one entry. One value per entry, even on FAILED.

```go
type EntryOutcome struct {
    Index         int       // 1-based, matches DDB sk and S3 key padding
    SafeName      string    // post-Sanitize; "" if path-validation failed before processing
    ChildKey      string    // S3 key; "" if Status == "FAILED"
    MimeType      string    // detected MIME; "" if Status == "FAILED"
    SizeBytes     int64     // decompressed bytes successfully uploaded; 0 if FAILED
    Status        string    // "UPLOADED" | "FAILED"
    FailureReason string    // controlled vocabulary per BR-DDB-005; empty if Status == "UPLOADED"
    RecordedAt    time.Time // from injected Clock
}
```

**Invariants**:
- `Status == "UPLOADED"` ⇒ `ChildKey != ""` AND `MimeType != ""` AND `SizeBytes ≥ 0` AND `FailureReason == ""`.
- `Status == "FAILED"` ⇒ `ChildKey == ""` (canonical) AND `FailureReason != ""`.
- `Index ≥ 1` always.

**Exception** for orphaned bomb-defence children (per BR-BOMB-006 + BR-SLIP-005): the **slipsheet** child entry for an orphaned object retains the `ChildKey` populated (so consumers know it exists in S3). But the **DDB record** for that same entry uses `ChildKey: ""` (canonical) since the orphan is no longer reachable through DDB queries. This split is intentional: the slipsheet is the audit record; DDB is the queryable index.

**PBT generator** (`gens.EntryOutcome()`):
- Half UPLOADED, half FAILED (configurable via biased generator).
- FAILED `FailureReason` drawn from the controlled vocabulary in BR-DDB-005.
- `Index` strictly increasing within a generated batch.

---

## 4. Persistence Entities

### `PipelineFile` (DynamoDB row)

One row per entry — written by `dynamodb.Adapter.RecordEntry` per BR-DDB-001 / 002 / 003.

```go
type PipelineFile struct {
    PK            string    `dynamodbav:"pk"`               // "PIPELINE#" + pipelineExecutionId
    SK            string    `dynamodbav:"sk"`               // "FILE#" + fmt.Sprintf("%04d", index)
    DocumentID    string    `dynamodbav:"documentId"`
    SourceArchive string    `dynamodbav:"sourceArchive"`    // = ClaimCheck.SourceKey
    ChildKey      string    `dynamodbav:"childKey"`
    MimeType      string    `dynamodbav:"mimeType"`
    Status        string    `dynamodbav:"status"`            // "UPLOADED" | "FAILED"
    SizeBytes     int64     `dynamodbav:"sizeBytes"`
    FailureReason string    `dynamodbav:"failureReason,omitempty"`
    RecordedAt    time.Time `dynamodbav:"recordedAt"`
}
```

**Constraints**:
- `PK` matches regex `^PIPELINE#[A-Za-z0-9_-]{1,128}$`.
- `SK` matches regex `^FILE#\d{4,}$` (4-digit zero-padded, allowing more digits for archives ≥10 000 entries — though those are rejected by BR-BOMB-001 rule #4).
- All other invariants flow from `EntryOutcome` (Status / ChildKey / MimeType / SizeBytes / FailureReason).

**Conditional write expression** (BR-IDEMPOTENCY-002):
```text
ConditionExpression: attribute_not_exists(pk)
```

**Round-trip property** (PBT-02): `Unmarshal(Marshal(rec)) == rec` for any valid generated `PipelineFile`.

---

### `Slipsheet` (S3 JSON object)

Written once per message via `slipsheet.Writer.Write` to `slipsheets/{PipelineExecutionID}.json` (BR-SLIP-001).

```go
type Slipsheet struct {
    Type                string       `json:"type"`                // constant "archive-container"
    PipelineExecutionID string       `json:"pipelineExecutionId"`
    SourceArchive       string       `json:"sourceArchive"`
    ChildCount          int          `json:"childCount"`
    Status              string       `json:"status"`              // "SUCCESS"|"PARTIAL_FAILED"|"FAILED"
    FailureReason       string       `json:"failureReason,omitempty"`
    WrittenAt           time.Time    `json:"writtenAt"`
    Children            []ChildEntry `json:"children"`
}

type ChildEntry struct {
    EntryIndex    int    `json:"entryIndex"`
    ChildKey      string `json:"childKey"`
    Status        string `json:"status"`                          // "UPLOADED" | "FAILED"
    FailureReason string `json:"failureReason,omitempty"`
    SizeBytes     int64  `json:"sizeBytes"`
}
```

**Constraints**:
- `Type == "archive-container"` (constant; used by downstream MIME-/Type-based routing).
- `Status == "SUCCESS"` ⇒ `FailureReason == ""` AND every `ChildEntry.Status == "UPLOADED"`.
- `Status == "PARTIAL_FAILED"` ⇒ ≥ 1 `ChildEntry.Status == "UPLOADED"` AND ≥ 1 `ChildEntry.Status == "FAILED"`.
- `Status == "FAILED"` ⇒ `FailureReason != ""` AND (every `ChildEntry.Status == "FAILED"` OR the failure was archive-level abort per BR-STATUS-002).
- `ChildCount == len(Children)` always.
- `Children[i].EntryIndex < Children[i+1].EntryIndex` (sorted) — gives downstream consumers a stable, deterministic order.
- `Children` for an **early terminal failure** (BR-SLIP-003 stub) is `[]` (empty array, NOT `null`).

**Round-trip property** (PBT-02): `Unmarshal(Marshal(ss)) == ss` for any valid generated `Slipsheet`.

**PBT generator** (`gens.Slipsheet()`):
- `Status`-driven: generators for SUCCESS, PARTIAL_FAILED, FAILED with appropriate `Children` shapes.
- `FailureReason` drawn from controlled vocabulary.

---

## 5. Outcome (Internal Return Type)

### `Outcome`

Returned by `extraction.Service.Process` to the SQS handler.

```go
type Outcome struct {
    Status      Status
    Reason      string // populated when Status != SUCCESS
    EntryCount  int
    FailureCount int
    DurationMs  int64
}

type Status int

const (
    StatusSuccess Status = iota
    StatusPartialFailed
    StatusFailed
)

func (s Status) String() string {
    switch s {
    case StatusSuccess:        return "SUCCESS"
    case StatusPartialFailed:  return "PARTIAL_FAILED"
    case StatusFailed:         return "FAILED"
    default:                   return "UNKNOWN"
    }
}
```

**Mapping to SQS disposition** (BR-DLQ-001 / 002 / 003):

| Outcome.Status | Outcome.Reason | SQS action |
|---|---|---|
| SUCCESS | "" | DeleteMessage |
| PARTIAL_FAILED | "<one or more entries failed>" | DeleteMessage |
| FAILED | "bomb-defence rule N" | DeleteMessage |
| FAILED | "path-traversal" / "absolute-path" / "invalid-filename" | DeleteMessage |
| FAILED | "unsupported: encrypted-zip" / etc. | DeleteMessage |
| FAILED | "corrupt-zip: …" | DeleteMessage |
| FAILED | "drain canceled" | DeleteMessage |
| FAILED | "permanent: source-download-failed" | DeleteMessage (per BR-DLQ-003 default) OR Leave (if operator override) |
| FAILED | "schema: <details>" | DeleteMessage |
| FAILED | "panic: <details>" | Leave (per BR-DLQ-002) |

---

## 6. Typed-Error Hierarchy

Defined in `internal/extraction/errors.go` per Q4 of application design.

### `BombDefenceError`

```go
type BombDefenceError struct {
    Rule   int    // 1..10 (matches FR-7 numbering)
    Reason string // human-readable, e.g., "cumulative extracted size 2.1GB exceeds cap 2GB"
}

func (e *BombDefenceError) Error() string {
    return fmt.Sprintf("bomb-defence rule %d: %s", e.Rule, e.Reason)
}
```

**Constraints**:
- `1 ≤ Rule ≤ 10`
- `Reason` non-empty

**Used by**: BR-BOMB-001 … 008, BR-RETRY-009 / 012, BR-STATUS-002

### `PathValidationError`

```go
type PathValidationError struct {
    Path   string // the offending raw input
    Reason string // controlled vocabulary: "path-traversal" | "absolute-path" | "empty-name" | "invalid-filename"
}

func (e *PathValidationError) Error() string {
    return fmt.Sprintf("path validation: %s (path=%q)", e.Reason, e.Path)
}
```

**Constraints**:
- `Path` may be very long — logging callers MUST truncate (e.g., to 128 chars) before emitting.
- `Reason` MUST match controlled vocabulary.

**Used by**: BR-PATH-001 … 006, BR-RETRY-009, BR-STATUS-002

### `UnsupportedFeatureError`

```go
type UnsupportedFeatureError struct {
    Feature string // "encrypted-zip" | "multi-disk" | "deflate64"
}

func (e *UnsupportedFeatureError) Error() string {
    return fmt.Sprintf("unsupported zip feature: %s", e.Feature)
}
```

**Constraints**:
- `Feature` MUST match controlled vocabulary; `default` case in classifier treats anything else as a programmer error and triggers an assertion in test builds.

**Used by**: FR-3.6, BR-RETRY-009

### `TransientError`

```go
type TransientError struct {
    Cause error
    Class string // "throttling" | "5xx" | "timeout" | "network"
}

func (e *TransientError) Error() string {
    return fmt.Sprintf("transient (%s): %v", e.Class, e.Cause)
}

func (e *TransientError) Unwrap() error { return e.Cause }
```

**Constraints**:
- `Class` MUST match controlled vocabulary.
- `Cause != nil` always.

**Used by**: BR-RETRY-001 … 008, BR-DLQ-003

### `PermanentError`

```go
type PermanentError struct {
    Cause error
}

func (e *PermanentError) Error() string {
    return fmt.Sprintf("permanent: %v", e.Cause)
}

func (e *PermanentError) Unwrap() error { return e.Cause }
```

**Constraints**:
- `Cause != nil` always.

**Used by**: BR-RETRY-008, BR-RETRY-010, BR-IDEMPOTENCY-002 (after exhaustion)

### Classifier helpers (per `component-methods.md`)

```go
func IsBombDefence(err error) (*BombDefenceError, bool)
func IsPathValidation(err error) (*PathValidationError, bool)
func IsUnsupportedFeature(err error) (*UnsupportedFeatureError, bool)
func IsTransient(err error) (*TransientError, bool)
func IsPermanent(err error) (*PermanentError, bool)
```

Each helper uses `errors.As` for unwrapping. **Multiple wrapping** (e.g., `fmt.Errorf("upload: %w", &TransientError{...})`) is supported because the helpers traverse the unwrap chain.

---

## 7. Value-Domain Reference Tables

### `EntryOutcome.Status` / `PipelineFile.Status` / `ChildEntry.Status`

| Value | When |
|---|---|
| `"UPLOADED"` | S3 PUT succeeded AND DDB write succeeded (or returned CCFE = idempotent re-write) |
| `"FAILED"` | Any of: retry-exhausted *TransientError, *PermanentError, archive-level abort caught this entry mid-flight |

### `Slipsheet.Status` / `Outcome.Status` (`Status` enum)

| Value | When |
|---|---|
| `"SUCCESS"` | Every entry UPLOADED |
| `"PARTIAL_FAILED"` | ≥1 entry UPLOADED AND ≥1 entry FAILED, no archive-level abort |
| `"FAILED"` | Archive-level abort OR zero entries succeeded OR early terminal failure |

### `FailureReason` (controlled vocabulary)

| Value | Source |
|---|---|
| `"bomb-defence rule 1"` … `"bomb-defence rule 10"` | BR-BOMB-* |
| `"path-traversal"` | BR-PATH-001 |
| `"absolute-path"` | BR-PATH-002 |
| `"empty-name"` | BR-PATH-003 |
| `"invalid-filename"` | BR-PATH-004 |
| `"unsupported: encrypted-zip"` | FR-3.6 |
| `"unsupported: multi-disk"` | FR-3.6 |
| `"unsupported: deflate64"` | FR-3.6 |
| `"retries exhausted: throttling"` | BR-RETRY-002 |
| `"retries exhausted: 5xx"` | BR-RETRY-002 |
| `"retries exhausted: timeout"` | BR-RETRY-002 |
| `"retries exhausted: network"` | BR-RETRY-002 |
| `"permanent: <aws-error-code>"` | BR-RETRY-008 |
| `"corrupt-zip: <details>"` | Opening failure |
| `"drain canceled"` | BR-DRAIN-* |
| `"archive aborted: <bomb-defence rule N>"` | Applied to entries marked FAILED **due to** an earlier archive-level abort |
| `"schema: <details>"` | BR-LOG-001 (malformed SQS message) |
| `"panic: <details>"` | BR-DLQ-002 (caught by recover) |

### `TransientError.Class`

| Value | Source AWS conditions |
|---|---|
| `"throttling"` | ProvisionedThroughputExceeded, SlowDown, RequestLimitExceeded, Throttling, ThrottlingException |
| `"5xx"` | HTTP status ∈ [500, 599] |
| `"timeout"` | RequestTimeout, per-request DeadlineExceeded |
| `"network"` | net.OpError, *url.Error (temp), DNS failures |

### `UnsupportedFeatureError.Feature`

| Value | Source |
|---|---|
| `"encrypted-zip"` | FR-3.6 |
| `"multi-disk"` | FR-3.6 |
| `"deflate64"` | FR-3.6 |

---

## 8. PBT Generator Catalogue (for Code Generation Hand-off)

These generator names are referenced in `business-rules.md` and `component-methods.md` Testable Properties tables. They live in `test/generators/` (PBT-07 centralisation).

| Generator | Domain | Used by tests in |
|---|---|---|
| `gens.ClaimCheck()` | `ClaimCheck` | `sqs`, `extraction` |
| `gens.ClaimCheck().Filter(invalid)` | invalid SQS messages | `sqs.parseMessage` |
| `gens.ArchiveMetadata()` | bounded valid + edge cases | `bombdefence.Checker.PreCheck` |
| `gens.ArchiveMetadata().Bomb(rule)` | metadata violating rule `N` | `bombdefence` negative tests |
| `gens.EntryInfo()` | valid entries | `bombdefence.Checker.EntryCheck` |
| `gens.EntryInfo().Bomb(rule)` | violating rule 5/6/9 | negative tests |
| `gens.RawPath()` | mix of legitimate + adversarial paths | `validation.Sanitize` |
| `gens.RawPath().Traversal()` / `.Absolute()` | adversarial subsets | negative tests |
| `gens.EntryOutcome()` | mixed UPLOADED + FAILED | `extraction.computeStatus`, `slipsheet.Build` |
| `gens.PipelineFile()` | round-trip generator | `dynamodb.Marshal/Unmarshal` |
| `gens.Slipsheet()` | round-trip generator | `slipsheet.Marshal/Unmarshal` |
| `gens.SDKError(class)` | synthetic AWS SDK errors per `class` | `retry.Classify` |
| `gens.AttemptN()` | bounded integer for backoff oracle | `retry.BackoffFor` |
| `gens.HeartbeatCommandSeq()` | command sequence for stateful PBT | `sqs.heartbeater` |

---

## 9. Compliance Notes

- **SECURITY-05** (input validation): every entity that crosses an external boundary (SQS message → `ClaimCheck`, ZIP entry → `EntryInfo`) has explicit field-level constraints documented above.
- **SECURITY-03** (logging): every `*Error.Error()` includes enough context to be diagnostic without leaking sensitive content. `PathValidationError` advises truncation when logging.
- **PBT-07** (generator quality): generators are domain-typed, parameterisable, and centralised — no PBT consumer creates ad-hoc generators inline.
- **PBT-02 / PBT-03 / PBT-04**: every entity participating in round-trip / invariant / idempotence properties has its constraint list documented as the authoritative source for property statements.

**No new blocking SECURITY or PBT findings at the Functional Design stage.**
