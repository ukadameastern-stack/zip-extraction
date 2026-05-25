# DEV05 deploy — service inventory

What `make deploy-dev05` creates and what `make undeploy-dev05` removes.
The **authoritative live record** is `deploy/dev05/state.json` — written by bootstrap, read by teardown.
This file is the **design / contract**; state.json is **runtime truth**.

## Target environment

| | |
|---|---|
| AWS account | `537462380503` |
| AWS region  | `eu-west-1` |
| AWS profile | `opus2-dev` (SSO via `opus2` session) |
| EKS cluster | `DEV05-EKS-CLUSTER` (v1.35) |
| OIDC issuer | `oidc.eks.eu-west-1.amazonaws.com/id/4CD18ACA973AEF3E3D289F4092A757EA` |
| ECR repo    | `doc-uploader-sandbox/zip-extraction-service` (re-used; not created/deleted) |

## Resources created by `make deploy-dev05`

Every resource is prefixed `zip-extraction-dev05*` so teardown is safe even with other tenants on the account.

### AWS

| Kind | Name | Notes |
|---|---|---|
| SQS queue       | `zip-extraction-dev05`                                            | redrive policy → DLQ, maxReceiveCount=3, visibilityTimeout=300s |
| SQS queue (DLQ) | `zip-extraction-dev05-dlq`                                        | |
| S3 bucket       | `zip-extraction-dev05-staging-537462380503-eu-west-1`             | SSE-S3 (AES256), public access blocked |
| S3 bucket       | `zip-extraction-dev05-uploads-537462380503-eu-west-1`             | source bucket — where ZIPs are uploaded |
| DynamoDB table  | `zip-extraction-dev05-pipeline_files`                             | pk=String, sk=String, PAY_PER_REQUEST |
| IAM role        | `zip-extraction-dev05`                                            | IRSA trust: OIDC sub = `system:serviceaccount:zip-extraction-dev05:zip-extraction` |
| IAM policy      | `zip-extraction-dev05-inline` (inline on the role)                | SQS read/delete on queue+DLQ, S3 R/W on both buckets, DDB CRUD on the table |
| ECR image tag   | `537462380503.dkr.ecr.eu-west-1.amazonaws.com/doc-uploader-sandbox/zip-extraction-service:dev05-<git-sha>` | tag NOT removed on teardown (repo is shared) |

### Kubernetes (in `ns/zip-extraction-dev05`)

Helm release `zip-extraction-dev05` rendered from `chart/` with overlay `chart/values-dev05.yaml`. Templates emit:

| Kind | Name |
|---|---|
| Namespace                    | `zip-extraction-dev05` |
| Deployment                   | `zip-extraction-dev05` (replicaCount=1 for DEV05) |
| Service (ClusterIP)          | `zip-extraction-dev05` (port 8080) |
| ServiceAccount               | `zip-extraction` (annotated with the IAM role ARN for IRSA) |
| ConfigMap                    | `zip-extraction-dev05` (bombDefence / streaming / retry / sqs tunables) |

## Targets

```
make deploy-dev05       # full bootstrap: install helm + AWS resources + image push + helm install
make undeploy-dev05     # reverse, in safe order: helm uninstall → ns delete → SQS/S3/DDB/IAM
make list-dev05         # show what's deployed (state.json + live AWS/K8s checks)
```

Sub-targets (called by the above, can be run individually):

```
make dev05-helm-install   # install helm v3 to ./bin/helm if missing
make dev05-bootstrap      # AWS resources only (writes state.json)
make dev05-push           # docker build + push, captures image digest into state.json
make dev05-helm-deploy    # helm upgrade --install (requires state.json with image+iam already populated)
make dev05-teardown-k8s   # helm uninstall + delete namespace (call BEFORE teardown-aws)
make dev05-teardown-aws   # delete AWS resources listed in state.json
```

## Logs

Every `make deploy-dev05` / `make undeploy-dev05` run writes its full output to:

```
deploy/dev05/logs/deploy-<UTC timestamp>.log
deploy/dev05/logs/undeploy-<UTC timestamp>.log
```

with `deploy/dev05/logs/latest-deploy.log` and `latest-undeploy.log` as symlinks to the most recent.

## State file lifecycle

```
make deploy-dev05    →  writes deploy/dev05/state.json
make undeploy-dev05  →  reads state.json; on success archives to deploy/dev05/state.<ts>.completed.json
```

Teardown **refuses to run without state.json**. If you lose it, recover by hand:

```bash
aws --profile opus2-dev sqs list-queues --queue-name-prefix zip-extraction-dev05
aws --profile opus2-dev s3 ls | grep zip-extraction-dev05
aws --profile opus2-dev dynamodb list-tables | grep zip-extraction-dev05
aws --profile opus2-dev iam list-roles --query 'Roles[?contains(RoleName,`zip-extraction-dev05`)]'
```

## Re-using vs. recreating ECR

The deploy reuses the existing repository `doc-uploader-sandbox/zip-extraction-service` rather than creating a per-env repo, but tags each image `dev05-<git-sha>` so multiple commits coexist without overlap. Teardown does **not** delete tags (other developers may have running pods pinned to the digest). Use the AWS console or `aws ecr batch-delete-image` if you want to prune.
