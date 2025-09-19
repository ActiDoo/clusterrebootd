# Cluster Reboot Coordinator

The Cluster Reboot Coordinator orchestrates safe, serialized kernel reboots across Linux fleets.  Each node runs the
coordinator, evaluates reboot requirements, validates cluster health, and acquires a distributed lock in etcd before
triggering the configured reboot command.

## Overview

The daemon has matured beyond the early building blocks: it now ships the full orchestration loop, lock manager,
observability plumbing, packaging assets, and CI/CD automation required by the PRD.  Operators receive a predictable
service that favours safety, explicit configuration, and verifiable supply-chain artefacts.

## Key Features

- **Detector engine** – combines file and command detectors, re-runs checks after lock acquisition, and surfaces detailed
  timing/exit-code data for diagnostics.
- **Health gating** – executes an operator-supplied script twice (pre- and post-lock) with rich environment variables for
  node identity, cluster policies, maintenance windows, and optional metrics endpoints.
- **Distributed coordination** – etcd-backed mutex with annotated metadata (`node`, `pid`, `acquired_at`) so operators can
  inspect lock holders during incidents.
- **Safeguards** – kill switch file, dry-run mode, deny/allow maintenance windows, and configurable retry/jitter for
  transient failures.
- **Observability** – structured JSON logs on stderr plus optional Prometheus metrics served from a configurable listener.
- **Packaging & release** – reproducible `.deb`/`.rpm` packages with SBOMs, checksums, and cosign signatures produced via
  the repository `Makefile` and GitHub Actions pipeline.

## Repository Layout

```
cmd/reboot-coordinator      # CLI entrypoint and daemon bootstrapper
internal/testutil           # Shared helpers for integration and packaging tests
pkg/config                  # YAML configuration structures, defaults, and validation
pkg/detector                # Pluggable reboot-required detectors (file/command)
pkg/health                  # Cluster health script runner with timeout enforcement
pkg/lock                    # etcd lock manager with metadata annotations
pkg/observability           # JSON logger, event types, and Prometheus collector
pkg/orchestrator            # Orchestration runner, loop, and outcome reporting
pkg/version                 # Version metadata exposed via the CLI
pkg/windows                 # Maintenance window parsing and evaluation
packaging/                  # nfpm config, systemd unit, tmpfiles entry, maintainer scripts, smoke tests
.github/workflows/          # CI and release automation
examples/config.yaml        # Annotated production-style configuration sample
```

## Documentation

- [Architecture Overview](docs/ARCHITECTURE.md) – component model, interfaces, and roadmap.
- [Operations Guide](docs/OPERATIONS.md) – installation, configuration, health script guidance, and troubleshooting.
- [Packaging Blueprint](docs/PACKAGING_BLUEPRINT.md) – agreed filesystem layout and packaging contract.
- [CI Pipeline Blueprint](docs/CI_PIPELINE.md) – reference GitHub Actions stages and security posture.
- [Project State](docs/STATE.md) – canonical backlog, next steps, and open questions.

## Quick Start

1. Install Go 1.23 or newer.
2. Fetch dependencies and run the test suite:

   ```bash
   go test ./...
   ```

   Alternatively use `make test` which also enforces formatting.
3. Create a configuration file based on `examples/config.yaml`.  The service defaults to
   `/etc/reboot-coordinator/config.yaml` but the CLI accepts `--config` for alternate paths.
4. Validate your configuration before running the daemon:

   ```bash
   reboot-coordinator validate-config --config /path/to/config.yaml
   ```

5. Start the coordinator once everything validates.  Use `--dry-run` during initial rollouts to exercise the full loop
   without rebooting the host.

## CLI Commands

- `reboot-coordinator run [--config FILE] [--dry-run] [--once]` – start the orchestration loop.  `--once` performs a
  single diagnostic pass while still honouring lock acquisition and health gating.
- `reboot-coordinator status [--skip-health] [--skip-lock]` – execute a dry-run orchestration pass and report the outcome
  (detectors, health gate, lock).  Skipping health or lock annotates the environment so scripts are aware of the bypass.
