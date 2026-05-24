# Group K Summary — Steps 50–52 (CI Workflows)

| Step | File | Highlights |
|---|---|---|
| 50 | `.github/workflows/ci.yml` | Push/PR workflow scoped to `services/zip-extraction/**` path filter. Runs: `go mod verify` → `golangci-lint` (pinned version) → `go build` → `go test -race -cover` with `RAPID_SEED=${{ github.run_id }}` for reproducibility → coverage gate ≥80% → `govulncheck` via pinned tools module → Helm lint + template render. Every action use marked with TODO to pin to full SHA before production. |
| 51 | `.github/workflows/release.yml` | Tag-triggered (`v*.*.*`) workflow with two jobs: (a) `build-sign-push` — assume IRSA, ECR login, Buildx multi-arch (`linux/amd64,linux/arm64`), syft SBOM (CycloneDX), cosign keyless sign via GitHub OIDC, cosign attest SBOM as in-toto, attach SBOM to GitHub Release; (b) `bump-sandbox-digest` — opens automated PR updating `chart/values-sandbox.yaml` with the new digest. Staging + production digests remain operator-driven manual PRs. |
| 52 | `aidlc-docs/construction/zip-extraction/code/ci-workflows.md` | This summary. |

Compliance:
- SECURITY-10 supply chain: actions to be pinned by SHA; image multi-arch with SBOM and signature.
- SECURITY-13 integrity: Sigstore Rekor records every signature; attested SBOMs verifiable post-hoc.
- NFR-Z-070..074 maintainability: lint + vuln + coverage gates enforce on every push.
- NFR-Z-082 PBT CI: rapid seed logged via `RAPID_SEED`.

Note: action-version pinning to SHAs is a final hardening step performed during the Build & Test stage. The current placeholders (`@v4`, `@v6`, etc.) are sufficient for the chart to render and reviewers to evaluate the workflow shape.
