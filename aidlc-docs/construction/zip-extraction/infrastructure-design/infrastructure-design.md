# Infrastructure Design — zip-extraction (UOW-SVC-12)

**Document Type**: AWS Infrastructure Mapping
**Phase**: CONSTRUCTION — Infrastructure Design (Part 2: Generation)
**Generated**: 2026-05-24
**Unit**: `zip-extraction` (UOW-SVC-12)

This document maps the logical components from `nfr-design/logical-components.md` to **concrete AWS / Kubernetes resources** with naming conventions, resource shapes, and explicit ownership boundaries (application team vs. platform team).

---

## 1. Cloud Provider & Region

| Property | Value |
|---|---|
| Cloud provider | AWS |
| Region | `eu-west-1` |
| Account model | Sandbox / Staging / Prod accounts (per platform-team convention) — chart consumes per-env values |
| EKS cluster | `DEV05-EKS-CLUSTER` (per §1 input spec) |
| Container registry | ECR — `537462380503.dkr.ecr.eu-west-1.amazonaws.com/doc-uploader-sandbox/zip-extraction` (per §22 input spec) |

---

## 2. Compute Infrastructure (Application Workload)

### 2.1 EKS Deployment
**AWS resource**: EKS Deployment via Helm chart
**Owner**: Application team (Helm chart) + Platform team (cluster + node pools)
**Shape**:
- Kind: `Deployment` (stateless workload — NFR-Z of `services.md`)
- Replicas: `replicaCount: 2` (default, env-overridable; min 2 per NFR-Z-002)
- Strategy: `RollingUpdate` with `maxUnavailable: 1`, `maxSurge: 1`
- Pod resources: CPU req 250m (no limit), memory req 96Mi / limit 128Mi (NFR-Z-003)
- Pod `securityContext`: restricted PSS (Q7 of infra plan)
  - `runAsNonRoot: true`
  - `runAsUser: 65532`
  - `runAsGroup: 65532`
  - `fsGroup: 65532`
  - `allowPrivilegeEscalation: false`
  - `capabilities: { drop: [ALL] }`
  - `seccompProfile: { type: RuntimeDefault }`
  - `readOnlyRootFilesystem: true`
- Volumes: `emptyDir` mounted at `/tmp` (writable; for AWS SDK transient files only)
- `topologySpreadConstraints` (chart README recommendation; not enforced by chart): `maxSkew: 1`, `topologyKey: topology.kubernetes.io/zone`, `whenUnsatisfiable: ScheduleAnyway`
- `terminationGracePeriodSeconds: 270` (250s drain + 20s teardown margin)

### 2.2 Container image
**AWS resource**: ECR image `537462380503.dkr.ecr.eu-west-1.amazonaws.com/doc-uploader-sandbox/zip-extraction:<tag>@sha256:<digest>`
**Owner**: Application team
**Shape**:
- Multi-arch: `linux/amd64` + `linux/arm64` (Q3 of infra plan)
- Base image: `gcr.io/distroless/static-debian12:nonroot@sha256:<digest>` (final stage)
- Builder base: `golang:1.24-bookworm@sha256:<digest>`
- Image is signed via `cosign` (Q4 of infra plan); SBOM is attached as in-toto attestation
- Pinning: tag (human-readable) + digest (authoritative) both committed in `values-<env>.yaml`
- Updated by CI release workflow's automated bot PR

### 2.3 Kubernetes Service
**Resource**: `Service` (ClusterIP) rendered by Helm chart
**Owner**: Application team (template) + Platform team (cluster-level Service mesh, if any)
**Shape**:
- Type: `ClusterIP` (in-cluster only; no external LB)
- `port: 8080`, `targetPort: 8080`
- Selector: `app.kubernetes.io/name: zip-extraction`
- Annotations (chart README recommendation): Prometheus scrape annotations if not using ServiceMonitor

### 2.4 Kubernetes ServiceAccount + IRSA
**Resource**: `ServiceAccount` rendered by Helm chart; IAM role provisioned by platform team
**Owner**: Application team (SA template) + Platform team (IAM role + trust policy)
**Shape**:
- SA name: `zip-extraction`
- Annotation: `eks.amazonaws.com/role-arn: arn:aws:iam::<account>:role/zip-extraction-<env>`
- IAM trust policy (platform team — chart README documents the OIDC condition):
  ```json
  {
    "Effect": "Allow",
    "Principal": { "Federated": "arn:aws:iam::<account>:oidc-provider/<oidc-id>" },
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringEquals": {
        "<oidc-id>:aud": "sts.amazonaws.com",
        "<oidc-id>:sub": "system:serviceaccount:<namespace>:zip-extraction"
      }
    }
  }
  ```
