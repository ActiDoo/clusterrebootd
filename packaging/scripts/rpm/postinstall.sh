#!/bin/sh
set -eu

TMPFILES_CONF="/usr/lib/tmpfiles.d/clusterrebootd.conf"

systemd_active() {
  [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1
}

if [ "$1" -ge 1 ]; then
  if command -v systemd-tmpfiles >/dev/null 2>&1; then
    if ! systemd-tmpfiles --create "${TMPFILES_CONF}"; then
      echo "warning: failed to apply tmpfiles configuration for clusterrebootd" >&2
    fi
  fi

  if systemd_active; then
    if ! systemctl daemon-reload; then
      echo "warning: systemctl daemon-reload failed; please run manually" >&2
    fi
  fi
fi

if [ "$1" -eq 1 ]; then
  cat <<'EOM'
Cluster Reboot Coordinator installed.

Review /etc/clusterrebootd/config.yaml, update it for your environment, and
run:
  sudo /usr/bin/clusterrebootd validate-config --config /etc/clusterrebootd/config.yaml

The service is not enabled automatically.  After validation succeeds, enable it
with:
  sudo systemctl enable --now clusterrebootd.service
EOM
fi

exit 0
