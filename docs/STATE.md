# Project State & Backlog

## Current State
- Core libraries for configuration parsing/validation, reboot detectors, and the
  health script runner are joined by an orchestrator runner that executes a
  single reboot pass with detector rechecks, kill-switch handling, and health
  gating through a pluggable lock manager (currently a no-op implementation).
- Detector engine aggregates per-detector results with timing and command output
  to support simulation, orchestration decisions, and CLI reporting.
- CLI offers `validate-config`, `simulate`, `run`, and `version`; `run`
  performs the single-pass orchestration flow and reports when a reboot would
  be triggered while leaving the reboot command execution stubbed for safety.
- A reproducible dev container (Go 1.22 with etcd 3.6.4) is available for local
  development and integration testing.

## Next Up
- Implement an etcd-backed lock manager and evolve the runner into the
  long-lived orchestration loop, including an execution path for the reboot
  command once safeguards are fully in place.
- Define structured logging fields and metrics scaffolding so the loop can emit
  observability data once integrated.

## Backlog
- Build observability surfaces (JSON logging defaults, Prometheus collectors).
- Add systemd service files and packaging assets for deb/rpm targets.
- Establish CI/CD workflows covering linting, tests, packaging, SBOM, and signing.
- Provide example configuration files and operator documentation.

## Open Questions
- Finalise the exact etcd keyspace layout and RBAC policy for the distributed lock.
- Decide whether SUSE-specific detectors ship in v1 or remain optional.
