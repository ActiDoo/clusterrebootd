# Project State & Backlog

## Current State
- Core libraries for configuration parsing/validation, reboot detectors, and the
  health script runner exist but the orchestrator loop is not yet implemented.
- Detector engine aggregates per-detector results with timing and command output
  to support simulation and future orchestration decisions.
- CLI offers `validate-config`, `simulate`, and `version`; `run` is a placeholder.

## Next Up
- Implement the orchestration loop with etcd locking, detector rechecks, and health
  gate enforcement as described in `docs/ARCHITECTURE.md`.
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
