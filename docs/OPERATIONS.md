# Operations Guide

The Cluster Reboot Coordinator guards production clusters against unsafe rolling
reboots.  This guide describes how operators install the daemon, tune the
configuration, implement health checks, and run day-to-day operations without
sacrificing safety, resilience, or performance.

## Installation Paths

### Using the provided packages

The packaging blueprint and Makefile generate Debian and RPM artefacts that
match the systemd contract documented in `docs/PACKAGING_BLUEPRINT.md`.  Build or
retrieve the packages, copy them to the target host, and install with the native
package manager, for example:

```bash
# Debian/Ubuntu
sudo dpkg -i clusterrebootd_*.deb

# RHEL/Alma/Rocky
sudo rpm -Uvh clusterrebootd-*.rpm
```

The maintainer scripts create `/etc/clusterrebootd/`, install a commented
configuration template, and register the systemd unit without enabling it.  The
service is only started after the operator enables it manually with
`systemctl enable --now clusterrebootd` once the configuration has been
reviewed.

### From source (early testing)

For lab environments or continuous integration jobs you can run the binary
without packaging:

```bash
go build ./cmd/clusterrebootd
sudo install -m 0755 clusterrebootd /usr/local/bin/
```

Create `/etc/clusterrebootd/` manually, copy `examples/config.yaml`, adjust
it for your environment, and run the daemon with `clusterrebootd run
--config /etc/clusterrebootd/config.yaml`.  The supplied systemd unit under
`packaging/systemd/` can also be installed manually when needed.

## Configuration Workflow

1. **Start from the example** – Copy `examples/config.yaml` and set
   `node_name`, detector definitions, and the reboot command that applies to the
   host.  File detectors watch for the presence of a path while command
   detectors interpret the exit code and inherit per-run timeouts.【F:examples/config.yaml†L1-L37】
2. **Define the health script** – Point `health_script` at an absolute path and
   configure `health_timeout_sec` so the runner can cancel long-running checks.
   The orchestration loop executes the script before and after lock acquisition,
  injecting context that indicates the current phase and lock state.【F:examples/config.yaml†L39-L55】【F:pkg/orchestrator/runner.go†L485-L500】
3. **Configure the distributed lock** – Supply at least one etcd endpoint,
   namespace, and `lock_key`; ensure `lock_ttl_sec` exceeds the health timeout so
   the lease outlives the slowest permissible health check.  Enable mutual TLS by
   providing the CA, client certificate, and key when required.【F:examples/config.yaml†L57-L83】【F:cmd/clusterrebootd/main.go†L218-L251】【F:pkg/config/config.go†L70-L117】
4. **Throttle cluster-wide reboots** – Set `min_reboot_interval_sec` to enforce a
   cluster-wide cooldown after each successful reboot.  The orchestrator records
   the interval in etcd and refuses new reboot attempts until the window expires,
   preventing back-to-back maintenance events.【F:examples/config.yaml†L23-L30】【F:cmd/clusterrebootd/main.go†L233-L272】【F:pkg/orchestrator/runner.go†L311-L376】
5. **Set cluster policies and maintenance windows** – `cluster_policies`
   expresses minimum healthy nodes and fallback protections.  Maintenance windows
   allow operators to block or explicitly permit reboots using cron-like day/time
   ranges; deny rules always win, while allow rules opt the coordinator into the
   listed windows.【F:examples/config.yaml†L85-L112】【F:pkg/windows/windows.go†L1-L123】
6. **Wire observability and safety toggles** – Define `kill_switch_file` so a
   single touch blocks reboots, and enable the Prometheus listener via
   `metrics.enabled`/`metrics.listen` when metrics are required.【F:examples/config.yaml†L41-L47】【F:examples/config.yaml†L114-L118】【F:cmd/clusterrebootd/main.go†L193-L252】
7. **Validate** – Run `clusterrebootd validate-config --config
   /etc/clusterrebootd/config.yaml` to fail fast on schema or semantic
   mistakes.  Follow up with `clusterrebootd simulate` to execute the
   detectors once and review their output without contacting etcd or running the
   health script.【F:cmd/clusterrebootd/main.go†L411-L472】

The configuration loader enforces sane defaults and rejects common mistakes such
as missing node names, empty detector lists, inverted backoff windows, and lock
TTLs that are shorter than the health timeout so misconfiguration is caught
before deployment.【F:pkg/config/config.go†L36-L171】

## Health Script Best Practices

Health scripts are the final safeguard before a reboot.  Follow these practices:

- **Keep scripts idempotent and fast** – They must complete before
  `health_timeout_sec` and tolerate being re-run after lock acquisition.
- **Require absolute paths and strict permissions** – Store scripts in a
  directory that is only writable by trusted operators and mark them executable.
- **Use the injected environment** – The coordinator exports static context such
  as `RC_NODE_NAME`, `RC_DRY_RUN`, `RC_LOCK_KEY`, etcd endpoints, kill switch
  location, cluster policy thresholds, fallback node list, and maintenance
  windows so scripts do not need to re-read the YAML file.【F:pkg/config/config.go†L230-L263】
- **React to runtime hints** – Each invocation adds `RC_PHASE` (`pre-lock` or
  `post-lock`), `RC_LOCK_ENABLED`, `RC_LOCK_HELD`, and `RC_LOCK_ATTEMPTS` so
  scripts can distinguish dry runs, skipped locks, and contention scenarios.
  Diagnostics invoked with `status --skip-health` or `--skip-lock` set
  `RC_SKIP_HEALTH`/`RC_SKIP_LOCK` to `true`, allowing scripts to short-circuit
  optional checks when operators intentionally bypass them.【F:cmd/clusterrebootd/main.go†L298-L305】【F:pkg/orchestrator/runner.go†L485-L500】
