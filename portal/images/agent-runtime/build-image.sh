#!/usr/bin/env bash
# Build and push the agent-runtime container image. The build context is this
# directory. The agents node pool is amd64, so default to linux/amd64. Override
# the tag with AGENT_RUNTIME_IMAGE and the platform with AGENT_RUNTIME_PLATFORM.
#
#   ./build-image.sh
#   AGENT_RUNTIME_IMAGE=panchalravi/agent-runtime:agentv1 ./build-image.sh
set -euo pipefail

IMAGE="${AGENT_RUNTIME_IMAGE:-panchalravi/agent-runtime:poc}"
PLATFORM="${AGENT_RUNTIME_PLATFORM:-linux/amd64}"
CTX="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "Building ${IMAGE} (${PLATFORM}) from ${CTX}"
docker buildx build --platform "${PLATFORM}" -f "${CTX}/Dockerfile" -t "${IMAGE}" --push "${CTX}"
echo "Pushed ${IMAGE}"
echo "Set var.agent_runtime_image / PORTAL_AGENT_RUNTIME_IMAGE=${IMAGE}"
