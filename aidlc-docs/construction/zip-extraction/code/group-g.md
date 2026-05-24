# Group G Summary — Steps 27–37 (Unit Tests per Package)

| Step | File | Coverage |
|---|---|---|
| 27 | `internal/validation/validator_test.go` | Example-based positive + negative cases + PBT-04 idempotence + PBT-03 invariant (no `/`, `\\`, `..`) + adversarial traversal rejection + absolute-path rejection. Uses `generators.RawPath`/`RawPathTraversal`/`RawPathAbsolute`. |
| 28 | `internal/bombdefence/checker_test.go` | Rules #1, #4, #5, #6, #9 example-based positive/negative + PBT-03 LimitedReader invariant (bytes-returned ≤ cap) + PBT-03 PreCheck accept + PBT-03 PreCheck reject for bomb-shaped metadata. Uses `generators.ArchiveMetadata`/`ArchiveMetadataBomb`. |
| 29 | `internal/retry/retry_test.go` | Success-on-first / retry-then-succeed / permanent-not-retried / exhaustion-wraps-as-permanent + PBT-05 oracle (`BackoffFor` matches closed-form within 1ns) + classifier table (throttling / SlowDown / RequestTimeout + non-retryable typed errors). |
| 30 | `internal/slipsheet/slipsheet_test.go` | Build SUCCESS + FAILED-stub shapes + PBT-02 round-trip + Writer end-to-end with `fakeUploader` + uploader-error propagation. |
| 31 | `internal/dynamodb/adapter_test.go` | PBT-02 round-trip + CCFE-maps-to-nil with `onSkip` callback test using `fakeDDB`. |
| 32 | `internal/storage/storage_test.go` | `DetectMIME` example table (PNG sniff / PDF sniff / octet-stream → extension fallback / unknown → octet-stream) + `Peek` no-advance-on-read test. |
| 33 | `internal/sqs/adapter_test.go` | `parseMessage` happy-path + missing-field rejection + malformed-JSON rejection. Internal package test exercising the unexported function. |
| 34 | `internal/extraction/service_test.go` | Status `String()` wire format + typed-error `Is*` helpers. |
| 35 | `internal/config/config_test.go` | Happy-path Load + unknown-YAML-key rejection (strict-decode) + missing-required-env-var rejection + SSE-KMS-requires-key validation. |
| 36 | `internal/log/log_test.go` | PBT-03 sensitive-key detection (case variants + embedded substrings) + non-sensitive key whitelist. |
| 37 | `internal/health/server_test.go` + `internal/metrics/metrics_test.go` | Gate atomic toggle + Server construction smoke + Metrics registration smoke via testutil. |

Test invocation: `make test` runs `go test -race -cover ./...`. PBT runs use random seeds in CI; failures log seed for `make pbt-replay SEED=<n>`.
