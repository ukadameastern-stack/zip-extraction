# Group E Summary — Steps 23–25 (Orchestrator + Entry Point)

| Step | File | Highlights |
|---|---|---|
| 23 | `internal/extraction/service.go` + `zip_open.go` + `mime_shim.go` | `Service.Process(ctx, msg)`: spill-to-temp → openZip → PreCheck → per-entry loop → status compute. Per-entry pipeline: Sanitize → EntryCheck → file.Open → LimitedReader → Peek+MIME → Retrier.Do(Upload) → Retrier.Do(RecordEntry). Slipsheet write in `defer` (BR-SLIP-002) using a 5s root-ctx budget (not the rule-10-bounded extractCtx). `computeStatus` truth table per BR-STATUS-001. `classifyArchiveErr` + `classifyEntryFailure` produce controlled-vocabulary failure reasons (BR-DDB-005). MIME-shim mirrors `internal/storage.DetectMIME` to break the import cycle. |
| 24 | `internal/app/app.go` | `Service.Run`: starts HTTP server → optional startup probe → flip readiness gate → start SQS receive-loop → block on shutdown signals → drain coordination (readiness false → wait for queue → http Shutdown). Handler closure converts `extraction.Outcome` to `(deleteMsg, err)` SQS disposition per BR-DLQ-001. |
| 25 | `cmd/zip-extraction/main.go` | Process bootstrap matching the DI wiring sketch in `logical-components.md` §3. Top-level `defer recover()` for fail-loud panic. `signal.NotifyContext` for SIGINT/SIGTERM. SIGUSR1 → `runtime/pprof.WriteHeapProfile(/tmp/heap-<RFC3339>.pprof)` per Q4 of NFR design. Custom Prometheus registry (not DefaultRegisterer) so tests can isolate metrics. `buildStartupProbe` does `GetQueueAttributes` against SQS as a reachability check. `queueAdapter` shim bridges `*sqs.Adapter.Run` to the `app.Queue` interface signature. |

Compliance:
- SECURITY-15: typed errors propagate fail-closed; top-level recover.
- NFR-Z-022: graceful drain with deadline.
- NFR-Z-033: idempotency via deterministic S3/DDB keys.
- BR-BOMB-005 rule #10: `context.WithTimeout(extractCtx, MaxExtractionDurationSec*Second)`.
- BR-SLIP-002: slipsheet write via `defer` covering panic + early-failure paths.
- BR-STATUS-001/002/003: `computeStatus` exhaustive logic; archive-level abort sets FAILED even with partial successes.
