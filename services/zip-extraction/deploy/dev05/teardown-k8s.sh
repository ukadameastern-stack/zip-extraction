#!/usr/bin/env bash
# Helm uninstall + namespace delete. Run BEFORE teardown-aws.sh so pods stop
# touching the AWS resources before they go away.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

verify_aws

HELM_BIN="${SERVICE_DIR}/bin/helm"
[ -x "${HELM_BIN}" ] || HELM_BIN="$(command -v helm 2>/dev/null || true)"

echo "==> Update kubeconfig for ${CLUSTER_NAME}"
aws ${AWS_CLI_ARGS} eks update-kubeconfig --name "${CLUSTER_NAME}" --alias "${CLUSTER_NAME}" >/dev/null

if [ -n "${HELM_BIN}" ] && "${HELM_BIN}" --kube-context "${CLUSTER_NAME}" -n "${NAMESPACE}" status "${HELM_RELEASE}" >/dev/null 2>&1; then
    echo "==> helm uninstall ${HELM_RELEASE}"
    "${HELM_BIN}" uninstall "${HELM_RELEASE}" --kube-context "${CLUSTER_NAME}" -n "${NAMESPACE}" --wait || true
else
    echo "==> helm release already absent (or helm missing)"
fi

if kubectl --context "${CLUSTER_NAME}" get namespace "${NAMESPACE}" >/dev/null 2>&1; then
    echo "==> Delete namespace: ${NAMESPACE}"
    kubectl --context "${CLUSTER_NAME}" delete namespace "${NAMESPACE}" --wait=true --timeout=2m || true
else
    echo "==> Namespace already absent: ${NAMESPACE}"
fi
