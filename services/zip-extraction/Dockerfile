# syntax=docker/dockerfile:1.6
#
# zip-extraction service Dockerfile.
# Multi-stage, multi-arch.
# Base image digest placeholders MUST be replaced with concrete sha256:... values
# before merging to main (NFR-Z-046 / SECURITY-10). Use:
#   docker manifest inspect golang:1.24-bookworm   | jq '.manifests[0].digest'
#   docker manifest inspect gcr.io/distroless/static-debian12:nonroot | jq '.manifests[0].digest'

# ---------- Builder stage ----------
FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS builder
# TODO(security): replace builder FROM line above with digest-pinned reference, e.g.
# FROM --platform=$BUILDPLATFORM golang:1.24-bookworm@sha256:<digest> AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source and build static binary.
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/zip-extraction \
        ./cmd/zip-extraction

# ---------- Final stage ----------
FROM gcr.io/distroless/static-debian12:nonroot
# TODO(security): replace final FROM line above with digest-pinned reference, e.g.
# FROM gcr.io/distroless/static-debian12:nonroot@sha256:<digest>

WORKDIR /app
COPY --from=builder /out/zip-extraction /app/zip-extraction
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/zip-extraction"]
