# NFR Requirements Plan — zip-extraction (UOW-SVC-12)

**Document Type**: NFR Requirements Plan (Part 1 — Planning)
**Project**: Zip Extraction Service (UOW-SVC-12)
**Unit**: `zip-extraction`
**Phase**: CONSTRUCTION — NFR Requirements (Plan)
**Generated**: 2026-05-24
**Source Inputs**:
- `aidlc-docs/inception/requirements/requirements.md` (NFR-1..11)
- `aidlc-docs/inception/application-design/application-design.md` (+ 4 detail docs)
- `aidlc-docs/construction/zip-extraction/functional-design/*.md` (3 files)
- `zip-extraction-service-input.md`

---

## Purpose

This is **Part 1 of NFR Requirements (Planning)** for the single unit `zip-extraction`. It captures:

1. The checklist of NFR artefacts to be produced once questions are answered (Part 2 — Generation).
2. A focused set of **NFR & tech-stack** clarifying questions for decisions not already settled by the input spec, requirements verification (12 Q&A), application design (8 Q&A), or functional design (8 Q&A). The intent is to lock the remaining quality-attribute targets and tooling choices.

Each question has option **A marked (Recommended)** with rationale. Reply per question, or use **"Accept all recommendations"** to lock all answers to A.

---

## Part A — Execution Checklist (Part 2 Will Run After Answers Are Approved)

Once all answers are confirmed, these artefacts are produced under `aidlc-docs/construction/zip-extraction/nfr-requirements/`:

- [x] **nfr-requirements.md** — Per-unit non-functional requirements: scalability, performance (SLOs / SLIs), availability, reliability, security baseline mapping, observability, maintainability. Numbered `NFR-Z-NNN` for the unit (distinct from project-level NFR-1..11 in `requirements.md`).
- [x] **tech-stack-decisions.md** — Locked tech-stack choices with rationale: Go toolchain version, AWS SDK v2, logging, metrics, YAML, testing, PBT, linting, vuln-scanning, SBOM, CI provider. Each decision links to the SECURITY / PBT rule it supports.
- [x] Validate against SECURITY-01…15 and PBT-09 (framework explicitly recorded)
- [x] Cross-reference every project-level NFR-1..11 (from `requirements.md`) to one or more unit-level NFR-Z-NNN entries

---

## Part B — Clarifying Questions (Answer Required)

### Question 1 — Throughput Target & Concurrency Sizing

The default `sqs.maxInFlight = 5` was set in NFR-7 without explicit throughput justification. What's the realistic message-per-pod throughput target?

A) **(Recommended)** **5 in-flight × ~10 archives/min average completion → ~50 archives/min/pod** for typical workloads (mix of small and 100 MB archives). With horizontal scaling (HPA per Q2 below), aggregate throughput grows linearly. Sized to fit comfortably within the 128 MiB memory limit (5 × 4 MiB streaming buffer = 20 MiB peak entry buffers; 5 × ~12 MiB SDK/zap overhead ≈ 60 MiB; ~50 MiB headroom for GC and ZIP central-directory parsing).

B) Higher throughput target — `maxInFlight = 10`, sized for ~100 archives/min/pod. Requires raising memory limit (likely 256 MiB) and reduces per-message margin.

C) Lower throughput target — `maxInFlight = 2`, sized for ~20 archives/min/pod. Maximises per-message memory headroom at the cost of more pods needed for the same aggregate throughput.

X) Other

[Answer]: A

**Recommendation rationale**: Option A respects the explicit 128 MiB pod-memory limit from §1 of the input spec while leaving comfortable GC headroom. Option B requires renegotiating the memory limit (out of scope for this stage). Option C inflates Kubernetes cost — for the same aggregate throughput you need 2.5× more pods, each carrying full overhead (sidecar, SDK init, log buffer).

---

### Question 2 — Horizontal Pod Autoscaling (HPA) Replica Bounds & Trigger

The Helm chart (per Q9 of requirements) is minimal and intentionally does NOT include an HPA template — that's platform-team scope. But the **NFR document needs to record the recommended autoscaling behaviour** so the platform team can author the HPA. What should we recommend?

