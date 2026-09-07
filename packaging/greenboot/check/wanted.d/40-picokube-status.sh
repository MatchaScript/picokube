#!/bin/bash
# greenboot wanted check: advisory surface of the most recent picokube
# lifecycle event. Non-failing by design — the message shows up in
# MOTD and `journalctl -u greenboot-healthcheck.service`.
set -u

EVENT_FILE=/var/lib/picokube/state/last-event

if [ -s "$EVENT_FILE" ] ; then
    echo "picokube: $(cat "$EVENT_FILE")"
else
    echo "picokube: no events recorded"
fi
