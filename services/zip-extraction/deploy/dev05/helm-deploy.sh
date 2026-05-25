#!/usr/bin/env bash
# Render and apply the helm chart against DEV05. Requires state.json (from
# bootstrap-aws.sh + push-image.sh) so we can pin to a real role ARN, queue URL,
# bucket, and image digest.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

verify_aws

if [ ! -f "${STATE_FILE}" ]; then
    echo "ERROR: ${STATE_FILE} missing — run bootstrap-aws.sh first." >&2
    exit 1
fi

ROLE_ARN="$(jq -r '.iam.roleArn' "${STATE_FILE}")"
QUEUE_URL="$(jq -r '.sqs.queueUrl' "${STATE_FILE}")"
STAGING_BUCKET="$(jq -r '.s3.stagingBucket' "${STATE_FILE}")"
DDB_TABLE="$(jq -r '.ddb.table' "${STATE_FILE}")"
IMG_TAG="$(jq -r '.image.tag // empty' "${STATE_FILE}")"
IMG_DIGEST="$(jq -r '.image.digest // empty' "${STATE_FILE}")"
HARNESS_TAG="$(jq -r '.harnessImage.tag // empty' "${STATE_FILE}")"
HARNESS_DIGEST="$(jq -r '.harnessImage.digest // empty' "${STATE_FILE}")"
SOURCE_BUCKET="$(jq -r '.s3.sourceBucket' "${STATE_FILE}")"
DLQ_URL="$(jq -r '.sqs.dlqUrl' "${STATE_FILE}")"

if [ -z "${IMG_TAG}" ] || [ -z "${IMG_DIGEST}" ]; then
    echo "ERROR: service image not pushed yet — run push-image.sh first." >&2
    exit 1
fi
if [ -z "${HARNESS_TAG}" ] || [ -z "${HARNESS_DIGEST}" ]; then
    echo "ERROR: harness image not pushed yet — run push-image.sh first." >&2
    exit 1
fi

# Pick helm: prefer local ./bin/helm, fall back to PATH.
HELM_BIN="${SERVICE_DIR}/bin/helm"
[ -x "${HELM_BIN}" ] || HELM_BIN="$(command -v helm)"
[ -n "${HELM_BIN}" ] || { echo "ERROR: helm not installed (run install-helm.sh)"; exit 1; }

echo "==> Update kubeconfig for ${CLUSTER_NAME}"
aws ${AWS_CLI_ARGS} eks update-kubeconfig --name "${CLUSTER_NAME}" --alias "${CLUSTER_NAME}" >/dev/null

echo "==> Ensure namespace: ${NAMESPACE}"
kubectl --context "${CLUSTER_NAME}" get namespace "${NAMESPACE}" >/dev/null 2>&1 \
    || kubectl --context "${CLUSTER_NAME}" create namespace "${NAMESPACE}"

echo "==> helm upgrade --install ${HELM_RELEASE} → ns/${NAMESPACE}"
"${HELM_BIN}" upgrade --install "${HELM_RELEASE}" "${SERVICE_DIR}/chart" \
    --kube-context "${CLUSTER_NAME}" \
    --namespace "${NAMESPACE}" \
    --create-namespace \
    -f "${SERVICE_DIR}/chart/values.yaml" \
    -f "${SERVICE_DIR}/chart/values-dev05.yaml" \
    --set "image.repository=${ECR_IMAGE_URI}" \
    --set "image.tag=${IMG_TAG}" \
    --set "image.digest=${IMG_DIGEST}" \
    --set "serviceAccount.roleArn=${ROLE_ARN}" \
    --set "infra.queueUrl=${QUEUE_URL}" \
    --set "infra.stagingBucket=${STAGING_BUCKET}" \
    --set "infra.dynamoTable=${DDB_TABLE}" \
    --set "harness.image.repository=${ECR_IMAGE_URI}" \
    --set "harness.image.tag=${HARNESS_TAG}" \
    --set "harness.image.digest=${HARNESS_DIGEST}" \
    --set "harness.sourceBucket=${SOURCE_BUCKET}" \
    --set "harness.dlqUrl=${DLQ_URL}" \
    --wait --timeout 5m

# Record the helm install in state.json.
tmp="$(mktemp)"
jq --arg ns "${NAMESPACE}" --arg rel "${HELM_RELEASE}" \
    '.k8s = { "namespace": $ns, "helmRelease": $rel }' \
    "${STATE_FILE}" > "${tmp}"
mv "${tmp}" "${STATE_FILE}"

echo ""
echo "==> Deploy complete. Inspect with:"
echo "    kubectl --context ${CLUSTER_NAME} -n ${NAMESPACE} get pods,svc,sa,cm"
echo "    kubectl --context ${CLUSTER_NAME} -n ${NAMESPACE} logs deploy/${HELM_RELEASE} -f"
