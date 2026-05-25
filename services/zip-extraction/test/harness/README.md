# zip-extraction test harness

A tiny **developer-only** browser UI for exercising the zip-extraction service against LocalStack.
Lives at `services/zip-extraction/test/harness/` — **not part of the production service**.

## What it does

1. Serves a single-page UI at `http://localhost:9000/`.
2. Accepts a ZIP via file picker.
3. Uploads it to the LocalStack S3 source bucket.
4. Sends a claim-check SQS message.
5. Polls DynamoDB + the slipsheet S3 object until the service produces a terminal outcome.
6. Renders entry rows, child-S3-key listing, slipsheet JSON, and live `/metrics` deltas
   captured at submit time (so you see this run's contribution, not cumulative state).

## Prerequisites

- LocalStack running on `:4566` (e.g. `make up && make bootstrap` from the service dir).
- The zip-extraction service running on `:8080` (or pass `-service-metrics-url`).
- All AWS resources provisioned (source bucket, staging bucket, queue + DLQ, DDB table).

## Run

```bash
# from services/zip-extraction/
make harness
# or, equivalently:
cd test/harness && go run .
```

Then open <http://localhost:9000/>.

## Flags

```
-listen              :9000                                              harness HTTP port
-endpoint-url        http://localhost:4566                              LocalStack endpoint; EMPTY (-endpoint-url=) means default real-AWS endpoints
-region              eu-west-1                                          AWS region
-queue-url           http://localhost:4566/000000000000/zip-extraction-queue
-dlq-url             http://localhost:4566/000000000000/zip-extraction-dlq   surfaced in the queue-depth panel
-source-bucket       doc-uploader-uploads-local                         where ZIPs are uploaded
-staging-bucket      doc-uploader-staging-local                         where children + slipsheets land
-dynamo-table        pipeline_files
-service-metrics-url http://localhost:8080/metrics                      proxied for the metrics panel
```

**Credential mode** is decided by `-endpoint-url`:
- Empty → SDK's default credential chain (IRSA in K8s, `AWS_PROFILE`/env locally with real AWS)
- Non-empty → hard-coded `test/test` credentials (LocalStack accepts anything)

## API surface

`/api/submit` — POST multipart with field `archive` (file) and optional `pipelineExecutionId`,
`tenantId`, `documentId`, `correlationId`. Returns the IDs used.

`/api/result?execId=...` — GET; returns `{state, slipsheet, ddbRows, s3Listing}`.

`/api/metrics` — GET; proxies the service's `/metrics`, filtered to the eight collectors.

`/api/config` — GET; the resolved flag values, for the UI to display.

## Cloud deployment (DEV05 only)

A `Dockerfile` and chart templates live alongside this source. The harness is OFF by default in the chart (`harness.enabled: false`) and only enabled in `values-dev05.yaml`. The DEV05 deploy stands it up behind an IP-allowlisted public ALB so developers can drive the real cloud pipeline from a browser.

| | |
|---|---|
| URL | `http://zip-extraction-dev-sandbox-v1.dev05.k8s.opus2dev.com/` |
| Access | ALB security-group allowlist (no auth) — set via `harness.ingress.inboundCidrs` |
| Image | re-uses `…/zip-extraction-service` ECR repo, tag prefix `dev05-harness-` |
| IRSA  | reuses the service's ServiceAccount; same SQS/S3/DDB permissions |
| Resources | request `128Mi` memory / `50m` CPU; limit `512Mi` (large headroom for multipart uploads — streaming PutObject means peak is ~30–50 MiB regardless of ZIP size) |
| `/tmp` | `emptyDir` volume — required by Go's `multipart.ParseMultipartForm` for uploads >32 MiB (the rest of the rootfs is read-only) |
| Deploy | `make deploy-dev05` from the service dir |
| Tear down | `make undeploy-dev05` (deletes namespace + ALB + Route 53 record + AWS resources) |

## Security note

This binary has **zero auth** of its own. Public exposure is only acceptable in DEV05 because of the ALB-level CIDR allowlist. **Never deploy outside DEV05** without first putting an auth layer (OIDC sidecar / basic-auth Ingress annotation / VPN-only ingress) in front. Production deployment artefacts live under `chart/` (with `harness.enabled: false`) and `cmd/zip-extraction/` only.
