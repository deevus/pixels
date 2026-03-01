#!/bin/bash
set -euo pipefail

echo "[$(date -Iseconds)] pixels egress enable starting"

/usr/local/bin/pixels-resolve-egress.sh
cp /etc/sudoers.d/pixel.restricted /etc/sudoers.d/pixel
chmod 0440 /etc/sudoers.d/pixel

echo "[$(date -Iseconds)] pixels egress enable complete"
