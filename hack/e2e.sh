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
# 8G / 2 vCPU. The control plane plus the flannel and nginx pods of Test11 do
# not fit in bcvk's smaller instance types, and memory buys disk here as well:
# an ephemeral VM has no writable block device, so /var is a tmpfs sized at 20%
# of RAM (4G gave 781M, 8G gives 1.6G). /var is where CRI-O keeps its image
# store, so Test11's pulls — flannel 90M, coredns 77M, flannel-cni-plugin 12M,
# nginx:alpine — plus etcd and the journal took a 781M /var past kubelet's
# nodefs eviction threshold. kubelet then taints the node disk-pressure:
# NoSchedule, the nginx pod stays Pending, and the Service it fronts has no
# endpoint — for which kube-proxy programs a REJECT, so the ClusterIP probe
# fails instantly with "connection refused". Both shapes of the Test11 flake
# were that.
MEMORY=${MEMORY:-8G}
VCPUS=${VCPUS:-2}

podman build -t "$IMAGE" -f packaging/Containerfile .

exec bcvk ephemeral run-ssh --rm --memory "$MEMORY" --vcpus "$VCPUS" "$IMAGE" -- \
    /usr/libexec/picokube/e2e.test -test.v -test.timeout 30m
