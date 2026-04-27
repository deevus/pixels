#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# Mask Ubuntu's background apt timers so they don't race with our setup.
systemctl mask --now apt-daily.timer apt-daily-upgrade.timer 2>/dev/null || true

apt-get -o DPkg::Lock::Timeout=600 update
apt-get -o DPkg::Lock::Timeout=600 install -y --no-install-recommends \
  python3 python3-pip python3-venv pipx
apt-get clean
rm -rf /var/lib/apt/lists/*
