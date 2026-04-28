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
  apt_run install -y --no-install-recommends ca-certificates curl gnupg
  mkdir -p /etc/apt/keyrings
  curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key \
    | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg
  echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_22.x nodistro main" \
    > /etc/apt/sources.list.d/nodesource.list
  apt_run update
  apt_run install -y --no-install-recommends nodejs
  apt_run clean
  rm -rf /var/lib/apt/lists/*
} 9>/var/lib/apt/daily_lock
