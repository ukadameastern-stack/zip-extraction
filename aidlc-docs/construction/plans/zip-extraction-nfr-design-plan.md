# NFR Design Plan — zip-extraction (UOW-SVC-12)

**Document Type**: NFR Design Plan (Part 1 — Planning)
**Project**: Zip Extraction Service (UOW-SVC-12)
**Unit**: `zip-extraction`
**Phase**: CONSTRUCTION — NFR Design (Plan)
**Generated**: 2026-05-24
**Source Inputs**:
- `aidlc-docs/construction/zip-extraction/nfr-requirements/nfr-requirements.md` (24 NFR-Z entries)
- `aidlc-docs/construction/zip-extraction/nfr-requirements/tech-stack-decisions.md` (16 locked tools)
- `aidlc-docs/construction/zip-extraction/functional-design/*.md`

---

## Purpose

This is **Part 1 of NFR Design (Planning)** for the single unit `zip-extraction`. It captures:

1. The checklist of NFR-design artefacts to be produced once questions are answered (Part 2 — Generation).
2. A focused set of **design-pattern & logical-component** clarifying questions for decisions not already settled by prior Q&A. The intent is to lock the implementation patterns before authoring `nfr-design-patterns.md` and `logical-components.md`.

Each question has option **A marked (Recommended)** with rationale. Reply per question, or use **"Accept all recommendations"** to lock all answers to A.

---

## Part A — Execution Checklist (Part 2 Will Run After Answers Are Approved)

Once all answers are confirmed, these artefacts are produced under `aidlc-docs/construction/zip-extraction/nfr-design/`:

- [x] **nfr-design-patterns.md** — Resilience, scalability, performance, security, and observability patterns mapped to specific Go packages / SDK options / Helm values. Each pattern records: NFR-Z source, implementation locus, configurable parameters, anti-patterns avoided.
- [x] **logical-components.md** — In-pod logical components (worker pool, heartbeater, retry classifier, logger pipeline) + out-of-pod infrastructure dependencies (SQS DLQ, S3 lifecycle, KMS, Prometheus scrape, CloudWatch). Dependency-injection wiring summary.
- [x] Validate against SECURITY-01..15 and PBT-01..10 (no new blocking findings)
- [x] Cross-reference every unit-level NFR-Z entry to one or more design-pattern entries

---

## Part B — Clarifying Questions (Answer Required)

### Question 1 — Circuit Breaker for AWS-Service Failures

When DynamoDB or S3 enters a chronic throttling / 5xx state (e.g., a regional incident), should the service short-circuit downstream calls (open the breaker), drain in-flight workers, and let SQS visibility expire the queue back into the DLQ?

A) **(Recommended)** **No explicit circuit breaker.** Rely on (a) the AWS SDK v2 adaptive retry mode (`aws.RetryModeAdaptive`) which already implements client-side rate limiting on repeated throttles, (b) the application-level 3-attempt classifier-driven retry (FR-12), and (c) `cfg.MaxInFlight` worker-pool bounding which acts as natural backpressure. A circuit breaker adds operational complexity (state, timeouts, alarms) for marginal benefit at the throughput tier we operate at (≤500 archives/min/cluster per NFR-Z-002).

B) Add an explicit per-service circuit breaker (`sony/gobreaker` or similar) that opens after 5 consecutive failures and half-opens after 30 s. Adds ~1 KB state per service and one new alert surface (`circuit_breaker_open_total`).

C) Hybrid: rely on SDK adaptive for DDB/SQS; add an explicit breaker only for S3 (highest call volume).

X) Other

[Answer]: A

**Recommendation rationale**: A is the simplest design that respects the SECURITY-15 fail-closed contract. AWS SDK v2's `aws.RetryModeAdaptive` is purpose-built for this scenario — it dynamically reduces request rate based on observed throttling and is well-tested across millions of production deployments. Adding a separate breaker on top duplicates the mechanism with different state and creates two places where "service is sad" decisions live — a coordination bug magnet. Option B is right for very-high-throughput services (>5000 RPS per pod) where SDK adaptive alone isn't enough; we're below that tier. Option C splits the abstraction unnecessarily.

