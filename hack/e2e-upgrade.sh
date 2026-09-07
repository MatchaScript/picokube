#!/usr/bin/env bash
# Build the two node images the minor-upgrade e2e needs and run the driver.
#
# FROM is the previous minor, built from the release branch; TO is the
# current tree. The driver (test/upgrade) runs on the host and talks to a
# libvirt VM through bcvk, so unlike hack/e2e.sh nothing is baked into the
# images beyond what a normal node carries.
#
#   hack/e2e-upgrade.sh
#
# Needs libvirt >= 11 (bcvk --bind-storage-ro uses read-only virtiofs).
# No sudo: bcvk runs against qemu:///session.
set -Eeuo pipefail

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."

FROM_BRANCH=${FROM_BRANCH:-release-1.35}
FROM_IMAGE=${FROM_IMAGE:-picokube-node:from}
TO_IMAGE=${TO_IMAGE:-picokube-node:to}

git fetch origin "$FROM_BRANCH"

worktree=$(mktemp -d -t picokube-from-XXXXXX)
cleanup() { git worktree remove --force "$worktree" >/dev/null 2>&1 || rm -rf "$worktree" ; }
trap cleanup EXIT

# origin/<branch> rather than a local branch: it is what the fetch above just
# updated, and detaching never collides with a checkout elsewhere.
git worktree add --detach "$worktree" "origin/$FROM_BRANCH"
podman build -t "$FROM_IMAGE" -f "$worktree/packaging/Containerfile" "$worktree"
podman build -t "$TO_IMAGE" -f packaging/Containerfile .

# The driver installs this image to disk and switches into FROM from there.
# It cannot install FROM directly: bcvk boots the source image as its own
# installer, our image's multi-user.target upholds crio.service, and the
# overlay containers-storage mounts under /var/lib/containers then defeats
# the `rm -rf /var/lib/containers` bcvk's install script starts with. The
# base image the node image is built on carries no container engine.
#
# The digest the Containerfile pins is dropped: bcvk's installer copies the
# source out of containers-storage into the target, and a digested reference
# resolves to the manifest list, which refuses the copy ("Copying this image
# would require changing layer representation ... Destination specifies a
# digest"). The tag is enough — this deployment is thrown away by the first
# `bootc switch`, so it is not part of what the scenarios assert on.
BASE_IMAGE=${BASE_IMAGE:-$(awk '/^FROM quay.io\/fedora\/fedora-bootc/{print $2; exit}' \
    packaging/Containerfile | sed 's/@sha256:.*//')}
podman pull "$BASE_IMAGE"

# The worktree is only an input to the build; drop it before the long test
# so the exec below can propagate the driver's exit status.
cleanup
trap - EXIT

exec env PICOKUBE_UPGRADE_BASE="$BASE_IMAGE" \
    PICOKUBE_UPGRADE_FROM="$FROM_IMAGE" PICOKUBE_UPGRADE_TO="$TO_IMAGE" \
    go test -tags upgrade -count=1 -timeout 60m -v ./test/upgrade
