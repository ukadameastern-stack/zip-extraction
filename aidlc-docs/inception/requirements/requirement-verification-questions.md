# Requirements Verification Questions — Zip Extraction Service (UOW-SVC-12)

The input specification `zip-extraction-service-input.md` is highly detailed. The questions below
clarify the remaining open areas and extension opt-ins. Please answer each question by filling in
the letter choice after the `[Answer]:` tag. If none of the options match, choose the "Other"
option and describe your preference inline after the tag.

---

## Question 1: Security Extensions
Should security extension rules be enforced for this project?

A) Yes — enforce all SECURITY rules as blocking constraints (recommended for production-grade applications)

B) No — skip all SECURITY rules (suitable for PoCs, prototypes, and experimental projects)

X) Other (please describe after [Answer]: tag below)

[Answer]: A

**Rationale**: The same codebase must run in both local development and production environments. Enforcing SECURITY rules as blocking constraints ensures:
- **Environment parity**: Local builds fail under the same security constraints as production, so vulnerabilities (zip-bomb attack vectors, path traversal, SSRF via S3 presigned URLs, IAM misconfigurations, secret leakage in logs) surface during development rather than after deployment.
- **Service criticality**: The Zip Extraction Service processes untrusted external archives, making it a high-value target for malicious payloads (zip bombs, symlink attacks, path traversal). Security must be a first-class blocking concern, not optional guidance.
- **Compliance posture**: Production-grade microservices deployed to EKS with AWS SDK integrations must meet baseline controls (least-privilege IAM, encrypted transit/at-rest, audit logging) that the SECURITY extension enforces automatically.
- **Shift-left economics**: Catching security findings in CI/local is dramatically cheaper than remediating in production. Treating them as blocking from day one prevents accumulation of security debt.

Option B (skip) was rejected because this is not a throwaway PoC — it is a production microservice handling untrusted input.

---

## Question 2: Property-Based Testing Extension
Should property-based testing (PBT) rules be enforced for this project?

A) Yes — enforce all PBT rules as blocking constraints (recommended for projects with business logic, data transformations, serialization, or stateful components)

B) Partial — enforce PBT rules only for pure functions and serialization round-trips (suitable for projects with limited algorithmic complexity)

C) No — skip all PBT rules (suitable for simple CRUD applications, UI-only projects, or thin integration layers with no significant business logic)

X) Other (please describe after [Answer]: tag below)

[Answer]: A

**Rationale**: The Zip Extraction Service has substantial business logic and security-critical invariants that PBT is uniquely suited to verify:
- **Data transformation paths**: ZIP entry parsing, slipsheet JSON serialization/deserialization, S3 key derivation, and DynamoDB record marshaling are serialization/round-trip code paths — classic PBT territory where example-based tests miss edge cases.
- **Security invariants**: PBT can prove bomb-defence properties (e.g., "for any input archive, decompressed size ≤ configured cap × compressed size"), path-normalization properties ("no extracted path escapes the staging prefix regardless of input"), and operation idempotency under SQS retry.
- **Stateful components**: SQS visibility heartbeat, partial-failure accounting (SUCCESS/PARTIAL_FAILED/FAILED transitions), and DynamoDB status updates form state machines where PBT generates non-obvious interleavings.
- **CI economics**: PBT cases run cheaply once authored and dramatically expand effective coverage — high ROI for a security-critical microservice.

Option B (partial) was rejected because the state-machine code paths (heartbeat, partial-failure) need PBT coverage and would be excluded. Option C (skip) was rejected because this service is neither simple CRUD nor a thin integration layer.

---

## Question 3: Go Toolchain Version
Which Go version should be the build target?

A) Go 1.22 (oldest supported stable)

B) Go 1.23

C) Go 1.24 (latest stable as of 2026-Q1)

D) Other (please describe after [Answer]: tag below)

[Answer]: C

**Rationale**: Targeting Go 1.24 (latest stable as of 2026-Q1) is the right choice for this greenfield service:
- **Greenfield economics**: No legacy code constrains the version choice; picking the newest stable now is the cheapest moment to do so. Locking to an older version creates a forced-upgrade obligation within 12 months as EOL approaches.
- **Toolchain compatibility**: AWS SDK v2, `zap`, the `archive/zip` stdlib, and Testcontainers all support Go 1.24 with no known compatibility risks.
- **Relevant stdlib gains**: `archive/zip` performance fixes (directly material to extraction throughput), `crypto/tls` improvements (S3/SQS/DynamoDB HTTPS performance), escape-analysis improvements (reduced allocations in hot extraction loops), and continued PGO maturity.
- **Security cadence**: Latest version receives security patches first and reaches EOL last — important for a security-critical service.
- **Container size**: Modern Go versions produce slightly smaller static binaries, reducing EKS image pull latency.

