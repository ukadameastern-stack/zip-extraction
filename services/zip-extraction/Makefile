SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

GO         ?= go
GOLANGCI   ?= golangci-lint
GOVULN     ?= govulncheck
SYFT       ?= syft
COSIGN     ?= cosign
DOCKER     ?= docker
COMPOSE    ?= docker compose
HELM       ?= helm

IMG_REPO   ?= 537462380503.dkr.ecr.eu-west-1.amazonaws.com/doc-uploader-sandbox/zip-extraction
IMG_TAG    ?= dev-$(shell git rev-parse --short HEAD 2>/dev/null || echo "untracked")
PLATFORMS  ?= linux/amd64,linux/arm64

.PHONY: help build test lint vuln sbom up down bootstrap run docker docker-multiarch helm-template helm-lint pbt-replay harness clean

help:
	@echo "Targets:"
	@echo "  build            - build static binary to bin/zip-extraction"
	@echo "  test             - run unit + PBT tests with race detector"
	@echo "  lint             - run golangci-lint"
	@echo "  vuln             - run govulncheck"
	@echo "  sbom             - generate CycloneDX SBOM"
	@echo "  up               - start LocalStack via docker-compose"
	@echo "  down             - stop docker-compose stack"
	@echo "  bootstrap        - provision LocalStack S3 bucket / SQS queue+DLQ / DynamoDB table"
	@echo "  run              - run service against LocalStack"
	@echo "  docker           - build single-arch image"
	@echo "  docker-multiarch - build linux/amd64,linux/arm64 multi-arch image and push"
	@echo "  helm-template    - render Helm chart against sandbox values"
	@echo "  helm-lint        - lint Helm chart"
	@echo "  pbt-replay       - replay a PBT failure (SEED=<n>)"
	@echo "  clean            - remove bin/ and CI artefacts"

build:
	$(GO) build -trimpath -o bin/zip-extraction ./cmd/zip-extraction

test:
	$(GO) test -race -coverpkg=./internal/... -coverprofile=cover.out ./...
	@$(GO) tool cover -func=cover.out | awk '/^total:/ {print "total coverage:", $$3}'

lint:
	$(GOLANGCI) run

vuln:
	$(GO) run -modfile=tools/go.mod golang.org/x/vuln/cmd/govulncheck ./...

sbom:
	$(SYFT) . -o cyclonedx-json=sbom.cyclonedx.json

up:
	$(COMPOSE) -f deploy/docker-compose.yml up -d

down:
	$(COMPOSE) -f deploy/docker-compose.yml down -v

bootstrap:
	AWS_ENDPOINT_URL=http://localhost:4566 AWS_REGION=eu-west-1 \
		AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test \
		bash deploy/bootstrap-localstack.sh

run:
	AWS_ENDPOINT_URL=http://localhost:4566 \
	AWS_REGION=eu-west-1 \
	AWS_ACCESS_KEY_ID=test \
	AWS_SECRET_ACCESS_KEY=test \
	LOG_FORMAT=console \
	LOG_LEVEL=debug \
	QUEUE_URL=http://localhost:4566/000000000000/zip-extraction-queue \
	STAGING_BUCKET=doc-uploader-staging-local \
	DYNAMO_TABLE=pipeline_files \
	HTTP_PORT=8080 \
	SSE_MODE=SSE-S3 \
	CONFIG_PATH=deploy/config-local.yaml \
	$(GO) run ./cmd/zip-extraction

docker:
	$(DOCKER) build -t $(IMG_REPO):$(IMG_TAG) .

docker-multiarch:
	$(DOCKER) buildx build --platform $(PLATFORMS) -t $(IMG_REPO):$(IMG_TAG) --push .

helm-template:
	$(HELM) template zip-extraction chart -f chart/values.yaml -f chart/values-sandbox.yaml

helm-lint:
	$(HELM) lint chart -f chart/values.yaml -f chart/values-sandbox.yaml

pbt-replay:
	@test -n "$$SEED" || (echo "Usage: make pbt-replay SEED=<n>" && exit 1)
	RAPID_SEED=$$SEED $(GO) test -run TestProperty ./...

harness:
	@echo "Starting test harness on http://localhost:9000 (LocalStack on :4566; service on :8080)"
	$(GO) run ./test/harness

clean:
	rm -rf bin/ sbom.cyclonedx.json coverage.out cover.out
