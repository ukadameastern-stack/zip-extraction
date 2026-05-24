# Integration Test Instructions — zip-extraction (UOW-SVC-12)

**Test Gate**: Gate 2 (per NFR-9.2 of requirements + NFR-Z-082)
**Scope**: End-to-end tests against Testcontainers-managed LocalStack
**Location**: `services/zip-extraction/test/e2e/`
**Build tag**: `e2e` (excluded from default `go test ./...` runs)

## Purpose

Verify that the full message-processing lifecycle works against real AWS-SDK-v2 client calls + actual SQS / S3 / DynamoDB / STS implementations (via LocalStack). Closes the gap between unit-level mocking and production.

## Prerequisites

- Docker daemon running with ≥ 2 GB available memory
- Network access to `localstack/localstack:3.7` (or the pinned digest)
- Linux/macOS host; Windows Docker Desktop also supported

## Scenarios

### Scenario 1: Happy-Path SUCCESS

| Aspect | Detail |
|---|---|
| Setup | LocalStack with provisioned bucket + queue + DLQ + DDB table. Upload a 3-entry valid ZIP to `s3://source/uploads/exec-1.zip`. Send SQS message referencing it. |
| Expected | (a) 3 child objects appear under `s3://staging/input/exec-1/`. (b) 3 DDB rows with `status=UPLOADED`. (c) 1 slipsheet at `s3://staging/slipsheets/exec-1.json` with `status=SUCCESS`. (d) Metric `zip_entries_total{status="UPLOADED"}` increments by 3. |
| Cleanup | Container teardown via Testcontainers' `defer container.Terminate(ctx)`. |

### Scenario 2: Bomb-Defence Rejection

| Aspect | Detail |
|---|---|
| Setup | Upload a ZIP whose central directory declares 11000 entries (rule #4 violation). |
| Expected | (a) NO objects under `s3://staging/input/exec-2/`. (b) NO DDB rows. (c) Slipsheet at `slipsheets/exec-2.json` with `status=FAILED, failureReason="bomb-defence rule 4"`. (d) Metric `zip_bomb_rejections_total{rule="4"}` increments by 1. |

### Scenario 3: Path-Traversal Rejection

| Aspect | Detail |
|---|---|
| Setup | ZIP containing an entry named `../../etc/passwd`. |
| Expected | (a) NO child upload for the malicious entry. (b) Slipsheet `status=FAILED, failureReason="path-traversal"`. |

### Scenario 4: Unsupported-Feature Rejection

| Aspect | Detail |
|---|---|
| Setup | Encrypted ZIP (any entry with flag bit 0x1). |
| Expected | Slipsheet `status=FAILED, failureReason="unsupported: encrypted-zip"`. |

### Scenario 5: Transient-then-Retry → PARTIAL_FAILED

| Aspect | Detail |
|---|---|
| Setup | 100-entry archive; one entry is configured (via test hook in storage adapter) to return throttling errors for 4 consecutive attempts. |
| Expected | (a) 99 entries `UPLOADED`. (b) 1 entry `FAILED` with `failureReason="retries exhausted: throttling"`. (c) Slipsheet `status=PARTIAL_FAILED`. (d) Metric `partial_failures_total` increments by 1. |

### Scenario 6: Re-Delivery Idempotency

| Aspect | Detail |
|---|---|
| Setup | After Scenario 1 completes, manually re-enqueue the same SQS message (simulating SQS at-least-once redelivery). |
| Expected | (a) Same 3 S3 children present (idempotent overwrite — same ETag). (b) DDB rows unchanged (CCFE intercepted). (c) `redelivery_skips_total` increments by 3. (d) Slipsheet rewritten with the same content. |

## Execution

### 1. Start the test harness

```bash
cd services/zip-extraction
go test -tags=e2e ./test/e2e/...
```

This:
1. Spins up a LocalStack container via Testcontainers.
2. Provisions bucket / queue / DLQ / DDB table.
3. Starts the service binary (in-process for the placeholder; full subprocess pattern to be wired in Build & Test stage post-approval).
4. Runs each scenario.
5. Tears down the container.

### 2. Verify outputs

Test output includes per-scenario PASS/FAIL lines. Expected: all PASS.

### 3. View LocalStack logs (debugging)

```bash
docker logs $(docker ps -q --filter "ancestor=localstack/localstack:3.7")
```

### 4. Run a single scenario

```bash
go test -tags=e2e -run TestE2E_HappyPath_SUCCESS ./test/e2e/...
```

## Configuration

LocalStack endpoint defaults to whatever Testcontainers exposes (random host port). Environment overrides for advanced debugging:

```bash
TESTCONTAINERS_RYUK_DISABLED=true \
    go test -tags=e2e -v ./test/e2e/...
# Disables the cleanup container — useful when debugging a hung test.
```

## Cleanup

Testcontainers' `defer container.Terminate(ctx)` handles teardown automatically. To force cleanup of orphaned containers:

```bash
docker ps --filter "label=org.testcontainers" -q | xargs -r docker rm -f
```

## Known Limitations

- LocalStack's SQS implementation does NOT fully simulate visibility-timeout behaviour for `ChangeMessageVisibility` extension under heavy load. Scenario 5 may need a slightly relaxed assertion window.
- LocalStack's S3 PutObject events are NOT triggered the same way they would be in production. Tests verify direct S3 reads instead.
- LocalStack's KMS does NOT actually encrypt — SSE-KMS tests verify only that the correct API parameters are sent.

## Skipped Scenarios

The `localstack_test.go` file currently includes `TestE2E_HappyPath_SUCCESS` as a smoke test plus documented placeholders for Scenarios 2–6. The Build & Test stage will populate the assertion bodies once the service-binary-lifecycle pattern (in-process vs subprocess) is finalised — this is the only remaining Code Generation TODO and is acknowledged in `code/group-h.md`.

## Gate 3 (Sandbox EKS E2E) — DEFERRED

Per Q11 of requirements verification, Gate 3 tests against a sandbox EKS environment with real IRSA credentials remain deferred. The chart README documents the platform-team-owned hand-off when that environment is available.
