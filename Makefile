SHELL := /bin/bash

BINARY_NAME := clusterrebootd
DIST_DIR := dist
BINARY_PATH := $(DIST_DIR)/$(BINARY_NAME)
NFPM ?= nfpm
NFPM_CONFIG := packaging/nfpm.yaml
ARCHES ?= amd64 arm64
PACKAGE_GOOS := linux
PKG_OUTPUT_DIR := $(DIST_DIR)/packages
PKG_RELEASE ?= 1
CGO_ENABLED ?= 0
GOOS ?= $(PACKAGE_GOOS)
GOARCH ?= $(shell go env GOARCH 2>/dev/null || echo amd64)
LDFLAGS ?= -s -w
VERSION ?= $(shell go run ./cmd/$(BINARY_NAME) version 2>/dev/null || echo 0.0.0-dev)
VERSION_PKG := github.com/clusterrebootd/clusterrebootd/pkg/version
GO_LDFLAGS := $(LDFLAGS) -X $(VERSION_PKG).Version=$(VERSION)
SYFT ?= syft
COSIGN ?= cosign
SBOM_FORMAT ?= cyclonedx-json
SBOM_DIR := $(PKG_OUTPUT_DIR)/sbom
CHECKSUM_DIR := $(PKG_OUTPUT_DIR)/checksums
SIGNATURE_DIR := $(PKG_OUTPUT_DIR)/signatures
SIGNING_KEY ?=
SIGNING_PUBKEY ?=

.PHONY: all build clean package prep-package package-% test

all: build

build:
	@echo "==> Building $(BINARY_NAME) ($(GOOS)/$(GOARCH))"
	@mkdir -p $(DIST_DIR)/$(GOOS)_$(GOARCH)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(GO_LDFLAGS)" -o $(DIST_DIR)/$(GOOS)_$(GOARCH)/$(BINARY_NAME) ./cmd/$(BINARY_NAME)
	@cp $(DIST_DIR)/$(GOOS)_$(GOARCH)/$(BINARY_NAME) $(BINARY_PATH)

clean:
	 rm -rf $(DIST_DIR)

package: prep-package $(ARCHES:%=package-%)

prep-package:
	@echo "==> Preparing packaging workspace"
	@mkdir -p $(PKG_OUTPUT_DIR) $(SBOM_DIR) $(SIGNATURE_DIR) $(CHECKSUM_DIR)
	@rm -f $(PKG_OUTPUT_DIR)/SHA256SUMS $(PKG_OUTPUT_DIR)/SHA512SUMS
	@rm -f $(CHECKSUM_DIR)/* $(SBOM_DIR)/* $(SIGNATURE_DIR)/*

package-%:
	@echo "==> Packaging for architecture $*"
	$(MAKE) build GOOS=$(PACKAGE_GOOS) GOARCH=$*
	@command -v $(NFPM) >/dev/null || { echo "nfpm binary '$(NFPM)' not found in PATH" >&2; exit 1; }
	@command -v $(SYFT) >/dev/null || { echo "syft binary '$(SYFT)' not found in PATH" >&2; exit 1; }
	@if [ -n "$(strip $(SIGNING_KEY))" ]; then \
	  command -v $(COSIGN) >/dev/null || { echo "cosign binary '$(COSIGN)' not found in PATH" >&2; exit 1; }; \
	fi
	@set -euo pipefail; \
	  bin_path="$(DIST_DIR)/$(PACKAGE_GOOS)_$*/$(BINARY_NAME)"; \
	  safe_version=$$(printf '%s' "$(VERSION)" | tr '/:' '__'); \
	  deb_arch=$$(case $* in \
	    amd64) echo amd64 ;; \
	    arm64) echo arm64 ;; \
	    *) echo $* ;; \
	  esac); \
	  rpm_arch=$$(case $* in \
	    amd64) echo x86_64 ;; \
	    arm64) echo aarch64 ;; \
	    *) echo $* ;; \
	  esac); \
	  signing_key="$(SIGNING_KEY)"; \
	  signing_pub="$(SIGNING_PUBKEY)"; \
	  deb_target="$(PKG_OUTPUT_DIR)/$(BINARY_NAME)_$${safe_version}_$$deb_arch.deb"; \
	  rpm_target="$(PKG_OUTPUT_DIR)/$(BINARY_NAME)-$${safe_version}-$(PKG_RELEASE).$$rpm_arch.rpm"; \
	  cp $$bin_path $(BINARY_PATH); \
	  ARCH=$$deb_arch VERSION="$(VERSION)" $(NFPM) package --config $(NFPM_CONFIG) --packager deb --target "$$deb_target"; \
	  ARCH=$$rpm_arch VERSION="$(VERSION)" $(NFPM) package --config $(NFPM_CONFIG) --packager rpm --target "$$rpm_target"; \
	  cp $$bin_path $(BINARY_PATH).$*; \
	  for artifact in "$$deb_target" "$$rpm_target"; do \
	    base=$$(basename "$$artifact"); \
	    echo "==> Generating SBOM for $$base"; \
	    $(SYFT) "$$artifact" -o $(SBOM_FORMAT) > "$(SBOM_DIR)/$$base.sbom.$(SBOM_FORMAT)"; \
	    echo "==> Calculating checksums for $$base"; \
	    sha256sum "$$artifact" | tee "$(CHECKSUM_DIR)/$$base.sha256" >> "$(PKG_OUTPUT_DIR)/SHA256SUMS"; \
	    sha512sum "$$artifact" | tee "$(CHECKSUM_DIR)/$$base.sha512" >> "$(PKG_OUTPUT_DIR)/SHA512SUMS"; \
	    if [ -n "$$signing_key" ]; then \
	      echo "==> Signing $$base"; \
	      $(COSIGN) sign-blob --yes --key "$$signing_key" --output-signature "$(SIGNATURE_DIR)/$$base.sig" "$$artifact"; \
	      if [ -n "$$signing_pub" ] && [ ! -f "$(SIGNATURE_DIR)/cosign.pub" ]; then \
	        cp "$$signing_pub" "$(SIGNATURE_DIR)/cosign.pub"; \
	      fi; \
	    fi; \
	  done

test:
	go test ./...
