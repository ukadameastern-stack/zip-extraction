# Infrastructure Design Plan — zip-extraction (UOW-SVC-12)

**Document Type**: Infrastructure Design Plan (Part 1 — Planning)
**Project**: Zip Extraction Service (UOW-SVC-12)
**Unit**: `zip-extraction`
**Phase**: CONSTRUCTION — Infrastructure Design (Plan)
**Generated**: 2026-05-24
**Source Inputs**:
- `aidlc-docs/construction/zip-extraction/nfr-design/logical-components.md` (§2 out-of-pod deps)
- `aidlc-docs/construction/zip-extraction/nfr-requirements/nfr-requirements.md` (NFR-Z-002..NFR-Z-049)
- `aidlc-docs/construction/zip-extraction/nfr-requirements/tech-stack-decisions.md`
- `zip-extraction-service-input.md` (§21, §22, §25, §28)

---

## Purpose

This is **Part 1 of Infrastructure Design (Planning)** for unit `zip-extraction`. It captures:

1. The checklist of infrastructure artefacts to be produced once questions are answered (Part 2 — Generation).
2. A focused set of **deployment / environments / image / networking** clarifying questions for decisions not yet locked by prior Q&A.

Each question has option **A marked (Recommended)** with rationale. Reply per question, or use **"Accept all recommendations"** to lock all answers to A.

---

## Part A — Execution Checklist (Part 2 Will Run After Answers Are Approved)

Once all answers are confirmed, these artefacts are produced under `aidlc-docs/construction/zip-extraction/infrastructure-design/`:

- [x] **infrastructure-design.md** — AWS service mapping (eu-west-1: SQS + S3 + DynamoDB + KMS + IAM + EKS + ECR + CloudWatch) with resource shapes, naming conventions, and platform-team vs. application-team ownership boundary.
- [x] **deployment-architecture.md** — Helm chart shape (templates + values structure), Dockerfile structure, GitHub Actions CI workflow shape, image-pinning strategy, multi-environment values layout, and the chart README's "Platform-Team Integration" section (HPA / NetworkPolicy / ServiceMonitor recommendations).
- [x] Validate against SECURITY-01..15 (encryption, hardening, supply chain, network restriction) and NFR-Z-002..Z-049 coverage
- [x] Cross-reference every logical component from `logical-components.md` to its AWS / K8s realisation

---

## Part B — Clarifying Questions (Answer Required)

### Question 1 — Helm Values Layout for Multiple Environments

How should the Helm chart support multiple deployment environments (sandbox / dev / staging / prod)?

A) **(Recommended)** **A single canonical `values.yaml` with sensible defaults + per-environment overlay files: `values-sandbox.yaml`, `values-staging.yaml`, `values-prod.yaml`.** Each environment file contains ONLY the keys that differ from defaults (queue URL, bucket name, table name, image digest, SSE mode, replica count, IRSA role ARN). Operators run `helm upgrade --install -f values.yaml -f values-<env>.yaml ...`. The chart README documents which keys are environment-specific.

B) Single `values.yaml` with everything; environments override via `helm --set key=value` on the command line.

C) Separate sub-charts per environment under `chart/sandbox/`, `chart/prod/` etc.

X) Other

[Answer]: A

**Recommendation rationale**: A is the most widely-used Helm convention for application charts in 2026. The per-environment overlay is small (10–20 keys), version-controlled, code-reviewed, and obviously dictates per-env state. Option B works for trivial diffs but loses reviewability as env-specific values grow. Option C is reserved for environments that genuinely have structural differences (different K8s resource kinds per env) — overkill here.

---

### Question 2 — Image Digest Pinning Strategy

Per SECURITY-10 / NFR-Z-046 the container image must be pinned by digest. Where is the digest stored / updated?

A) **(Recommended)** **Digest pinned in `values-<env>.yaml` per environment, in two fields: `image.repository` (mutable tag pointer like `:v1.2.3` for human readability) + `image.digest` (`sha256:...` — authoritative).** The Helm `_helpers.tpl` renders the deployment's `image:` field as `{{ .Values.image.repository }}@{{ .Values.image.digest }}` (digest takes precedence). CI's release workflow updates the digest in `values-<env>.yaml` via a bot PR after each successful image push. Promotion across environments is a digest-bump PR.

