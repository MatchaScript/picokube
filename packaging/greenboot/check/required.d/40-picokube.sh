#!/bin/bash
# greenboot required check: the local control plane must be healthy
# this boot. Probes the apiserver + node + control-plane static pods
# via `picokube healthcheck`, which decouples cluster-health judgement
# from the picokube.service exit code (a transient bookkeeping failure
# inside the supervisor must not flip a working cluster into rollback).
#
# Greenboot retries this script across boot_counter iterations before
# tripping the rollback path, so a brief startup race is tolerated.
set -eu

if ! systemctl is-active --quiet picokube.service ; then
    echo "picokube.service is not active" >&2
    systemctl status --no-pager picokube.service >&2 || true
    exit 1
fi

if ! picokube healthcheck --timeout=5m ; then
    echo "picokube healthcheck failed" >&2
    exit 1
fi
