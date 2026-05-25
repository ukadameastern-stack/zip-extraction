# syntax=docker/dockerfile:1.6
#
# Test-harness Dockerfile. Mirrors the service Dockerfile pattern.
# Build from the service root so the Go module + index.html embed both resolve:
#   docker build -f test/harness/Dockerfile -t harness:dev .

FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/harness \
        ./test/harness

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/harness /app/harness
USER 65532:65532
EXPOSE 9000
ENTRYPOINT ["/app/harness"]
