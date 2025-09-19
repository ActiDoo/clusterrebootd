# CI Pipeline Blueprint

This document captures the foundational CI/CD workflow that will guard the
Cluster Reboot Coordinator repository.  The current focus is stage one of the
pipeline, which ensures that formatting and unit tests gate every change before
it merges.  Subsequent stages (linting, packaging, SBOM/signature generation)
will build on the same framework once the orchestrator stabilises further.

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

## Security, Stability, and Performance Considerations

- **Supply Chain:** All reusable actions are referenced by commit SHA rather than
  floating tags, preventing unreviewed upstream changes from altering the
  workflow unexpectedly.  We will monitor upstream releases and update the
  commits alongside changelog review.
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
- Add packaging smoke tests that build the Debian/RPM artefacts inside
  containers, reusing the existing `Makefile` targets.
- Generate SBOMs and verify signatures as part of release workflows before
  publishing artefacts.
- Integrate integration tests against the dev-container etcd instance to cover
  lock acquisition and health gate behaviour.