Option A (1.22) was rejected as the "oldest supported" floor with no offsetting benefit. Option B (1.23) was rejected as offering no advantage over 1.24 on a greenfield project.

---

## Question 4: Nested Archives Support
Section 10 of the spec marks nested archives as "Optional (depth-limited)". Should the service
extract archives found inside the parent ZIP?

A) Do NOT extract nested archives — upload them as opaque child entries and let downstream pipelines decide

B) Reject archives containing nested archives entirely (treat as a bomb-defence violation)

C) Extract nested archives up to depth 1 (single level of nesting only)

D) Other (please describe after [Answer]: tag below)

[Answer]: A

**Rationale**: Treating nested archives as opaque child entries (uploaded to S3 without further extraction) is the cleanest, safest, and most composable choice:
- **Single-responsibility boundary**: This service's job is "extract one level of ZIP". Embedding TAR/GZIP/RAR/7z parsers here violates that boundary and bloats the security review surface.
- **Bomb-defence stays tractable**: Recursive extraction multiplies the attack surface (a 1 MB outer ZIP → 10 inner ZIPs → 10 more = 100× amplification compounding). Keeping extraction non-recursive makes the bomb-defence size/ratio invariants trivial to enforce and audit.
- **Event-driven composability**: If a child is itself a ZIP, the S3 PutObject event naturally re-triggers this same service via SQS — the pipeline handles depth through orchestration rather than in-process recursion. Each level becomes a separately observable pipeline execution with its own DynamoDB record and slipsheet.
- **Operational visibility**: Per-level pipeline executions are dramatically easier to debug, retry, and audit than a single nested extraction with internal recursion bookkeeping.

Option B (reject) was rejected as overly strict — it would break legitimate workflows (e.g., a ZIP of TAR.GZ release artifacts). Option C (depth-1) was rejected because it adds recursion bookkeeping and cross-level bomb-defence accounting for marginal benefit when the event-driven re-trigger flow already handles nesting cleanly.

---

## Question 5: Local Development Tooling Depth
Section 28 mandates LocalStack compatibility. How deep should the local-dev tooling go?

A) Provide a `docker-compose.yml` with LocalStack + the service container, plus a Makefile target that auto-provisions the S3 bucket / SQS queue / DynamoDB table

B) Provide only `docker-compose.yml` for LocalStack; provisioning is left to the developer

C) Provide configuration support only (env vars, endpoint override) — no Docker Compose, no provisioning scripts

D) Other (please describe after [Answer]: tag below)

[Answer]: A

**Rationale**: A `docker-compose.yml` with LocalStack + service container, plus a Makefile target that auto-provisions S3 bucket / SQS queue + DLQ / DynamoDB table is the right level of investment:
- **Zero-friction onboarding**: New engineers run `make up && make bootstrap` and have a working dev environment — eliminates the long tail of "works on my machine" debugging and stale README setup steps.
- **Local-CI parity**: Gate 2 tests (Testcontainers + LocalStack) and local development share the same provisioning logic, dramatically reducing "passes locally, fails in CI" bugs.
- **Provisioning is the high-leverage piece**: Bucket policies, SQS DLQ redrive configuration, DynamoDB key schemas, and IAM-shaped permissions are exactly the artifacts that get forgotten and bite in production. Encoding them as Makefile targets makes them version-controlled, code-reviewed, and discoverable.
- **Low build cost**: A `make bootstrap` target is ~30 lines of `aws --endpoint-url=http://localhost:4566 ...` calls. Strong ROI for a one-time investment.

Option B (compose only) was rejected because it pushes every developer to repeat the same manual setup steps. Option C (config support only) was rejected because it provides almost no local-dev value for a service whose entire job is AWS service interaction.

---

## Question 6: SQS Heartbeat Implementation
Section 18 requires "Heartbeat extension every 30s". This is typically used to extend SQS message
visibility while extraction is in flight. How should it be implemented?

A) Background goroutine per in-flight message calling `ChangeMessageVisibility` every 30s until extraction completes or hits the 240s extraction hard timeout

B) Single periodic ticker covering all in-flight messages

