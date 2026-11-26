#!/usr/bin/env bash
set -euo pipefail

PLIST_LABEL="dev.sidequestlab.porter"
PLIST_PATH="${HOME}/Library/LaunchAgents/${PLIST_LABEL}.plist"
UID_NUM="$(id -u)"

launchctl bootout "gui/${UID_NUM}/${PLIST_LABEL}" 2>/dev/null || true
rm -f "${PLIST_PATH}"
echo "Stopped and removed ${PLIST_LABEL}."
