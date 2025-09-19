# Cluster Reboot Coordinator

The Cluster Reboot Coordinator is a daemon designed to orchestrate safe, serialized
kernel reboots across a fleet of Linux machines.  It uses etcd for distributed
locking, validates cluster health via a pluggable script, and integrates with
systemd for hands-off operation.

This repository currently provides the foundational building blocks for the
coordinator: configuration loading and validation, reboot requirement detectors,
and an execution harness for the cluster health script.  The long-term goal is a
fully production-ready agent that satisfies the requirements outlined in the
project PRD.

## Repository Layout

```
cmd/reboot-coordinator  # CLI entrypoint and daemon bootstrapper
pkg/config              # YAML configuration structures, defaults, and validation
pkg/detector            # Pluggable reboot-required detectors (file/command)
pkg/health              # Cluster health script runner with timeout enforcement
pkg/version             # Version metadata exposed via the CLI
packaging               # nfpm packaging config, systemd unit, and maintainer scripts
```

Additional directories such as `deploy/`, `scripts/`, and packaging assets will
be introduced as the orchestrator matures.

## Getting Started

1. Ensure Go 1.23 or newer is installed.
2. Fetch module dependencies and run the tests:

   ```bash
   go test ./...
   ```

3. Create a configuration file.  See `examples/config.yaml` for a production-
   inspired starting point or adapt the PRD sample.  The CLI defaults to
   `/etc/reboot-coordinator/config.yaml` but accepts an explicit `--config` flag.

## Build & Packaging Workflow

The repository ships with a `Makefile` that standardises common developer tasks:

- `make build` compiles the `reboot-coordinator` binary for Linux using static
  linking defaults and stages the artefact under `dist/`.
