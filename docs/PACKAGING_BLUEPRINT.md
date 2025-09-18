# Packaging Blueprint

This document defines the packaging blueprint for the Cluster Reboot Coordinator.
It captures the agreed contract for systemd integration, filesystem layout, and
the `nfpm` configuration that will produce both `.deb` and `.rpm` artefacts.  The
blueprint guides the subsequent implementation work so the packaging effort can
focus on execution rather than debating fundamentals.

## Goals

- Deliver reproducible `.deb` and `.rpm` packages for amd64 and arm64 that install
  the coordinator with minimal manual steps.
- Ship a hardened systemd service with predictable behaviour and clear operator
  hooks (configuration files, kill switch, health script location).
- Ensure packages respect FHS conventions, track configuration as conffiles, and
  avoid clobbering operator-managed assets during upgrades.
- Keep the packaging pipeline deterministic so it can plug into the planned
  CI/CD workflows.

## Target Platforms & Toolchain

- **Distributions:** Ubuntu 20.04/22.04/24.04, Debian 11/12/13, RHEL/Alma/Rocky
  9/10.  Packages should avoid distribution-specific dependencies beyond
  `systemd` and standard core utilities.
- **Architectures:** `amd64` and `arm64`.
- **Build Tooling:**
  - Go 1.23+ to build the static binary (using `CGO_ENABLED=0` to simplify
    dependency handling).
  - [`nfpm`](https://nfpm.goreleaser.com/) invoked via a repository-local config
    file to generate `.deb` and `.rpm` artefacts in a reproducible manner.
  - Checksums (SHA256) emitted alongside packages; signing is handled in later
    roadmap stages but the blueprint reserves space for detached signatures.

## Filesystem Layout

The following layout applies to both package families and adheres to FHS
recommendations:

| Destination | Type        | Owner/Mode | Notes |
|-------------|-------------|------------|-------|
| `/usr/bin/reboot-coordinator` | Binary | `root:root 0755` | Statically linked CLI/daemon. |
| `/etc/reboot-coordinator/` | Directory | `root:root 0750` | Hosts config and kill switch. Created in pre-install if absent. |
| `/etc/reboot-coordinator/config.yaml` | Conffile | `root:root 0640` | Disabled template with documentation comments; packaged as a conffile so local edits survive upgrades. |
| `/etc/reboot-coordinator/config.d/` | Directory | `root:root 0750` | Optional drop-in directory reserved for future overrides; empty directory shipped to reserve the namespace. |
| `/etc/reboot-coordinator/disable` | File (absent by default) | `root:root 0640` | Kill switch sentinel. Package does **not** create the file; documentation explains its semantics. |
| `/usr/lib/systemd/system/reboot-coordinator.service` (`/lib/systemd/system` on Debian/Ubuntu) | systemd unit | `root:root 0644` | Primary service unit. |
| `/usr/lib/tmpfiles.d/reboot-coordinator.conf` | tmpfiles.d | `root:root 0644` | Ensures `/run/reboot-coordinator` runtime directory exists with 0750 permissions for future state files. |
| `/usr/share/doc/reboot-coordinator/README.Debian` (Deb-based) | Documentation | `root:root 0644` | Points to upstream docs and summarises enablement steps. |
| `/usr/share/licenses/reboot-coordinator/LICENSE` | Documentation | `root:root 0644` | License file copied from repository root. |

### Configuration Handling

- `config.yaml` ships as a heavily-commented example with all options disabled by
  default (detectors and health script commented out).  Operators must tailor the
  file before enabling the service.  The service fails fast with a clear error if
  required fields remain unset.
- The template sets `dry_run: true` and leaves critical identifiers blank so
  installing the package never triggers an immediate reboot attempt.
- Drop-ins under `config.d/` are ignored until the daemon gains merge support, but
  the directory is reserved now to avoid future incompatible moves.

### Runtime Directory

While the daemon currently does not persist data under `/run`, reserving
`/run/reboot-coordinator` via `tmpfiles.d` enables future extensions (lock
metadata, health cache) without modifying the package layout.  The systemd unit
references the runtime directory using `RuntimeDirectory=reboot-coordinator` so
systemd manages creation on start for hosts without `tmpfiles` support.

## Systemd Service Contract

The packaged unit `reboot-coordinator.service` codifies the following contract:

```
[Unit]
Description=Cluster Reboot Coordinator
Documentation=https://github.com/clusterrebootd/clusterrebootd
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=60
StartLimitBurst=5
ConditionPathExists=!/etc/reboot-coordinator/disable

[Service]
Type=simple
ExecStart=/usr/bin/reboot-coordinator run --config /etc/reboot-coordinator/config.yaml
Restart=always
RestartSec=5
RuntimeDirectory=reboot-coordinator
RuntimeDirectoryMode=0750
# Health script and metrics inherit defaults from config.yaml
# Environment overrides can be placed in /etc/systemd/system/reboot-coordinator.service.d/*.conf

[Install]
WantedBy=multi-user.target
```

Key points:

- **Kill switch integration:** `ConditionPathExists=!/etc/reboot-coordinator/disable`
  prevents automatic starts when operators intentionally disable reboots.
- **Graceful shutdown:** the binary already handles `SIGINT/SIGTERM`, so `KillMode`
  remains default (`control-group`).
- **Restart policy:** short retry window (`RestartSec=5`) paired with
  `StartLimit*` guards to avoid crash loops; consistent with orchestrator
  backoff inside the daemon.
- **RuntimeDirectory:** ensures `/run/reboot-coordinator` exists with controlled
  permissions before execution.
- **Operator overrides:** the blueprint expects drop-in units for custom
  environment variables (`Environment=`) or additional dependencies.

The package will include a README entry instructing operators to run `systemctl
enable --now reboot-coordinator.service` after configuring the daemon.  To avoid
accidental reboots the post-install script will **not** enable or start the
service automatically.

## Maintainer Scripts

Both package formats share logically equivalent scripts (translated to shell and
Lua as required by `rpm`):

- **Pre-install:**
  - Create `/etc/reboot-coordinator` with `0750` perms if missing.
  - Ensure `/etc/reboot-coordinator/config.d` exists.
  - Do **not** create the kill-switch file; if present, preserve permissions.
- **Post-install:**
  - Run `systemd-tmpfiles --create /usr/lib/tmpfiles.d/reboot-coordinator.conf`.
  - Execute `systemctl daemon-reload` if systemd is active.
  - Emit a notice reminding operators to review `/etc/reboot-coordinator/config.yaml`
    and enable the service manually.
- **Pre-uninstall:**
  - Stop the service (`systemctl stop`) only when removing the package (not during
    upgrades) and ignore failures if the service is already inactive.
- **Post-uninstall:**
  - Reload systemd units.
  - Leave configuration files untouched unless the operator removes them
    explicitly (packaging does not delete `/etc/reboot-coordinator`).

The scripts must treat non-systemd environments defensively (check for the
presence of `/run/systemd/system`).  Because the daemon may be installed on hosts
where reboots are sensitive, maintainer scripts should fail gracefully rather
than aborting the entire upgrade.

## nfpm Configuration Skeleton

The `nfpm.yaml` template will capture shared metadata and use build-time
variables for versioning:

```yaml
name: reboot-coordinator
arch: ${ARCH}
platform: linux
version: ${VERSION}
section: admin
priority: optional
description: |
  Cluster Reboot Coordinator daemon that serialises kernel reboots behind etcd locking and
  cluster health gates.
license: Apache-2.0
homepage: https://github.com/clusterrebootd/clusterrebootd
maintainer: Platform SRE Team <sre@example.com>
contents:
  - src: ./dist/reboot-coordinator
    dst: /usr/bin/reboot-coordinator
    file_info:
      mode: 0755
  - src: ./packaging/config.yaml
    dst: /etc/reboot-coordinator/config.yaml
    type: config
    file_info:
      mode: 0640
  - src: ./packaging/systemd/reboot-coordinator.service
    dst: /lib/systemd/system/reboot-coordinator.service
  - src: ./packaging/tmpfiles/reboot-coordinator.conf
    dst: /usr/lib/tmpfiles.d/reboot-coordinator.conf
  - src: ./LICENSE
    dst: /usr/share/licenses/reboot-coordinator/LICENSE
    file_info:
      mode: 0644
  - src: ./docs/PACKAGING_BLUEPRINT.md
    dst: /usr/share/doc/reboot-coordinator/PACKAGING_BLUEPRINT.md
    file_info:
      mode: 0644
overrides:
  deb:
    depends:
      - systemd
    recommends:
      - ca-certificates
    scripts:
      preinstall: ./packaging/scripts/deb/preinst
      postinstall: ./packaging/scripts/deb/postinst
      prerm: ./packaging/scripts/deb/prerm
      postrm: ./packaging/scripts/deb/postrm
  rpm:
    depends:
      - systemd
    scripts:
      preinstall: ./packaging/scripts/rpm/preinstall.sh
      postinstall: ./packaging/scripts/rpm/postinstall.sh
      preremove: ./packaging/scripts/rpm/preremove.sh
      postremove: ./packaging/scripts/rpm/postremove.sh
```

Key practices:

- Build pipeline copies the compiled binary to `./dist/reboot-coordinator` and
  staged packaging assets into `./packaging/...` before invoking `nfpm`.
- The `config` type ensures dpkg/rpm treat `config.yaml` as a managed conffile.
- All scripts are version-controlled so changes go through code review.
- `nfpm` will emit metadata to `dist/packages/` (e.g. `.deb`, `.rpm`, `.sha256`).

## Security & Compliance Considerations

- Service runs as `root` because the daemon must execute the reboot command.
  Future work may explore privilege separation for detector execution but the
  packaging keeps the contract explicit.
- Binary is statically linked to minimise runtime dependencies and simplify
  SBOM generation later.
- Configuration directory restricts read access to root to avoid leaking etcd
  credentials or health script parameters.
- Maintainer scripts avoid sourcing environment variables from untrusted paths
  and quote file names to protect against spaces or shell metacharacters.

## Next Steps

1. Create the `packaging/` directory with the skeleton files referenced above.
2. Implement the `nfpm` config and maintainer scripts following this blueprint.
3. Add CI automation to build the binary, invoke `nfpm`, and publish artefacts
   for review.
4. Expand documentation (README, install guide) to reference the packages once
   available.
