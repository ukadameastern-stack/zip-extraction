# UOW-SVC-12 — Zip Extraction Service

---

# 1. Unit Overview

| Field | Value |
|---|---|
| Unit ID | UOW-SVC-12 |
| Component(s) | COMP-PIPE-12 |
| Service Name | Zip Extraction Service |
| Type | Kubernetes Queue Consumer |
| Language | Go |
| Directory | `services/zip-extraction/` |
| Queue | `zip-extraction-queue` |
| Queue Visibility Timeout | 300s |
| DLQ maxReceiveCount | 3 |
| Runtime Memory Limit | 128 Mi |
| Phase | Phase 6 — Remaining Pipeline Services |
| Deployment Target | DEV05-EKS-CLUSTER |
| AWS Region | eu-west-1 |
| Parallelism | Parallel with all Phase 6 units |

---

# 2. Service Purpose

The Zip Extraction Service performs secure streaming decompression of uploaded ZIP archives and fans out extracted child files into the document ingestion pipeline.

The service is optimized for:

- Memory-bounded streaming extraction
- Horizontal scalability
- Secure zip bomb defence
- Event-driven child pipeline execution
- Kubernetes deployability
- Independent child processing fan-out

The service does NOT orchestrate downstream processing directly.

Instead:

```text
ZIP Archive
    ↓
Zip Extraction Service
    ↓
Extracted child files uploaded to S3 staging bucket
    ↓
S3 Event Notifications
    ↓
Independent downstream pipeline executions
```

This architecture ensures:

- loose coupling
- natural parallelism
- retry isolation
- simplified orchestration
- high scalability

---

# 3. Responsibilities

## Core Responsibilities

- Consume ZIP extraction jobs from SQS
- Perform streaming decompression
- Execute 10-point zip bomb defence validation
- Reject malicious archives
- Upload extracted child files to S3 staging bucket
- Create per-entry DynamoDB records
- Generate parent archive slipsheet
- Trigger independent child pipeline executions through S3 events
- Emit processing metrics and logs
- Cleanup temporary extraction workspace

---

# 4. Non-Responsibilities

The service MUST NOT:

- Fully buffer archives in memory
- Extract entire archives to disk before processing
- Directly invoke downstream services
- Support arbitrary archive formats
- Persist long-lived temporary files
- Perform content conversion
- Perform OCR or classification

---

# 5. High-Level Architecture

```text
                 +----------------------+
                 | zip-extraction-queue |
                 +----------+-----------+
                            |
                            v
               +--------------------------+
               | Zip Extraction Service   |
               | (Go / K8s Consumer)     |
               +------------+-------------+
                            |
          +-----------------+------------------+
          |                                    |
          v                                    v
+----------------------+          +----------------------+
| S3 Staging Bucket    |          | DynamoDB             |
| input/ prefix        |          | pipeline_files       |
+----------------------+          +----------------------+
          |
          v
+------------------------------+
| S3 Event Notifications       |
+------------------------------+
          |
          v
+------------------------------+
| Independent Child Pipelines  |
+------------------------------+
```

---

# 6. Processing Flow

```text
1. Receive SQS claim-check message
2. Download ZIP archive from S3
3. Open ZIP stream
4. Validate archive metadata
5. Execute 10-point bomb defence
6. Iterate entries sequentially
7. Validate entry path safety
8. Stream child file upload to S3
9. Create DynamoDB record
10. Continue extraction
11. Generate parent slipsheet
12. SendTaskSuccess
13. Cleanup temporary workspace
```

---

# 7. Queue Contract

## Input Queue

| Property | Value |
|---|---|
| Queue Name | `zip-extraction-queue` |
| Visibility Timeout | 300 seconds |
| DLQ maxReceiveCount | 3 |

---

## Input Message Schema

```json
{
  "pipelineExecutionId": "exec-123",
  "tenantId": "tenant-001",
  "documentId": "doc-456",
  "sourceBucket": "doc-uploader-staging",
  "sourceKey": "uploads/archive.zip",
  "correlationId": "corr-789"
}
```