- `reboot-coordinator simulate` – instantiate detectors, execute them once, and print per-detector summaries without
  contacting etcd or running the health script.
- `reboot-coordinator validate-config` – parse and validate the YAML configuration.
- `reboot-coordinator version` – print the build metadata.

## Observability & Telemetry

`reboot-coordinator run` emits structured JSON logs to stderr for each orchestration stage.  Entries include timestamps,
levels, node identity, event labels, and contextual fields so journald or log shippers can route them without additional
parsing.  When metrics are enabled the daemon listens on the configured address, serves Prometheus counters/histograms,
and exports `RC_METRICS_ENDPOINT` into the health script environment so custom checks can verify scrapeability.

The etcd lock metadata is stored alongside the mutex key in JSON form:

```json
{"node":"node-a","pid":1234,"acquired_at":"2024-03-07T11:45:12.123Z"}
```

Use `etcdctl get <lock-key>` to inspect the current holder during investigations.

## Build, Packaging, and Release

The top-level `Makefile` streamlines builds and packaging:

- `make build` – compile a statically linked Linux binary and stage it in `dist/`.
- `make package` – cross-compile for `amd64`/`arm64`, run `nfpm` to produce `.deb`/`.rpm` packages, generate CycloneDX
  SBOMs via `syft`, write SHA-256/512 manifests, and create cosign signatures when signing keys are supplied.  Outputs
  live under `dist/packages/`.
- `packaging/scripts/verify_artifacts.sh` – re-validate checksums and signatures after a build by supplying
  `COSIGN_PUBLIC_KEY`.

GitHub Actions mirror this workflow (`.github/workflows/ci.yaml`) and upload build artefacts on every push/PR.  The
release workflow rebuilds tagged revisions, generates release notes, and publishes packages, SBOMs, checksums, and
signatures to the GitHub Release.

## Development Environment

A VS Code Dev Container definition under `.devcontainer/` provisions Go 1.22, etcd 3.6.4, and `nfpm` 2.43.1 so packaging
and smoke tests run without additional setup.  Launch it via **Reopen in Container** or `devcontainer up`.  An etcd
instance suitable for smoke tests can be started inside the container with:

```bash
etcd --data-dir /tmp/etcd-data \
  --listen-client-urls http://0.0.0.0:2379 \
  --advertise-client-urls http://127.0.0.1:2379
```

## Operations

The [Operations Guide](docs/OPERATIONS.md) expands on deployment, maintenance windows, health script practices, and
incident response.  Highlights include graceful handling of `SIGINT`/`SIGTERM`, exponential backoff for transient
failures, and recommended packaging workflows.

## Exit Codes

| Code | Meaning | Returned By |
| ---- | ------- | ----------- |
| 0 | Success. No reboot required, prerequisites satisfied, or configuration validated. | All commands on success, including `run --once` and `status` when they report `no_action`, `recheck_cleared`, or `ready`. |
| 1 | Runtime failure. Setup or orchestration error prevented evaluation. | `run`, `run --once`, `status`, `simulate`. |
| 2 | Invalid configuration. | `run`, `status`, `validate-config`. |
| 3 | Blocked by the health script (pre- or post-lock). | `run --once`, `status`, and long-running `run` when terminated while health is blocking. |
| 4 | Lock contention prevented progress. | `run --once`, `status`, and long-running `run` when terminated while unable to acquire the lock. |
| 5 | Kill switch present. | `run --once`, `status`, and long-running `run` when terminated while the kill switch is active. |
| 6 | Detector evaluation failed during simulation. | `simulate`. |
| 64 | CLI usage error (unknown command or flag parsing failure). | All commands. |

## Development Philosophy

The project prioritises safety, resilience, and long-term maintainability:

- **Safety:** strict configuration validation, conservative defaults, and explicit kill-switch semantics prevent accidental
  reboot storms.
- **Extensibility:** detectors, health checks, and observability hooks are pluggable so environments can tailor policies.
- **Testability:** extensive unit tests cover configuration parsing, detectors, health execution, locking, packaging
  assets, and the orchestration loop.

Contributions should continue to respect these principles and the staged roadmap captured in the PRD.