A) **(Recommended)** **Min 2 / max 10 replicas, scaling trigger on SQS queue `ApproximateNumberOfMessagesVisible / 5` (target 1 message visible per pod-in-flight-slot)** via the KEDA SQS scaler (or HPA + custom metrics adapter). Min 2 ensures availability during rolling updates. Max 10 caps aggregate throughput at ~500 archives/min per cluster (Q1 throughput × 10), leaving runway before SQS/DDB/S3 throttling becomes a concern.

B) Min 1 / max 5 replicas — conservative cost posture; relies on queue backlog tolerance.

C) Min 3 / max 20 replicas — aggressive scaling for higher peak throughput at higher cluster baseline cost.

X) Other

[Answer]: A

**Recommendation rationale**: A balances cost (min 2) against availability and burst capacity. Triggering on `ApproximateNumberOfMessagesVisible / maxInFlight` keeps the queue depth bounded relative to processing capacity — a battle-tested SQS autoscaling pattern. KEDA's `aws-sqs-queue` scaler is the standard way to express this in 2026; the platform team can equally use HPA + `external.metrics.k8s.io` with CloudWatch metric adapter if KEDA is unavailable.

---

### Question 3 — Pod Resource Requests (CPU & Memory)

What Kubernetes resource requests should the Helm chart document for the platform team?

A) **(Recommended)** **CPU request 250m, CPU limit unset; memory request 96Mi, memory limit 128Mi (matches §1 input spec).** CPU-request-only (no limit) is the recommended Kubernetes pattern for latency-sensitive Go workloads — avoids CFS-throttling stutter. Memory limit set tight matches the input-spec contract and forces OOM-kill if the bomb-defence streaming invariants are violated (an explicit failure beats silent memory bloat).

B) CPU request 500m, CPU limit 1000m, memory request 128Mi, memory limit 128Mi — stricter limits at the cost of potential CFS throttling under burst load.

C) CPU request 100m, CPU limit unset, memory request 64Mi, memory limit 128Mi — lower baseline for higher pod-density on smaller nodes.

X) Other

[Answer]: A

**Recommendation rationale**: A applies the Go-on-Kubernetes consensus (no CPU limit for latency-sensitive services; memory request slightly below limit to allow burst). 250m CPU is enough for a Go service whose hot path is `compress/flate` decompression + S3 upload (mostly I/O-bound). The memory request 96Mi reserves enough for Kubernetes scheduler decisions while letting the GC reach the 128Mi ceiling under heavy load — at which point the OOM-killer is a deliberate fail-loud signal.

---

### Question 4 — Service-Level Objectives (SLOs)

What SLOs should the unit publish (consumed by the platform team's SLI dashboards + alert rules)?

A) **(Recommended)** **A two-objective SLO set**:
- **Success-rate SLO**: ≥ **99.5%** of received SQS messages reach a terminal status of SUCCESS or PARTIAL_FAILED (i.e., **not** FAILED) over a 28-day rolling window. **Excludes** bomb-defence-rejected and unsupported-feature-rejected archives (those are deterministic FAILED outcomes attributable to upstream input quality, not service quality).
- **Latency SLO**: P95 end-to-end processing latency ≤ **180 s** for archives ≤ 100 MB / ≤ 100 entries (matches NFR-1.1 from `requirements.md`). P99 ≤ **220 s** (below the 240 s extraction-hard-timeout rule #10 with margin).

B) Single SLO: 99% success rate over 7-day window; no explicit latency SLO (covered implicitly by rule #10).

C) Three-objective: success rate 99.9% / 28-day, P95 latency, P99 latency, throughput floor 100 archives/min per cluster.

X) Other

[Answer]: A

**Recommendation rationale**: A is the right granularity for an event-driven worker service. A 99.5% target is realistic given the inherent transient-failure rate of S3 / DDB under throttling — it gives an error budget of ~3.5 hours of zero-success time per 28 days. Excluding bomb-defence and unsupported-feature FAILED from the SLO numerator is critical: those are **deterministic** outcomes (the upstream archive is the problem), not service-quality regressions. Counting them would let the SLO appear to fail simply because a bunch of bad archives arrived. Option B is too coarse for a security-critical service. Option C adds a throughput SLO that is more naturally enforced via HPA configuration (Q2) than via alerting.