- IAM permissions policy (per NFR-Z-044 — exact actions, no wildcards):
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
        "Action": ["s3:GetObject","s3:GetObjectVersion"],
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
      // Conditional: only rendered when sse.mode == "SSE-KMS"
      // { "Sid": "Kms",
      //   "Effect": "Allow",
      //   "Action": ["kms:Decrypt","kms:GenerateDataKey"],
      //   "Resource": "arn:aws:kms:eu-west-1:<account>:key/<id>" }
    ]
  }
  ```

### 2.5 Kubernetes ConfigMap
**Resource**: `ConfigMap` rendered by Helm chart
**Owner**: Application team
**Shape**:
- Name: `zip-extraction-config`
- Single key: `config.yaml` (full YAML payload per FR-14 / NFR-7 of `requirements.md`)
- Mounted at `/etc/zip-extraction/config.yaml` (env: `CONFIG_PATH=/etc/zip-extraction/config.yaml`)
- Values sourced from Helm `bombDefence:`, `streaming:`, `retry:`, `sqs:` keys in `values-<env>.yaml`

---

## 3. Messaging Infrastructure

### 3.1 SQS Main Queue
**AWS resource**: `arn:aws:sqs:eu-west-1:<account>:zip-extraction-queue`
**Owner**: Platform team (provisioning + IaC); Application team consumes
**Shape**:
- Type: Standard (NOT FIFO — at-least-once delivery is acceptable given idempotency contract per BR-IDEMPOTENCY-*)
- Visibility timeout: 300 s (matches NFR-Z-034 heartbeat target)
- Message retention period: 4 days (default)
- Receive wait time: 20 s (long-poll; matches NFR-Z-001 dispatch pattern)
- Encryption at rest: SSE-SQS (AWS-managed) or SSE-KMS (platform-team choice)
- Redrive policy: `deadLetterTargetArn: <dlq-arn>`, `maxReceiveCount: 3` (per §1, §7 input spec)
- KMS data key reuse: default

### 3.2 SQS Dead-Letter Queue
**AWS resource**: `arn:aws:sqs:eu-west-1:<account>:zip-extraction-dlq`
**Owner**: Platform team
**Shape**:
- Type: Standard
- Message retention period: 14 days (allow operator investigation time)
- Encryption at rest: same as main queue
- Alarm (chart README recommendation): `ApproximateNumberOfMessagesVisible > 5` for 10 minutes

---

## 4. Storage Infrastructure

### 4.1 S3 Staging Bucket
**AWS resource**: `arn:aws:s3:::<staging-bucket>` (name per environment, e.g., `doc-uploader-staging-prod-eu-west-1`)
**Owner**: Platform team
**Shape**:
- Encryption at rest: SSE-S3 (default) OR SSE-KMS with customer-managed key (per chart `sse.mode`)
- Block public access: enabled (all 4 settings)
- Bucket policy: deny non-TLS requests (per SECURITY-01 — chart README provides the template):
  ```json
  {
    "Sid": "DenyNonTLS",
    "Effect": "Deny",
    "Principal": "*",
    "Action": "s3:*",
    "Resource": ["arn:aws:s3:::<bucket>", "arn:aws:s3:::<bucket>/*"],
    "Condition": { "Bool": { "aws:SecureTransport": "false" } }
  }
  ```
- Lifecycle policy on `input/` prefix:
  - Transition to S3 Glacier Instant Retrieval after 7 days (cheap audit storage) OR delete after 7 days (platform-team choice)
  - Abort incomplete multipart uploads after 1 day
- Lifecycle policy on `slipsheets/` prefix:
  - Transition to S3 Glacier Instant Retrieval after 30 days
  - Delete after 90 days (audit-retention floor)
- S3 PutObject event notification:
  - `Filter: { Prefix: "input/" }` → SNS/SQS/Lambda for downstream pipeline trigger (downstream pipeline's responsibility; this service does NOT subscribe)
  - **NO** event notification on `slipsheets/` prefix (BR-SLIP-001 isolation)
- Versioning: enabled (defence-in-depth against accidental delete)
- Server-access logging: enabled (per SECURITY-02 best practice for cloud object storage)

### 4.2 DynamoDB pipeline_files Table
**AWS resource**: `arn:aws:dynamodb:eu-west-1:<account>:table/pipeline_files`
**Owner**: Platform team
**Shape**:
- Partition key: `pk` (String) — format `PIPELINE#<execId>`
- Sort key: `sk` (String) — format `FILE#<entryIndex:04d>`
- Billing mode: `PAY_PER_REQUEST` (on-demand — handles bursty workload without capacity planning)
- Encryption: AWS-managed CMK (default; SSE-KMS with customer-managed key optional)
- Point-in-time recovery (PITR): enabled
- TTL: disabled (records are durable audit logs; lifecycle handled by an out-of-band retention job, NOT TTL)
- Streams: optional — disabled by default; platform team enables if downstream analytics consume DDB Streams