B) `image.repository` ONLY (no separate digest field) — tag mutability accepted.

C) Digest in CI environment variable, injected into Helm at deploy time via `--set image.digest=...` — digest never committed to the chart.

X) Other

[Answer]: A

**Recommendation rationale**: A gives the GitOps property: the source of truth for "what image is deployed in each environment" is the Git-controlled `values-<env>.yaml` file. PR review and audit log are automatic. The dual `repository`+`digest` shape is the Helm-community convention — tag for humans, digest for security. Option B violates SECURITY-10 (`latest`-style mutability). Option C bypasses Git review for what is effectively the most important deployment fact.

---

### Question 3 — Container Image Architecture(s)

EKS on AWS in 2026 supports both `linux/amd64` (Intel/AMD) and `linux/arm64` (Graviton) node types. Which architecture(s) to build?

A) **(Recommended)** **Multi-arch image: `linux/amd64` + `linux/arm64` via `docker buildx`.** Graviton instances offer ~20% cost savings and similar performance for I/O-bound Go workloads. Multi-arch ensures the same image manifest works regardless of node mix. Build time is ~2× single-arch but absolute cost is minimal in CI.

B) Single-arch `linux/amd64` only — simpler build, smaller manifest, broader fleet compatibility (matches input-spec §22 which implies amd64 by default).

C) Single-arch `linux/arm64` only — requires the entire EKS node pool to be Graviton.

X) Other

[Answer]: A

**Recommendation rationale**: A is the future-proof choice and the prevailing pattern in 2026 for Go services. `docker buildx build --platform linux/amd64,linux/arm64` is a single command — no per-arch CI matrix needed. The resulting manifest list lets Kubernetes pick the right architecture per node. Option B forfeits the Graviton cost savings the platform team may want to capture. Option C ties deployment to a specific node pool decision the platform team owns.

---

### Question 4 — SBOM Signing / Attestation

NFR-Z-071 generates an SBOM via `syft`. Should the SBOM be signed / attested?

A) **(Recommended)** **Sign the container image AND attach the SBOM as an in-toto attestation via `cosign`.** Image and SBOM are both signed with a project-owned cosign key (keyless via OIDC from GitHub Actions per Sigstore convention). Downstream consumers can verify with `cosign verify` and `cosign verify-attestation`.

B) Generate the SBOM only (no signing). SBOM is uploaded as a GitHub Release asset; consumers retrieve and trust the GitHub-asset signature implicitly.

C) Sign only the image (no SBOM attestation); SBOM remains a separate artefact.

X) Other

[Answer]: A

**Recommendation rationale**: A is the supply-chain-security best practice in 2026 (matches SLSA Level 3 attestation expectations). Sigstore keyless signing via GitHub OIDC requires no key-management infrastructure — the platform team can verify SBOM provenance with `cosign verify-attestation --certificate-identity-regexp <github-repo-url>`. Option B leaves the SBOM unsigned — a determined attacker can substitute a fake SBOM. Option C addresses image tampering but not SBOM integrity.

---

### Question 5 — VPC Endpoints vs Internet Egress for AWS Services

The pod calls SQS, S3, DynamoDB, STS. Should these go through VPC endpoints (PrivateLink) or via the cluster's NAT gateway to the public AWS endpoints?

A) **(Recommended)** **Document VPC endpoints as the recommended pattern; chart README references them for the platform team but the chart itself does NOT create VPC endpoints.** VPC endpoints (Gateway endpoint for S3 + DynamoDB, Interface endpoints for SQS + STS) eliminate NAT-gateway egress costs ($45/GB → $0/GB for these), reduce latency, and keep traffic on the AWS backbone (better security posture for SECURITY-07). Platform team owns the endpoint provisioning at the VPC level.

