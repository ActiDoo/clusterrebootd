#!/bin/sh
set -eu

systemd_active() {
  if ! command -v systemctl >/dev/null 2>&1; then
    return 1
  fi

  if [ ! -S /run/systemd/private ]; then
    return 1
  fi

  return 0
}

if [ "$1" -eq 0 ]; then
  if systemd_active; then
    if ! systemctl daemon-reload; then
      echo "warning: systemctl daemon-reload failed; please run manually" >&2
    fi
  fi
fi

exit 0
