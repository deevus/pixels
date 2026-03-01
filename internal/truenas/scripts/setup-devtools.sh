#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo "[$(date -Iseconds)] pixels devtools setup starting"

apt-get update -qq
apt-get install -y -qq build-essential git curl

# Install mise and dev tools for the pixel user (runs as root, uses su -).
su - pixel -c 'curl -fsSL https://mise.run | sh'
su - pixel -c 'echo '\''eval "$(/home/pixel/.local/bin/mise activate bash)"'\'' >> /home/pixel/.bashrc'
su - pixel -c '/home/pixel/.local/bin/mise use --global node@lts claude-code@latest codex@latest opencode@latest'

touch /root/.devtools-provisioned
echo "[$(date -Iseconds)] pixels devtools setup complete"
