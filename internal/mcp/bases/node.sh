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
  # Expected fingerprint of NodeSource's apt repo signing key. NodeSource
  # doesn't publish this in their docs; pinned here from the key fetched on
  # 2026-04-28 (RSA-2048, created 2016-05-23, NSolid <nsolid-gpg@nodesource.com>).
  # If a future build fails the check below, do NOT just update this value —
  # verify out-of-band that NodeSource has actually rotated the key.
  expected_fpr="6F71F525282841EEDAF851B42F59B5F99B1BE0B4"
  curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key \
    | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg
  got_fpr=$(gpg --show-keys --with-colons /etc/apt/keyrings/nodesource.gpg \
    | awk -F: '/^fpr:/{print $10; exit}')
  if [ "$got_fpr" != "$expected_fpr" ]; then
    echo "NodeSource GPG key fingerprint mismatch:" >&2
    echo "  got:  $got_fpr" >&2
    echo "  want: $expected_fpr" >&2
    rm -f /etc/apt/keyrings/nodesource.gpg
    exit 1
  fi
  echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_22.x nodistro main" \
    > /etc/apt/sources.list.d/nodesource.list
  apt_run update
  apt_run install -y --no-install-recommends nodejs
  apt_run clean
  rm -rf /var/lib/apt/lists/*
} 9>/var/lib/apt/daily_lock
