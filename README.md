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

1. Ensure Go 1.22 or newer is installed.
2. Fetch module dependencies and run the tests:

   ```bash
   go test ./...
   ```

3. Create a configuration file (see `examples/config.yaml` to be added in a
   future iteration) or adapt the PRD sample.  The CLI defaults to
   `/etc/reboot-coordinator/config.yaml` but accepts an explicit `--config` flag.

### CLI Commands

The CLI currently offers early validation and introspection helpers:

- `reboot-coordinator validate-config --config /path/to/config.yaml`
  Validates the configuration schema, defaulting missing fields and ensuring
  semantic requirements such as TTL bounds.
- `reboot-coordinator simulate --config /path/to/config.yaml`
  Loads the configuration, instantiates detectors, executes them once, and
  prints a summary with per-detector results without interacting with etcd or
  rebooting the host.
- `reboot-coordinator version`
  Prints the build version string.
- `reboot-coordinator run`
  Executes a single orchestration pass: evaluates detectors, runs the health
  script, attempts to acquire the distributed lock, and reports whether a
  reboot would be triggered.  The current build stops short of executing the
  reboot command, even when prerequisites are satisfied, to keep the loop
  safe while the reboot executor is implemented.

Future milestones will add the long-running coordinator loop, integration with
etcd locking, systemd units, packaging, observability, and the full CI/CD
pipeline described in the PRD.

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
