#!/usr/bin/env bash
# Build and push the easyvcs-server image WITHOUT a local build daemon.
#
# Pipeline (same verified shape as artifact/build-image.sh and
# easylab/build-image.sh):
#   1. buildctl targets the shared cluster buildkitd, builds the Dockerfile
#      with the easyvcs repo root as context, and exports a docker archive
#      (RepoTag set). buildkitd does NOT push.
#   2. skopeo copies the archive to the deployment OCI registry. Images are
#      hosted on **artifact** (the platform registry); the artifact write token
#      authenticates the push. (Only the `artifact` image itself must stay on
#      the registry — it cannot pull from itself.)
#
# Prereqs: a reachable buildkitd, skopeo, and the base images in the registry
# (root/golang:1.26-alpine, root/alpine:3.24).
set -euo pipefail
SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Push destination registry host. The platform hosts images on artifact; override
# only for a special case (e.g. the artifact image itself).
REGISTRY="${REGISTRY:-artifact.worker.svc.cluster.local}"
NAMESPACE="${NAMESPACE:-easy-vcs}"
NAME="${NAME:-easyvcs}"
TAG="${TAG:-$(date +%Y%m%d%H%M%S)}"
DEST="${REGISTRY}/${NAMESPACE}/${NAME}:${TAG}"
BUILDKIT="${BUILDKIT_ADDR:-tcp://buildkitd.temp.svc.cluster.local:1234}"
# artifact write credential (write-level artifact token; dev default below).
REGISTRY_USER="${REGISTRY_USER:-${ARTIFACT_USER:-root}}"
REGISTRY_PASS="${REGISTRY_PASS:-${ARTIFACT_TOKEN:-dev-artifact-token}}"
PROXY="${PROXY:-http://mihomo.develop.svc.cluster.local:7890}"
# Base-image registry host (build-time). Artifact hosts the mirrored base images
# (root/golang, root/alpine); buildkitd reaches it over HTTP.
BASE_REGISTRY="${BASE_REGISTRY:-artifact.worker.svc.cluster.local}"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# Context is the repo root (single module: go.mod + all packages + cmd/).
# Exclude VCS metadata and any local data dir to keep the context small.
CTX="${WORK}/ctx"
mkdir -p "${CTX}"
tar -C "${SRC_DIR}" \
  --exclude='./.git' --exclude='./data' --exclude='./*.tar' \
  -cf - . | tar -C "${CTX}" -xf -

echo "Building easyvcs-server image -> ${DEST} (buildkitd=${BUILDKIT})"
buildctl --addr "${BUILDKIT}" build \
  --frontend dockerfile.v0 \
  --local "context=${CTX}" \
  --local "dockerfile=${CTX}" \
  --opt "filename=Dockerfile" \
  --opt "build-arg:REGISTRY=${BASE_REGISTRY}/root" \
  --opt "build-arg:HTTP_PROXY=${PROXY}" \
  --opt "build-arg:HTTPS_PROXY=${PROXY}" \
  --opt "build-arg:NO_PROXY=localhost,127.0.0.1,.svc.cluster.local,.svc,.nip.io,10.199.64.20,develop.10.199.64.20.nip.io" \
  --output "type=docker,name=${NAMESPACE}/${NAME}:${TAG},dest=${WORK}/image.tar" \
  --progress plain

echo "Pushing to ${DEST}"
skopeo copy \
  --dest-creds "${REGISTRY_USER}:${REGISTRY_PASS}" \
  --dest-tls-verify=false \
  "docker-archive:${WORK}/image.tar:${NAMESPACE}/${NAME}:${TAG}" \
  "docker://${DEST}"

echo "Verifying push:"
skopeo inspect --creds "${REGISTRY_USER}:${REGISTRY_PASS}" --tls-verify=false "docker://${DEST}" >/dev/null 2>&1 \
  && echo "OK ${DEST}" \
  || echo "inspect failed for ${DEST} (image may still be present)"