---

### Question 2 — Rate Limiting on Outbound S3 Calls

A single archive can produce up to `MaxEntryCount = 10,000` S3 PutObject calls in a single execution. With `maxInFlight = 5` per pod and max 10 pods, peak burst = 50 × ~167/min = ~138 PUTs/sec to the same S3 prefix. S3 prefix rate limits are 3,500 PUT/sec — comfortably below. Do we still want explicit rate limiting?

A) **(Recommended)** **No explicit rate limiter.** SDK adaptive retry handles transient `SlowDown` responses (BR-RETRY-004). For pathological "many small entries" archives, the natural concurrency cap (worker-pool semaphore) limits per-pod throughput. If we hit S3 prefix limits in practice (e.g., a single hot pipeline-execution prefix gets thousands of entries), the platform team can shard the staging bucket across prefixes — a deployment-level fix, not a code change.

B) Add a per-pod token-bucket rate limiter (`golang.org/x/time/rate.Limiter`) at ~50 PUT/sec/pod. Conservative; aligned with 10-pod × 50 = 500 PUT/sec aggregate vs the 3500 prefix limit.

C) Add a per-prefix rate limiter keyed on `pipelineExecutionId` (treats each archive as its own prefix domain). Most precise; most code.

X) Other

[Answer]: A

**Recommendation rationale**: A respects the YAGNI principle. The math (~138 PUT/sec peak per cluster against 3500/sec S3 prefix limit) shows we have ~25× headroom. SDK adaptive retry handles the rare burst case gracefully. Option B is defensive over-engineering — adds a new tunable parameter to operate and a new metric to ignore. Option C is the right answer at a much higher scale (e.g., 100 pods).

---

### Question 3 — AWS Client Lifecycle (Singleton vs Per-Worker)

The AWS SDK v2 clients (`sqs.Client`, `s3.Client`, `dynamodb.Client`, `s3manager.Uploader`) are concurrent-safe. Should we construct them once per pod and share, or per-worker?

A) **(Recommended)** **Singleton per pod.** Constructed once in `cmd/zip-extraction/main.go` and injected into every adapter via dependency injection. Shares the underlying HTTP connection pool across all goroutines — maximises connection reuse, minimises TLS handshake cost.

B) Per-worker (one client set per worker goroutine) — provides natural isolation if one worker poisons a client (e.g., misconfigures retry options mid-call). At our scale of `maxInFlight = 5`, the cost is small but unnecessary.

C) Per-request — re-construct on every method call. Catastrophic for HTTPS handshake cost; included for completeness only.

X) Other

[Answer]: A

**Recommendation rationale**: A matches the AWS SDK v2 documentation's explicit guidance ("clients are safe for concurrent use; share them across goroutines"). The connection-pool reuse is a substantial latency win when many concurrent goroutines hit the same service. Option B trades that win for an isolation property we don't need (clients are immutable post-construction; nothing poisons them).

---

### Question 4 — pprof Endpoint Exposure

`net/http/pprof` is invaluable when diagnosing GC pauses, goroutine leaks, or memory growth. Should the service expose it?

A) **(Recommended)** **Never in any environment.** SECURITY-09 hardening forbids debug endpoints in production; allowing them locally creates muscle-memory risk and an attractive misconfiguration target. Diagnostic profiling is done offline via `go tool pprof` against a saved heap dump produced via Kubernetes `kubectl exec` + `curl localhost:8080/debug/pprof/heap > heap.pprof` — but that endpoint is NOT registered; profiling is via a separate one-shot binary or `runtime/pprof.WriteHeapProfile` triggered by a SIGUSR1 handler that writes to `/tmp/heap-<timestamp>.pprof`.

B) Gate behind `DEBUG_PPROF=true` env var (off by default). Off in production via Helm values; convenient to enable locally.

C) Always expose at `/debug/pprof/*` (with no auth — service is in-cluster only) — standard Go practice for in-cluster services.

X) Other

[Answer]: A