---

### Question 5 — Multi-AZ Pod Replication

Should pods be spread across availability zones in eu-west-1?

A) **(Recommended)** **Yes — `topologySpreadConstraints` documented in the Helm chart README for the platform team to apply**, distributing replicas across the cluster's available AZs with `maxSkew: 1`. Min-2-replicas (Q2) means at least one pod survives an AZ outage. The service is stateless (per §27 + NFR-4.1), so AZ-aware spreading has no penalty.

B) Not configured by this chart — leave to platform-team defaults (the cluster may already enforce topology spread via a default `PodTopologySpread`).

C) Force strict anti-affinity with `requiredDuringSchedulingIgnoredDuringExecution` — pods must NOT co-locate on the same node. Lower availability risk but higher scheduling friction.

X) Other

[Answer]: A

**Recommendation rationale**: A delegates the policy correctly (Helm chart is platform-coupled enough to document spread; doesn't enforce it — that's platform-team scope per Q9 of requirements). Multi-AZ spread is essentially free for stateless services and the standard production posture in eu-west-1 (3 AZs available). Option B risks the chart shipping without an AZ-spread recommendation, which is operationally weaker. Option C is over-restrictive — pods may legitimately co-locate on a node with 8 GiB free.

---

### Question 6 — Linter, Vulnerability Scanner, and SBOM Tool

Per SECURITY-10 the project must include a dependency vulnerability scanner and SBOM generator in CI. Which tools should the Makefile + CI invoke?

A) **(Recommended)** **`golangci-lint` (linter) + `govulncheck` (vuln) + `syft` (SBOM).** All three are widely-adopted Go-native or Go-aware tools maintained by Go team / Anchore. `golangci-lint` aggregates ~70 linters (we enable a curated subset). `govulncheck` is the official Go vulnerability database scanner — narrow, accurate, fast. `syft` produces CycloneDX/SPDX SBOMs in seconds.

B) `staticcheck` + `osv-scanner` + `cyclonedx-gomod` — alternative open-source stack; tightly Go-focused.

C) `golangci-lint` + `trivy` (vuln + SBOM combined) — one fewer tool, but `trivy` is image-scoped and slower for Go-module-only scans.

X) Other

[Answer]: A

**Recommendation rationale**: A is the most ergonomic Go-native toolchain in 2026. `golangci-lint` is the de-facto Go linter aggregator and has YAML-configurable rule sets; we'll define a config covering errcheck, govet, ineffassign, staticcheck, gosec, gocritic. `govulncheck` is laser-focused on Go-vulnerability matching (vs CVE-list-scanners that produce more noise). `syft` generates SBOMs in either CycloneDX or SPDX format, both consumable by SBOM-ingestion tools the platform team may run.

---

### Question 7 — YAML Library & Testing Helper

For the YAML config loader (FR-14) and unit-test assertions, which Go libraries should we lock?

A) **(Recommended)** **`gopkg.in/yaml.v3` for YAML; `github.com/stretchr/testify` for testing assertions (`require`, `assert`) AND mock generation (`testify/mock`).** Both are the most widely-adopted choices in Go-2026 with stable APIs.

B) `sigs.k8s.io/yaml` (wraps JSON unmarshal for YAML — same struct tags as JSON, used in Kubernetes ecosystem) + `testify`.

C) `goccy/go-yaml` (faster, stricter) + minimal stdlib `testing` only (no `testify`).

X) Other

[Answer]: A

**Recommendation rationale**: A is the path of least surprise for Go developers in 2026. `yaml.v3` has strict-decode (`KnownFields(true)`) which we need for FR-14.4 schema validation. `testify` reduces test boilerplate dramatically (one line vs five) — its `require.NoError(t, err)` idiom is the Go testing standard. `testify/mock` complements PBT (which handles property-level testing) by providing simple example-based fakes where PBT generators are overkill. Option B is fine but ties config-loading semantics to JSON tags, which is awkward when JSON and YAML representations diverge. Option C trades 0.5 s of test setup for substantial DX regression.

