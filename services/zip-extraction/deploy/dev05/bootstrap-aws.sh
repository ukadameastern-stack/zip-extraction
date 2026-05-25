#!/usr/bin/env bash
# Bootstrap DEV05 AWS resources: SQS (queue + DLQ), S3 (staging + source),
# DynamoDB, IAM role (IRSA-trusted by the DEV05 EKS OIDC provider).
#
# Idempotent — re-running is a no-op.
# State written to deploy/dev05/state.json so teardown-aws.sh knows what to delete.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

verify_aws

echo "==> DEV05 bootstrap | account=${ACCOUNT_ID} region=${AWS_REGION} cluster=${CLUSTER_NAME}"

# ----- OIDC issuer for the cluster (needed for IRSA trust policy) -----
echo "==> Look up DEV05 cluster OIDC issuer"
OIDC_ISSUER="$(aws ${AWS_CLI_ARGS} eks describe-cluster --name "${CLUSTER_NAME}" \
    --query 'cluster.identity.oidc.issuer' --output text)"
OIDC_HOST="${OIDC_ISSUER#https://}"
OIDC_PROVIDER_ARN="arn:aws:iam::${ACCOUNT_ID}:oidc-provider/${OIDC_HOST}"
echo "    oidc=${OIDC_HOST}"

# ----- S3 source + staging buckets -----
for bucket in "${S3_SOURCE_BUCKET}" "${S3_STAGING_BUCKET}"; do
    if aws ${AWS_CLI_ARGS} s3api head-bucket --bucket "${bucket}" 2>/dev/null; then
        echo "==> S3 bucket exists: ${bucket}"
    else
        echo "==> Create S3 bucket: ${bucket}"
        aws ${AWS_CLI_ARGS} s3api create-bucket \
            --bucket "${bucket}" \
            --create-bucket-configuration "LocationConstraint=${AWS_REGION}" >/dev/null
        aws ${AWS_CLI_ARGS} s3api put-bucket-encryption \
            --bucket "${bucket}" \
            --server-side-encryption-configuration \
              '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'
        aws ${AWS_CLI_ARGS} s3api put-public-access-block \
            --bucket "${bucket}" \
            --public-access-block-configuration \
              'BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true'
    fi
done

# ----- SQS DLQ + main queue with redrive policy -----
echo "==> SQS DLQ: ${SQS_DLQ_NAME}"
DLQ_URL="$(aws ${AWS_CLI_ARGS} sqs create-queue --queue-name "${SQS_DLQ_NAME}" \
    --query 'QueueUrl' --output text)"
DLQ_ARN="$(aws ${AWS_CLI_ARGS} sqs get-queue-attributes --queue-url "${DLQ_URL}" \
    --attribute-names QueueArn --query 'Attributes.QueueArn' --output text)"

echo "==> SQS main queue: ${SQS_QUEUE_NAME} (redrive→${SQS_DLQ_NAME}, maxReceiveCount=3)"
REDRIVE="{\"deadLetterTargetArn\":\"${DLQ_ARN}\",\"maxReceiveCount\":\"3\"}"
QUEUE_URL="$(aws ${AWS_CLI_ARGS} sqs create-queue --queue-name "${SQS_QUEUE_NAME}" \
    --attributes "{\"RedrivePolicy\":\"$(printf '%s' "$REDRIVE" | sed 's/"/\\"/g')\",\"VisibilityTimeout\":\"300\"}" \
    --query 'QueueUrl' --output text)"
QUEUE_ARN="$(aws ${AWS_CLI_ARGS} sqs get-queue-attributes --queue-url "${QUEUE_URL}" \
    --attribute-names QueueArn --query 'Attributes.QueueArn' --output text)"

# ----- DynamoDB table -----
if aws ${AWS_CLI_ARGS} dynamodb describe-table --table-name "${DDB_TABLE_NAME}" >/dev/null 2>&1; then
    echo "==> DDB table exists: ${DDB_TABLE_NAME}"
else
    echo "==> Create DDB table: ${DDB_TABLE_NAME}"
    aws ${AWS_CLI_ARGS} dynamodb create-table \
        --table-name "${DDB_TABLE_NAME}" \
        --attribute-definitions AttributeName=pk,AttributeType=S AttributeName=sk,AttributeType=S \
        --key-schema AttributeName=pk,KeyType=HASH AttributeName=sk,KeyType=RANGE \
        --billing-mode PAY_PER_REQUEST >/dev/null
    aws ${AWS_CLI_ARGS} dynamodb wait table-exists --table-name "${DDB_TABLE_NAME}"
fi