**Recommendation rationale**: A is the strictest interpretation of SECURITY-09 (no debug routes). The SIGUSR1 mechanism gives operators an emergency profiling tool without a permanent HTTP surface. Option B is defensible if the local-prod parity argument is weighted differently, but it adds a code path that branches on env var (NFR-Z-090 forbids), so it's actually a parity violation. Option C is incompatible with SECURITY-09 in production-grade services even when the service is in-cluster only — defence in depth says don't expose what you don't need.

---

### Question 5 — Backpressure Source-of-Truth

When messages arrive faster than we can process them, what mechanism slows down the receive-loop?

A) **(Recommended)** **Worker-pool semaphore is the only backpressure source.** The receive-loop's `dispatch(msg)` call blocks on a buffered channel `chan struct{}` of capacity `maxInFlight`. When all slots are taken, the next `ReceiveMessage` call doesn't happen until a worker completes. SQS visibility timeout (300 s) gives us a hard ceiling: if the pod fully stalls, SQS will redeliver after 5 minutes.

B) Worker-pool semaphore + an explicit SQS-visibility-based feedback loop that monitors `ApproximateNumberOfMessagesNotVisible` and pauses receive when it gets close to `maxInFlight`. Defensive; doesn't trigger unless workers genuinely stall.

C) Use SQS FIFO queue with deduplication (would require changing queue type from Standard — outside our spec). Strongest backpressure but biggest architectural change.

X) Other

[Answer]: A

**Recommendation rationale**: A is sufficient. The semaphore-blocking pattern is idiomatic Go for bounded-concurrency consumers. The HPA scaling on queue depth (NFR-Z-002) handles the long-tail backpressure: when the queue grows, HPA scales pods, which increases aggregate capacity. Option B adds a second control loop that fights HPA — two things controlling concurrency is a coordination hazard. Option C is out of scope.

---

### Question 6 — Server-Side Encryption Strategy

The S3 staging bucket needs encryption at rest (SECURITY-01). What does the service code need to support?

A) **(Recommended)** **Support both SSE-S3 (Amazon-managed keys) and SSE-KMS (customer-managed keys); selection via Helm `values.yaml`.** Helm values: `sse.mode: "SSE-S3"` (default) OR `sse.mode: "SSE-KMS"` with `sse.kmsKeyId: "<key-arn>"`. The Helm chart renders the IRSA policy with `kms:Decrypt` + `kms:GenerateDataKey` only when SSE-KMS is selected. Service code uses `s3.PutObject(ServerSideEncryption: ...)` based on the loaded config.

B) Support only SSE-S3 (Amazon-managed). Simpler IRSA, simpler IAM review. SSE-KMS is the platform team's problem to retrofit.

C) Support only SSE-KMS (require explicit customer-managed key). Strictest posture; requires KMS-key provisioning per environment.

X) Other

[Answer]: A

**Recommendation rationale**: A gives the platform team flexibility without forcing a hard decision now (FR-2.x of NFR-Z-041 deferred this). SSE-S3 is the cheapest, simplest default. SSE-KMS is required for some compliance regimes (e.g., FIPS 140-2 with FIPS-validated KMS). Helm-conditional rendering means the IRSA policy contains `kms:` actions ONLY when SSE-KMS is enabled, preserving least-privilege when SSE-S3 is in use. Option B forces a future Helm chart breaking change if SSE-KMS becomes a requirement. Option C is unnecessary lock-in.

---

### Question 7 — Bulkhead / Workload Isolation

Should the worker pool be partitioned by archive size class (e.g., a dedicated pool for "large" archives so they don't starve "small" archives)?

A) **(Recommended)** **No partitioning.** Single uniform worker pool of size `maxInFlight`. Rationale: (a) archive size is unknown until after pre-check, so we cannot route at receive time without an additional metadata-only round trip; (b) the throughput math at our scale (50 archives/min/pod with 240 s extraction ceiling) gives natural fairness — a worst-case "large" archive consumes one slot for up to 240 s, leaving 4 slots for other work; (c) starvation requires a sustained inflow of large archives that exceed the per-slot 4-MiB streaming budget, which the bomb-defence single-file rule #9 (250 MB) bounds.