- `make test` executes `go test ./...`.
- `make package` cross-compiles the binary for `amd64` and `arm64` and invokes
  [`nfpm`](https://nfpm.goreleaser.com/) to produce `.deb` and `.rpm` packages in
  `dist/packages/`.

Set `ARCHES=amd64` (or `arm64`) to restrict the architectures, override
`VERSION` to package a specific release string, or point `NFPM` to an alternate
`nfpm` binary when developing inside containers.  Ensure `nfpm` is available in
the `PATH` before invoking the packaging target; the provided dev container
ships with version 2.43.1 pre-installed.

Generated artefacts live under `dist/` and are ignored by git so developers can
cleanly iterate on builds and packages without polluting commits.

## Continuous Integration

A GitHub Actions workflow (documented in `docs/CI_PIPELINE.md`) now runs `gofmt`
and `go test ./...` for pushes to `main` and all pull requests.  The job uses
pinned actions, read-only permissions, and a 15-minute timeout to provide an
initial quality gate while the broader pipeline is built out.

## Example Configuration

The repository ships an annotated sample at `examples/config.yaml` that
demonstrates how to wire the implemented features together:

- Two reboot detectors: a Debian/Ubuntu marker file and the RHEL `needs-restarting`
  command with a guard for its reboot-required exit code.
- Cluster guardrails: health script location and timeout, an operator-controlled
  kill switch file, and cluster policy hints (minimum healthy nodes and
  designated fallback nodes) that are exported to the health script environment
  via `RC_CLUSTER_MIN_HEALTHY_FRACTION`, `RC_CLUSTER_MIN_HEALTHY_ABSOLUTE`,
  `RC_CLUSTER_FORBID_IF_ONLY_FALLBACK_LEFT`, and
  `RC_CLUSTER_FALLBACK_NODES`.  If reboot windows are configured they are
  injected through `RC_WINDOWS_ALLOW` and `RC_WINDOWS_DENY` so scripts can
  honour operator-defined maintenance schedules.
- Distributed coordination: three etcd endpoints, an explicit namespace,
  lock key, and optional mutual TLS credentials.
- Observability: metrics listener enabled so the daemon injects
  `RC_METRICS_ENDPOINT` alongside `RC_NODE_NAME`, `RC_LOCK_KEY`,
  `RC_ETCD_ENDPOINTS`, and `RC_KILL_SWITCH_FILE` into the health script
  environment.

Copy the file, update endpoints, file paths, and policy thresholds, and then run
`reboot-coordinator validate-config --config /path/to/config.yaml` to confirm
the configuration is accepted before rolling it out.

## Development Container

The repository ships with a [VS Code Dev Container](https://containers.dev/)
definition under `.devcontainer/`.  It builds on the official Go 1.22 base
image, upgrades the underlying packages, installs etcd v3.6.4, and layers in
[`nfpm`](https://nfpm.goreleaser.com/) 2.43.1 so integration tests and packaging
workflows can run against recent upstream releases without additional manual
setup.

To use it, open the folder in VS Code and select **Reopen in Container**, or run
`devcontainer up` from the CLI.  After the container starts you can launch etcd
locally with:

```bash
etcd --data-dir /tmp/etcd-data \
  --listen-client-urls http://0.0.0.0:2379 \
  --advertise-client-urls http://127.0.0.1:2379
```

The dev container forwards ports 2379/2380 by default, and `etcdctl` is
available in the `$PATH` for smoke tests and debugging.

### CLI Commands

The CLI currently offers early validation and introspection helpers:

- `reboot-coordinator validate-config --config /path/to/config.yaml`
  Validates the configuration schema, defaulting missing fields and ensuring
  semantic requirements such as TTL bounds.
- `reboot-coordinator simulate --config /path/to/config.yaml`
  Loads the configuration, instantiates detectors, executes them once, and
  prints a summary with per-detector results without interacting with etcd or
  rebooting the host.
- `reboot-coordinator status --config /path/to/config.yaml`
  Runs a dry-run orchestration pass that evaluates detectors, executes the
  health script, and attempts to acquire the etcd lock once, reporting the
  resulting outcome without invoking the reboot command.  Pass `--skip-health`
  to bypass the health script or `--skip-lock` to avoid contacting etcd when
  running offline diagnostics.
- `reboot-coordinator version`
  Prints the build version string.
- `reboot-coordinator run`
  Starts the long-running orchestration loop.  The daemon continuously
  evaluates detectors, re-runs the health gate, and executes the configured
  reboot command once all safeguards succeed.  Transient errors during an
  iteration are logged to `stderr` and retried with an exponential backoff so
  operators do not need to restart the process manually.  Pass `--once` to
  execute a single diagnostic pass without invoking the reboot command.

Future milestones will extend the loop with structured logging, observability
integrations, packaging assets, and the full CI/CD pipeline described in the
PRD.

### Exit Codes

The CLI normalises exit codes so automation and operators can reason about the
daemon's state without parsing logs:

| Code | Meaning | Returned By |
| ---- | ------- | ----------- |
| 0    | Success. No reboot required, prerequisites satisfied, or configuration validated. | All commands on success, including `run --once` and `status` when they report `no_action`, `recheck_cleared`, or `ready`. |
| 1    | Runtime failure. Setup or orchestration error prevented evaluation. | `run`, `run --once`, `status`, `simulate`. |
| 2    | Invalid configuration. | `run`, `status`, `validate-config`. |
| 3    | Blocked by the health script (pre- or post-lock). | `run --once`, `status`, and long-running `run` when terminated while health is blocking. |
| 4    | Lock contention prevented progress. | `run --once`, `status`, and long-running `run` when terminated while unable to acquire the lock. |
| 5    | Kill switch present. | `run --once`, `status`, and long-running `run` when terminated while the kill switch is active. |
| 6    | Detector evaluation failed during simulation. | `simulate`. |
| 64   | CLI usage error (unknown command or flag parsing failure). | All commands. |

The long-running `run` mode applies the same mappings when it exits due to a
signal: if the last observed outcome was blocked by health, lock contention, or
the kill switch, the process returns that exit code so supervisors can reflect
the blocking condition.

### Observability

Running `reboot-coordinator run` now emits structured JSON logs to stderr for
each orchestration event (detector evaluation, lock attempts, health script
results, final outcomes).  The logs include fields for node identity, stage,
duration, and status so they can be forwarded directly into log processors or
systemd journal.

Metrics can be exposed via Prometheus by enabling the configuration block:

```yaml
metrics:
  enabled: true
  listen: 0.0.0.0:9090
```

When enabled, the daemon starts an HTTP listener on the configured address and
serves Prometheus-compatible counters and histograms under `/metrics`.  The
environment of the health script receives `RC_METRICS_ENDPOINT` so checks can
optionally validate that scraping works as expected.

## Operational Guidance

- `reboot-coordinator run` listens for `SIGINT` and `SIGTERM` and exits
  gracefully, which allows service managers such as systemd to stop the daemon
  without forcing a reboot attempt mid-flight.
- When the loop encounters a runtime error (for example a transient etcd
  failure or health script timeout), the CLI prints a retry message and waits
  with an exponential backoff (5s doubling up to 1m by default) before trying
  again.

## Development Philosophy

The implementation emphasises:

- **Safety:** configuration validation, conservative defaults, and clear failure
  modes prevent accidental reboot storms.
- **Extensibility:** detectors and health checks are pluggable, enabling
  environment-specific policies.
- **Testability:** units for configuration parsing, detectors, and the health
  runner ensure correctness as features evolve.

Contributions should uphold these principles and follow the best practices laid
out in the PRD, especially around resilience, security, and maintainability.