- **Return meaningful exit codes** – Exit `0` to allow the reboot, non-zero to
  block it.  Write concise status details to stdout/stderr; they are captured in
  the JSON logs and CLI output for incident response.【F:cmd/clusterrebootd/main.go†L482-L517】

Before deploying a new script, execute it manually on a staging host and run
`clusterrebootd status --skip-lock` to verify it behaves as expected.

## Running and Managing the Daemon

The CLI exposes commands for validation, dry-run orchestration, and steady-state
operation:

- `run` – Starts the long-lived loop.  `--dry-run` forces the daemon to skip the
  reboot command, while `--once` performs a single iteration for diagnostics.
- `validate-config` – Parses and validates the configuration file, returning
  exit code `2` on failure.
- `simulate` – Runs the detectors once, prints their outputs, and exits without
  contacting etcd or invoking the health script.  Detector errors trigger exit
  code `6`.
- `status` – Executes a dry-run orchestration pass, reporting detector and health
  results and attempting to acquire the lock once.  `--skip-health` and
  `--skip-lock` bypass the corresponding steps when debugging isolated issues.

Exit codes also surface operational states: `3` indicates the health gate
blocked, `4` means the lock was unavailable, and `5` is returned when the kill
switch is active.【F:cmd/clusterrebootd/main.go†L18-L53】【F:cmd/clusterrebootd/main.go†L251-L356】

### Systemd lifecycle

After editing the configuration run `systemctl daemon-reload` (when the unit file
changes) and start the service with `systemctl enable --now`.  Use `journalctl
-u clusterrebootd` to inspect structured JSON logs emitted by the daemon.
The packaged unit honours the kill switch and restarts on failure with a small
backoff, aligning with the orchestrator's internal retry logic.【F:docs/PACKAGING_BLUEPRINT.md†L65-L111】

## Observability and Diagnostics

- **Logs** – The orchestrator streams JSON events to stderr; under systemd they
  appear in the journal.  Each log includes detector outcomes, health script
  results, lock acquisition metadata, and the final outcome so operations teams
  can reconstruct each decision.【F:cmd/clusterrebootd/main.go†L178-L206】【F:pkg/orchestrator/runner.go†L129-L211】
- **Metrics** – When enabled, the Prometheus collector listens on the configured
  socket and exposes counters and histograms that summarise orchestration
  results.  The listener address is exported to health scripts through
  `RC_METRICS_ENDPOINT` for optional self-checks.【F:cmd/clusterrebootd/main.go†L193-L252】
- **Dry-run diagnostics** – `clusterrebootd status` prints detector and
  health results, highlights whether the lock was acquired, and shows the planned
  reboot command.  The final status string mirrors the exit code so scripts and
  automation can react consistently.【F:cmd/clusterrebootd/main.go†L314-L356】

## Maintenance and Upgrades

- **Immediate stop** – Create the kill switch file (`touch
  /etc/clusterrebootd/disable`) to block reboots across future iterations.
  Remove the file to resume operations; the daemon re-checks on the next pass.
- **Planned work** – Deny windows prevent orchestration during sensitive periods
  while allow windows strictly constrain when reboots may occur.  Update the
  configuration and reload the service to apply new schedules.【F:pkg/windows/windows.go†L29-L123】
- **Upgrades** – Run `make package` to produce fresh packages, install them, and
  confirm `clusterrebootd validate-config` still succeeds.  Use
  `status --skip-lock` on individual nodes to validate detectors and health
  scripts before removing the kill switch.

## Troubleshooting

| Symptom | Likely Cause | Remediation |
|---------|--------------|-------------|
| CLI exits with code `2` and `invalid configuration` | Schema or semantic error (e.g. missing node name, TTL too small) | Run `validate-config` and fix the listed problems; the loader aggregates all validation failures to minimise iterations.【F:pkg/config/config.go†L90-L171】 |
| `status` reports `health_blocked` with non-zero exit codes | Health script failed or timed out | Review stdout/stderr in the command output, inspect the script logs, and adjust cluster policy checks or timeouts as needed.【F:cmd/clusterrebootd/main.go†L482-L517】 |
| `status` reports `lock_unavailable` | etcd unreachable or contended | Confirm network reachability, validate TLS credentials, and inspect the lock key metadata (node, PID, timestamp) to identify the current holder before retrying.【F:cmd/clusterrebootd/main.go†L193-L252】【F:pkg/orchestrator/runner.go†L133-L211】 |
| Orchestration skipped with `window_denied`/`window_outside_allow` | Current time falls inside a deny window or outside all allow windows | Adjust the `windows` expressions or wait for the next permitted slot; the decision is also exported to the health script via maintenance window environment variables.【F:pkg/windows/windows.go†L29-L123】【F:pkg/config/config.go†L230-L263】 |
| Metrics server fails to start | Address already in use or invalid listen string | Update `metrics.listen` to a free address/port combination and restart the daemon; the listener prints an error during startup when binding fails.【F:cmd/clusterrebootd/main.go†L193-L252】 |

## Additional References

- [Architecture Overview](ARCHITECTURE.md)
- [Packaging Blueprint](PACKAGING_BLUEPRINT.md)
- [CI Pipeline Blueprint](CI_PIPELINE.md)
- [Sample Configuration](../examples/config.yaml)

Keep `docs/STATE.md` up-to-date with operational learnings and follow-up work so
future contributors understand which scenarios still need documentation or
automation.