---

# 8. Core Methods

| Method | Signature | Purpose |
|---|---|---|
| ProcessMessage | `ProcessMessage(msg ClaimCheckMessage)` | Main queue processing entrypoint |
| ExtractEntries | `ExtractEntries(sourceKey string) (entryCount int, childKeys []string, err error)` | Streaming decompression + fan-out |
| CheckBombDefence | `CheckBombDefence(metrics ZipMetrics) (safe bool, reason string)` | Validate zip bomb protection rules |
| ValidateEntryPath | `ValidateEntryPath(path string) error` | Prevent traversal and slips |
| UploadChildEntry | `UploadChildEntry(entry io.Reader)` | Streaming child upload |
| CreatePipelineRecord | `CreatePipelineRecord(record PipelineFile)` | DynamoDB entry creation |
| GenerateSlipSheet | `GenerateSlipSheet(...)` | Parent archive metadata |

---

# 9. Streaming Constraints

The implementation MUST remain memory bounded.

## Required Constraints

| Constraint | Rule |
|---|---|
| Full archive buffering | Forbidden |
| Full extraction to disk | Forbidden |
| Entry processing | Sequential |
| Max in-memory buffer | 4 MB |
| Multipart upload | Required for files >5 MB |
| Temporary storage | Ephemeral only |
| Extraction model | Streaming I/O only |

---

# 10. Supported ZIP Features

| Feature | Supported |
|---|---|
| ZIP | Yes |
| ZIP64 | Yes |
| Encrypted ZIP | No |
| Multi-disk ZIP | No |
| Deflate64 | No |
| Nested archives | Optional (depth-limited) |

---

# 11. 10-Point Zip Bomb Defence

The service MUST reject archives violating any defence rule.

| # | Defence Rule |
|---|---|
| 1 | Maximum compressed archive size |
| 2 | Maximum extracted size |
| 3 | Maximum compression ratio |
| 4 | Maximum entry count |
| 5 | Maximum directory nesting depth |
| 6 | Reject symlinks |
| 7 | Reject absolute paths |
| 8 | Reject path traversal (`../`) |
| 9 | Maximum single file size |
| 10 | Maximum extraction duration |

---

## Recommended Limits

| Validation | Limit |
|---|---|
| Max compressed size | 500 MB |
| Max extracted size | 2 GB |
| Max compression ratio | 100x |
| Max entries | 10,000 |
| Max directory depth | 10 |
| Max single file size | 250 MB |
| Max extraction duration | 240s |

---

# 12. Entry Path Validation

The service MUST reject:

```text
../../etc/passwd
/absolute/path/file.txt
C:\Windows\System32
```

---

# 13. S3 Storage Strategy

## Child File Upload Location

```text
input/{pipelineExecutionId}/{entryIndex}-{safeFilename}
```

Example:

```text
input/exec-123/0001-report.pdf
```

---

# 14. DynamoDB Record Model

## Table

```text
pipeline_files
```

---

## Child Entry Record

```json
{
  "pk": "PIPELINE#exec-123",
  "sk": "FILE#0001",
  "documentId": "doc-456",
  "sourceArchive": "uploads/archive.zip",
  "childKey": "input/exec-123/0001-report.pdf",
  "mimeType": "application/pdf",
  "status": "UPLOADED",
  "sizeBytes": 1048576
}
```

---

# 15. Parent Archive Slipsheet

## Slipsheet Example

```json
{
  "type": "archive-container",
  "sourceArchive": "uploads/archive.zip",
  "childCount": 3,
  "children": [
    "input/exec-123/0001-report.pdf",
    "input/exec-123/0002-image.png",
    "input/exec-123/0003-notes.docx"
  ]
}
```

---

# 16. Failure Semantics

| Status | Meaning |
|---|---|
| SUCCESS | All entries processed |
| PARTIAL_FAILED | Some entries failed |
| FAILED | Archive rejected or extraction aborted |

---

# 17. Idempotency Strategy