B) Make the chart agnostic — works equally well via VPC endpoints or NAT gateway. (This is effectively A from the chart's perspective — A just adds a README recommendation.)

C) Require VPC endpoints — chart README states "this chart will not function without VPC endpoints configured" with no NAT-gateway fallback documented.

X) Other

[Answer]: A

**Recommendation rationale**: A is the right separation. VPC endpoints are a platform-VPC decision, not an application decision. The chart doesn't need to know — the AWS SDK uses the same endpoint hostnames regardless. The README recommendation flags the cost / latency / security benefits so the platform team can prioritise. Option C is too prescriptive (chart README shouldn't refuse to work).

---

### Question 6 — NetworkPolicy Egress Allowlist Representation

NFR-Z-045 / SECURITY-07 mandate a restrictive egress NetworkPolicy. The chart README documents the required allowlist. How should the allowlist be expressed?

A) **(Recommended)** **A combined representation in the README**: (a) **AWS service endpoint FQDN list** (`sqs.eu-west-1.amazonaws.com`, `*.s3.eu-west-1.amazonaws.com`, `dynamodb.eu-west-1.amazonaws.com`, `sts.eu-west-1.amazonaws.com`) for hostname-based egress controls (e.g., AWS GuardDuty + Network Firewall); (b) **AWS IP ranges for eu-west-1 service prefixes** (referenceable via the AWS-published `ip-ranges.json`, kept current by an automated PR bot) for K8s `NetworkPolicy` CIDR-based egress (since `NetworkPolicy` doesn't support hostnames natively). Plus DNS egress to the cluster's CoreDNS service.

B) Hostnames only — leave CIDR resolution to platform-team tooling.

C) Hard-coded CIDR list snapshot — committed to the chart README as static text, refreshed manually on chart releases.

X) Other

[Answer]: A

**Recommendation rationale**: A acknowledges that `NetworkPolicy` is a CIDR-based primitive but hostname egress controls (FQDN-aware firewalls, GuardDuty, AWS Network Firewall) operate at a different layer. Documenting both lets the platform team pick the tools they have. The CIDR-bot recommendation handles drift over time — AWS IP ranges change quarterly. Option B leaves the practical implementation work undefined. Option C is unmaintainable.

---

### Question 7 — Pod-Level securityContext Beyond Non-Root

The Helm chart sets non-root + read-only root FS (per NFR-Z-046). What additional hardening should the chart's `securityContext` include?

A) **(Recommended)** **Add `allowPrivilegeEscalation: false`, `capabilities: drop: [ALL]`, `seccompProfile.type: RuntimeDefault`, `runAsNonRoot: true`, `runAsUser: 65532` (distroless nonroot UID), `runAsGroup: 65532`, `fsGroup: 65532`.** This matches the Kubernetes "restricted" Pod Security Standard, which is the strictest baseline.

B) Add only the basics that the distroless image needs to function (`allowPrivilegeEscalation: false`, `runAsNonRoot: true`).

C) No additional hardening beyond the existing read-only root FS + non-root — rely on the cluster's PodSecurity admission controller to enforce defaults.

X) Other

[Answer]: A

**Recommendation rationale**: A applies the **restricted** Pod Security Standard at the chart level, ensuring the chart deploys cleanly into the strictest-enforcement namespaces without relying on cluster defaults that vary across environments. The capability-drop (`ALL`) and seccomp `RuntimeDefault` materially reduce the kernel-syscall attack surface — important for a service processing untrusted input. None of these options breaks the Go static binary in the distroless image (verified at code-generation by Gate 2 test). Option B is minimal hardening that works but leaves syscall surface broad. Option C delegates security policy to cluster defaults — fragile.

---

## Part C — Notes for Part 2 (Generation)

After answers are confirmed, Part 2 will produce:

