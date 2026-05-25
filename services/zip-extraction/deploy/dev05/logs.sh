#!/usr/bin/env bash
# Print every DEV05-related log source into one stream:
#   1. The N most recent deploy/undeploy logs from deploy/dev05/logs/
#   2. K8s events in the namespace (most recent first)
#   3. Pod logs for the main service
#   4. Pod logs for the harness
#
# Tail length controlled by DEV05_LOG_LINES (default 100). Use FOLLOW=1 to
# stream pod logs (will block until Ctrl-C; deploy logs + events are still
# one-shot in that mode).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

LINES="${DEV05_LOG_LINES:-100}"
FOLLOW="${FOLLOW:-0}"

hr() { printf '═%.0s' {1..72}; echo; }
section() { echo; hr; echo "  $*"; hr; }

# ----- 1. Deploy/undeploy logs -----
section "DEPLOY / UNDEPLOY LOGS (deploy/dev05/logs/) — last 3 files, ${LINES} lines each"
if compgen -G "${SCRIPT_DIR}/logs/*.log" >/dev/null; then
    # shellcheck disable=SC2012
    for f in $(ls -1t "${SCRIPT_DIR}"/logs/*.log 2>/dev/null | head -3); do
        echo
        echo "── $(basename "$f") ──"
        tail -n "${LINES}" "$f"
    done
else
    echo "(no deploy/undeploy logs yet)"
fi

# Make sure kubeconfig is wired before we run kubectl.
aws ${AWS_CLI_ARGS} eks update-kubeconfig --name "${CLUSTER_NAME}" --alias "${CLUSTER_NAME}" >/dev/null 2>&1 || true

# ----- 2. K8s events -----
section "KUBERNETES EVENTS — ns/${NAMESPACE} (oldest → newest)"
kubectl --context "${CLUSTER_NAME}" -n "${NAMESPACE}" get events \
    --sort-by=.lastTimestamp 2>&1 || echo "(namespace not present)"

# ----- 3 + 4. Pod logs -----
if [ "${FOLLOW}" = "1" ]; then
    section "POD LOGS — streaming both deployments (Ctrl-C to stop)"
    # Use a label selector so all pods (service + harness) come together,
    # prefixed by pod name for clarity. `-f --max-log-requests` keeps a
    # single kubectl process; works for up to ~10 pods comfortably.
    exec kubectl --context "${CLUSTER_NAME}" -n "${NAMESPACE}" \
        logs -f --prefix --timestamps --max-log-requests 10 \
        --selector app.kubernetes.io/instance="${HELM_RELEASE}" \
        --tail="${LINES}"
fi

section "POD LOGS — main service (deploy/${HELM_RELEASE}) — last ${LINES} lines"
kubectl --context "${CLUSTER_NAME}" -n "${NAMESPACE}" \
    logs "deploy/${HELM_RELEASE}" --tail="${LINES}" --timestamps 2>&1 || echo "(no service pod)"

section "POD LOGS — harness (deploy/${HELM_RELEASE}-harness) — last ${LINES} lines"
kubectl --context "${CLUSTER_NAME}" -n "${NAMESPACE}" \
    logs "deploy/${HELM_RELEASE}-harness" --tail="${LINES}" --timestamps 2>&1 || echo "(no harness pod)"

echo
hr
echo "  Tips:"
echo "    DEV05_LOG_LINES=500 make logs-dev05   # longer tail"
echo "    FOLLOW=1 make logs-dev05              # stream pod logs (Ctrl-C stops)"
hr
