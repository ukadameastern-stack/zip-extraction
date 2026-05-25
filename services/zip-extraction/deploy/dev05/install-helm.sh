#!/usr/bin/env bash
# Install helm v3 locally to ./bin/helm if not already on PATH.
# No sudo, no system change. The Makefile prefers ./bin/helm when present.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
BIN_DIR="${SERVICE_DIR}/bin"
TARGET="${BIN_DIR}/helm"

if command -v helm >/dev/null 2>&1; then
    echo "==> helm already on PATH: $(command -v helm)"
    helm version --short
    exit 0
fi

if [ -x "${TARGET}" ]; then
    echo "==> helm already installed at ${TARGET}"
    "${TARGET}" version --short
    exit 0
fi

mkdir -p "${BIN_DIR}"
ARCH="$(uname -m)"
case "${ARCH}" in
    x86_64) HELM_ARCH=amd64 ;;
    aarch64|arm64) HELM_ARCH=arm64 ;;
    *) echo "ERROR: unsupported arch ${ARCH}" >&2; exit 1 ;;
esac
HELM_VERSION="${HELM_VERSION:-v3.16.4}"
URL="https://get.helm.sh/helm-${HELM_VERSION}-linux-${HELM_ARCH}.tar.gz"

echo "==> Download helm ${HELM_VERSION} (${HELM_ARCH}) → ${TARGET}"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
curl -fsSL "${URL}" -o "${TMP}/helm.tgz"
tar -xzf "${TMP}/helm.tgz" -C "${TMP}"
install -m 0755 "${TMP}/linux-${HELM_ARCH}/helm" "${TARGET}"

"${TARGET}" version --short
