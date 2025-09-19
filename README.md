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

## Documentation

- [Architecture Overview](docs/ARCHITECTURE.md) explains the daemon's component
  model and roadmap.
- The new [Operations Guide](docs/OPERATIONS.md) describes installation,
  configuration workflows, health script practices, and troubleshooting for
  production rollouts.
- [Packaging Blueprint](docs/PACKAGING_BLUEPRINT.md) and
  [CI Pipeline Blueprint](docs/CI_PIPELINE.md) capture the packaging and
  automation contracts that releases adhere to.

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
- `make package` cross-compiles the binary for `amd64` and `arm64`, invokes
  [`nfpm`](https://nfpm.goreleaser.com/) to produce `.deb` and `.rpm` packages in
  `dist/packages/`, generates CycloneDX SBOMs via
  [`syft`](https://github.com/anchore/syft) under `dist/packages/sbom/`, writes
  SHA-256/512 manifests to `dist/packages/checksums/` (plus aggregated
  `SHA256SUMS`/`SHA512SUMS` files), and produces cosign signatures in
  `dist/packages/signatures/` when a signing key is provided.

Set `ARCHES=amd64` (or `arm64`) to restrict the architectures, override
`VERSION` to package a specific release string, or point `NFPM` to an alternate
`nfpm` binary when developing inside containers.  The packaging target expects
`nfpm`, `syft`, and `cosign` to be on the `PATH`; pass
`SIGNING_KEY=/path/to/cosign.key` and
`SIGNING_PUBKEY=/path/to/cosign.pub` to sign artefacts with your production
keypair.  Export `COSIGN_PASSWORD` (and optionally `COSIGN_YES=true`) when using
password-protected keys so signing runs non-interactively.  The helper script
`packaging/scripts/verify_artifacts.sh` reruns the
checksum and signature validation when given the public key via
`COSIGN_PUBLIC_KEY=/path/to/cosign.pub`.

Generated artefacts live under `dist/` and are ignored by git so developers can
cleanly iterate on builds and packages without polluting commits.

## Continuous Integration

A GitHub Actions workflow (documented in `docs/CI_PIPELINE.md`) now runs `gofmt`
and `go test ./...` for pushes to `main` and all pull requests, then builds the
Debian/RPM packages via `make package`.  The packaging job installs pinned
versions of `nfpm`, `syft`, and `cosign`, generates SBOMs, checksums, and
cosign signatures, verifies them with
`packaging/scripts/verify_artifacts.sh`, and uploads the resulting artefacts for
review.  All actions are pinned by commit SHA and the workflow continues to use
read-only permissions to preserve repository integrity.

Tagging a commit with `v*` (or invoking the release workflow manually) now
builds the packages again, generates release notes from the commits since the
previous release via GitHub's API, and publishes the packages, SBOMs, checksum
manifests, and optional cosign signatures directly to the GitHub Release.
Provide `RELEASE_COSIGN_KEY`/`RELEASE_COSIGN_PUB` secrets (base64-encoded) and a
`RELEASE_COSIGN_PASSWORD` secret when signing should be enabled; otherwise the
workflow ships unsigned artefacts while still verifying checksums and SBOMs.

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
  `RC_CLUSTER_FALLBACK_NODES`.  The boolean flag is always exported (set to
  `false` when unset) to avoid scripts having to special-case missing keys. 
  Cron-like `windows.deny`
  expressions short-circuit the orchestration loop before detector execution
  while `windows.allow` restricts runs to explicitly approved slots.  The
  configured windows are also exported to the health script environment via
  `RC_WINDOWS_ALLOW` and `RC_WINDOWS_DENY` so scripts can remain consistent with
  the daemon's enforcement.
- Distributed coordination: three etcd endpoints, an explicit namespace,
  lock key, and optional mutual TLS credentials.
- Observability: metrics listener enabled so the daemon injects
  `RC_METRICS_ENDPOINT` alongside `RC_NODE_NAME`, `RC_LOCK_KEY`,
  `RC_ETCD_ENDPOINTS`, and `RC_KILL_SWITCH_FILE` into the health script
  environment.

Copy the file, update endpoints, file paths, and policy thresholds, and then run
`reboot-coordinator validate-config --config /path/to/config.yaml` to confirm
the configuration is accepted before rolling it out.

### Health Script Environment

The CLI initialises the health runner with configuration context so scripts do
not need to re-parse the YAML on every invocation.  Static entries include:

- `RC_NODE_NAME`, `RC_DRY_RUN`, `RC_LOCK_KEY`, `RC_ETCD_ENDPOINTS`,
  `RC_KILL_SWITCH_FILE`
- Cluster policy hints exposed via `RC_CLUSTER_MIN_HEALTHY_FRACTION`,
  `RC_CLUSTER_MIN_HEALTHY_ABSOLUTE`,
  `RC_CLUSTER_FORBID_IF_ONLY_FALLBACK_LEFT`, and `RC_CLUSTER_FALLBACK_NODES`
- Maintenance windows provided through `RC_WINDOWS_ALLOW` and
  `RC_WINDOWS_DENY`
- Metrics exposure via `RC_METRICS_ENDPOINT` when the listener is enabled
- Optional skip hints (`RC_SKIP_HEALTH`, `RC_SKIP_LOCK`) when diagnostic flags
  are used

Each execution is further annotated with runtime context:

- `RC_PHASE` (`pre-lock` or `post-lock`)
- `RC_LOCK_ENABLED` indicating whether the runner will attempt to hold the
  distributed lock
- `RC_LOCK_HELD` capturing whether the current invocation is under lock
- `RC_LOCK_ATTEMPTS` showing how many acquisition attempts were needed before
  the script ran (zero before lock acquisition)

These values allow operators to write a single health script that can reason
about configuration, maintenance windows, and the current orchestration state
without shelling out to auxiliary utilities.

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

#### Etcd lock metadata

When the coordinator acquires the distributed mutex it overwrites the lock key
with a JSON object describing the holder.  Operators can inspect the key via
`etcdctl` or the API to confirm which node is currently rebooting and when it
claimed the lease:

```json
{"node":"node-a","pid":1234,"acquired_at":"2024-03-07T11:45:12.123Z"}
```

`node` reflects the configured `node_name`, `pid` is the coordinator process
ID, and `acquired_at` is the RFC3339 timestamp recorded when the lock was
obtained.

When enabled, the daemon starts an HTTP listener on the configured address and
serves Prometheus-compatible counters and histograms under `/metrics`.  The
environment of the health script receives `RC_METRICS_ENDPOINT` so checks can
optionally validate that scraping works as expected.

## Operational Guidance

Operators looking for end-to-end deployment and maintenance steps should follow
the [Operations Guide](docs/OPERATIONS.md), which expands on the highlights
below with detailed runbooks and troubleshooting advice.

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
