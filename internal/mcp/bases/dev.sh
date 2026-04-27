#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  git curl wget jq vim ca-certificates build-essential
apt-get clean
rm -rf /var/lib/apt/lists/*
