#!/bin/sh
set -e
if [ ! -f /root/.ssh-provisioned ]; then
    apt-get update -qq
    apt-get install -y -qq openssh-server sudo curl

    if ! id pixel >/dev/null 2>&1; then
        userdel -r ubuntu 2>/dev/null || true
        groupdel ubuntu 2>/dev/null || true
        groupadd -g 1000 pixel
        useradd -m -u 1000 -g 1000 -s /bin/bash -G sudo pixel
    fi
    cp -rn /etc/skel/. /home/pixel/
    mkdir -p /home/pixel/.ssh

    echo 'pixel ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/pixel
    chmod 0440 /etc/sudoers.d/pixel

    chown -R pixel:pixel /home/pixel
    chmod 700 /home/pixel/.ssh

    systemctl enable --now ssh
    touch /root/.ssh-provisioned

    # Phase 2: zmx-orchestrated provisioning.
    if [ -x /usr/local/bin/pixels-provision.sh ]; then
        nohup /usr/local/bin/pixels-provision.sh >/var/log/pixels-provision.log 2>&1 &
    fi
fi
exit 0
