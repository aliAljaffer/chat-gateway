#!/usr/bin/env bash
set -euo pipefail
tag="${1:-latest}"
docker buildx build \
  --platform linux/amd64 \
  --tag "ghcr.io/alialjaffer/chat-gateway:${tag}" \
  --push \
  .
