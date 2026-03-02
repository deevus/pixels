#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo "[$(date -Iseconds)] pixels egress setup starting"

apt-get install -y -qq -o Dpkg::Options::="--force-confold" nftables dnsutils

echo "[$(date -Iseconds)] pixels egress setup complete"