### 4.3 KMS Key (Optional — SSE-KMS Mode Only)
**AWS resource**: `arn:aws:kms:eu-west-1:<account>:key/<id>` (customer-managed CMK)
**Owner**: Platform team
**Shape**:
- Type: Symmetric, customer-managed
- Key policy: permits the IRSA role's `kms:Decrypt` + `kms:GenerateDataKey` actions; permits the S3 service principal to encrypt (for SSE-KMS) and DDB service principal (for table SSE) if used
- Key rotation: enabled (annual)
- Multi-region: platform-team decision

---

## 5. Networking Infrastructure

### 5.1 VPC + Subnets
**AWS resource**: VPC + private subnets in 3 AZs
**Owner**: Platform team
**Shape**: chart README documents requirements:
- Private subnets in ≥ 2 AZs (3 AZs recommended for the multi-AZ pod spread per NFR-Z-004)
- NAT gateway or VPC endpoints (Q5 of infra plan — VPC endpoints recommended)

### 5.2 VPC Endpoints (Recommended)
**AWS resource**: Gateway + Interface VPC endpoints
**Owner**: Platform team
**Shape**: chart README recommends provisioning the following endpoints in the VPC carrying the EKS node pool:
| Endpoint | Type | Purpose |
|---|---|---|
| `com.amazonaws.eu-west-1.s3` | Gateway | S3 GetObject/PutObject — free, no NAT egress |
| `com.amazonaws.eu-west-1.dynamodb` | Gateway | DynamoDB PutItem — free, no NAT egress |
| `com.amazonaws.eu-west-1.sqs` | Interface | SQS ReceiveMessage/DeleteMessage/ChangeMessageVisibility — billed |
| `com.amazonaws.eu-west-1.sts` | Interface | IRSA AssumeRoleWithWebIdentity — billed |
| `com.amazonaws.eu-west-1.logs` (optional) | Interface | CloudWatch logs ingest if EKS log driver uses CW directly |

**Cost benefit**: ~$45/GB → $0/GB for the S3+DDB traffic (Gateway endpoints are free). Interface endpoints cost ~$0.01/hour per AZ + $0.01/GB.

### 5.3 NetworkPolicy (Egress Allowlist)
**AWS resource**: K8s `NetworkPolicy` (NOT rendered by chart per Q9 of requirements — platform team)
**Owner**: Platform team
**Shape**: chart README documents the required allowlist (Q6 of infra plan):

```yaml
# Egress allowlist (illustrative; platform team renders the actual NetworkPolicy)
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: zip-extraction
  policyTypes: [Egress]
  egress:
    # 1. DNS to CoreDNS
    - to:
        - namespaceSelector: { matchLabels: { kubernetes.io/metadata.name: kube-system } }
          podSelector: { matchLabels: { k8s-app: kube-dns } }
      ports:
        - { port: 53, protocol: UDP }
        - { port: 53, protocol: TCP }
    # 2. AWS service endpoints (CIDR list from AWS-published ip-ranges.json filtered to eu-west-1)
    - to:
        - ipBlock: { cidr: <SQS-eu-west-1-CIDR> }
        - ipBlock: { cidr: <S3-eu-west-1-CIDR> }
        - ipBlock: { cidr: <DYNAMODB-eu-west-1-CIDR> }
        - ipBlock: { cidr: <STS-eu-west-1-CIDR> }
      ports:
        - { port: 443, protocol: TCP }
```

**FQDN representation (for hostname-aware firewalls like AWS Network Firewall or GuardDuty)**:
- `sqs.eu-west-1.amazonaws.com`
- `*.s3.eu-west-1.amazonaws.com` (S3 path-style + virtual-hosted style + transfer accel)
- `dynamodb.eu-west-1.amazonaws.com`
- `sts.eu-west-1.amazonaws.com`
- (optional) `logs.eu-west-1.amazonaws.com` if not using EKS log driver

**CIDR drift handling**: chart README references AWS's `ip-ranges.json` and recommends an automated bot (e.g., `ip-ranges-controller`) to refresh `NetworkPolicy` CIDRs on AWS updates. This keeps the policy current without manual snapshots.

---

## 6. Identity & Access Management

