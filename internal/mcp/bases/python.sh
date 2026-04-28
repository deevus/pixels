#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# Retry apt operations on lock contention. Setting DPkg::Lock::Timeout=0
# makes apt fail immediately instead of polling once per second; our outer
# loop waits 30s and emits a single semantic message per retry.
apt_run() {
  for attempt in $(seq 1 20); do
    apt -q -o DPkg::Lock::Timeout=0 "$@" && return 0
    [ "$attempt" -lt 20 ] || return 1
    echo "Still waiting for apt lock... (attempt $attempt/20)" >&2
    sleep 30
  done
}

# Flock apt.systemd.daily's lockfile so apt-daily can't fire mid-build.
{
  flock --timeout 600 9
  apt_run update
  apt_run install -y --no-install-recommends \
    python3 python3-pip python3-venv pipx
  apt_run clean
  rm -rf /var/lib/apt/lists/*
} 9>/var/lib/apt/daily_lock
