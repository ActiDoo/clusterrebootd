#!/usr/bin/env bash
set -euo pipefail

PKG_DIR=${1:-dist/packages}
CHECKSUM_DIR="${PKG_DIR}/checksums"
SBOM_DIR="${PKG_DIR}/sbom"
SIGNATURE_DIR="${PKG_DIR}/signatures"
SBOM_FORMAT_VALUE=${SBOM_FORMAT:-cyclonedx-json}
COSIGN_CMD=${COSIGN:-cosign}

if [[ ! -d "${PKG_DIR}" ]]; then
  echo "package directory not found: ${PKG_DIR}" >&2
  exit 1
fi

if [[ ! -d "${CHECKSUM_DIR}" ]]; then
  echo "checksum directory missing: ${CHECKSUM_DIR}" >&2
  exit 1
fi

if [[ ! -d "${SBOM_DIR}" ]]; then
  echo "sbom directory missing: ${SBOM_DIR}" >&2
  exit 1
fi

if [[ ! -d "${SIGNATURE_DIR}" ]]; then
  echo "signature directory missing: ${SIGNATURE_DIR}" >&2
  exit 1
fi

if [[ ! -f "${PKG_DIR}/SHA256SUMS" ]]; then
  echo "missing aggregated SHA256SUMS manifest in ${PKG_DIR}" >&2
  exit 1
fi

if [[ ! -f "${PKG_DIR}/SHA512SUMS" ]]; then
  echo "missing aggregated SHA512SUMS manifest in ${PKG_DIR}" >&2
  exit 1
fi

sha256sum --check "${PKG_DIR}/SHA256SUMS"
sha512sum --check "${PKG_DIR}/SHA512SUMS"

shopt -s nullglob
artifacts=("${PKG_DIR}"/*.deb "${PKG_DIR}"/*.rpm)
shopt -u nullglob

if [[ ${#artifacts[@]} -eq 0 ]]; then
  echo "no packages found in ${PKG_DIR}" >&2
  exit 1
fi

for artifact in "${artifacts[@]}"; do
  base=$(basename "${artifact}")
  sbom_file="${SBOM_DIR}/${base}.sbom.${SBOM_FORMAT_VALUE}"
  if [[ ! -s "${sbom_file}" ]]; then
    echo "missing SBOM for ${base} (${sbom_file})" >&2
    exit 1
  fi
  python -m json.tool "${sbom_file}" >/dev/null

  for algo in sha256 sha512; do
    sum_file="${CHECKSUM_DIR}/${base}.${algo}"
    if [[ ! -s "${sum_file}" ]]; then
      echo "missing ${algo} checksum for ${base}" >&2
      exit 1
    fi
  done

  sig_file="${SIGNATURE_DIR}/${base}.sig"
  if [[ -f "${sig_file}" ]]; then
    pub_key=${COSIGN_PUBLIC_KEY:-}
    if [[ -z "${pub_key}" && -f "${SIGNATURE_DIR}/cosign.pub" ]]; then
      pub_key="${SIGNATURE_DIR}/cosign.pub"
    fi
    if [[ -z "${pub_key}" ]]; then
      echo "signature present for ${base} but no COSIGN_PUBLIC_KEY provided" >&2
      exit 1
    fi
    if ! command -v "${COSIGN_CMD}" >/dev/null; then
      echo "cosign command not available while signatures must be verified" >&2
      exit 1
    fi
    "${COSIGN_CMD}" verify-blob --key "${pub_key}" --signature "${sig_file}" "${artifact}" >/dev/null
  fi

done

echo "All artefacts verified under ${PKG_DIR}."