### 6.1 IRSA Role
See §2.4 above — full trust policy + permissions policy documented.

### 6.2 IAM Policy Boundaries (Optional)
**Owner**: Platform team
**Shape**: chart README recommends attaching a permissions boundary to the IRSA role limiting it to never grant any actions beyond what the role's identity policy contains — defence in depth against role-escalation misconfigurations.

---

## 7. Observability Infrastructure

### 7.1 CloudWatch Log Group
**AWS resource**: `/aws/eks/<cluster-name>/zip-extraction` (or platform-team-convention)
**Owner**: Platform team (provisioning); EKS log driver (writes); service writes JSON to stdout
**Shape**:
- Retention: ≥ 90 days (NFR-Z minimum; SECURITY-14)
- IRSA role does NOT have `logs:DeleteLogStream` permission (SECURITY-14 tamper-evident requirement)
- Optional KMS encryption with customer-managed CMK

### 7.2 Prometheus Scrape Target
**AWS resource**: `ServiceMonitor` CR (NOT rendered by chart per Q9 of requirements)
**Owner**: Platform team
**Shape**: chart README documents the labels Prometheus Operator should select on. Scrape interval: 15–30 s. Scrape path: `/metrics`.

### 7.3 Recommended Prometheus Alert Rules
**Owner**: Platform team (operates); chart README provides the templates
**Shape**: 5 alert rules per NFR-Z-062 (`AlertZipExtractionSloViolation`, `AlertZipExtractionLatencyP99`, `AlertZipDLQDepth`, `AlertZipBombSpike`, `AlertZipRedeliverySpike`). Full PromQL in chart README.

### 7.4 SLI Dashboard
**Owner**: Platform team
**Shape**: chart README provides Grafana dashboard JSON OR documents the 6 SLI queries (per `nfr-requirements.md` §11) so the platform team renders the dashboard.

---

## 8. Container Registry & Image Distribution

