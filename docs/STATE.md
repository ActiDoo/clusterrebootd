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
- CLI also provides a `status` command that reuses the orchestrator runner in
  enforced dry-run mode to report detector, health, and lock readiness without
  triggering a reboot.  Optional `--skip-health` and `--skip-lock` flags allow
  operators to bypass the health script or etcd lock when running offline
  diagnostics.
- The orchestration loop now retries transient runtime failures with an
  exponential backoff and listens for SIGINT/SIGTERM so operators can stop the
  daemon cleanly when managed by service supervisors.
- A reproducible dev container (Go 1.22 with etcd 3.6.4 and nfpm 2.43.1) is
  available for local development, packaging experiments, and integration
  testing.
- CLI run mode now wires the reporter into a JSON logger on stderr and an
  optional Prometheus metrics listener, exporting the address to the health
  script environment for runtime validation.
- The health script base environment now includes cluster policy thresholds,
  fallback node lists, and configured maintenance windows so gating logic can
  enforce operator intent without re-reading the configuration file.
- Health script executions are annotated with runtime lock context via
  `RC_PHASE`, `RC_LOCK_ENABLED`, `RC_LOCK_HELD`, and `RC_LOCK_ATTEMPTS`, giving
  scripts enough detail to reason about contention and ensure post-lock checks
  remain valid without bespoke plumbing.
- A packaging blueprint (`docs/PACKAGING_BLUEPRINT.md`) documents the target
  systemd contract, filesystem layout, and `nfpm` packaging skeleton so
  implementation can proceed without revisiting foundational decisions.
- Repository now includes a `packaging/` skeleton with the `nfpm` configuration,
  systemd unit, tmpfiles entry, default config template, and Debian/RPM
  maintainer scripts described in the packaging blueprint, ready for build
  integration.
- Packaging assets are now exercised by automated tests that verify the config
  template's safe defaults, the systemd unit contract, maintainer script
  safeguards, and the `nfpm` file layout so regressions are caught early.
- A top-level `Makefile` now standardises local builds, cross-compilation for
  `amd64`/`arm64`, and wraps `nfpm` so developers can reproducibly stage
  binaries in `dist/` and generate `.deb`/`.rpm` packages without ad-hoc
  commands.
- An annotated example configuration (`examples/config.yaml`) and README
  guidance now show how to combine detectors, the health gate, metrics, and
  etcd TLS so operators have a concrete starting point before broader
  documentation lands.

- A CI pipeline blueprint (`docs/CI_PIPELINE.md`) and pinned GitHub Actions
  workflow now gate gofmt and `go test ./...` on pushes and pull requests,
  giving the repository its first automated quality gate.

## Next Up
- Extend the CI pipeline with static analysis (e.g. `go vet`, `staticcheck`)
  and caching once the baseline formatting and unit-test job has proven
  reliable.

## Backlog
- Establish complete CI/CD workflows covering linting, tests, packaging, SBOM,
  and signing after the initial pipeline slice proves out.
- Expand operator documentation beyond the sample config to include install
  guides, health script best practices, and troubleshooting once the reference
  example is reviewed.
- Design automated install/uninstall validation (e.g. container-based smoke
  tests) for the produced packages once the initial packaging skeleton is in
  place.

## Open Questions
- Finalise the exact etcd keyspace layout and RBAC policy for the distributed lock.
- Decide whether SUSE-specific detectors ship in v1 or remain optional.
