# zip-extraction Helm Chart

Application-team-owned chart for the **Zip Extraction Service (UOW-SVC-12)**. Minimal scope per Q9 of requirements verification — Deployment, Service, ConfigMap, ServiceAccount only. HPA, NetworkPolicy, PodDisruptionBudget, and ServiceMonitor are **platform-team scope** with recommendations documented below.

## Install

```bash
# Sandbox
helm upgrade --install zip-extraction ./chart \
    -f chart/values.yaml -f chart/values-sandbox.yaml

# Staging
helm upgrade --install zip-extraction ./chart \
    -f chart/values.yaml -f chart/values-staging.yaml

# Production
helm upgrade --install zip-extraction ./chart \
    -f chart/values.yaml -f chart/values-prod.yaml
```

The per-env `values-<env>.yaml` overlays each environment's:
- `image.digest` (populated by CI release bot)
- `serviceAccount.roleArn` (IRSA role ARN)
- `infra.queueUrl`, `infra.stagingBucket`
- `sse.mode` + `sse.kmsKeyId` when SSE-KMS

## Platform-team integration

### IRSA role (IAM)
Required IAM policy (no wildcards — least privilege per SECURITY-06 / NFR-Z-044):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Sid": "Sqs",
      "Effect": "Allow",
      "Action": ["sqs:ReceiveMessage","sqs:DeleteMessage","sqs:ChangeMessageVisibility","sqs:GetQueueAttributes"],
      "Resource": "arn:aws:sqs:eu-west-1:<account>:zip-extraction-queue" },
    { "Sid": "S3Get",
      "Effect": "Allow",
      "Action": ["s3:GetObject"],
      "Resource": "arn:aws:s3:::<source-bucket>/uploads/*" },
    { "Sid": "S3Put",
      "Effect": "Allow",
      "Action": ["s3:PutObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],
      "Resource": [
        "arn:aws:s3:::<staging-bucket>/input/*",
        "arn:aws:s3:::<staging-bucket>/slipsheets/*"
      ] },
    { "Sid": "Ddb",
      "Effect": "Allow",
      "Action": ["dynamodb:PutItem","dynamodb:UpdateItem"],
      "Resource": "arn:aws:dynamodb:eu-west-1:<account>:table/pipeline_files" }
  ]
}
```

When `sse.mode == "SSE-KMS"`, add:
```json
{ "Sid": "Kms",
  "Effect": "Allow",
  "Action": ["kms:Decrypt","kms:GenerateDataKey"],
  "Resource": "<KMS-key-ARN>" }
```

Trust policy: bind to the K8s ServiceAccount via OIDC condition (`<oidc-id>:sub == system:serviceaccount:<namespace>:zip-extraction`).

### HPA / KEDA (NFR-Z-002)

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: { name: zip-extraction }
spec:
  scaleTargetRef: { name: zip-extraction }
  minReplicaCount: 2
  maxReplicaCount: 10
  triggers:
    - type: aws-sqs-queue
      metadata:
        queueURL: <queue-url>
        awsRegion: eu-west-1
        queueLength: "5"
      identityOwner: operator
```

### NetworkPolicy (NFR-Z-045)

Egress allowlist:
- DNS to CoreDNS (UDP+TCP 53)
- AWS service endpoints on TCP 443: `sqs.eu-west-1.amazonaws.com`, `*.s3.eu-west-1.amazonaws.com`, `dynamodb.eu-west-1.amazonaws.com`, `sts.eu-west-1.amazonaws.com`

For CIDR-based `NetworkPolicy`, source from AWS-published `ip-ranges.json` filtered to eu-west-1 (use an ip-range-controller bot for drift).

### VPC endpoints (recommended)

| Endpoint | Type |
|---|---|
| `com.amazonaws.eu-west-1.s3` | Gateway (free) |
| `com.amazonaws.eu-west-1.dynamodb` | Gateway (free) |
| `com.amazonaws.eu-west-1.sqs` | Interface |
| `com.amazonaws.eu-west-1.sts` | Interface |

### topologySpreadConstraints (NFR-Z-004)

```yaml
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: topology.kubernetes.io/zone
    whenUnsatisfiable: ScheduleAnyway
    labelSelector: { matchLabels: { app.kubernetes.io/name: zip-extraction } }
```

### Prometheus ServiceMonitor

Selector labels: `app.kubernetes.io/name: zip-extraction`. Scrape path: `/metrics`. Interval: 15–30s.

### Recommended alert rules (NFR-Z-062)

```yaml
- alert: ZipExtractionSloViolation
  expr: |
    sum(rate(zip_extraction_failures_total{reason!~"bomb-defence.*|unsupported.*"}[1h]))
      / sum(rate(zip_entries_total[1h])) > 0.005
  for: 30m
- alert: ZipExtractionLatencyP99
  expr: histogram_quantile(0.99, sum(rate(zip_extraction_duration_seconds_bucket[10m])) by (le)) > 230
  for: 10m
- alert: ZipDLQDepth
  expr: aws_sqs_approximate_number_of_messages_visible{queue=~".*zip-extraction-dlq"} > 5
  for: 10m
- alert: ZipBombSpike
  expr: rate(zip_bomb_rejections_total[5m]) > 0.01 * rate(zip_entries_total[5m])
  for: 10m
- alert: ZipRedeliverySpike
  expr: rate(redelivery_skips_total[5m]) > 0.05 * rate(zip_entries_total[5m])
  for: 10m
```

### S3 bucket policy (deny non-TLS)

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Sid": "DenyNonTLS",
      "Effect": "Deny",
      "Principal": "*",
      "Action": "s3:*",
      "Resource": ["arn:aws:s3:::<bucket>","arn:aws:s3:::<bucket>/*"],
      "Condition": { "Bool": { "aws:SecureTransport": "false" } } }
  ]
}
```

### S3 lifecycle (recommended)
- `input/` prefix: expire after 7 days (handles bomb-defence orphans + retention)
- `slipsheets/` prefix: expire after 90 days (audit retention)
- Abort incomplete multipart uploads after 1 day

### CloudWatch log retention
Set ≥ 90 days. **Do NOT grant the IRSA role `logs:DeleteLogStream`** (SECURITY-14 tamper-evident requirement).

## Gate 3 (sandbox EKS E2E) — DEFERRED

Per Q11 of requirements verification, the Gate 3 test harness is deferred until the platform team provisions a sandbox EKS environment with real IRSA credentials.
