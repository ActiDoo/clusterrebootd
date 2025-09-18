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
```

Additional directories such as `deploy/`, `scripts/`, and packaging assets will
be introduced as the orchestrator matures.

## Getting Started

1. Ensure Go 1.23 or newer is installed.
2. Fetch module dependencies and run the tests:

   ```bash
   go test ./...
   ```

3. Create a configuration file (see `examples/config.yaml` to be added in a
   future iteration) or adapt the PRD sample.  The CLI defaults to
   `/etc/reboot-coordinator/config.yaml` but accepts an explicit `--config` flag.

## Development Container

The repository ships with a [VS Code Dev Container](https://containers.dev/)
definition under `.devcontainer/`.  It builds on the official Go 1.22 base
image, upgrades the underlying packages, and installs etcd v3.6.4 so integration
tests can run against a recent upstream release without additional manual setup.

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
