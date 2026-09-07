#!/bin/bash
# greenboot red.d: runs immediately before bootc rolls back to the
# previous deployment (boot_counter hits zero). Leaves a marker so the
# post-rollback boot's picokube.service restores the latest backup for
# whichever deployment bootc returns us to.
#
# The marker lives under /var/lib, which is preserved across bootc
# rollback; the restore action that consumes it is maybeRestore (see
# internal/boot/boot.go).
set -eu

# Only act when greenboot has decided the next boot is a rollback. On
# non-rollback failure iterations (boot_counter still > 0) we must not
# pre-commit to restoring, because the boot may simply be retried.
#
# greenboot runs red.d before it inspects boot_counter (greenboot-rs
# src/main.rs:381 run_red, :392 get_boot_counter), and the counter is
# decremented by GRUB at boot, not by greenboot (grub2/08_greenboot.cfg:13).
# So on the attempt that trips the rollback the value already reads 0.
if ! grub2-editenv - list 2>/dev/null | grep -q '^boot_counter=0' ; then
    echo "picokube: boot_counter != 0, not requesting restore"
    exit 0
fi

# Atomic rollback only applies on ostree / bootc. On RPM-only hosts the
# backup package is a no-op and restore would have nothing to consume.
if [ ! -e /run/ostree-booted ] ; then
    echo "picokube: non-ostree system, no restore marker needed"
    exit 0
fi

mkdir -p /var/lib/picokube/backups
: > /var/lib/picokube/backups/restore
echo "picokube: restore marker placed at /var/lib/picokube/backups/restore"
