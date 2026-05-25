#!/usr/bin/env bash
# Reverse of bootstrap-aws.sh. Reads deploy/dev05/state.json and deletes only
# the resources recorded there. Refuses to run if state file is missing —
# this prevents accidentally deleting resources we don't own.
#
# Order matters:
#   1. SQS queues (drain in-flight first)
#   2. S3 buckets (empty + delete; including all object versions)
#   3. DynamoDB table
#   4. IAM role (detach inline policy then delete role)
#
# K8s teardown is handled separately by teardown-k8s.sh — call it BEFORE this
# script so pods stop using the role/queues/buckets cleanly.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

verify_aws

if [ ! -f "${STATE_FILE}" ]; then
    echo "ERROR: no state file at ${STATE_FILE}" >&2
    echo "       Nothing to tear down. If you believe DEV05 resources exist," >&2
    echo "       inspect manually with:  aws sqs list-queues --queue-name-prefix zip-extraction-dev05" >&2
    exit 1
fi

# Pull names from state file (jq required).
ROLE_NAME="$(jq -r '.iam.role' "${STATE_FILE}")"
POLICY_NAME="$(jq -r '.iam.policy' "${STATE_FILE}")"
QUEUE_URL="$(jq -r '.sqs.queueUrl' "${STATE_FILE}")"
DLQ_URL="$(jq -r '.sqs.dlqUrl' "${STATE_FILE}")"
STAGING_BUCKET="$(jq -r '.s3.stagingBucket' "${STATE_FILE}")"
SOURCE_BUCKET="$(jq -r '.s3.sourceBucket' "${STATE_FILE}")"
DDB_TABLE="$(jq -r '.ddb.table' "${STATE_FILE}")"

echo "==> DEV05 teardown | account=${ACCOUNT_ID} region=${AWS_REGION}"
echo "    Resources from state file:"
echo "      role:           ${ROLE_NAME}"
echo "      queue:          ${QUEUE_URL}"
echo "      dlq:            ${DLQ_URL}"
echo "      staging bucket: s3://${STAGING_BUCKET}"
echo "      source bucket:  s3://${SOURCE_BUCKET}"
echo "      ddb table:      ${DDB_TABLE}"
echo ""

# ----- SQS: delete main first (no more redrive), then DLQ -----
for url in "${QUEUE_URL}" "${DLQ_URL}"; do
    if aws ${AWS_CLI_ARGS} sqs get-queue-attributes --queue-url "${url}" --attribute-names QueueArn >/dev/null 2>&1; then
        echo "==> Delete SQS: ${url}"
        aws ${AWS_CLI_ARGS} sqs delete-queue --queue-url "${url}"
    else
        echo "==> SQS already absent: ${url}"
    fi
done

# ----- S3: empty buckets (incl. object versions + delete markers), then delete -----
empty_and_delete_bucket() {
    local b="$1"
    if ! aws ${AWS_CLI_ARGS} s3api head-bucket --bucket "${b}" 2>/dev/null; then
        echo "==> S3 bucket already absent: ${b}"
        return
    fi
    echo "==> Empty S3 bucket: ${b}"
    # Plain objects.
    aws ${AWS_CLI_ARGS} s3 rm "s3://${b}" --recursive >/dev/null || true
    # Versioned objects + delete markers (only present if versioning ever enabled).
    local payload
    payload=$(aws ${AWS_CLI_ARGS} s3api list-object-versions --bucket "${b}" \
        --query '{Objects: Versions[].{Key:Key,VersionId:VersionId}, Quiet: `true`}' 2>/dev/null || echo '{"Objects":null}')
    if [ "$(echo "$payload" | jq '.Objects | length // 0')" -gt 0 ]; then
        aws ${AWS_CLI_ARGS} s3api delete-objects --bucket "${b}" --delete "${payload}" >/dev/null || true
    fi
    local markers
    markers=$(aws ${AWS_CLI_ARGS} s3api list-object-versions --bucket "${b}" \
        --query '{Objects: DeleteMarkers[].{Key:Key,VersionId:VersionId}, Quiet: `true`}' 2>/dev/null || echo '{"Objects":null}')
    if [ "$(echo "$markers" | jq '.Objects | length // 0')" -gt 0 ]; then
        aws ${AWS_CLI_ARGS} s3api delete-objects --bucket "${b}" --delete "${markers}" >/dev/null || true
    fi
    echo "==> Delete S3 bucket: ${b}"
    aws ${AWS_CLI_ARGS} s3api delete-bucket --bucket "${b}"
}
empty_and_delete_bucket "${STAGING_BUCKET}"
empty_and_delete_bucket "${SOURCE_BUCKET}"

# ----- DynamoDB -----
if aws ${AWS_CLI_ARGS} dynamodb describe-table --table-name "${DDB_TABLE}" >/dev/null 2>&1; then
    echo "==> Delete DDB table: ${DDB_TABLE}"
    aws ${AWS_CLI_ARGS} dynamodb delete-table --table-name "${DDB_TABLE}" >/dev/null
    aws ${AWS_CLI_ARGS} dynamodb wait table-not-exists --table-name "${DDB_TABLE}" || true
else
    echo "==> DDB table already absent: ${DDB_TABLE}"
fi

# ----- IAM role + inline policy -----
if aws ${AWS_CLI_ARGS} iam get-role --role-name "${ROLE_NAME}" >/dev/null 2>&1; then
    echo "==> Detach inline policy: ${POLICY_NAME}"
    aws ${AWS_CLI_ARGS} iam delete-role-policy \
        --role-name "${ROLE_NAME}" --policy-name "${POLICY_NAME}" 2>/dev/null || true
    echo "==> Delete IAM role: ${ROLE_NAME}"
    aws ${AWS_CLI_ARGS} iam delete-role --role-name "${ROLE_NAME}"
else
    echo "==> IAM role already absent: ${ROLE_NAME}"
fi

# Archive the state file rather than delete it — gives an audit trail.
ts="$(date -u +%Y%m%dT%H%M%SZ)"
archived="${STATE_FILE%.json}.${ts}.completed.json"
mv "${STATE_FILE}" "${archived}"
echo ""
echo "==> Teardown complete. State archived to: ${archived}"
echo "    ECR image (tag dev05-*) was NOT removed — repo is shared. Use deploy/dev05/prune-ecr.sh if you want to."