C) No heartbeat — rely entirely on the 300s visibility timeout being sufficient for a 240s extraction limit

D) Other (please describe after [Answer]: tag below)

[Answer]: A

**Rationale**: A per-message background goroutine calling `ChangeMessageVisibility` every 30s (cancelled via context when extraction completes or the 240s extraction hard timeout fires) is the right design:
- **Natural lifecycle binding**: The heartbeat lifetime is exactly one in-flight message — start on receive, beat, cancel on complete/fail. A `context.WithCancel` pattern expresses this cleanly with no external bookkeeping.
- **Independent failure isolation**: A long-running message near the 240s ceiling and a fast message finishing in 5s have independent contexts and independent cancellation paths, so failures don't cross-contaminate.
- **Idiomatic Go concurrency**: Goroutines are cheap (~KB stack). With max-in-flight typically ≤10–20 messages, ≤20 heartbeat goroutines is trivial overhead.
- **Simpler error handling**: Each goroutine only handles its own message's `ChangeMessageVisibility` failures (most commonly "message no longer exists" → log and exit). A shared ticker has to handle a vector of failures with partial successes — far more complex.

Option B (single shared ticker) was rejected because it introduces shared mutable in-flight state, races between extraction completion and ticker iteration, and one tick error can affect multiple messages. Option C (no heartbeat) was rejected as unsafe: a 60s margin (300s visibility − 240s extraction limit) disappears under GC pauses, slow S3 uploads, or AWS latency spikes, causing duplicate message delivery and double-processing.

---

## Question 7: Slipsheet Storage Location
Section 15 defines the parent slipsheet but does not state its S3 location. Where should the
slipsheet object be written?

A) Same S3 staging bucket under `input/{pipelineExecutionId}/_slipsheet.json` (fans out alongside child files)

B) Same S3 staging bucket under `slipsheets/{pipelineExecutionId}.json` (separate prefix so it does NOT trigger child-pipeline S3 events)

C) Embed slipsheet only as a DynamoDB record (no S3 object)

D) Other (please describe after [Answer]: tag below)

[Answer]: B

**Rationale**: Storing the slipsheet at `slipsheets/{pipelineExecutionId}.json` in the same staging bucket (separate prefix from `input/`) is the safest, clearest design:
- **S3 event isolation (critical)**: Child files under `input/{pipelineExecutionId}/` trigger the downstream pipeline via S3 PutObject events. Co-locating the slipsheet in `input/` would cause the slipsheet to be misinterpreted as an input document and re-fanout — a silent, catastrophic, hard-to-diagnose bug. A separate prefix eliminates this class of failure structurally.
- **Operational clarity**: A dedicated `slipsheets/` prefix makes lifecycle policies, access logs, IAM scoping, and human debugging unambiguous — "is this object an input or metadata?" is answered by the path.
- **Future-proof lifecycle policies**: Separate prefixes let you set distinct retention (e.g., 7-day input retention, 90-day slipsheet audit retention) as trivial lifecycle rules rather than complex object-tag-based rules.
- **Audit/replay value**: Keeping the slipsheet as an S3 artifact preserves it as an immutable, durable record outside DynamoDB, which is valuable for incident replay and downstream consumers preferring S3.

Option A (same prefix as children) was rejected as architecturally unsafe — pipeline event mis-fanout is silent corruption. Option C (DynamoDB-only) was rejected because it removes the audit/replay value of an immutable S3 artifact and complicates bulk-read access patterns.

---

## Question 8: Configuration Mechanism
How should the service ingest runtime configuration (queue URLs, bucket names, limits, endpoints)?

A) Environment variables only (12-factor style, fully Kubernetes/Helm friendly)

B) YAML config file mounted into the pod, with env-var overrides

C) Environment variables for infrastructure (endpoints, queue/bucket names) + a YAML file for tunable limits (bomb defence, timeouts)

D) Other (please describe after [Answer]: tag below)

[Answer]: C

**Rationale**: A hybrid approach — environment variables for infrastructure values and a YAML config file for tunable limits — separates configuration by change frequency and review surface:
- **Change-frequency separation**: Infrastructure values (queue URL, bucket name, DynamoDB table, AWS endpoint override) are per-environment and naturally injected by Helm/Kustomize as env vars. Tunable limits (max archive size, max decompression ratio, per-entry timeout, max entry count) are operator-tuned, change rarely, and benefit from being code-reviewed in a versioned YAML file.
- **Security-critical reviewability**: Bomb-defence thresholds are security boundaries. Storing them in a YAML file (committed, diff-trackable in PRs) is dramatically more auditable than hiding them in Helm `values.yaml` env-var lists where they're easy to miss in review.
- **Kubernetes ergonomics**: Env vars remain the source of truth for what Helm/Kubernetes inject. A ConfigMap-mounted YAML file keeps structured config close to the code that consumes it.
- **Testability**: Loading a YAML fixture in unit tests is cleaner than `os.Setenv` for 15+ variables, and supports table-driven testing of limit configurations.

