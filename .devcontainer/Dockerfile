# syntax=docker/dockerfile:1.4
ARG VARIANT="1-1.22-bookworm"
FROM mcr.microsoft.com/devcontainers/go:${VARIANT}

ARG DEBIAN_FRONTEND=noninteractive

# Keep the base image packages patched and install common utilities needed for
# development and release tooling.
RUN apt-get update \
    && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        jq \
    && apt-get autoremove -y \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Install etcd from the latest stable upstream release so local tests can rely
# on an etcd binary that matches production expectations.
ARG ETCD_VERSION="3.6.4"
ARG ETCD_SHA256="4d5f3101daa534e45ccaf3eec8d21c19b7222db377bcfd5e5a9144155238c105"
RUN curl -fsSL https://github.com/etcd-io/etcd/releases/download/v${ETCD_VERSION}/etcd-v${ETCD_VERSION}-linux-amd64.tar.gz -o /tmp/etcd.tgz \
    && echo "${ETCD_SHA256}  /tmp/etcd.tgz" | sha256sum -c - \
    && tar -xzf /tmp/etcd.tgz -C /tmp \
    && install -m 0755 /tmp/etcd-v${ETCD_VERSION}-linux-amd64/etcd /usr/local/bin/etcd \
    && install -m 0755 /tmp/etcd-v${ETCD_VERSION}-linux-amd64/etcdctl /usr/local/bin/etcdctl \
    && install -m 0755 /tmp/etcd-v${ETCD_VERSION}-linux-amd64/etcdutl /usr/local/bin/etcdutl \
    && rm -rf /tmp/etcd.tgz /tmp/etcd-v${ETCD_VERSION}-linux-amd64

# Install nfpm so packaging workflows inside the container can build .deb/.rpm
# artefacts without extra manual setup.  Assets are fetched from the official
# release and verified using the published SHA256 checksum.
ARG NFPM_VERSION="2.43.1"
ARG NFPM_SHA256="2bc2c0b4a13ddbf8ffb0e1df36c43208db6d65a38832c9fe0de097f985653267"
RUN curl -fsSL https://github.com/goreleaser/nfpm/releases/download/v${NFPM_VERSION}/nfpm_${NFPM_VERSION}_Linux_x86_64.tar.gz -o /tmp/nfpm.tgz \
    && echo "${NFPM_SHA256}  /tmp/nfpm.tgz" | sha256sum -c - \
    && tar -xzf /tmp/nfpm.tgz -C /tmp \
    && install -m 0755 /tmp/nfpm /usr/local/bin/nfpm \
    && rm -f /tmp/nfpm /tmp/nfpm.tgz /tmp/LICENSE.md /tmp/README.md \
    && rm -rf /tmp/completions /tmp/manpages

# Validate the installation early so container builds fail fast if upstream
# assets change or become unavailable.
RUN etcd --version && etcdctl version && nfpm --version
