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
-endpoint-url        http://localhost:4566                              LocalStack endpoint
-region              eu-west-1                                          AWS region
-queue-url           http://localhost:4566/000000000000/zip-extraction-queue
-source-bucket       doc-uploader-uploads-local                         where ZIPs are uploaded
-staging-bucket      doc-uploader-staging-local                         where children + slipsheets land
-dynamo-table        pipeline_files
-service-metrics-url http://localhost:8080/metrics                      proxied for the metrics panel
```

## API surface

`/api/submit` — POST multipart with field `archive` (file) and optional `pipelineExecutionId`,
`tenantId`, `documentId`, `correlationId`. Returns the IDs used.

`/api/result?execId=...` — GET; returns `{state, slipsheet, ddbRows, s3Listing}`.

`/api/metrics` — GET; proxies the service's `/metrics`, filtered to the eight collectors.

`/api/config` — GET; the resolved flag values, for the UI to display.

## Security note

This binary has zero auth. **Do not deploy** — it's purely for local development.
Production deployment artefacts live under `chart/` and `cmd/zip-extraction/` only.