### 8.1 ECR Repository
**AWS resource**: `537462380503.dkr.ecr.eu-west-1.amazonaws.com/doc-uploader-sandbox/zip-extraction`
**Owner**: Platform team (provisioning); Application team (push from CI)
**Shape**:
- Image scanning: ECR enhanced scanning enabled (defence-in-depth on top of CI's `govulncheck`)
- Image immutability: enabled (tag mutation prohibited; digest stays authoritative)
- Lifecycle policy: retain last 50 images per repo; expire older after 365 days
- KMS encryption for image layers (optional; CMK)

### 8.2 ECR Pull Access for EKS Nodes
**AWS resource**: EKS node IAM role has `AmazonEC2ContainerRegistryReadOnly` policy
**Owner**: Platform team

---

## 9. Resource Naming Conventions

| Resource | Name pattern | Example |
|---|---|---|
| EKS cluster | `<env>-EKS-CLUSTER` | `DEV05-EKS-CLUSTER` (per §1) |
| K8s namespace | `<env>` or `doc-uploader` | (platform-team convention) |
| K8s Deployment | `zip-extraction` | — |
| K8s Service | `zip-extraction` | — |
| K8s ServiceAccount | `zip-extraction` | — |
| K8s ConfigMap | `zip-extraction-config` | — |
| SQS main queue | `zip-extraction-queue` | (per §7 input spec) |
| SQS DLQ | `zip-extraction-dlq` | — |
| S3 staging bucket | `<env>-staging-eu-west-1` | (platform-team convention) |
| DynamoDB table | `pipeline_files` | (per §14 input spec) |
| IRSA role | `zip-extraction-<env>` | — |
| KMS alias (optional) | `alias/doc-uploader-<env>` | — |
| ECR repo | `doc-uploader-<env>/zip-extraction` | (per §22 input spec) |
| CloudWatch log group | `/aws/eks/<cluster>/zip-extraction` | — |

---

## 10. Ownership Boundary Table

| Resource | Application Team | Platform Team |
|---|---|---|
| Go application code | ✅ Authors | — |
| Helm chart templates | ✅ Authors | (reviews) |
| Helm `values.yaml` defaults | ✅ Authors | — |
| Helm `values-<env>.yaml` | ✅ Authors values keys; (CI bot updates digest); platform reviews | (reviews) |
| Dockerfile | ✅ Authors | — |
| GitHub Actions CI workflow | ✅ Authors | — |
| EKS cluster | — | ✅ Owns |
| Node pools + AMI | — | ✅ Owns |
| VPC + subnets + route tables | — | ✅ Owns |
| VPC endpoints | — | ✅ Owns |
| NAT gateway (if used) | — | ✅ Owns |
| NetworkPolicy manifests | — | ✅ Owns (chart provides allowlist spec) |
| SQS queue + DLQ + redrive | — | ✅ Owns (chart provides config requirements) |
| S3 bucket + lifecycle + policy | — | ✅ Owns (chart provides config requirements) |
| DynamoDB table + PITR | — | ✅ Owns (chart provides schema requirements) |
| KMS key (if SSE-KMS) | — | ✅ Owns |
| IRSA role + trust policy + permissions | — | ✅ Owns (chart provides exact policy JSON) |
| ECR repo + lifecycle | — | ✅ Owns |
| K8s namespace + RBAC + quotas | — | ✅ Owns |
| HPA / KEDA ScaledObject | — | ✅ Owns (chart README provides spec) |
| Prometheus ServiceMonitor + alerts | — | ✅ Owns (chart README provides templates) |
| Grafana dashboards | — | ✅ Owns (chart README provides SLI queries) |
| CloudWatch log group + retention | — | ✅ Owns |
| EKS log driver (fluent-bit, etc.) | — | ✅ Owns |

---

## 11. Logical-Component → AWS-Resource Mapping (Cross-Reference)

| Logical component (from logical-components.md) | AWS / K8s realisation |
|---|---|
| `cmd/zip-extraction` (binary) | Container image in ECR running in EKS Deployment Pod |
| `internal/app.Service` | Pod main goroutine |
| `internal/sqs.Adapter` + heartbeater | SQS API calls to `arn:aws:sqs:.../zip-extraction-queue` |
| `internal/extraction.Service` | Pod worker goroutine (1 per in-flight message) |
| `internal/bombdefence.Checker` + `LimitedReader` | In-process (no AWS) |
| `internal/validation.PathValidator` | In-process (no AWS) |
| `internal/storage.Adapter` (S3) | S3 GetObject/PutObject API calls; multipart via `s3manager.Uploader` |
| `internal/dynamodb.Adapter` | DynamoDB PutItem API calls |
| `internal/slipsheet.Writer` | S3 PutObject API call to `slipsheets/` prefix |
| `internal/retry.Retrier` | In-process (no AWS) |
| `internal/metrics.Metrics` | Prometheus collectors served via `/metrics` HTTP endpoint |
| `internal/health.Server` | HTTP server in pod; consumed by kubelet probes + Prometheus scraper |
| `internal/config.Config` | K8s ConfigMap mounted at `/etc/zip-extraction/config.yaml` + env vars |
| `internal/awsclients.Set` | AWS SDK v2 client objects authenticated via IRSA token from `AWS_WEB_IDENTITY_TOKEN_FILE` |
| `internal/log.Logger` | zap JSON output to stdout; ingested by EKS log driver into CloudWatch |
| SIGUSR1 handler | In-process; produces heap profile file in `/tmp` emptyDir |
| Out-of-pod: SQS main queue | §3.1 |
| Out-of-pod: SQS DLQ | §3.2 |
| Out-of-pod: S3 staging bucket | §4.1 |
| Out-of-pod: DynamoDB table | §4.2 |
| Out-of-pod: KMS key (conditional) | §4.3 |
| Out-of-pod: IRSA role + SA | §2.4 + §6.1 |
| Out-of-pod: ConfigMap | §2.5 |
| Out-of-pod: K8s Service | §2.3 |
| Out-of-pod: HPA / KEDA | (platform team; chart README) |
| Out-of-pod: NetworkPolicy | §5.3 |
| Out-of-pod: Prometheus scrape | §7.2 |
| Out-of-pod: CloudWatch ingest | §7.1 |

---

## 12. Compliance Summary

- **SECURITY-01 (encryption)**: §4.1 (bucket SSE + non-TLS deny), §4.2 (DDB SSE), §3 (SQS SSE), §4.3 (KMS optional)
- **SECURITY-06 (least privilege)**: §2.4 (exact IAM actions, no wildcards)
- **SECURITY-07 (network restriction)**: §5.3 (egress allowlist), §5.2 (VPC endpoints)
- **SECURITY-09 (hardening)**: §2.1 (restricted PSS), §2.2 (distroless), §4.1 (bucket policy)
- **SECURITY-10 (supply chain)**: §2.2 (pinned digests, cosign signing), §8.1 (ECR scanning + immutability)
- **SECURITY-12 (credentials)**: §2.4 (IRSA, no static creds)
- **SECURITY-14 (alerting + log retention)**: §7.3 (alerts), §7.1 (≥ 90 d retention; no `DeleteLogStream` in IRSA)

**No blocking SECURITY findings at the Infrastructure Design stage.**
