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
- The orchestrator keeps the etcd lease held until the reboot command is
  invoked, preventing concurrent node reboots while exposing an explicit lock
  release hook for diagnostic and dry-run workflows.
- Reboot orchestration enforces an operator-defined cooldown between
  successful reboots by persisting a short-lived marker in etcd; nodes skip
  new reboot attempts until the interval expires so clusters cannot drain too
  quickly.【F:pkg/orchestrator/runner.go†L311-L376】【F:cmd/clusterrebootd/main.go†L233-L272】
- The health script base environment now includes cluster policy thresholds,
  fallback node lists, and configured maintenance windows so gating logic can
  enforce operator intent without re-reading the configuration file.
- Cluster health coordination now records unhealthy nodes in etcd so any peer
  that detects a reboot requirement blocks until the failing node reports a
  healthy script outcome again.  The daemon runs the gate script even when no
  reboot is pending and a dedicated heartbeat loop publishes the latest result
  according to `health_publish_interval_sec`, keeping the shared view fresh while
  applying the configured cluster policy thresholds to prevent cascading outages
  when the cluster is already degraded.【F:pkg/clusterhealth/etcd.go†L18-L153】【F:pkg/orchestrator/runner.go†L321-L469】【F:pkg/orchestrator/health_publisher.go†L1-L96】【F:cmd/clusterrebootd/main.go†L233-L305】
- Reboot command execution now expands the same environment placeholders (e.g.
  `RC_NODE_NAME`) so the logged and invoked command reflects the active node
  context without depending on shell-specific substitution.
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
- Packaging tests now build the Debian artefact with `nfpm` and assert the
  binary, systemd unit, configuration template, and tmpfiles entry are packaged
  correctly so overrides cannot silently drop essential files during installs.
- Containerised smoke tests now build the Debian and RPM packages via the `nfpm`
  configuration and install them inside Debian, Ubuntu, and Rocky Linux
  containers.  The suite validates package manager integration, ensures the
  binary starts, boots a transient systemd instance with a drop-in override to
  confirm the packaged unit launches successfully, and confirms assets land at
  the expected paths while gracefully skipping when no container runtime is
  available.
- A top-level `Makefile` now standardises local builds, cross-compilation for
  `amd64`/`arm64`, and wraps `nfpm` so developers can reproducibly stage
  binaries in `dist/` and generate `.deb`/`.rpm` packages without ad-hoc
  commands.
- An annotated example configuration (`examples/config.yaml`) and README
  guidance now show how to combine detectors, the health gate, metrics, and
  etcd TLS so operators have a concrete starting point before broader
  documentation lands.
- A comprehensive operations guide (`docs/OPERATIONS.md`) now walks operators
  through installation paths, configuration workflows, health script practices,
  observability, maintenance operations, and troubleshooting so rollouts do not
  rely on tribal knowledge.

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
- Implement the SIGHUP-driven configuration reload path (FR-CF-2): design which
  fields can be hot-reloaded, add runner/orchestrator hooks, document operator
  expectations, and cover the behaviour with unit tests.
- Measure CI run times with caching/static analysis enabled and tune job
  parallelism or cache key strategy before layering heavier integration tests.
- Integrate the containerised smoke tests into CI so packaging regressions are
  detected automatically once runner support for Docker/Podman is provisioned.

## Backlog
- Extend the release workflow with Sigstore/SLSA provenance once production
  signing keys are wired in.
- Implement the post-reboot marker described in FR-SD-3 so successful reboots
  leave an auditable timestamp under `/run/clusterrebootd`.

## Open Questions
- Finalise the exact etcd keyspace layout and RBAC policy for the distributed lock.
- Decide whether SUSE-specific detectors ship in v1 or remain optional.
