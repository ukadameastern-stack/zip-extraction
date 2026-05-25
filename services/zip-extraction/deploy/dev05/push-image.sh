#!/usr/bin/env bash
# Build, tag, and push the zip-extraction image to ECR.
# Tag format: dev05-<git-short-sha>  (immutable per commit; uniquely identifies what was deployed).
# Captures the pushed digest and writes it back into state.json so the Helm overlay can pin to sha256:....
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

verify_aws

GIT_SHA="$(cd "${SERVICE_DIR}" && git rev-parse --short HEAD 2>/dev/null || echo "untracked-$(date -u +%Y%m%d%H%M%S)")"
TAG="dev05-${GIT_SHA}"
FULL_IMG="${ECR_IMAGE_URI}:${TAG}"

# Use a temporary DOCKER_CONFIG so we don't depend on whatever credential
# helper the user's ~/.docker/config.json points at (e.g. desktop→pass on
# Linux, which is a common breakage when pass isn't initialised).
export DOCKER_CONFIG="$(mktemp -d)"
trap 'rm -rf "${DOCKER_CONFIG}"' EXIT
echo '{}' > "${DOCKER_CONFIG}/config.json"

echo "==> ECR login (${AWS_REGION}) [isolated DOCKER_CONFIG=${DOCKER_CONFIG}]"
aws ${AWS_CLI_ARGS} ecr get-login-password \
    | docker login --username AWS --password-stdin "${ACCOUNT_ID}.dkr.ecr.${AWS_REGION}.amazonaws.com"

echo "==> Build image: ${FULL_IMG}"
docker buildx build \
    --platform linux/amd64 \
    --tag "${FULL_IMG}" \
    --load \
    "${SERVICE_DIR}"

echo "==> Push: ${FULL_IMG}"
docker push "${FULL_IMG}"

echo "==> Resolve pushed digest"
DIGEST="$(aws ${AWS_CLI_ARGS} ecr describe-images \
    --repository-name "${ECR_REPO}" \
    --image-ids imageTag="${TAG}" \
    --query 'imageDetails[0].imageDigest' --output text)"
echo "    tag:    ${TAG}"
echo "    digest: ${DIGEST}"

# Patch state file with image details.
tmp="$(mktemp)"
jq --arg t "${TAG}" --arg d "${DIGEST}" '.image = { "tag": $t, "digest": $d, "uri": ("'"${ECR_IMAGE_URI}"':" + $t) }' \
    "${STATE_FILE}" > "${tmp}"
mv "${tmp}" "${STATE_FILE}"

# ----- Harness image (re-uses the same ECR repo, distinct tag prefix) -----
HARNESS_TAG="dev05-harness-${GIT_SHA}"
HARNESS_FULL_IMG="${ECR_IMAGE_URI}:${HARNESS_TAG}"

echo "==> Build harness image: ${HARNESS_FULL_IMG}"
docker buildx build \
    --platform linux/amd64 \
    --file "${SERVICE_DIR}/test/harness/Dockerfile" \
    --tag "${HARNESS_FULL_IMG}" \
    --load \
    "${SERVICE_DIR}"

echo "==> Push harness: ${HARNESS_FULL_IMG}"
docker push "${HARNESS_FULL_IMG}"

HARNESS_DIGEST="$(aws ${AWS_CLI_ARGS} ecr describe-images \
    --repository-name "${ECR_REPO}" \
    --image-ids imageTag="${HARNESS_TAG}" \
    --query 'imageDetails[0].imageDigest' --output text)"
echo "    harness tag:    ${HARNESS_TAG}"
echo "    harness digest: ${HARNESS_DIGEST}"

tmp2="$(mktemp)"
jq --arg t "${HARNESS_TAG}" --arg d "${HARNESS_DIGEST}" \
    '.harnessImage = { "tag": $t, "digest": $d, "uri": ("'"${ECR_IMAGE_URI}"':" + $t) }' \
    "${STATE_FILE}" > "${tmp2}"
mv "${tmp2}" "${STATE_FILE}"
