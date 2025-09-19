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
- CLI exit codes now follow the PRD contract: health blocks return 3, lock
  contention returns 4, kill switches return 5, and the long-running `run`
  command propagates the last blocked outcome when it exits on a signal.
  Documentation covering the exit-code behaviour was added for operators.
- The orchestration loop now retries transient runtime failures with a
  jittered exponential backoff and listens for SIGINT/SIGTERM so operators can
  stop the daemon cleanly when managed by service supervisors.
- A reproducible dev container (Go 1.22 with etcd 3.6.4 and nfpm 2.43.1) is
  available for local development, packaging experiments, and integration
  testing.
- CLI run mode now wires the reporter into a JSON logger on stderr and an
  optional Prometheus metrics listener, exporting the address to the health
  script environment for runtime validation.
- The etcd-backed lock manager now writes JSON metadata (node name, PID, and
  acquisition timestamp) to the mutex key so operators can identify the
  current holder during incident response.
- The health script base environment now includes cluster policy thresholds,
  fallback node lists, and configured maintenance windows so gating logic can
  enforce operator intent without re-reading the configuration file.
- Health script executions are annotated with runtime lock context via
  `RC_PHASE`, `RC_LOCK_ENABLED`, `RC_LOCK_HELD`, and `RC_LOCK_ATTEMPTS`, giving
  scripts enough detail to reason about contention and ensure post-lock checks
  remain valid without bespoke plumbing.
- The orchestrator now enforces configured maintenance windows, short-circuiting
  orchestration passes during deny periods and requiring explicit allow matches
  when operators specify them.  The health script base environment still
  includes cluster policy thresholds, fallback node lists, and window
  definitions so custom checks observe the same schedule without re-reading the
  configuration file.
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
  workflow now restore module/build caches, run gofmt, `go vet`,
  `staticcheck`, and `go test ./...`, then build `.deb`/`.rpm` artefacts with
  SBOMs, checksums, and cosign signatures on every push/pull request,
  uploading the `dist/packages/` directory for review.
- The release workflow builds tagged revisions with the verified packaging
  toolchain, generates release notes from the commits since the previous tag,
  and uploads packages, SBOMs, checksums, and signatures directly to the
  corresponding GitHub Release while honouring optional cosign secrets for
  signing.

## Next Up
- Measure CI run times with caching/static analysis enabled and tune job
  parallelism or cache key strategy before layering heavier integration tests.
- Add containerised smoke tests that install the generated packages on target
  distributions to validate maintainer scripts and service wiring.

## Backlog
- Extend the release workflow with Sigstore/SLSA provenance once production
  signing keys are wired in.
- Expand operator documentation beyond the sample config to include install
  guides, health script best practices, and troubleshooting once the reference
  example is reviewed.
- Design automated install/uninstall validation (e.g. container-based smoke
  tests) for the produced packages once the initial packaging skeleton is in
  place.

## Open Questions
- Finalise the exact etcd keyspace layout and RBAC policy for the distributed lock.
- Decide whether SUSE-specific detectors ship in v1 or remain optional.
