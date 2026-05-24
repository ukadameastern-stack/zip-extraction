# Tech Stack Decisions — zip-extraction (UOW-SVC-12)

**Document Type**: Locked Tech-Stack Decisions
**Phase**: CONSTRUCTION — NFR Requirements (Part 2: Generation)
**Generated**: 2026-05-24
**Unit**: `zip-extraction` (UOW-SVC-12)

This document records the locked tech-stack choices for the Zip Extraction Service. Each entry includes the **decision, source, alternatives considered, version-pinning policy, and SECURITY / PBT alignment**. Any deviation from these choices during Code Generation requires re-opening the relevant Q&A.

---

## 1. Language & Toolchain

| Decision | Go 1.24 |
|---|---|
| Source | Q3 of requirements verification |
| Alternatives rejected | Go 1.22 (oldest supported floor, no offsetting benefit); Go 1.23 (no advantage over 1.24 on greenfield) |
| Version pinning | `go.mod` declares `go 1.24`. CI uses `actions/setup-go@<full-sha>` with `go-version: '1.24'`. Dockerfile base image pinned by digest (NFR-Z-046). |
| SECURITY / PBT | SECURITY-09 (supported runtime), SECURITY-10 (newest patches) |

---

## 2. AWS SDK

| Decision | `aws-sdk-go-v2` (latest minor on each release; ~v2.30.x at time of writing) |
|---|---|
| Source | §23 of input spec |
| Alternatives rejected | aws-sdk-go v1 (deprecated since 2025); custom HTTP client (reinvents auth, signing, retry) |
| Modules used | `github.com/aws/aws-sdk-go-v2/service/s3`, `.../sqs`, `.../dynamodb`, `.../feature/s3/manager` (multipart uploader), `.../aws` (config + middleware) |
| Version pinning | `go.mod` pins minor versions; renovate / dependabot updates on a rolling basis. CI's `govulncheck` blocks releases with known CVEs (NFR-Z-070). |
| Configuration | `aws.Config.RetryMaxAttempts` = 3 (matches application-level retry — but app classifier is the primary retry decision per BR-RETRY-001); `WithEndpointResolverWithOptions` set if `AWS_ENDPOINT_URL` env var present (FR-15.1). |
| SECURITY / PBT | SECURITY-01 (TLS by default; no `DisableSSL` calls), SECURITY-15 (typed errors via `smithy-go`) |

---

## 3. Logging

