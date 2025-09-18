#!/bin/sh
set -eu

CONFIG_DIR="/etc/reboot-coordinator"
CONFIG_DROPIN="${CONFIG_DIR}/config.d"

if [ "$1" -ge 1 ]; then
  if [ ! -d "${CONFIG_DIR}" ]; then
    mkdir -p "${CONFIG_DIR}"
  fi
  chmod 0750 "${CONFIG_DIR}"
  chown root:root "${CONFIG_DIR}"

  if [ ! -d "${CONFIG_DROPIN}" ]; then
    mkdir -p "${CONFIG_DROPIN}"
  fi
  chmod 0750 "${CONFIG_DROPIN}"
  chown root:root "${CONFIG_DROPIN}"

  if [ -f "${CONFIG_DIR}/disable" ]; then
    chmod 0640 "${CONFIG_DIR}/disable"
    chown root:root "${CONFIG_DIR}/disable"
  fi
fi

exit 0
