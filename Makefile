SHELL := /bin/bash

BINARY_NAME := reboot-coordinator
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

.PHONY: all build clean package package-% test

all: build

build:
	@echo "==> Building $(BINARY_NAME) ($(GOOS)/$(GOARCH))"
	@mkdir -p $(DIST_DIR)/$(GOOS)_$(GOARCH)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(GOOS)_$(GOARCH)/$(BINARY_NAME) ./cmd/$(BINARY_NAME)
	@cp $(DIST_DIR)/$(GOOS)_$(GOARCH)/$(BINARY_NAME) $(BINARY_PATH)

clean:
	rm -rf $(DIST_DIR)

package: $(ARCHES:%=package-%)

package-%:
	@echo "==> Packaging for architecture $*"
	$(MAKE) build GOOS=$(PACKAGE_GOOS) GOARCH=$*
	@command -v $(NFPM) >/dev/null || { echo "nfpm binary '$(NFPM)' not found in PATH" >&2; exit 1; }
	@mkdir -p $(PKG_OUTPUT_DIR)
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
	  cp $$bin_path $(BINARY_PATH); \
	  ARCH=$$deb_arch VERSION="$(VERSION)" $(NFPM) package --config $(NFPM_CONFIG) --packager deb --target $(PKG_OUTPUT_DIR)/$(BINARY_NAME)_$${safe_version}_$$deb_arch.deb; \
	  ARCH=$$rpm_arch VERSION="$(VERSION)" $(NFPM) package --config $(NFPM_CONFIG) --packager rpm --target $(PKG_OUTPUT_DIR)/$(BINARY_NAME)-$${safe_version}-$(PKG_RELEASE).$$rpm_arch.rpm; \
	  cp $$bin_path $(BINARY_PATH).$*

test:
	go test ./...
