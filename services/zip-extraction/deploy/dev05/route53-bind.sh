#!/usr/bin/env bash
# Wait for the harness Ingress's ALB to be provisioned, then UPSERT a Route 53
# A-alias record at the configured hostname. Idempotent — re-running just no-ops
# if the record already points at the same ALB.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

verify_aws

if [ ! -f "${STATE_FILE}" ]; then
    echo "ERROR: ${STATE_FILE} missing — run helm-deploy.sh first." >&2; exit 1
fi

HOSTNAME="$(grep -E 'host:' "${SERVICE_DIR}/chart/values-dev05.yaml" | head -1 | awk -F'"' '{print $2}')"
[ -n "${HOSTNAME}" ] || { echo "ERROR: harness ingress.host not set in values-dev05.yaml" >&2; exit 1; }

# Derive the parent hosted zone (strip the leading "host" label).
ZONE_NAME="${HOSTNAME#*.}."
echo "==> Look up Route 53 hosted zone: ${ZONE_NAME}"
ZONE_ID="$(aws ${AWS_CLI_ARGS} route53 list-hosted-zones-by-name --dns-name "${ZONE_NAME}" \
    --query 'HostedZones[?Name==`'"${ZONE_NAME}"'`].Id' --output text | sed 's|/hostedzone/||')"
[ -n "${ZONE_ID}" ] || { echo "ERROR: no Route 53 zone found for ${ZONE_NAME}" >&2; exit 1; }
echo "    zoneId=${ZONE_ID}"

echo "==> Wait for harness Ingress ALB to be provisioned (up to 3 min)"
ALB_DNS=""
for i in $(seq 1 36); do
    ALB_DNS="$(kubectl --context "${CLUSTER_NAME}" -n "${NAMESPACE}" \
        get ingress "${HELM_RELEASE}-harness" \
        -o jsonpath='{.status.loadBalancer.ingress[0].hostname}' 2>/dev/null || true)"
    [ -n "${ALB_DNS}" ] && break
    sleep 5
done
[ -n "${ALB_DNS}" ] || { echo "ERROR: ALB never appeared on ingress" >&2; exit 1; }
echo "    alb=${ALB_DNS}"

# ALB Canonical Hosted Zone IDs by region — published by AWS.
# eu-west-1: Z32O12XQLNTSW2
case "${AWS_REGION}" in
    eu-west-1) ALB_HOSTED_ZONE_ID="Z32O12XQLNTSW2" ;;
    *) echo "ERROR: unknown ALB hosted zone for region ${AWS_REGION}" >&2; exit 1 ;;
esac

CHANGE_BATCH=$(cat <<EOF
{
  "Comment": "zip-extraction DEV05 harness ingress",
  "Changes": [{
    "Action": "UPSERT",
    "ResourceRecordSet": {
      "Name": "${HOSTNAME}",
      "Type": "A",
      "AliasTarget": {
        "HostedZoneId": "${ALB_HOSTED_ZONE_ID}",
        "DNSName": "${ALB_DNS}",
        "EvaluateTargetHealth": false
      }
    }
  }]
}
EOF
)
echo "==> Route 53 UPSERT: ${HOSTNAME} → ${ALB_DNS}"
CHANGE_ID="$(aws ${AWS_CLI_ARGS} route53 change-resource-record-sets \
    --hosted-zone-id "${ZONE_ID}" \
    --change-batch "${CHANGE_BATCH}" \
    --query 'ChangeInfo.Id' --output text)"
echo "    changeId=${CHANGE_ID}"

# Record everything needed for teardown.
tmp="$(mktemp)"
jq --arg z "${ZONE_ID}" --arg h "${HOSTNAME}" --arg a "${ALB_DNS}" --arg az "${ALB_HOSTED_ZONE_ID}" --arg c "${CHANGE_ID}" \
    '.route53 = { "hostedZoneId": $z, "hostname": $h, "albDns": $a, "albHostedZoneId": $az, "lastChangeId": $c }' \
    "${STATE_FILE}" > "${tmp}"
mv "${tmp}" "${STATE_FILE}"

echo ""
echo "==> Bound. Harness URL: http://${HOSTNAME}/"
echo "    (DNS propagation may take 30–60s)"
