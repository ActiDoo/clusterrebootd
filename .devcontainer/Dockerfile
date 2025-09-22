# syntax=docker/dockerfile:1.4
ARG VARIANT="1-1.23-bookworm"
FROM mcr.microsoft.com/devcontainers/go:${VARIANT}

# Use bash globally for pipefail
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG DEBIAN_FRONTEND=noninteractive

RUN set -eux; \
    apt-get update; \
    apt-get -y upgrade; \
    apt-get install -y --no-install-recommends \
        ca-certificates curl fuse-overlayfs iptables jq podman slirp4netns uidmap; \
    rm -rf /var/lib/apt/lists/*

# Podman shim
RUN <<'BASH'
set -euo pipefail
echo "vscode ALL=(ALL) NOPASSWD: /usr/bin/install, /usr/bin/podman" >/etc/sudoers.d/010-vscode-podman
chmod 0440 /etc/sudoers.d/010-vscode-podman
install -d -m 0755 /usr/local/bin
BASH

# Install etcd (arch-aware, ohne Hash)
ARG ETCD_VERSION="3.6.4"
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    curl -fsSL -o /tmp/etcd.tgz "https://github.com/etcd-io/etcd/releases/download/v${ETCD_VERSION}/etcd-v${ETCD_VERSION}-linux-${arch}.tar.gz"; \
    tar -xzf /tmp/etcd.tgz -C /tmp; \
    install -m 0755 /tmp/etcd-v${ETCD_VERSION}-linux-${arch}/etcd /usr/local/bin/etcd; \
    install -m 0755 /tmp/etcd-v${ETCD_VERSION}-linux-${arch}/etcdctl /usr/local/bin/etcdctl; \
    install -m 0755 /tmp/etcd-v${ETCD_VERSION}-linux-${arch}/etcdutl /usr/local/bin/etcdutl; \
    rm -rf /tmp/etcd.tgz /tmp/etcd-v${ETCD_VERSION}-linux-${arch}

# Install nfpm (arch-aware, ohne Hash)
ARG NFPM_VERSION="2.43.1"
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) nfpm_arch="x86_64" ;; \
      arm64) nfpm_arch="arm64" ;; \
      *) echo "Unsupported arch: $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/nfpm.tgz "https://github.com/goreleaser/nfpm/releases/download/v${NFPM_VERSION}/nfpm_${NFPM_VERSION}_Linux_${nfpm_arch}.tar.gz"; \
    tar -xzf /tmp/nfpm.tgz -C /tmp; \
    install -m 0755 /tmp/nfpm /usr/local/bin/nfpm; \
    rm -f /tmp/nfpm /tmp/nfpm.tgz /tmp/LICENSE.md /tmp/README.md; \
    rm -rf /tmp/completions /tmp/manpages

# Smoke test
RUN set -eux; etcd --version; etcdctl version; nfpm --version