```text
pipelineExecutionId + entryIndex
```

---

# 18. Timeout Strategy

| Operation | Timeout |
|---|---|
| Queue visibility | 300s |
| Extraction hard timeout | 240s |
| Heartbeat extension | Every 30s |

---

# 19. Observability

## Metrics

| Metric | Purpose |
|---|---|
| zip_entries_total | Number of extracted entries |
| zip_extraction_duration_seconds | Extraction latency |
| zip_extraction_failures_total | Failure count |
| zip_bomb_rejections_total | Security rejection count |
| extracted_bytes_total | Throughput |
| partial_failures_total | Partial extraction monitoring |

---

# 20. Health Endpoints

| Endpoint | Purpose |
|---|---|
| `/healthz/live` | Liveness probe |
| `/healthz/ready` | Readiness probe |
| `/metrics` | Prometheus metrics |

---

# 21. Kubernetes Deployment

```bash
helm upgrade --install zip-extraction \
  services/zip-extraction/chart/ \
  -f values/doc-uploader-sandbox.yaml
```

---

# 22. Docker Packaging

```text
537462380503.dkr.ecr.eu-west-1.amazonaws.com/doc-uploader-sandbox/zip-extraction
```

---

# 23. Required Go Libraries

| Purpose | Library |
|---|---|
| ZIP handling | `archive/zip` |
| AWS SDK | `aws-sdk-go-v2` |
| Logging | `zap` |
| Metrics | `prometheus/client_golang` |
| Testing | `testcontainers-go` |
| Property testing | `rapid` |

---

# 24. Testing Requirements

## Gate 1 — Unit Tests

- path validation
- bomb defence
- idempotency logic
- slipsheet generation
- S3 key generation

---

## Gate 2 — Local E2E

- Testcontainers
- LocalStack
- SQS
- S3
- DynamoDB

---

## Gate 3 — Sandbox E2E

- EKS integration
- Queue polling
- Extraction validation
- S3 event fan-out
- Metrics verification

---

# 25. Security Requirements

Mandatory controls:

- Non-root containers
- Read-only root filesystem
- Strict path validation
- Zip bomb protection
- Timeout enforcement
- Structured audit logging

---

# 26. Repository Structure

```text
services/zip-extraction/
├── cmd/
├── internal/
│   ├── extraction/
│   ├── bombdefence/
│   ├── storage/
│   ├── dynamodb/
│   ├── slipsheet/
│   ├── metrics/
│   └── validation/
├── chart/
├── Dockerfile
├── Makefile
├── go.mod
└── README.md
```

---

# 27. Final Notes

The Zip Extraction Service is intentionally designed as:

- stateless
- streaming-first
- queue-driven
- horizontally scalable
- event-oriented
- memory bounded

All child processing MUST occur independently through S3 event fan-out.


---

# 28. LocalStack Compatibility

The service MUST fully support LocalStack-based local development and integration testing.

---

## Required LocalStack Services

| AWS Service | Purpose |
|---|---|
| SQS | Queue emulation |
| S3 | Staging bucket emulation |
| DynamoDB | Metadata persistence |
| STS | AWS identity emulation |

---

## Local Development Requirements

The implementation MUST support:

- LocalStack endpoint configuration
- Docker Compose local execution
- Local S3 bucket creation
- Local SQS queue creation
- Local DynamoDB table provisioning
- Offline development without AWS access
- Local E2E pipeline execution

---

## Environment Configuration

Example local configuration:

```yaml
AWS_ENDPOINT_URL: http://localstack:4566
AWS_REGION: eu-west-1
AWS_ACCESS_KEY_ID: test
AWS_SECRET_ACCESS_KEY: test
```

---

## Local E2E Expectations

The service MUST successfully run locally with:

```text
Zip Extraction Service
    +
LocalStack
    +
Testcontainers
```

The local environment MUST validate:

- ZIP extraction
- S3 uploads
- DynamoDB writes
- Queue consumption
- S3 event fan-out
- Retry behaviour
- Bomb defence validation
