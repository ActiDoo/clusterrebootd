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

# Validate the installation early so container builds fail fast if upstream
# assets change or become unavailable.
RUN etcd --version && etcdctl version
