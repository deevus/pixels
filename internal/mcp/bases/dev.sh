#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# Wait for any background apt (unattended-upgrades on first boot) to finish.
while fuser /var/lib/apt/lists/lock /var/lib/dpkg/lock-frontend /var/lib/dpkg/lock >/dev/null 2>&1; do
  sleep 2
done

apt-get -o DPkg::Lock::Timeout=-1 update
apt-get -o DPkg::Lock::Timeout=-1 install -y --no-install-recommends \
  git curl wget jq vim ca-certificates build-essential
apt-get clean
rm -rf /var/lib/apt/lists/*
