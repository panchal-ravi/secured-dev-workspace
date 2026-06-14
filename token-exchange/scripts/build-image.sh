#!/usr/bin/env bash
# Build and push the token-exchange container image. The build context is the
# token-exchange/ directory. The node pool is amd64, so default to linux/amd64.
# Override the tag with TOKEN_EXCHANGE_IMAGE and the platform with TOKEN_EXCHANGE_PLATFORM.
#
#   ./scripts/build-image.sh
#   TOKEN_EXCHANGE_IMAGE=panchalravi/token-exchange:v1 ./scripts/build-image.sh
set -euo pipefail

IMAGE="${TOKEN_EXCHANGE_IMAGE:-panchalravi/token-exchange:poc}"
PLATFORM="${TOKEN_EXCHANGE_PLATFORM:-linux/amd64}"
CTX="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" # token-exchange/

echo "Building ${IMAGE} (${PLATFORM}) from ${CTX}"
docker buildx build --platform "${PLATFORM}" -f "${CTX}/Dockerfile" -t "${IMAGE}" --push "${CTX}"
echo "Pushed ${IMAGE}"
echo "Set var.token_exchange_image=${IMAGE} and apply terraform/infra/token-exchange.tf"