# ----- IAM role for IRSA -----
TRUST_POLICY=$(cat <<EOF
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": { "Federated": "${OIDC_PROVIDER_ARN}" },
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringEquals": {
        "${OIDC_HOST}:sub": "system:serviceaccount:${NAMESPACE}:${K8S_SERVICE_ACCOUNT}",
        "${OIDC_HOST}:aud": "sts.amazonaws.com"
      }
    }
  }]
}
EOF
)

if aws ${AWS_CLI_ARGS} iam get-role --role-name "${IAM_ROLE_NAME}" >/dev/null 2>&1; then
    echo "==> IAM role exists: ${IAM_ROLE_NAME} (refreshing trust policy)"
    aws ${AWS_CLI_ARGS} iam update-assume-role-policy \
        --role-name "${IAM_ROLE_NAME}" \
        --policy-document "${TRUST_POLICY}" >/dev/null
else
    echo "==> Create IAM role: ${IAM_ROLE_NAME}"
    aws ${AWS_CLI_ARGS} iam create-role \
        --role-name "${IAM_ROLE_NAME}" \
        --assume-role-policy-document "${TRUST_POLICY}" \
        --description "IRSA role for zip-extraction on ${CLUSTER_NAME}" >/dev/null
fi

INLINE_POLICY=$(cat <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    { "Sid": "SQSReadMain", "Effect": "Allow",
      "Action": ["sqs:ReceiveMessage","sqs:DeleteMessage","sqs:ChangeMessageVisibility","sqs:GetQueueAttributes","sqs:SendMessage"],
      "Resource": ["${QUEUE_ARN}","${DLQ_ARN}"] },
    { "Sid": "S3RW", "Effect": "Allow",
      "Action": ["s3:GetObject","s3:PutObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],
      "Resource": ["arn:aws:s3:::${S3_SOURCE_BUCKET}/*","arn:aws:s3:::${S3_STAGING_BUCKET}/*"] },
    { "Sid": "S3List", "Effect": "Allow",
      "Action": ["s3:ListBucket"],
      "Resource": ["arn:aws:s3:::${S3_SOURCE_BUCKET}","arn:aws:s3:::${S3_STAGING_BUCKET}"] },
    { "Sid": "DDB", "Effect": "Allow",
      "Action": ["dynamodb:PutItem","dynamodb:GetItem","dynamodb:UpdateItem","dynamodb:DeleteItem","dynamodb:Query"],
      "Resource": "arn:aws:dynamodb:${AWS_REGION}:${ACCOUNT_ID}:table/${DDB_TABLE_NAME}" }
  ]
}
EOF
)

echo "==> Attach inline policy: ${IAM_POLICY_NAME}"
aws ${AWS_CLI_ARGS} iam put-role-policy \
    --role-name "${IAM_ROLE_NAME}" \
    --policy-name "${IAM_POLICY_NAME}" \
    --policy-document "${INLINE_POLICY}"

ROLE_ARN="arn:aws:iam::${ACCOUNT_ID}:role/${IAM_ROLE_NAME}"

# ----- Write state file (authoritative record for teardown) -----
mkdir -p "${SCRIPT_DIR}"
cat > "${STATE_FILE}" <<EOF
{
  "deployedAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "account": "${ACCOUNT_ID}",
  "region": "${AWS_REGION}",
  "cluster": "${CLUSTER_NAME}",
  "namespace": "${NAMESPACE}",
  "helmRelease": "${HELM_RELEASE}",
  "iam": { "role": "${IAM_ROLE_NAME}", "policy": "${IAM_POLICY_NAME}", "roleArn": "${ROLE_ARN}" },
  "sqs": { "queueArn": "${QUEUE_ARN}", "queueUrl": "${QUEUE_URL}", "dlqArn": "${DLQ_ARN}", "dlqUrl": "${DLQ_URL}" },
  "s3": { "stagingBucket": "${S3_STAGING_BUCKET}", "sourceBucket": "${S3_SOURCE_BUCKET}" },
  "ddb": { "table": "${DDB_TABLE_NAME}" },
  "ecr": { "repo": "${ECR_REPO}", "imageUri": "${ECR_IMAGE_URI}" }
}
EOF

echo ""
echo "==> Bootstrap complete. State: ${STATE_FILE}"
echo "    Role ARN:       ${ROLE_ARN}"
echo "    Queue URL:      ${QUEUE_URL}"
echo "    Staging bucket: s3://${S3_STAGING_BUCKET}"
echo "    Source bucket:  s3://${S3_SOURCE_BUCKET}"
echo "    DDB table:      ${DDB_TABLE_NAME}"