1. **infrastructure-design.md** — AWS service mapping with resource shapes:
   - **Compute**: EKS DEV05-EKS-CLUSTER in eu-west-1, Graviton or Intel node pools (Q3 multi-arch supports both), `topologySpreadConstraints` documented (NFR-Z-004)
   - **Messaging**: SQS main + DLQ with redrive (visibility 300s, maxReceiveCount 3, SSE-SQS), DLQ retention 14d
   - **Storage**: S3 staging bucket (SSE-S3 default or SSE-KMS via Helm Q6 of NFR design — provisioned by platform team with bucket policy denying non-TLS), DynamoDB pipeline_files table (on-demand mode, PITR enabled, encryption-at-rest)
   - **Identity**: IRSA role + SA (Q4.1 pattern), trust-policy template, conditional KMS permission rendering
   - **Networking**: VPC endpoints recommended (Q5), NetworkPolicy allowlist (Q6), no internet-LB (in-cluster Service only)
   - **Image registry**: ECR `537462380503.dkr.ecr.eu-west-1.amazonaws.com/doc-uploader-sandbox/zip-extraction` (per §22 input spec)
   - **Observability**: CloudWatch logs (90d retention, no DeleteLogStream in IRSA), Prometheus scrape (chart README guidance), recommended alert rules (NFR-Z-062)
   - Resource naming conventions table
   - Ownership boundary table (platform team vs application team)

2. **deployment-architecture.md**:
   - **Helm chart structure**:
     - `chart/Chart.yaml`, `chart/values.yaml`, `chart/values-sandbox.yaml`, `chart/values-staging.yaml`, `chart/values-prod.yaml` (Q1 strategy)
     - `chart/templates/deployment.yaml`, `service.yaml`, `configmap.yaml`, `serviceaccount.yaml`, `_helpers.tpl`
     - `chart/README.md` with platform-team integration guidance (HPA / NetworkPolicy / ServiceMonitor recommendations + VPC endpoint guidance + CIDR-allowlist documentation)
   - **Dockerfile**:
     - Multi-stage: builder (`golang:1.24-bookworm@sha256:<digest>`) → final (`gcr.io/distroless/static-debian12:nonroot@sha256:<digest>`)
     - Multi-arch via `docker buildx` (Q3)
     - Pod-level securityContext per Q7 (restricted PSS)
   - **CI workflow** `.github/workflows/ci.yml`:
     - `golangci-lint` + `go test -race -cover` + `govulncheck` + (release-only) Docker buildx multi-arch + `syft` SBOM + `cosign` sign + attest (Q4)
     - Release workflow updates `values-<env>.yaml` digest field via bot PR (Q2)
   - **Mermaid deployment-topology diagram** (pod → ConfigMap / SA → AWS services via VPC endpoints / kubelet probes / Prometheus scrape)

---

## Part D — How to Respond

1. Edit `[Answer]:` tags in this file with a letter or `X: <free-text>`.
2. Or reply inline (e.g., "Q1=A, …").
3. **"Accept all recommendations"** locks all 7 answers to option A.

Once answers are confirmed, Part 2 generates the 2 infrastructure-design artefact files.

---

## Part E — User Answers (Confirmed)

**Confirmed 2026-05-24T13:23:00Z** — user reply: **"Accept all recommendations"**.

| Question | Answer | Decision |
|---|---|---|
| Q1 — Helm values layout | A | values.yaml + values-sandbox.yaml + values-staging.yaml + values-prod.yaml |
| Q2 — Image digest pinning | A | `image.repository` + `image.digest` in `values-<env>.yaml`; CI bot updates via PR |
| Q3 — Container architectures | A | Multi-arch `linux/amd64` + `linux/arm64` via `docker buildx` |
| Q4 — SBOM signing | A | `cosign` keyless via GitHub OIDC; sign image + attach SBOM as in-toto attestation |
| Q5 — VPC endpoints | A | Chart agnostic; README documents VPC endpoints as recommended for cost / latency / security |
| Q6 — NetworkPolicy allowlist | A | FQDN list (hostname firewalls) + CIDR-bot AWS prefix list (NetworkPolicy CIDR) + DNS to CoreDNS |
| Q7 — Pod securityContext | A | Restricted Pod Security Standard (drop ALL caps, seccomp RuntimeDefault, no privesc, UID 65532) |

**Ambiguity analysis**: All 7 answers are unambiguous letter selections. No follow-up questions required. Proceeding to Part 2 (Generation).

