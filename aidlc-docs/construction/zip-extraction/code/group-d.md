# Group D Summary — Steps 19–22 (Adapter Layer; I/O)

| Step | File | Highlights |
|---|---|---|
| 19 | `internal/storage/adapter.go` + `mime.go` | S3 adapter implementing both `extraction.S3Downloader` and `extraction.S3Uploader`. Multipart routing on `sizeHint > MultipartThresholdBytes`. `DetectMIME(peek, fileName)` hybrid + `Peek(r, n)` no-extra-read-pass helper (BR-MIME-001). All errors wrap through `retry.AsTransient`. SSE mode/key applied to PutObject input. |
| 20 | `internal/dynamodb/adapter.go` | Conditional PutItem with `attribute_not_exists(pk)` (BR-IDEMPOTENCY-002). `ConditionalCheckFailedException` → `nil` + optional `onSkip()` callback (metrics hook for BR-IDEMPOTENCY-006). `Marshal/Unmarshal` exported for PBT-02 round-trip property. |
| 21 | `internal/sqs/adapter.go` | Single long-poll receiver + bounded worker-pool semaphore (Q2 of application design). Per-message heartbeat goroutine resets visibility to `cfg.VisibilityTimeoutSec` (default 300s) every interval (Q6 of NFR design). Schema validation via `parseMessage` → `validateClaimCheck`. Graceful drain on root-ctx cancellation with `gracefulShutdownTimeoutSec` deadline. `MessageHandler` returns `(deleteMsg, err)` tuple letting orchestrator drive DLQ-bound decisions per BR-DLQ-001..003. Worker `defer recover()` for fail-safe panic handling. |
| 22 | `internal/metrics/metrics.go` + `internal/health/server.go` | 8 Prometheus collectors registered: `zip_entries_total{status}`, `zip_extraction_duration_seconds{outcome}` (histogram), `zip_extraction_failures_total{reason}`, `zip_bomb_rejections_total{rule}`, `extracted_bytes_total`, `partial_failures_total`, `redelivery_skips_total`, `slipsheet_write_failures_total`. HTTP server on single port with `/healthz/live` (always 200) + `/healthz/ready` (200 iff gate.Ready()) + `/metrics`. |

Compliance:
- SECURITY-01: TLS by default via SDK; no DisableSSL.
- SECURITY-03: structured logging via zap throughout.
- SECURITY-05: SQS message schema validation in `parseMessage`.
- SECURITY-09: only operational HTTP endpoints; no debug/pprof routes.
- NFR-Z-001: bounded worker pool (cfg.MaxInFlight).
- NFR-Z-034: per-message heartbeat goroutine.
- NFR-Z-022: graceful drain with deadline.
- NFR-Z-060: full Prometheus collector taxonomy.
