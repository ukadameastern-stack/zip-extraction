# Unit Test Execution Instructions — zip-extraction (UOW-SVC-12)

**Test Gate**: Gate 1 (per NFR-9.1 of requirements + NFR-Z-073)
**Scope**: All `_test.go` files under `services/zip-extraction/internal/*` PLUS PBT property tests using `pgregory.net/rapid`

## Run All Unit Tests

```bash
cd services/zip-extraction
make test
```

This invokes:
```bash
go test -race -cover ./...
```

`-race` enables the data-race detector, which is mandatory given the goroutine concurrency in `internal/sqs` and `internal/extraction`.

## Test Categories (per Package)

| Package | Test File | Test Categories |
|---|---|---|
| `internal/validation` | `validator_test.go` | Example: traversal/absolute/empty/control-char rejection. PBT-03 invariants (no `/`, `\\`, `..`, len ≤ 255). PBT-04 idempotence (Sanitize ∘ Sanitize = Sanitize). PBT-03 negative on traversal + absolute generators. |
| `internal/bombdefence` | `checker_test.go` | Example: rules #1, #4, #5, #6, #9. PBT-03 invariant (LimitedReader bytes-returned ≤ cap). PBT-03 positive PreCheck accept. PBT-03 negative PreCheck reject (bomb-shaped metadata). |
| `internal/retry` | `retry_test.go` | Example: first-success / retry-then-succeed / permanent-not-retried / exhaustion-wraps-as-permanent. PBT-05 oracle (`BackoffFor` matches closed-form within 1ns). Classify table (throttling / SlowDown / RequestTimeout / typed-errors-not-retried). |
| `internal/slipsheet` | `slipsheet_test.go` | Example: SUCCESS shape / FAILED-stub-empty-children. PBT-02 round-trip (Marshal ∘ Unmarshal = identity). Writer behaviour via fake uploader. |
| `internal/dynamodb` | `adapter_test.go` | PBT-02 round-trip Marshal/Unmarshal. CCFE-maps-to-nil with `onSkip` callback. |
| `internal/storage` | `storage_test.go` | DetectMIME oracle (PNG / PDF / sniff-octet-fallback-to-extension / unknown-fully-octet). Peek non-advancing-read behaviour. |
| `internal/sqs` | `adapter_test.go` | `parseMessage` happy-path + missing-field + malformed-JSON rejection. |
| `internal/extraction` | `service_test.go` | Status.String wire format. `Is*` helper functions. |
| `internal/config` | `config_test.go` | Happy-path Load. Unknown-YAML-key rejection (strict-decode). Missing-required-env rejection. SSE-KMS-requires-key validation. |
| `internal/log` | `log_test.go` | PBT-03 sensitive-key detection across case variants and embedded substrings. Non-sensitive whitelist. |
| `internal/health` | `server_test.go` | Gate atomic toggle. Server construction smoke. |
| `internal/metrics` | `metrics_test.go` | Collector registration + counter increment smoke via prometheus testutil. |

## Review Test Results

Expected outcome:
- Exit code 0
- `ok` for every package
- No `FAIL` lines
- No `DATA RACE` warnings
- Coverage ≥ 80% (NFR-Z-073 threshold)

The Makefile target adds the coverage gate when run in CI. To run locally with the gate:

```bash
go test -race -cover -coverprofile=cover.out ./...
go tool cover -func=cover.out | tail -1
# Total coverage line; must be ≥ 80%
```

## PBT-Specific Workflow

### Seed reproducibility (PBT-08)

When a PBT test fails, `rapid` logs the seed:

```
--- FAIL: TestPropertySanitizeIdempotent (0.05s)
    rapid: failed after 27 tests: ...
    To reproduce, specify -run "TestPropertySanitizeIdempotent" -rapid.seed=1234567890
```

To reproduce locally:

```bash
make pbt-replay SEED=1234567890
# expands to: RAPID_SEED=1234567890 go test -run TestProperty ./...
```

### CI seed

The CI workflow sets `RAPID_SEED=${{ github.run_id }}` so every run produces a unique-but-logged seed. On failure, copy the seed from the workflow log into `make pbt-replay`.

## Fix Failing Tests

1. Read the failure output. PBT failures include the shrunk minimal counter-example.
2. Reproduce locally with the seed: `make pbt-replay SEED=<n>`.
3. Add the failing case as an explicit example-based test in the same `_test.go` file (PBT-10 — promote discovered counter-examples to permanent regression tests).
4. Fix the underlying code defect.
5. Re-run `make test`; all tests must pass.

## Excluded From This Gate

- **Gate 2** integration tests under `test/e2e/` (build tag `e2e`) — see `integration-test-instructions.md`.
- **Gate 3** sandbox-EKS E2E — deferred per Q11 of requirements.

## Skipped / Quarantined Tests

Policy: **no skipped or quarantined tests** without an explicit `// SKIP-RATIONALE: <link>` comment AND a tracking ticket. Per PBT-08, flaky PBT must be investigated, not retried.
