# Project State & Backlog

## Current State
- Core libraries for configuration parsing/validation, reboot detectors, and the
  health script runner are joined by an orchestrator runner and loop that perform
  detector rechecks, honour the kill switch, enforce health gating, and, when
  prerequisites succeed, execute the configured reboot command via the long-lived
  coordinator loop.
- Detector engine aggregates per-detector results with timing and command output
  to support simulation, orchestration decisions, and CLI reporting.
- CLI offers `validate-config`, `simulate`, `run`, and `version`; `run` now drives
  the long-lived orchestration loop while `--once` preserves the diagnostic
  single-pass flow for smoke tests.
- A reproducible dev container (Go 1.22 with etcd 3.6.4) is available for local
  development and integration testing.
- Observability scaffolding emits structured events and metric observations via a
  pluggable reporter so logs and counters can be wired into future sinks without
  touching orchestration logic.

## Next Up
- Harden the loop with graceful error handling (transient retry strategy,
  signal-aware shutdown) and document operational guidance for running it as a
  daemon.
- Wire the reporter into concrete sinks (JSON logs, Prometheus collectors) and
  expose the metrics endpoint described in the PRD.

## Backlog
- Build observability surfaces (JSON logging defaults, Prometheus collectors).
- Add systemd service files and packaging assets for deb/rpm targets.
- Establish CI/CD workflows covering linting, tests, packaging, SBOM, and signing.
- Provide example configuration files and operator documentation.

## Open Questions
- Finalise the exact etcd keyspace layout and RBAC policy for the distributed lock.
- Decide whether SUSE-specific detectors ship in v1 or remain optional.