---

### Question 8 — CI/CD Provider Assumption for Build Instructions

The Build & Test stage will produce CI instructions. Which provider should the example pipeline target?

A) **(Recommended)** **GitHub Actions** — produce `.github/workflows/ci.yml` as the canonical example, with notes describing how to adapt to GitLab CI / CodeBuild. GitHub Actions is the most widely-used Go CI provider in 2026 and has first-class support from `actions/setup-go`, `golangci-lint-action`, `govulncheck-action`.

B) GitLab CI — produce `.gitlab-ci.yml`.

C) AWS CodeBuild — produce `buildspec.yml` (closest to deployment target).

X) Other

[Answer]: A

**Recommendation rationale**: A is the most reproducible / discoverable choice. GitHub Actions workflows are widely understood, the ecosystem has battle-tested Go reusable actions, and CodeBuild / GitLab translations are well-documented elsewhere. Option C aligns with AWS deployment but the workflow YAML is less expressive (no community-action ecosystem) and harder for new engineers to read.

---

## Part C — Notes for Part 2 (Generation)

After answers are confirmed, Part 2 will produce:

1. **nfr-requirements.md** — Per-unit NFRs labelled `NFR-Z-001` … `NFR-Z-NNN` covering:
   - Scalability (Q1 throughput, Q2 HPA, Q3 resources, Q5 multi-AZ)
   - Performance (Q4 SLOs, NFR-1 from requirements.md, memory bounds, streaming I/O)
   - Availability (HPA min, multi-AZ, graceful drain)
   - Reliability (FR-12 retry, FR-9 heartbeat, FR-11 cleanup)
   - Security (SECURITY-01…15 unit-specific application)
   - Observability (logging, metrics, traces)
   - Maintainability (Q6 linter / vuln / SBOM, Q7 libraries, Q8 CI)
   - Cross-reference matrix: project NFR-1..11 → unit NFR-Z-NNN.
2. **tech-stack-decisions.md** — One row per tool with:
   - Decision (e.g., "Go 1.24")
   - Source (FR/NFR/Q reference)
   - Alternatives considered + rejected with rationale
   - Version pinning policy (e.g., go.mod minor-version, GitHub Action versions, Docker image digest pinning)
   - SECURITY / PBT rule alignment

---

## Part D — How to Respond

1. Edit `[Answer]:` tags in this file with a letter or `X: <free-text>`.
2. Or reply inline (e.g., "Q1=A, …").
3. **"Accept all recommendations"** locks all 8 answers to option A.

Once answers are confirmed, Part 2 generates the 2 NFR artefact files.

---

## Part E — User Answers (Confirmed)

**Confirmed 2026-05-24T12:59:00Z** — user reply: **"Accept all recommendations"**.

| Question | Answer | Decision |
|---|---|---|
| Q1 — Throughput sizing | A | `maxInFlight=5`, ~50 archives/min/pod, fits 128 MiB memory |
| Q2 — HPA bounds | A | Min 2 / max 10; KEDA SQS scaler on `ApproximateNumberOfMessagesVisible / maxInFlight` |
| Q3 — Resource requests | A | CPU req 250m (no limit); memory req 96Mi / limit 128Mi |
| Q4 — SLOs | A | 99.5% success-rate / 28-day rolling (excl. bomb-defence + unsupported); P95 ≤ 180 s, P99 ≤ 220 s for ≤ 100 MB archives |
| Q5 — Multi-AZ | A | `topologySpreadConstraints` documented in chart README; min 2 replicas |
| Q6 — Lint/vuln/SBOM tools | A | `golangci-lint` + `govulncheck` + `syft` |
| Q7 — YAML + testing libs | A | `gopkg.in/yaml.v3` + `stretchr/testify` (with `testify/mock`) |
| Q8 — CI provider | A | GitHub Actions (`.github/workflows/ci.yml`); GitLab/CodeBuild adaptation notes |

**Ambiguity analysis**: All 8 answers are unambiguous letter selections. No follow-up questions required. Proceeding to Part 2 (Generation).

