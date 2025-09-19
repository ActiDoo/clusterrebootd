#!/bin/sh
set -eu

systemd_active() {
  [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1
}

if [ "$1" -eq 0 ]; then
  if systemd_active; then
    if ! systemctl --no-reload stop clusterrebootd.service; then
      echo "warning: failed to stop clusterrebootd.service" >&2
    fi
  fi
fi

exit 0
