# Zip Extraction Service (UOW-SVC-12)

**Streaming ZIP decompression service** for the document-uploader pipeline. Consumes claim-check messages from SQS, downloads ZIP archives from S3, performs 10-point bomb-defence validation, fans out child entries to a staging S3 prefix, and writes a parent slipsheet for downstream consumers.

Generated end-to-end via the AI-DLC workflow — see `aidlc-docs/` at the workspace root for the full design trail.

## Quick Start (Local Development)

```bash
# 1. Bring up LocalStack + the service container
make up

# 2. Provision LocalStack (idempotent)
make bootstrap

# 3. Tail logs
docker compose -f deploy/docker-compose.yml logs -f zip-extraction

# Or run the service in-process against LocalStack:
make run
```

Send a test message:

```bash
aws --endpoint-url=http://localhost:4566 sqs send-message \
    --queue-url http://localhost:4566/000000000000/zip-extraction-queue \
    --message-body '{
        "pipelineExecutionId": "exec-test-1",
        "tenantId": "tenant-1",
        "documentId": "doc-1",
        "sourceBucket": "doc-uploader-uploads-local",
        "sourceKey": "uploads/sample.zip",
        "correlationId": "corr-1"
    }'
```

## Architecture

```
SQS (zip-extraction-queue)
        │
        ▼
┌─────────────────────────┐
│  Zip Extraction Service │   stateless Go 1.24, EKS pod
│   (this repo / chart)   │   • 10-rule bomb defence
└──┬──────────────────┬───┘   • streaming I/O only
   │                  │       • per-msg SQS heartbeat
   ▼                  ▼       • Q12 retry classifier
S3 staging      DynamoDB
input/*.json    pipeline_files
slipsheets/*.json
   │
   ▼
S3 PutObject events → downstream pipelines
```

See:
- `aidlc-docs/inception/application-design/application-design.md` — components, methods, services, dependencies
- `aidlc-docs/construction/zip-extraction/functional-design/` — state machine, business rules, domain entities
- `aidlc-docs/construction/zip-extraction/nfr-design/` — patterns + logical components
- `aidlc-docs/construction/zip-extraction/infrastructure-design/` — AWS mapping + deployment

## Environment Variables

| Var | Purpose | Example |
|---|---|---|
| `AWS_REGION` | AWS region | `eu-west-1` |
| `AWS_ENDPOINT_URL` | LocalStack override (empty in prod) | `http://localstack:4566` |
| `QUEUE_URL` | SQS queue URL | `https://sqs.eu-west-1.amazonaws.com/<acct>/zip-extraction-queue` |
| `STAGING_BUCKET` | S3 staging bucket name | `doc-uploader-staging-eu-west-1` |
| `DYNAMO_TABLE` | DynamoDB table | `pipeline_files` |
| `LOG_FORMAT` | `json` (prod) or `console` (local) | `json` |
| `LOG_LEVEL` | `info`, `debug`, `warn`, `error` | `info` |
| `HTTP_PORT` | Operational HTTP server port | `8080` |
| `SSE_MODE` | `SSE-S3` or `SSE-KMS` | `SSE-S3` |
| `SSE_KMS_KEY_ID` | KMS key ARN when SSE-KMS | `arn:aws:kms:...:key/...` |
| `CONFIG_PATH` | Path to tunable YAML config | `/etc/zip-extraction/config.yaml` |

## YAML Config Schema (Mounted ConfigMap)

```yaml
bombDefence:    { maxCompressedSizeBytes, maxExtractedSizeBytes, maxCompressionRatio,
                  maxEntryCount, maxDirectoryDepth, maxSingleFileSizeBytes, maxExtractionDurationSec }
streaming:      { maxInMemoryBufferBytes, multipartThresholdBytes }
retry:          { maxAttempts, backoffBaseMillis, backoffFactor, jitterFraction }
sqs:            { heartbeatIntervalSec, maxInFlight, gracefulShutdownTimeoutSec, visibilityTimeoutSec }
```

See `deploy/config-local.yaml` for the canonical default values.

## Observability

- `GET /healthz/live` — liveness; always 200 once running
- `GET /healthz/ready` — readiness; 200 iff AWS reachability passed AND not draining
- `GET /metrics` — Prometheus exposition

Eight emitted metrics (see `internal/metrics/metrics.go`):
- `zip_entries_total{status}` (counter)
- `zip_extraction_duration_seconds{outcome}` (histogram)
- `zip_extraction_failures_total{reason}` (counter)
- `zip_bomb_rejections_total{rule}` (counter)
- `extracted_bytes_total` (counter)
- `partial_failures_total` (counter)
- `redelivery_skips_total` (counter — idempotent redelivery indicator)
- `slipsheet_write_failures_total` (counter)

## Deployment

```bash
# Sandbox
helm upgrade --install zip-extraction ./chart \
    -f chart/values.yaml -f chart/values-sandbox.yaml

# Staging / Production: same pattern with the corresponding overlay.
```

See `chart/README.md` for the platform-team integration guide (HPA, NetworkPolicy, VPC endpoints, IRSA policy, alert rules).

## Testing Gates

| Gate | Command | What it runs |
|---|---|---|
| Gate 1 (unit + PBT) | `make test` | All `_test.go` + `pgregory.net/rapid` properties |
| Gate 2 (LocalStack E2E) | `go test -tags=e2e ./test/e2e/...` | Testcontainers + LocalStack integration |
| Gate 3 (sandbox EKS E2E) | _Deferred_ per Q11 of requirements verification | — |

## Operational Tools

- **Heap dump on demand**: `kubectl exec <pod> -- kill -USR1 1` writes `/tmp/heap-<RFC3339>.pprof`. `kubectl cp` to retrieve. No HTTP pprof endpoint is exposed (SECURITY-09).
- **PBT failure replay**: failing CI run logs the seed; `make pbt-replay SEED=<n>` re-runs deterministically.

## Security Posture (summary)

- IRSA-only AWS authentication (no static credentials)
- Distroless non-root container, read-only root filesystem
- TLS 1.2+ enforced via AWS SDK defaults
- Sensitive-field log deny-list (passwords, tokens, credentials, AWS keys)
- 10-point streaming zip-bomb defence with mid-stream short-circuit
- Path-traversal / absolute-path / symlink rejection
- Conditional DynamoDB writes + deterministic S3 keys → idempotent under SQS re-delivery
- Image + SBOM cosign-signed via Sigstore keyless OIDC

Full SECURITY-01…15 + PBT-01…10 compliance matrix in `aidlc-docs/`.

## License

See workspace root `LICENSE` (placeholder — populate during repository onboarding).
