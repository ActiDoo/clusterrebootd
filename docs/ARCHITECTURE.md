# Architecture Overview

This document captures the initial architecture for the Cluster Reboot
Coordinator.  The daemon's mission is to guarantee serialized, policy-compliant
kernel reboots across a distributed cluster.

## Component Model

```
+-------------------------------+
|  clusterrebootd daemon        |
|                               |
|  +-------------------------+  |
|  | Detector Engine         |  |  -> Evaluates reboot-required signals via
|  +-------------------------+  |     pluggable detectors (file, command, API)
|  | Health Gate             |  |  -> Executes operator-provided script within
|  +-------------------------+  |     a bounded timeout; exit 0 permits reboot
|  | Orchestration Loop      |  |  -> Coordinates etcd lock acquisition, rechecks
|  +-------------------------+  |     pre- and post-lock, triggers reboot command
|  | Observability Layer     |  |  -> JSON logs, optional Prometheus metrics
|  +-------------------------+  |
+-------------------------------+
```

Supporting packages are structured under `pkg/` to keep core logic testable and
reusable:

- `pkg/config`: YAML configuration parsing, validation, and defaults.
- `pkg/detector`: Detector abstractions and implementations.
- `pkg/health`: Health-script execution with timeout enforcement and environment
  injection.
- `pkg/lock` (future): etcd v3 mutex handling with lease renewal.
- `pkg/metrics` (future): Prometheus collectors, JSON logging helpers.

The `cmd/clusterrebootd` binary wires the packages into a cohesive daemon
and exposes CLI helpers for validation and simulation.

## Configuration Lifecycle

1. **Load & Parse** – Config is read from YAML (default
   `/etc/clusterrebootd/config.yaml`).  Unknown fields are rejected to
   prevent silent misconfiguration.
2. **Defaulting** – Sensible defaults (TTL 90s, health timeout 30s, backoff 5–60s,
   metrics listener, kill-switch path) reduce boilerplate.
3. **Validation** – Ensures
   - Required identifiers (`node_name`, `health_script`, `lock_key`),
   - Detector schemas, including type-specific requirements,
   - Backoff and TTL bounds (TTL must exceed health timeout),
   - Cluster-policy sanity (fractions within (0, 1], absolute counts ≥ 0),
   - TLS artifacts present when TLS is enabled.

## Detector Engine

Detectors implement a simple interface returning whether a reboot is required.
Two detectors are implemented initially:

- **File Detector** – Checks for the existence of a marker file (e.g.
  `/var/run/reboot-required`).
- **Command Detector** – Executes a command and interprets the exit code.  The
  default policy treats any non-zero exit code as "reboot required", but the
  configuration can provide explicit exit codes for finer control.

Each detector supports per-run timeouts (for commands) to avoid hanging the
coordinator.  Future detectors may include package-manager integrations or API
probes.

Detector evaluations yield structured results capturing the reboot decision,
execution duration, and for command detectors the captured stdout/stderr and
exit codes.  This data powers the simulation CLI and will feed structured
logging in the orchestrator.

## Health Gate Execution

The health gate runs an operator-provided script.  The runner enforces:

- Absolute script path requirement (prevents accidental execution of relative
  paths).
- Configurable timeout with context cancellation.
- Controlled environment injection (base env + per-run additions such as node
  name or etcd endpoints).
- Result capture (exit code, stdout, stderr) for structured logging and
  observability.

The daemon will evaluate the script twice:

1. **Pre-lock** – quick rejection if cluster policies fail.
2. **Post-lock** – ensures the situation has not changed while holding the lock.

Non-zero exit codes are surfaced to the orchestration loop as a logical block,
while execution failures (timeout, permission, missing binary) are treated as
errors and logged accordingly.

## Orchestration Loop (Planned)

The core loop will:

1. Wait for a reboot requirement signal (detectors).
2. Acquire the global etcd mutex with exponential backoff and jitter.
3. Re-evaluate detectors to avoid stale signals.
4. Run the health gate script (pre- and post-lock) with context enriched by
   configuration (node name, fallback sets, etc.).
5. Honour kill switches and maintenance windows.
6. Issue the configured reboot command (default `shutdown -r now ...`).
7. Publish events/metrics and mark completion via `/run` file.

Crash resilience hinges on the etcd lease TTL being larger than the health
timeout.  The design includes watchdogs to ensure the lock is released promptly
if the process dies.

## Observability & Security

- **Logging** – The CLI wires the orchestrator reporter into a JSON logger that
  emits structured events (timestamps, node identity, detector summaries,
  health script output) to stderr for consumption by journald or log shippers.
- **Metrics** – A built-in Prometheus `/metrics` endpoint reports counters
  (reboots, blocked actions) and histograms (lock acquisition times); when
  enabled the listener address is exported via `RC_METRICS_ENDPOINT` for health
  scripts and probes.
- **Security** – TLS mutual auth for etcd, restricted RBAC prefix, SBOM &
  signature generation during packaging.

## Testing Strategy

- **Unit Tests** – Already cover configuration validation, detector semantics,
  and health script execution.  Future units will span locking, backoff, and
  metrics.
- **Integration** – Will add tests with embedded etcd, exercising lock
  contention, crash recovery, and script gating.
- **End-to-End** – Multi-node VM scenarios ensuring only one node reboots at a
  time and policies are enforced.
- **Chaos** – Fault injection (network partitions, latency, disk pressure) to
  validate resilience.

## Roadmap Snapshot

1. Implement orchestrator loop with etcd locking and health gating.
2. Add structured logging and metrics emission.
3. Provide packaging assets (deb/rpm) with systemd units and lifecycle scripts.
4. Integrate CI/CD pipeline (lint, test, build, package, sign, release).
5. Deliver documentation: install guides, runbooks, security checklists.

The current repository state focuses on creating strong, tested foundations for
steps 1–3.
