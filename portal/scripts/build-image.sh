#!/usr/bin/env bash
# Build and push the Developer Portal container image. The build context is the
# portal/ directory (the Dockerfile compiles both the frontend and the backend).
# The node pool is amd64, so default to linux/amd64. Override the tag with
# PORTAL_IMAGE and the platform with PORTAL_PLATFORM.
#
#   ./scripts/build-image.sh
#   PORTAL_IMAGE=panchalravi/developer-portal:v1 ./scripts/build-image.sh
set -euo pipefail

IMAGE="${PORTAL_IMAGE:-panchalravi/developer-portal:poc}"
PLATFORM="${PORTAL_PLATFORM:-linux/amd64}"
CTX="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" # portal/

echo "Building ${IMAGE} (${PLATFORM}) from ${CTX}"
docker buildx build --platform "${PLATFORM}" -f "${CTX}/Dockerfile" -t "${IMAGE}" --push "${CTX}"
echo "Pushed ${IMAGE}"
echo "Set var.developer_portal_image=${IMAGE} and apply terraform/infra/developer-portal.tf"
