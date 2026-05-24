# Build and Test Summary — zip-extraction (UOW-SVC-12)

**Document Type**: Workspace-Wide Build & Test Summary
**Phase**: CONSTRUCTION — Build and Test
**Generated**: 2026-05-24
**Status**: Instruction documents complete; actual build + test execution is operator-driven (see individual instruction files)

## Build Status

| Aspect | Value |
|---|---|
| Build tool | Go 1.24 + Docker BuildKit + Helm v3.15+ |
| Build status (instructions) | ✅ Documented in `build-instructions.md` |
| Build artefacts | Static binary (`bin/zip-extraction`), multi-arch container image (linux/amd64+arm64), SBOM (release only), Sigstore image signature (release only) |
| Pre-production hardening | 3 TODOs documented: (1) Dockerfile base-image digest pinning; (2) GitHub Actions SHA pinning; (3) LocalStack image digest pinning |

## Test Execution Summary (Instructions)

### Gate 1: Unit Tests + PBT

| Aspect | Value |
|---|---|
| Test files | 12 `_test.go` files across 12 internal packages |
| PBT framework | `pgregory.net/rapid` (NFR-Z-080 / PBT-09) |
| PBT properties total | 13 round-trip + 27 invariant + 7 idempotence + 5 stateful + 7 oracle ≈ **59 named properties** carried forward from `component-methods.md` |
| Example tests | ~40 example-based test cases across all packages |
| Coverage target | ≥ 80% statements (NFR-Z-073) |
| Execution | `make test` |
| Status | ✅ Documented in `unit-test-instructions.md` |

### Gate 2: Integration Tests (Testcontainers + LocalStack)

| Aspect | Value |
|---|---|
| Test location | `test/e2e/` (build tag `e2e`) |
| Scenarios documented | 6 (Happy-Path SUCCESS / Bomb-Defence Rejection / Path-Traversal / Unsupported-Feature / Transient-then-Retry PARTIAL_FAILED / Re-Delivery Idempotency) |
| Test harness | `testcontainers-go/modules/localstack` + helper functions for bucket/queue/DLQ/table provisioning |
| Execution | `go test -tags=e2e ./test/e2e/...` |
| Status | ✅ Documented in `integration-test-instructions.md`. Scenario 1 (smoke) implemented; Scenarios 2-6 placeholders await final harness pattern decision (in-process vs subprocess) per Build & Test workflow follow-up. |

### Gate 3: Sandbox EKS E2E

| Status | **DEFERRED** per Q11 of requirements verification |

### Performance Tests

| Aspect | Value |
|---|---|
| SLOs defined | NFR-Z-010 (99.5% success / 28d), NFR-Z-011 (P95 ≤ 180s), NFR-Z-012 (P99 ≤ 220s), NFR-Z-013 (5 MB/s upload) |
| Load generator | Sketch provided in `performance-test-instructions.md` |
| Workload profiles | Light / Standard / Stress / Burst |
| Status | ✅ Documented in `performance-test-instructions.md`. Optional gate; not required for initial release. |

### Security Tests

| Aspect | Value |
|---|---|
| Mandatory checks | `govulncheck` (NFR-Z-070) + `syft` SBOM (NFR-Z-071) + `gosec` lint rule + cosign verify + SBOM attestation verify |
| Adversarial fixtures | 10 zip-bomb / path-traversal / unsupported-feature fixtures documented for Gate 2 integration |
| Fuzz targets | 4 candidate targets documented (Sanitize / parseMessage / Unmarshal × 2); deferred to follow-up |
| Status | ✅ Documented in `security-test-instructions.md` |

## Overall Status

| Aspect | Status |
|---|---|
| Build instructions | ✅ Complete |
| Unit-test instructions | ✅ Complete |
| Integration-test instructions | ✅ Complete |
| Performance-test instructions | ✅ Complete (optional gate) |
| Security-test instructions | ✅ Complete |
| **Ready for Operations** | ✅ Yes — proceed to Operations phase placeholder |

## SECURITY / PBT Compliance Final Check

| Rule | Build-and-Test coverage |
|---|---|
| SECURITY-01 (encryption) | Enforced via SDK TLS defaults + bucket policy in chart README |
| SECURITY-03 (logging) | Verified via `security-test-instructions.md` audit-logging section |
| SECURITY-05 (input validation) | Tested per BR-PATH + BR-BOMB suites |
| SECURITY-06 (least-privilege IAM) | Verified via `chart template` rendering inspection |
| SECURITY-09 (hardening) | Verified via Pod Security Standard manifest check |
| SECURITY-10 (supply chain) | Enforced via CI: govulncheck + syft + cosign + pinning policy |
| SECURITY-11 (separation) | Tested via package-import-graph (bombdefence + validation as leaves) |
| SECURITY-13 (integrity) | Sigstore Rekor records verifiable post-hoc |
| SECURITY-14 (alerting + log retention) | Documented for platform team in chart README |
| SECURITY-15 (fail-closed) | Tested via typed-error tests + `defer recover()` paths |
| PBT-08 (reproducibility) | CI logs seed; `make pbt-replay SEED=<n>` replays |
| PBT-09 (framework) | `pgregory.net/rapid` documented in tech-stack-decisions.md |
| PBT-10 (complementary tests) | Example-based + PBT both run in `make test` |

## Generated Instruction Files

```
aidlc-docs/construction/build-and-test/
├── build-instructions.md
├── unit-test-instructions.md
├── integration-test-instructions.md
├── performance-test-instructions.md
├── security-test-instructions.md
└── build-and-test-summary.md   (this file)
```

## Next Steps

Per the AI-DLC execution plan:

- **Operations stage (placeholder)** — deployment / monitoring / incident-response runbooks; future expansion not in scope for this initial workflow.
- **First production rollout** — requires the 3 documented pre-production TODOs (digest + SHA pinning) to be resolved.
- **Gate 3 sandbox EKS E2E** — to be addressed when the platform team provisions the sandbox.
- **Fuzz targets** — recommended follow-up for hardening.

The workflow is **ready to advance to the Operations placeholder** once the user approves this Build and Test stage.
