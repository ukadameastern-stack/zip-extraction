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

.PHONY: help build test lint vuln sbom up down bootstrap run docker docker-multiarch helm-template helm-lint pbt-replay harness clean \
        deploy-dev05 undeploy-dev05 list-dev05 logs-dev05 dev05-helm-install dev05-bootstrap dev05-push dev05-helm-deploy dev05-route53-bind dev05-route53-unbind dev05-teardown-k8s dev05-teardown-aws

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
	@echo ""
	@echo "DEV05 (AWS account 537462380503 / eu-west-1 / DEV05-EKS-CLUSTER):"
	@echo "  deploy-dev05     - full bootstrap: AWS resources + image push + helm install. Logs to deploy/dev05/logs/deploy-*.log"
	@echo "  undeploy-dev05   - reverse of deploy-dev05; reads deploy/dev05/state.json. Logs to deploy/dev05/logs/undeploy-*.log"
	@echo "  list-dev05       - show what's deployed (from state.json + live AWS/K8s checks)"
	@echo "  logs-dev05       - aggregate every DEV05 log (deploy/undeploy + K8s events + pod logs). Vars: DEV05_LOG_LINES=N, FOLLOW=1"

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

# -------------------------------------------------------------------------
# DEV05 deploy / revert
# -------------------------------------------------------------------------
# Every step writes its full output to deploy/dev05/logs/<phase>-<timestamp>.log
# AND streams to the terminal (tee). `latest.log` is a symlink to the most
# recent log for convenience.
#
# State of record: deploy/dev05/state.json (created by bootstrap, consumed by
# everything else). Undeploy refuses to run without it.

DEV05_LOG_DIR := deploy/dev05/logs
DEV05_TS       = $(shell date -u +%Y%m%dT%H%M%SZ)

dev05-helm-install:
	@mkdir -p $(DEV05_LOG_DIR)
	@bash deploy/dev05/install-helm.sh

dev05-bootstrap: dev05-helm-install
	@bash deploy/dev05/bootstrap-aws.sh

dev05-push:
	@bash deploy/dev05/push-image.sh

dev05-helm-deploy:
	@bash deploy/dev05/helm-deploy.sh

dev05-route53-bind:
	@bash deploy/dev05/route53-bind.sh

dev05-route53-unbind:
	@bash deploy/dev05/route53-unbind.sh

dev05-teardown-k8s:
	@bash deploy/dev05/teardown-k8s.sh

dev05-teardown-aws:
	@bash deploy/dev05/teardown-aws.sh

deploy-dev05:
	@mkdir -p $(DEV05_LOG_DIR)
	@LOG=$(DEV05_LOG_DIR)/deploy-$(DEV05_TS).log; \
	    echo "==> Logging to $$LOG"; \
	    set -o pipefail; \
	    ( \
	        echo "### deploy-dev05 start $$(date -u +%Y-%m-%dT%H:%M:%SZ)"; \
	        $(MAKE) --no-print-directory dev05-helm-install && \
	        $(MAKE) --no-print-directory dev05-bootstrap && \
	        $(MAKE) --no-print-directory dev05-push && \
	        $(MAKE) --no-print-directory dev05-helm-deploy && \
	        $(MAKE) --no-print-directory dev05-route53-bind; \
	        rc=$$?; \
	        echo "### deploy-dev05 end   $$(date -u +%Y-%m-%dT%H:%M:%SZ) rc=$$rc"; \
	        exit $$rc \
	    ) 2>&1 | tee "$$LOG"; \
	    ln -sf "$$(basename "$$LOG")" $(DEV05_LOG_DIR)/latest-deploy.log

undeploy-dev05:
	@mkdir -p $(DEV05_LOG_DIR)
	@LOG=$(DEV05_LOG_DIR)/undeploy-$(DEV05_TS).log; \
	    echo "==> Logging to $$LOG"; \
	    set -o pipefail; \
	    ( \
	        echo "### undeploy-dev05 start $$(date -u +%Y-%m-%dT%H:%M:%SZ)"; \
	        $(MAKE) --no-print-directory dev05-route53-unbind && \
	        $(MAKE) --no-print-directory dev05-teardown-k8s && \
	        $(MAKE) --no-print-directory dev05-teardown-aws; \
	        rc=$$?; \
	        echo "### undeploy-dev05 end   $$(date -u +%Y-%m-%dT%H:%M:%SZ) rc=$$rc"; \
	        exit $$rc \
	    ) 2>&1 | tee "$$LOG"; \
	    ln -sf "$$(basename "$$LOG")" $(DEV05_LOG_DIR)/latest-undeploy.log

list-dev05:
	@bash deploy/dev05/list.sh

logs-dev05:
	@bash deploy/dev05/logs.sh
