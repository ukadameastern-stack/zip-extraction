# Build Instructions — zip-extraction (UOW-SVC-12)

**Document Type**: Build Execution Instructions
**Phase**: CONSTRUCTION — Build and Test
**Generated**: 2026-05-24

## Prerequisites

| Requirement | Value |
|---|---|
| Build tool | Go 1.24 toolchain |
| Container build | Docker (BuildKit) ≥ 24.0; `docker buildx` for multi-arch |
| Helm CLI | v3.15.0+ |
| Make | GNU Make (POSIX-compatible) |
| Optional: cosign | v2.x for image signing |
| Optional: syft | v1.x for SBOM generation |
| Optional: govulncheck | Installed via `tools/go.mod` (no manual install needed) |
| Optional: golangci-lint | v1.60.0+ |
| OS / arch | Linux x86_64 or arm64; macOS x86_64 or arm64 |
| Disk | ~500 MB for Go module cache + binary + image layers |
| Memory | ≥ 2 GB during `docker buildx` multi-arch build |

## Environment Variables (Build Time)

| Var | Used by | Default |
|---|---|---|
| `IMG_REPO` | Makefile (`docker`, `docker-multiarch`) | `537462380503.dkr.ecr.eu-west-1.amazonaws.com/doc-uploader-sandbox/zip-extraction` |
| `IMG_TAG` | Makefile | `dev-<git-sha>` (auto-derived) |
| `PLATFORMS` | Makefile (`docker-multiarch`) | `linux/amd64,linux/arm64` |
| `VERSION` | Dockerfile arg | `dev` (set to release tag in CI) |

## Build Steps

### 1. Verify Go module integrity

```bash
cd services/zip-extraction
go mod verify
go mod tidy -diff
```

Expected: `all modules verified` and no diff from `go mod tidy`. A non-empty diff indicates `go.sum` is out of sync.

### 2. Run linter (must pass before build)

```bash
make lint
```

Expected: exit code 0, no warnings. The curated rule set is documented in `.golangci.yml` and includes errcheck / govet / staticcheck / ineffassign / gocritic / gosec / unused / unparam / unconvert / gofmt / goimports / revive.

### 3. Build the binary

```bash
make build
```

This invokes:

```bash
go build -trimpath -o bin/zip-extraction ./cmd/zip-extraction
```

Expected output: `bin/zip-extraction` static binary, ~15 MB.

### 4. Build the Docker image (local single-arch)

```bash
make docker
# or, with custom tag:
IMG_TAG=v0.1.0 make docker
```

Expected: image `537462380503.dkr.ecr.eu-west-1.amazonaws.com/doc-uploader-sandbox/zip-extraction:dev-<sha>` present in local Docker daemon. Image size: ~25 MB (distroless static base + Go binary).

### 5. Multi-arch image build (CI release pipeline)

```bash
docker buildx create --use --name multiarch || true
docker buildx build \
    --platform linux/amd64,linux/arm64 \
    --build-arg VERSION=v0.1.0 \
    -t "${IMG_REPO}:v0.1.0" \
    --push .
```

Expected: manifest list pushed to ECR with both architectures.

### 6. Helm chart render verification

```bash
make helm-template
# or render against a specific overlay:
helm template zip-extraction chart -f chart/values.yaml -f chart/values-prod.yaml
```

Expected: rendered Kubernetes YAML printed to stdout with NO `<no value>` references and NO `Error:` lines. Each per-env overlay (`values-sandbox.yaml` / `values-staging.yaml` / `values-prod.yaml`) MUST render successfully.

### 7. Helm chart lint

```bash
make helm-lint
```

Expected: `0 chart(s) failed`.

## Build Artifact Inventory

| Artefact | Path |
|---|---|
| Static binary | `services/zip-extraction/bin/zip-extraction` |
| Container image | ECR digest reference |
| SBOM (release only) | `sbom.cyclonedx.json` (CI artifact + GitHub Release asset) |
| Image signature (release only) | Sigstore Rekor transparency log entry |
| Helm chart | `services/zip-extraction/chart/` (referenced; not packaged here) |

## Pre-Production Hardening (Required Before Production Release)

These items are documented as TODOs in the generated source files. They MUST be resolved before the first production release per SECURITY-10:

1. **Dockerfile base-image digest pinning** — replace `FROM golang:1.24-bookworm` and `FROM gcr.io/distroless/static-debian12:nonroot` with `@sha256:<digest>` references. Obtain digests via `docker manifest inspect <image>`.
2. **GitHub Actions SHA pinning** — replace every `uses: <action>@<tag>` with `uses: <action>@<full-sha>` in `.github/workflows/{ci.yml,release.yml}`. Use [pin-github-action](https://github.com/mheap/pin-github-action) or the GitHub UI to get SHAs.
3. **LocalStack image digest pinning** — replace `image: localstack/localstack:3.7` in `deploy/docker-compose.yml` with the digest.

## Troubleshooting

### `go mod verify` fails
- **Cause**: `go.sum` corrupted or partial download.
- **Solution**: `rm -rf $GOPATH/pkg/mod` (or `~/go/pkg/mod`) and re-run.

### `go build` fails with "module not found"
- **Cause**: Module path placeholder in `go.mod` not yet customised for your organisation.
- **Solution**: Replace `github.com/org-placeholder/doc-uploader/services/zip-extraction` in `go.mod` AND every Go file's import statement with your actual repo path. A `gofmt -r` or `find … sed …` can automate this.

### `docker buildx` fails on Apple Silicon for amd64
- **Cause**: QEMU emulation not registered.
- **Solution**: `docker run --privileged --rm tonistiigi/binfmt --install all`.

### Helm template fails with "executing template at <_helpers.tpl>"
- **Cause**: Per-env overlay missing a required value (e.g., `serviceAccount.roleArn`).
- **Solution**: Verify `chart/values-<env>.yaml` populates all `REQUIRED in per-env overlay`-commented fields.

## Build Status Reporting

Successful build:
```
✓ go mod verify
✓ golangci-lint (exit 0)
✓ go build → bin/zip-extraction (~15 MB)
✓ docker build → 25 MB image
✓ helm template (3 overlays)
✓ helm lint (0 errors)
```
