# Security Test Instructions — zip-extraction (UOW-SVC-12)

**Test Gate**: Mandatory gate (per SECURITY-10, SECURITY-14, NFR-Z-070, NFR-Z-071)
**Scope**: Dependency vulnerability scanning, SBOM generation/verification, supply-chain integrity, adversarial-input fuzzing

## Mandatory Checks (Run on Every Push)

### 1. Dependency vulnerability scan (`govulncheck`)

```bash
cd services/zip-extraction
make vuln
# expands to:
# go run -modfile=tools/go.mod golang.org/x/vuln/cmd/govulncheck ./...
```

**Pass criteria**: exit code 0; no HIGH or CRITICAL findings.

**Suppressions**: any finding requires an explicit suppression comment in the code AND an open ticket linked to the remediation work. NEVER ignore a HIGH/CRITICAL CVE silently.

### 2. SBOM generation

```bash
make sbom
# expands to:
# syft . -o cyclonedx-json=sbom.cyclonedx.json
```

**Pass criteria**: SBOM file produced. CI uploads it as a release-tag asset.

### 3. Lint security rules (`gosec` via golangci-lint)

`gosec` is enabled in `.golangci.yml`. `make lint` runs it. Pass criteria identical to `make lint`.

### 4. Image-signature verification (post-release)

After a release-tag CI run, verify the signed image:

```bash
cosign verify \
    --certificate-identity-regexp "https://github.com/<org>/doc-uploader/.github/workflows/release.yml" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
    <registry>/<image>@sha256:<digest>
```

**Pass criteria**: cosign reports a valid Sigstore signature with the expected GitHub Actions identity.

### 5. SBOM-attestation verification

```bash
cosign verify-attestation \
    --type cyclonedx \
    --certificate-identity-regexp "https://github.com/<org>/doc-uploader/.github/workflows/release.yml" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
    <registry>/<image>@sha256:<digest>
```

## Adversarial-Input Hardening Tests

### Zip-bomb suite

A curated set of malicious ZIP fixtures should be added under `test/testdata/bombs/`. Each fixture exercises one or more bomb-defence rules. The Gate 2 integration tests reference these fixtures.

| Fixture | Rule(s) exercised | Expected outcome |
|---|---|---|
| `42.zip` (classic recursive bomb) | #2 (cumulative extracted size) | FAILED, rule 2 |
| `huge-singlefile.zip` | #9 (single-file size) | FAILED, rule 9 |
| `many-entries.zip` (12k entries) | #4 (entry count) | FAILED, rule 4 |
| `deep-nesting.zip` (depth 25) | #5 (directory depth) | FAILED, rule 5 |
| `symlink.zip` | #6 (symlink) | FAILED, rule 6 |
| `traversal.zip` (entry name `../etc/passwd`) | path validation (rule 8) | FAILED, path-traversal |
| `absolute.zip` (entry name `/etc/passwd`) | path validation (rule 7) | FAILED, absolute-path |
| `encrypted.zip` (AES) | unsupported-feature | FAILED, encrypted-zip |
| `deflate64.zip` | unsupported-feature | FAILED, deflate64 |
| `slow-bomb.zip` (mid-stream blowup) | #2 mid-extraction | FAILED, rule 2; orphans NOT deleted (BR-BOMB-006) |

### Fuzzing (planned for follow-up)

Go 1.24 supports native fuzz testing via `go test -fuzz=Fuzz...`. Recommended fuzz targets:

- `internal/validation.Sanitize` — adversarial path inputs
- `internal/sqs.parseMessage` — malformed JSON bodies
- `internal/dynamodb.Unmarshal` — adversarial AttributeValue maps
- `internal/slipsheet.Unmarshal` — adversarial JSON

Fuzzing is **deferred to a follow-up task** — not required for the initial release. PBT-03 / PBT-04 properties on these same functions catch the most likely adversarial cases.

## Pen-test Posture (Out of Scope)

External pen-testing is a platform-team responsibility once the service is deployed to staging. This document does NOT specify pen-test scenarios — that's the engagement scope of the security team's chosen vendor.

## Audit Logging Verification

Per SECURITY-14:

1. **CloudWatch log retention** (90+ days): platform team verifies via AWS Console / Terraform plan.
2. **IRSA does NOT have `logs:DeleteLogStream`**: inspect rendered IRSA policy:
    ```bash
    helm template zip-extraction chart -f chart/values.yaml -f chart/values-prod.yaml | \
        yq '.[] | select(.kind == "ServiceAccount")'
    # Cross-reference with the platform-team IAM policy JSON.
    ```
3. **Sensitive-field redaction**: verify log entries via:
    ```bash
    kubectl logs <pod> -c zip-extraction | jq -r '. as $e | $e' | grep -iE 'password|token|secret|access_key' | grep -v REDACTED
    ```
    Pass criteria: command returns nothing (no unredacted sensitive content).

## Configuration Hardening Verification

Verify the rendered Deployment manifest against the restricted Pod Security Standard:

```bash
helm template zip-extraction chart -f chart/values.yaml -f chart/values-prod.yaml | \
    yq '.[] | select(.kind == "Deployment") | .spec.template.spec'
```

Expected:
- `securityContext.runAsNonRoot: true`
- `securityContext.runAsUser: 65532`
- `securityContext.seccompProfile.type: RuntimeDefault`
- `containers[0].securityContext.allowPrivilegeEscalation: false`
- `containers[0].securityContext.readOnlyRootFilesystem: true`
- `containers[0].securityContext.capabilities.drop: [ALL]`

Plus image reference uses digest pinning (`image: <repo>@sha256:...`), not a floating tag.

## Status

All mandatory checks (vuln + SBOM + lint + signature + attestation + log discipline) are wired into CI per `.github/workflows/ci.yml` and `.github/workflows/release.yml`. The remaining hardening steps documented as TODOs in `Dockerfile` + CI workflows (digest + SHA pinning) MUST be completed before the first production release per SECURITY-10.
