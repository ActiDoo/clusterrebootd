# CI Pipeline Blueprint

This document captures the foundational CI/CD workflow that will guard the
Cluster Reboot Coordinator repository.  The pipeline now encompasses two
operational stages: the formatting/unit-test gate and a packaging job that
produces reviewable supply-chain artefacts (SBOM, checksums, cosign signatures)
for every push or pull request.  Subsequent stages (linting, integration
testing, publication automation) will build on the same framework once the
orchestrator stabilises further.

## Platform Choice

We will run the pipeline on **GitHub Actions** for the following reasons:

- It integrates directly with the hosting platform so pull requests and branch
  protections can rely on native status checks without extra plumbing.
- Ubuntu runners provide first-class support for Go toolchains, simplifying
  upgrades to new language releases without maintaining bespoke images.
- The marketplace offers vetted, open-source actions for setup, caching, and
  notifications.  We can pin them to immutable commits to satisfy supply-chain
  hardening requirements while still receiving upstream security updates by
  bumping the pinned revisions.
- Secrets management and per-branch protections are built-in, which will be
  valuable once publishing, signing, and SBOM stages come online.

## Stage 1 – Format and Test Gate

The first workflow stage enforces baseline hygiene checks across pushes to the
`main` branch and incoming pull requests.

- **Trigger:** `push` events targeting `main` and all `pull_request` events.
- **Environment:** Ubuntu 22.04 runners with Go `1.23.x`, matching the module's
  declared minimum version.
- **Steps:**
  1. Check out the repository using a pinned `actions/checkout` commit.
  2. Install the toolchain with `actions/setup-go`, pinned to a published
     commit and configured to install the latest Go `1.23` patch.
  3. Run a formatting guard that executes `gofmt -l` against the tracked Go
     sources (via `git ls-files '*.go'`) and fails if any files require
     formatting.
  4. Execute `go test ./...` to run the unit suite.

The formatting script exits early when the repository tracks no Go sources so
that ancillary documentation-only changes do not fail spuriously.  The job caps
runtime at 15 minutes to prevent hung builds from tying up runners indefinitely.

## Stage 2 – Packaging and Supply-Chain Artefacts

The second workflow stage validates the release pipeline by building Debian and
RPM packages for each supported architecture, producing SBOMs, and validating
signatures/checksums.  It runs after the format/test gate succeeds and surfaces
artefacts for human review via the workflow summary.

- **Trigger:** Same as stage one; executes on every push to `main` and all pull
  requests so supply-chain artefacts are continuously exercised.
- **Toolchain provisioning:** The job downloads pinned versions of `nfpm`
  (`v2.37.0`), `syft` (`v1.14.0`), and `cosign` (`v2.2.4`).  Release checksums
  are verified before the binaries are installed into the runner’s `~/bin`
  directory to guard against tampering.
- **Signing keys:** CI generates an ephemeral cosign key pair
  (`ci.cosign.key`/`ci.cosign.pub`) purely for verification exercises.  Real
  release workflows can override `SIGNING_KEY`/`SIGNING_PUBKEY` to use managed
  keys.  The job sets `COSIGN_PASSWORD` and `COSIGN_YES=true` so signing runs
  fully unattended during automation.
- **Build:** `make package SIGNING_KEY=… SIGNING_PUBKEY=…` cross-compiles the
  daemon for `amd64` and `arm64`, runs `nfpm` for both `.deb` and `.rpm`, and
  produces:
  - CycloneDX SBOMs via `syft`, stored under `dist/packages/sbom/` with the
    pattern `<artefact>.sbom.cyclonedx-json`.
  - Per-artefact SHA-256/512 manifests in `dist/packages/checksums/`, plus
    aggregated `SHA256SUMS`/`SHA512SUMS` manifests at the package root.
  - Cosign signatures in `dist/packages/signatures/` when a signing key is
    provided.
- **Verification:** `packaging/scripts/verify_artifacts.sh` replays
  `sha256sum/sha512sum`, ensures SBOMs parse as JSON, and uses
  `cosign verify-blob` with the provided public key to validate signatures.  Any
  mismatches halt the job.
- **Artefact publishing:** The entire `dist/packages/` directory (packages,
  SBOMs, checksums, signatures, and public key copy) is uploaded via
  `actions/upload-artifact` for manual inspection.

## Security, Stability, and Performance Considerations

- **Supply Chain:** All reusable actions are referenced by commit SHA rather than
  floating tags, preventing unreviewed upstream changes from altering the
  workflow unexpectedly.  We will monitor upstream releases and update the
  commits alongside changelog review.
- **Tool integrity:** Packaging binaries (`nfpm`, `syft`, `cosign`) are installed
  only after their published checksums are validated, ensuring the build job
  executes with trusted tooling.
- **Permissions:** The workflow requests `contents: read` only, which is
  sufficient for status reporting and disallows repository modifications from
  the CI job context.
- **Determinism:** The job uses a single runner image and a fixed Go version
  family to avoid behaviour drift between contributors.  We will expand the
  matrix (for example to exercise multiple Go releases) only when necessary.
- **Resilience:** Explicit timeouts and minimal dependencies reduce external
  points of failure.  Future enhancements can add module caching once run-time
  characteristics are measured to ensure caches do not mask correctness issues.

## Roadmap for Later Stages

- Introduce static analysis (`go vet`, `staticcheck`) in a dedicated stage once
  the baseline job is stable.
- Layer containerised smoke tests that install the produced packages inside
  representative distributions to validate maintainer scripts and service
  wiring.
- Extend release automation to publish artefacts and provenance to GitHub
  Releases, wiring in cosign attestations once production signing keys are
  available.
- Integrate integration tests against the dev-container etcd instance to cover
  lock acquisition and health gate behaviour.
