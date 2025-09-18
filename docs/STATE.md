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
- The orchestration loop now retries transient runtime failures with an
  exponential backoff and listens for SIGINT/SIGTERM so operators can stop the
  daemon cleanly when managed by service supervisors.
- A reproducible dev container (Go 1.22 with etcd 3.6.4) is available for local
  development and integration testing.
- CLI run mode now wires the reporter into a JSON logger on stderr and an
  optional Prometheus metrics listener, exporting the address to the health
  script environment for runtime validation.
- Metrics listener lifecycle now streams serve errors to stderr as they occur
  and guarantees graceful shutdown so observability regressions surface
  immediately during orchestration.

## Next Up
- Implement the `status` CLI command to expose current lock state and last
  orchestration outcome for operators without parsing logs.

## Backlog
- Add systemd service files and packaging assets for deb/rpm targets.
- Establish CI/CD workflows covering linting, tests, packaging, SBOM, and signing.
- Provide example configuration files and operator documentation.

## Open Questions
- Finalise the exact etcd keyspace layout and RBAC policy for the distributed lock.
- Decide whether SUSE-specific detectors ship in v1 or remain optional.
