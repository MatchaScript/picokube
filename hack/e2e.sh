#!/usr/bin/env bash
# Build the bootc node image and run the e2e suite inside an ephemeral VM.
#
# The suite binary is baked into the image (packaging/Containerfile), so the
# VM needs no Go toolchain and no source tree. `ephemeral run-ssh` is the
# bcvk mode that propagates the guest command's exit status, so this script
# exits with the suite's result.
#
#   hack/e2e.sh              build and run
#   IMAGE=... hack/e2e.sh    reuse a different tag
set -Eeuo pipefail

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."

IMAGE=${IMAGE:-picokube-node:dev}
# 4G / 2 vCPU: the control plane plus the flannel and nginx pods of Test11
# do not fit in bcvk's smaller instance types.
MEMORY=${MEMORY:-4G}
VCPUS=${VCPUS:-2}

podman build -t "$IMAGE" -f packaging/Containerfile .

exec bcvk ephemeral run-ssh --rm --memory "$MEMORY" --vcpus "$VCPUS" "$IMAGE" -- \
    /usr/libexec/picokube/e2e.test -test.v -test.timeout 30m