| Decision | `go.uber.org/zap` |
|---|---|
| Source | §23 of input spec |
| Alternatives rejected | `slog` stdlib (mature but slower in benchmarks for the high-cardinality field set this service uses); `zerolog` (no JSON-vs-console mode switch); `logrus` (slower, deprecated by maintainer's recommendation) |
| Configuration | Production: `zap.NewProductionConfig()` → JSON to stdout. Local: `zap.NewDevelopmentConfig()` with colour. Selection via `LOG_FORMAT` env var (Q10 of requirements). |
| Version pinning | `go.mod` pin |
| SECURITY / PBT | SECURITY-03 (structured fields), BR-LOG-002 (sensitive-field deny-list wrapped around Logger) |

---

## 4. Metrics

| Decision | `github.com/prometheus/client_golang` |
|---|---|
| Source | §23 of input spec |
| Alternatives rejected | OpenTelemetry-only stack (overkill for the metric set; chart README leaves OTel as future work — NFR-Z-063) |
| Version pinning | `go.mod` pin |
| Configuration | Default `prometheus.DefaultRegisterer` + `promhttp.Handler()` on `/metrics`. Metrics named per FR-13.2 + NFR-Z-060. |
| SECURITY / PBT | SECURITY-14 (alerting hook surface) |

---

## 5. ZIP Handling

| Decision | `archive/zip` (stdlib) |
|---|---|
| Source | §23 of input spec |
| Alternatives rejected | `github.com/klauspost/compress/zip` (faster, but BR-BOMB-006 streaming invariants need careful re-validation against its API; stdlib is the audited baseline); custom parser (reinvents zip-spec edge cases) |
| Version pinning | Bound to stdlib version of `go 1.24` |
| Constraint | Encrypted ZIPs, multi-disk ZIPs, and Deflate64 entries are explicitly REJECTED as `*UnsupportedFeatureError` (FR-3.6). |
| SECURITY / PBT | SECURITY-13 (audited stdlib; no untrusted-deserialization beyond ZIP itself, which is defended) |

---

## 6. YAML Library

| Decision | `gopkg.in/yaml.v3` |
|---|---|
| Source | Q7 of NFR plan |
| Alternatives rejected | `sigs.k8s.io/yaml` (ties config to JSON tags; awkward for nested structures); `goccy/go-yaml` (faster but stricter — net DX regression for limit-config YAML with operator-facing edits) |
| Configuration | Uses `yaml.NewDecoder(r).KnownFields(true)` for strict-decode (FR-14.4) — rejects unknown keys at startup. |
| Version pinning | `go.mod` pin (v3.x) |
| SECURITY / PBT | SECURITY-15 (fail-closed config validation), PBT-02 round-trip property on Config marshal/unmarshal |

---

## 7. Testing Helpers

| Decision | `github.com/stretchr/testify` (incl. `testify/mock` for example-based mocks) |
|---|---|
| Source | Q7 of NFR plan |
| Alternatives rejected | stdlib-only `testing` (too much boilerplate for our ~60 business rules' worth of assertions); `gotest.tools` (smaller community, fewer mock utilities) |
| Version pinning | `go.mod` pin |
| Usage | `require.NoError(t, err)` everywhere for "expected success" assertions. `assert.Equal` / `assert.Contains` for richer assertions in example-based tests. `mock.Mock` for example-based fakes where PBT generators would be overkill. |
| SECURITY / PBT | Complements PBT-10 (PBT for property-level coverage; testify for explicit known-scenario coverage) |

---

## 8. Property-Based Testing Framework

| Decision | `pgregory.net/rapid` |
|---|---|
| Source | §23 of input spec; PBT-09 |
| Alternatives rejected | `github.com/leanovate/gopter` (older; smaller community; less idiomatic Go); `github.com/flyingmutant/rapid` (renamed/forked to `pgregory.net/rapid`) |
| Configuration | Default config. CI seed via `RAPID_SEED` env var; failure output includes seed for replay (PBT-08). Domain generators live in `test/generators/` (PBT-07). |
| Version pinning | `go.mod` pin |
| SECURITY / PBT | PBT-09 explicit framework selection; PBT-08 reproducibility |

---

## 9. Integration Testing

| Decision | `github.com/testcontainers/testcontainers-go` + LocalStack container |
|---|---|
| Source | §23 of input spec; NFR-9.2 of requirements (Gate 2) |
| Alternatives rejected | `dockertest` (older API; less ergonomic for multi-container Gate 2 scenarios); pure docker-compose-based tests (less hermetic per Go-test run) |
| Configuration | Spins up a single LocalStack container with `SERVICES=sqs,s3,dynamodb,sts`. Auto-provisions bucket / queue / table via the same Makefile `bootstrap` target as local dev (FR-15.3). |
| Version pinning | `go.mod` pin; LocalStack image pinned by digest (e.g., `localstack/localstack@sha256:…`). |
| SECURITY / PBT | SECURITY-10 (pinned image), PBT-10 (Gate 2 complements example-based + PBT in Gate 1) |

---

## 10. Linter

| Decision | `golangci-lint` |
|---|---|
| Source | Q6 of NFR plan |
| Alternatives rejected | `staticcheck` standalone (subset of golangci-lint's enabled checks); `revive` standalone (only style/lint, no static analysis) |
| Configuration | `.golangci.yml` at repo root enabling: `errcheck`, `govet`, `staticcheck`, `ineffassign`, `gocritic`, `gosec`, `unused`, `unparam`, `unconvert`, `gofmt`, `goimports`, `revive` (with `exported` rule for Godoc enforcement) |
| Version pinning | Pinned major.minor in `.golangci.yml` `version:` directive; CI uses `golangci/golangci-lint-action@<full-sha>` |
| SECURITY / PBT | SECURITY-09 (gosec rule), SECURITY-15 (errcheck) |

---

## 11. Vulnerability Scanner

| Decision | `golang.org/x/vuln/cmd/govulncheck` |
|---|---|
| Source | Q6 of NFR plan; SECURITY-10 |
| Alternatives rejected | `osv-scanner` (broader scope but more false positives outside Go ecosystem); `trivy` (image-scanner-first; slower for module-only scans); Snyk / Sonatype IQ (commercial; out of scope for OSS project skeleton) |
| Configuration | CI runs `govulncheck ./...`. Build fails on HIGH or CRITICAL findings. Suppressions require explicit Go comment with rationale + scheduled review. |
| Version pinning | `go install -modfile=tools/go.mod golang.org/x/vuln/cmd/govulncheck@<tag>` (or pinned in CI workflow) |
| SECURITY / PBT | SECURITY-10 (vulnerability scanning in CI) |

---

## 12. SBOM Generator

| Decision | `anchore/syft` |
|---|---|
| Source | Q6 of NFR plan; SECURITY-10 |
| Alternatives rejected | `cyclonedx-gomod` (Go-only; syft handles both Go modules + container-image layers); manual SBOM (unmaintainable) |
| Configuration | CI runs `syft . -o cyclonedx-json=sbom.cyclonedx.json` on release tags. SBOM uploaded as GitHub Release asset. |
| Version pinning | `anchore/sbom-action@<full-sha>` in GitHub Actions; CLI binary pinned by checksum if installed standalone. |
| SECURITY / PBT | SECURITY-10 (SBOM generation) |

---

## 13. CI/CD Provider

| Decision | GitHub Actions (canonical example) |
|---|---|
| Source | Q8 of NFR plan |
| Alternatives rejected | GitLab CI (provided as adaptation notes only); AWS CodeBuild (provided as adaptation notes only) |
| Workflow file | `.github/workflows/ci.yml` running on `push` to any branch + `pull_request` |
| Steps | (1) `actions/checkout@<sha>` (2) `actions/setup-go@<sha>` with `go-version: '1.24'` (3) `go mod download` (4) `golangci/golangci-lint-action@<sha>` (5) `go test -race -cover ./...` (6) `govulncheck ./...` (7) `anchore/sbom-action@<sha>` (release tags only) (8) Docker build + push (release tags only) |
| Version pinning | Every reusable action pinned by full SHA (not by tag) per SECURITY-10 / NFR-Z-047 |
| SECURITY / PBT | SECURITY-10 (CI/CD integrity), SECURITY-13 (pinned actions = tamper-evident pipeline) |

---

## 14. Container Image

| Decision | Multi-stage Dockerfile; final stage `gcr.io/distroless/static-debian12:nonroot@sha256:<digest>` |
|---|---|
| Source | §22, §25 of input spec; NFR-Z-046 |
| Alternatives rejected | Alpine (more attack surface than distroless); Ubuntu (much larger; many CVEs irrelevant to Go static binary); scratch (no ca-certs, breaks HTTPS to AWS) |
| Build stage | `golang:1.24-bookworm@sha256:<digest>` (build); copies the static binary into the distroless final stage. |
| User | `nonroot` (UID 65532 in distroless) |
| Root filesystem | Read-only (`securityContext.readOnlyRootFilesystem: true` in Helm); `/tmp` mounted as `emptyDir`. |
| Version pinning | Both base images referenced by full digest. Updated via dependabot + manual review on each release. |
| SECURITY / PBT | SECURITY-09 (hardening), SECURITY-10 (pinned digests, current versions) |

---

## 15. Helm Chart Dependencies

| Decision | None — minimal chart per Q9 of requirements |
|---|---|
| Templates | `Deployment`, `Service`, `ConfigMap`, `ServiceAccount`, `values.yaml` |
| Excluded | `HPA`, `PodDisruptionBudget`, `NetworkPolicy`, `ServiceMonitor` — platform-team scope (chart README documents recommended values) |
| Version pinning | Chart `apiVersion: v2`; Helm CLI pinned in CI to a major version (`helm/setup-helm@<sha>` with explicit `version:`) |
| Dependencies | No upstream chart dependencies (no `Chart.yaml.dependencies`); all templates authored in this repo |

---

## 16. Local Development Stack

| Decision | `docker-compose.yml` with LocalStack + service container + Makefile bootstrap |
|---|---|
| Source | Q5 of requirements verification; FR-15 |
| LocalStack version | Pinned by digest in `docker-compose.yml` |
| Services emulated | SQS, S3, DynamoDB, STS (§28 of input spec) |
| Makefile targets | `make up` (compose up -d) / `make down` / `make bootstrap` (auto-provision bucket/queue/DLQ/table) / `make run` (in-process against LocalStack) / `make test` / `make lint` / `make docker` (build local image) / `make pbt-replay SEED=<n>` |
| SECURITY / PBT | Local-prod parity (NFR-Z-090, NFR-Z-091) |

---

## 17. Summary — Version-Pinning Policy

The repository pins **every** external dependency:

| Layer | Pinning mechanism |
|---|---|
| Go modules | `go.mod` + `go.sum` (lockfile committed; renovate / dependabot for updates) |
| Docker base images | Pinned by digest (no `latest`; no `1.24` floating tag) |
| GitHub Actions | Pinned by full commit SHA (not by tag) |
| Helm chart | Chart version in `Chart.yaml`; values schema documented in `values.yaml` |
| Local-dev images | LocalStack pinned by digest in `docker-compose.yml` |
| CLI tools (golangci-lint, govulncheck, syft) | Version pinned in CI workflow OR installed from a versioned `tools/go.mod` |

Pinning policy is enforced by:
- `dependabot.yml` + `renovate.json` configuration in the repo
- CI step inspecting the rendered manifests for unpinned references
- Build & Test stage checklist item (NFR-Z-047)

---

## 18. Compliance Summary

| Rule | Coverage |
|---|---|
| **SECURITY-01** (TLS) | AWS SDK v2 defaults; no `DisableSSL` |
| **SECURITY-03** (logging) | zap + sensitive-field deny-list wrapper |
| **SECURITY-09** (hardening) | distroless base, non-root, RO root FS, pinned digests, no debug routes |
| **SECURITY-10** (supply chain) | go.sum + govulncheck + syft + pinned actions/images |
| **SECURITY-11** (separation) | bombdefence + validation as leaf packages |
| **SECURITY-13** (integrity) | Pinned actions, pinned images, govulncheck on every push |
| **SECURITY-14** (alerting) | Prometheus client_golang + recommended alert rules in README |
| **SECURITY-15** (fail-closed) | yaml.v3 strict-decode, typed errors, golangci-lint errcheck rule |
| **PBT-07** (generators) | `test/generators/` centralised domain generators |
| **PBT-08** (shrinking + reproducibility) | `rapid` framework + seed logging in CI |
| **PBT-09** (framework selection) | `pgregory.net/rapid` documented here |
| **PBT-10** (complementary tests) | `testify` for example-based + `rapid` for property-based |

**No blocking SECURITY or PBT findings at the NFR Requirements stage.**

---

## 19. Forward References (Used by Subsequent Stages)

- **NFR Design** will translate these tooling choices into concrete patterns (logger construction, metrics taxonomy, retry/backoff implementation, IRSA credential chain).
- **Infrastructure Design** will reference NFR-Z-002 (HPA) / NFR-Z-003 (resources) / NFR-Z-004 (multi-AZ) / NFR-Z-046 (hardening) for the Helm chart shape.
- **Code Generation** will render the Dockerfile, Makefile, `.golangci.yml`, `.github/workflows/ci.yml`, `dependabot.yml`, and `tools/go.mod` consistent with the pinning policy in §17.