B) Two-pool bulkhead: a "fast lane" pool (3 slots) and a "slow lane" pool (2 slots). Routing decision deferred to post-pre-check (rebalance after metadata read). Adds complexity for a problem we don't measurably have.

C) Per-tenant bulkhead: dedicated worker per `tenantId`. Strongest fairness but requires up-front capacity reservation per tenant.

X) Other

[Answer]: A

**Recommendation rationale**: A respects YAGNI. Bulkhead patterns add value at much higher concurrency (≥50 in-flight per pod) where fairness genuinely matters. At our concurrency tier, the natural HPA scaling + bounded extraction timeout (rule #10) handle worst-case archives without dedicated lanes. Option B is right for a service that demonstrably suffers from starvation — we have no evidence of that. Option C is right for multi-tenant SaaS with strong fairness SLAs, which isn't our model (our SLO is system-wide success rate, not per-tenant).

---

## Part C — Notes for Part 2 (Generation)

After answers are confirmed, Part 2 will produce:

1. **nfr-design-patterns.md** — One subsection per pattern category:
   - **Resilience**: Retry classifier + exponential backoff (Q1 no breaker), heartbeat lifecycle, graceful drain, idempotency-via-deterministic-keys, fail-closed exception handling
   - **Scalability**: Bounded worker pool (Q5 backpressure SoT), HPA on SQS depth (NFR-Z-002), AWS SDK adaptive retry mode (Q2 no rate limit), multi-AZ pod spread (NFR-Z-004)
   - **Performance**: Streaming I/O (NFR-Z-014), short-circuiting `LimitedReader`, S3 multipart upload threshold, singleton AWS clients (Q3), MIME-detection peek-without-rewind
   - **Security**: IRSA + least-privilege IAM, SSE-S3 default + SSE-KMS opt-in via Helm (Q6), TLS 1.2+ via SDK defaults, sensitive-field log redaction, distroless non-root container, pinned digests, govulncheck CI gate
   - **Observability**: Prometheus collectors taxonomy, structured-log field discipline, health-probe semantics (ready=false on drain), recommended alert rules, no pprof endpoint (Q4)
2. **logical-components.md** — Two halves:
   - **In-pod logical components** (DI graph): worker-pool, heartbeater, retry-classifier, logger-pipeline, metrics-registry, health-gate
   - **Out-of-pod infrastructure dependencies**: SQS main queue + DLQ + visibility-timeout config, S3 staging bucket + lifecycle policy on `input/` prefix + bucket policy denying non-TLS, DynamoDB `pipeline_files` table + PITR, KMS key (when SSE-KMS in use), Prometheus scrape target, CloudWatch logs ingest (via EKS log driver)
   - For each: NFR-Z source, ownership boundary (this repo vs platform team), configuration surface

---

## Part D — How to Respond

1. Edit `[Answer]:` tags in this file with a letter or `X: <free-text>`.
2. Or reply inline (e.g., "Q1=A, …").
3. **"Accept all recommendations"** locks all 7 answers to option A.

Once answers are confirmed, Part 2 generates the 2 NFR-design artefact files.

---

## Part E — User Answers (Confirmed)

**Confirmed 2026-05-24T13:11:00Z** — user reply: **"Accept all recommendations"**.

| Question | Answer | Decision |
|---|---|---|
| Q1 — Circuit breaker | A | None; SDK adaptive + classifier + worker-pool bounding |
| Q2 — S3 rate limiting | A | None; SDK adaptive handles SlowDown |
| Q3 — AWS client lifecycle | A | Singleton per pod |
| Q4 — pprof endpoint | A | Never expose; SIGUSR1 → heap dump file |
| Q5 — Backpressure source | A | Worker-pool semaphore only |
| Q6 — SSE strategy | A | Support both SSE-S3 (default) + SSE-KMS (Helm-opt-in with conditional IRSA) |
| Q7 — Bulkhead | A | No partitioning; uniform pool |

**Ambiguity analysis**: All 7 answers are unambiguous letter selections. No follow-up questions required. Proceeding to Part 2 (Generation).

