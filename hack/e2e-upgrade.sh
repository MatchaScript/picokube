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

# A fresh CI clone has the remote-tracking ref but no local branch; a
# developer checkout usually has both.
if ! git rev-parse --verify --quiet "$FROM_BRANCH" >/dev/null ; then
    git fetch origin "$FROM_BRANCH:$FROM_BRANCH"
fi

worktree=$(mktemp -d -t picokube-from-XXXXXX)
cleanup() { git worktree remove --force "$worktree" >/dev/null 2>&1 || rm -rf "$worktree" ; }
trap cleanup EXIT

# --detach: the branch may already be checked out elsewhere, and the build
# only needs the tree.
git worktree add --detach "$worktree" "$FROM_BRANCH"
podman build -t "$FROM_IMAGE" -f "$worktree/packaging/Containerfile" "$worktree"
podman build -t "$TO_IMAGE" -f packaging/Containerfile .

# The worktree is only an input to the build; drop it before the long test
# so the exec below can propagate the driver's exit status.
cleanup
trap - EXIT

exec env PICOKUBE_UPGRADE_FROM="$FROM_IMAGE" PICOKUBE_UPGRADE_TO="$TO_IMAGE" \
    go test -tags upgrade -count=1 -timeout 60m -v ./test/upgrade
