#!/usr/bin/env bash
# Print everything DEV05 has, side by side from state file + live AWS/K8s.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

verify_aws

if [ ! -f "${STATE_FILE}" ]; then
    echo "(no active state — has anything been deployed?)"
    echo "Checked: ${STATE_FILE}"
    echo ""
    echo "Archived states:"
    ls -1 "${SCRIPT_DIR}"/state.*.completed.json 2>/dev/null || echo "  (none)"
    exit 0
fi

echo "=========================================="
echo "  DEV05 STATE (from state.json)"
echo "=========================================="
jq . "${STATE_FILE}"
echo ""

HELM_BIN="${SERVICE_DIR}/bin/helm"
[ -x "${HELM_BIN}" ] || HELM_BIN="$(command -v helm 2>/dev/null || true)"

aws ${AWS_CLI_ARGS} eks update-kubeconfig --name "${CLUSTER_NAME}" --alias "${CLUSTER_NAME}" >/dev/null 2>&1 || true

echo "=========================================="
echo "  K8s resources in ns/${NAMESPACE}"
echo "=========================================="
kubectl --context "${CLUSTER_NAME}" -n "${NAMESPACE}" get all,configmap,serviceaccount 2>&1 || echo "(namespace missing)"
echo ""

echo "=========================================="
echo "  AWS resources (live check)"
echo "=========================================="
QUEUE_URL="$(jq -r '.sqs.queueUrl' "${STATE_FILE}")"
DLQ_URL="$(jq -r '.sqs.dlqUrl' "${STATE_FILE}")"
STAGING="$(jq -r '.s3.stagingBucket' "${STATE_FILE}")"
SOURCE="$(jq -r '.s3.sourceBucket' "${STATE_FILE}")"
TABLE="$(jq -r '.ddb.table' "${STATE_FILE}")"
ROLE="$(jq -r '.iam.role' "${STATE_FILE}")"

printf "  SQS main:    "; aws ${AWS_CLI_ARGS} sqs get-queue-attributes --queue-url "${QUEUE_URL}" \
    --attribute-names ApproximateNumberOfMessages \
    --query 'Attributes.ApproximateNumberOfMessages' --output text 2>/dev/null \
    && echo " messages visible" || echo "(missing)"
printf "  SQS DLQ:     "; aws ${AWS_CLI_ARGS} sqs get-queue-attributes --queue-url "${DLQ_URL}" \
    --attribute-names ApproximateNumberOfMessages \
    --query 'Attributes.ApproximateNumberOfMessages' --output text 2>/dev/null \
    && echo " messages in DLQ" || echo "(missing)"
printf "  S3 staging:  "; aws ${AWS_CLI_ARGS} s3api head-bucket --bucket "${STAGING}" 2>/dev/null && echo "ok" || echo "(missing)"
printf "  S3 source:   "; aws ${AWS_CLI_ARGS} s3api head-bucket --bucket "${SOURCE}" 2>/dev/null && echo "ok" || echo "(missing)"
printf "  DDB table:   "; aws ${AWS_CLI_ARGS} dynamodb describe-table --table-name "${TABLE}" \
    --query 'Table.TableStatus' --output text 2>/dev/null || echo "(missing)"
printf "  IAM role:    "; aws ${AWS_CLI_ARGS} iam get-role --role-name "${ROLE}" \
    --query 'Role.Arn' --output text 2>/dev/null || echo "(missing)"

echo ""
echo "=========================================="
echo "  Cross-namespace dependencies"
echo "=========================================="
# Classification service — cross-namespace HTTP dependency. Not AWS; not in
# state.json. We probe via the Service DNS the chart configures.
CLASS_SVC="classification-ui.classification-service-sandbox.svc.cluster.local"
printf "  classification ns: "
if kubectl --context "${CLUSTER_NAME}" get ns classification-service-sandbox >/dev/null 2>&1; then
    echo "exists"
    printf "  classification pod ready: "
    kubectl --context "${CLUSTER_NAME}" -n classification-service-sandbox get pods \
        -l app=classification-ui -o jsonpath='{.items[*].status.containerStatuses[0].ready}' 2>/dev/null \
        || echo "(no pods)"
    echo ""
    printf "  endpoint:           "
    echo "http://${CLASS_SVC}/api/classify (configured in chart/values-dev05.yaml)"
else
    echo "(missing — classification team must deploy)"
fi
