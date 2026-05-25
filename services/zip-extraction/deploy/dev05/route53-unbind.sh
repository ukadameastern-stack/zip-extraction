#!/usr/bin/env bash
# Delete the Route 53 record created by route53-bind.sh.
# Reads everything from state.json so we only ever delete what we own.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

verify_aws

if [ ! -f "${STATE_FILE}" ]; then
    echo "(no state file — nothing to unbind)"; exit 0
fi

ZONE_ID="$(jq -r '.route53.hostedZoneId // empty' "${STATE_FILE}")"
HOSTNAME="$(jq -r '.route53.hostname // empty' "${STATE_FILE}")"
ALB_DNS="$(jq -r '.route53.albDns // empty' "${STATE_FILE}")"
ALB_HZ="$(jq -r '.route53.albHostedZoneId // empty' "${STATE_FILE}")"

if [ -z "${ZONE_ID}" ] || [ -z "${HOSTNAME}" ]; then
    echo "(no route53 record recorded in state — skipping)"; exit 0
fi

# Confirm record still exists with the same alias target before deleting.
CURRENT="$(aws ${AWS_CLI_ARGS} route53 list-resource-record-sets \
    --hosted-zone-id "${ZONE_ID}" \
    --query "ResourceRecordSets[?Name=='${HOSTNAME}.' && Type=='A'] | [0].AliasTarget.DNSName" \
    --output text 2>/dev/null || echo "")"

if [ -z "${CURRENT}" ] || [ "${CURRENT}" = "None" ]; then
    echo "==> Route 53 record already absent: ${HOSTNAME}"
else
    CHANGE_BATCH=$(cat <<EOF
{
  "Changes": [{
    "Action": "DELETE",
    "ResourceRecordSet": {
      "Name": "${HOSTNAME}",
      "Type": "A",
      "AliasTarget": {
        "HostedZoneId": "${ALB_HZ}",
        "DNSName": "${ALB_DNS}",
        "EvaluateTargetHealth": false
      }
    }
  }]
}
EOF
    )
    echo "==> Route 53 DELETE: ${HOSTNAME}"
    aws ${AWS_CLI_ARGS} route53 change-resource-record-sets \
        --hosted-zone-id "${ZONE_ID}" \
        --change-batch "${CHANGE_BATCH}" >/dev/null
fi
