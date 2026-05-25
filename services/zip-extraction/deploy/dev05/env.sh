#!/usr/bin/env bash
# Single source of truth for DEV05 resource names. All deploy/teardown scripts
# source this file. Every resource is DEV05-prefixed so teardown is safe even
# when other tenants share the account.
set -euo pipefail

: "${AWS_PROFILE:=opus2-dev}"
: "${AWS_REGION:=eu-west-1}"
: "${ACCOUNT_ID:=537462380503}"
: "${CLUSTER_NAME:=DEV05-EKS-CLUSTER}"

# Resource names — DEV05-namespaced.
export ENV_TAG="dev05"
export NAMESPACE="zip-extraction-dev05"
export HELM_RELEASE="zip-extraction-dev05"
export IAM_ROLE_NAME="zip-extraction-dev05"
export IAM_POLICY_NAME="zip-extraction-dev05-inline"
export SQS_QUEUE_NAME="zip-extraction-dev05"
export SQS_DLQ_NAME="zip-extraction-dev05-dlq"
export S3_STAGING_BUCKET="zip-extraction-dev05-staging-${ACCOUNT_ID}-${AWS_REGION}"
export S3_SOURCE_BUCKET="zip-extraction-dev05-uploads-${ACCOUNT_ID}-${AWS_REGION}"
export DDB_TABLE_NAME="zip-extraction-dev05-pipeline_files"
export ECR_REPO="doc-uploader-sandbox/zip-extraction-service"
export ECR_IMAGE_URI="${ACCOUNT_ID}.dkr.ecr.${AWS_REGION}.amazonaws.com/${ECR_REPO}"
export K8S_SERVICE_ACCOUNT="zip-extraction"  # matches chart/values.yaml serviceAccount.name

# Derived endpoints used by the chart values overlay.
export SQS_QUEUE_URL="https://sqs.${AWS_REGION}.amazonaws.com/${ACCOUNT_ID}/${SQS_QUEUE_NAME}"

# Paths.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export SCRIPT_DIR
export STATE_FILE="${SCRIPT_DIR}/state.json"
export SERVICE_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"

AWS=(aws --profile "$AWS_PROFILE" --region "$AWS_REGION")
export AWS_CLI_ARGS="--profile ${AWS_PROFILE} --region ${AWS_REGION}"

verify_aws() {
    if ! "${AWS[@]}" sts get-caller-identity --query 'Account' --output text >/dev/null 2>&1; then
        echo "ERROR: AWS profile '${AWS_PROFILE}' not authenticated." >&2
        echo "       Run: aws sso login --profile ${AWS_PROFILE}" >&2
        exit 1
    fi
    local got
    got="$("${AWS[@]}" sts get-caller-identity --query 'Account' --output text)"
    if [ "$got" != "$ACCOUNT_ID" ]; then
        echo "ERROR: profile points at account ${got}, expected ${ACCOUNT_ID}." >&2
        exit 1
    fi
}

state_get() {
    # state_get <key>  — reads a string field from state.json (jq-style path).
    local key="$1"
    [ -f "$STATE_FILE" ] || { echo ""; return; }
    jq -r --arg k "$key" 'getpath($k | split("."))' "$STATE_FILE" 2>/dev/null
}