Option A (env-only) was rejected because it makes tunable limits hard to review and forces ugly env-var lists for any nested structure (e.g., per-archive-type limits). Option B (YAML-first with env overrides) was rejected because it inverts the correct precedence — infrastructure values genuinely belong in env vars for Kubernetes ergonomics.

---

## Question 9: Build and Deployment Artifact Scope
Section 21 references `helm upgrade`. Which deployment artifacts should be generated by this
project?

A) Application code + `Dockerfile` + `Makefile` only (no Helm chart — defer chart authoring to platform team)

B) Application code + `Dockerfile` + `Makefile` + minimal Helm chart skeleton (Deployment, Service, ConfigMap, ServiceAccount, values.yaml)

C) Application code + `Dockerfile` + `Makefile` + full Helm chart (skeleton + HPA, PodDisruptionBudget, NetworkPolicy, ServiceMonitor for Prometheus)

D) Other (please describe after [Answer]: tag below)

[Answer]: B

**Rationale**: Generating the application code, `Dockerfile`, `Makefile`, and a minimal Helm chart skeleton (Deployment, Service, ConfigMap, ServiceAccount, `values.yaml`) hits the right scope boundary for an application-team deliverable:
- **Documents the deployment contract in code**: The skeleton makes env-var shape, IRSA ServiceAccount annotation pattern, and ConfigMap shape (for the YAML limits from Q8) explicit and reviewable — not buried in README prose.
- **Application-coupled vs platform-coupled separation**: ServiceAccount/IRSA bindings and ConfigMap shape are application-coupled and belong with the code. HPA tuning, PodDisruptionBudget sizing, NetworkPolicy rules, and Prometheus ServiceMonitor selectors are platform-team responsibilities that depend on cluster conventions this team does not own.
- **Avoids speculative ops artifacts**: Generating HPA/PDB/NetworkPolicy/ServiceMonitor against unknown cluster conventions guarantees rewrites by the platform team — creates wasted work and ambiguity about which version is canonical.
- **Right size**: ~5 templates + ~50 lines of `values.yaml`. Sufficient to deploy end-to-end on a test cluster without overreaching.

Option A (no chart) was rejected because it leaves the K8s deployment contract implicit and slows platform-team integration. Option C (full chart with HPA/PDB/NetworkPolicy/ServiceMonitor) was rejected as overreach — speculative ops resources that will almost certainly be rewritten against real cluster conventions.

---

## Question 10: Structured Logging Format
Section 23 lists `zap` as the logging library. Which output format should be the default for
production?

A) JSON structured logs to stdout (recommended for EKS / CloudWatch / Loki ingestion)

B) Human-readable console logs to stdout

C) JSON in production, console in local development (selected via env var)

D) Other (please describe after [Answer]: tag below)

[Answer]: C

**Rationale**: A runtime-selected format (JSON in production, console in local dev, controlled by a `LOG_FORMAT=json|console` env var) is the right balance:
- **Production: structured logs for machines**: CloudWatch / Loki / OpenSearch parse JSON natively. Structured fields (`pipelineExecutionId`, `s3Key`, `entryCount`, `bombDefenceReason`, `latencyMs`) become queryable filters and alertable signals — dramatically more valuable than grep-based investigation of unstructured logs.
- **Local dev: readable logs for humans**: Pretty-printed colored console output at a terminal is far easier to scan during debugging than JSON; forcing JSON locally pushes engineers to pipe through `jq` and slows iteration.
- **Single binary, single build**: `zap` supports both formats via configuration. The runtime switch keeps the binary and image identical across environments — no separate build flags or images, just a different `LOG_FORMAT` env var (default JSON, overridden to `console` in `make run`).
- **Zero-surprise UX**: First-time local users get readable logs; deployed instances emit structured logs. Behaviour matches the environment it runs in.

Option A (JSON everywhere) was rejected as actively painful in local development. Option B (console everywhere) was rejected as wrong for production — unstructured logs in CloudWatch/Loki are expensive to query at scale and lose field-level alerting capability.

---

## Question 11: Sandbox E2E Test Execution (Gate 3)
Section 24 Gate 3 calls for sandbox EKS E2E tests. Should this project include the Gate 3 test
harness now, or defer it?

A) Generate Gate 1 (unit) and Gate 2 (Testcontainers + LocalStack) only — Gate 3 (sandbox EKS) deferred to a later phase since it depends on real AWS credentials and live infrastructure

B) Generate all three gates now, including a Gate 3 harness skeleton that can be wired to real AWS credentials later

C) Other (please describe after [Answer]: tag below)

[Answer]: A

**Rationale**: Generating only Gate 1 (unit) and Gate 2 (Testcontainers + LocalStack) now, while explicitly deferring Gate 3 (sandbox EKS E2E), is the right scope:
- **Gate 3 has hard external prerequisites**: Real AWS sandbox account, IAM roles, OIDC for IRSA from a real EKS cluster, provisioned S3/SQS/DynamoDB infrastructure, and CI credentials to assume the role. These resources belong to the platform/ops team and exist outside this project's ownership boundary.
- **Gates 1 + 2 already cover the meaningful test surface**: Unit tests verify business logic and bomb-defence invariants. Testcontainers + LocalStack exercises full AWS SDK call patterns, error paths, retry behaviour, and event-driven flow — catching the majority of integration bugs at a fraction of the cost and flakiness of sandbox EKS tests.
- **Skeleton-without-target is worse than absent**: Generating test scaffolding against credentials, URLs, and account IDs that do not yet exist forces hard-coded placeholders that rot, mislead readers, and produce false confidence (test files that look complete but never execute).
- **Future-add is self-contained**: When the platform team provisions a sandbox EKS environment with wired credentials, adding Gate 3 is an isolated follow-up — application code does not need to change, only the test harness.

Option B (include Gate 3 skeleton now) was rejected because it generates speculative test infrastructure dependent on external constraints not yet decided, leading to placeholder rot and false confidence.

---

## Question 12: Error / Partial-Failure Behaviour
Section 16 defines SUCCESS / PARTIAL_FAILED / FAILED. When a SINGLE entry fails mid-extraction
(e.g. S3 upload error on entry 5 of 100), what should the service do?

A) Continue extracting remaining entries; mark overall pipeline execution as PARTIAL_FAILED; still emit S3 events for the successful children

B) Abort extraction on first entry error; mark overall pipeline execution as FAILED; rely on DLQ + retry

C) Retry the failing entry up to N times (with backoff), then continue with remaining entries and mark PARTIAL_FAILED

D) Other (please describe after [Answer]: tag below)

[Answer]: C

**Rationale**: Bounded per-entry retry with exponential backoff, followed by continue-and-mark-PARTIAL_FAILED if retries are exhausted, is the right failure-handling strategy:
- **Most AWS failures are transient**: ProvisionedThroughputExceeded, S3 SlowDown, 503s, and network blips usually resolve within seconds. Bounded retry (3 attempts) recovers from these cheaply and prevents one flake from corrupting a 100-entry batch.
- **Right blast radius**: 3 attempts with exponential backoff + jitter (e.g., 200ms / 800ms / 3.2s, ±25% jitter) covers the realistic transient-failure window. If retries still fail, the entry is genuinely broken (corrupt entry data, oversized after decompression, sustained AWS issue), and the right move is to record per-entry failure and let the rest of the archive complete.
- **Retry must be classifier-driven**: Retry ONLY on retryable error classes (throttling, 5xx, timeouts). NEVER retry on bomb-defence violations or 4xx client errors — those are deterministic and must fail fast.
- **Signal preservation**: PARTIAL_FAILED with a meaningful per-entry failure reason (after retries exhausted) is operationally actionable. PARTIAL_FAILED inflated by transient throttles dilutes the signal and trains operators to ignore it.
- **Slipsheet records per-entry outcome**: Each entry's final status (and retry-exhaustion reason) goes into the slipsheet, giving downstream pipelines and humans full visibility into what succeeded and why anything failed.

Option A (continue without retry) was rejected because it wastes recoverable work and inflates PARTIAL_FAILED noise. Option B (abort on first failure) was rejected as too brittle — a 100-entry archive should not fail because entry 5 hit a transient throttle; the DLQ re-run cost (redoing entries 1–4) is wasteful and risks repeated partial work.
