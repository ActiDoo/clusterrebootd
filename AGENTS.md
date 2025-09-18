# Agent Guidelines

These instructions govern the entire repository.  Follow them for every
modification unless a more specific `AGENTS.md` overrides them.

## 1. Always orient yourself
- Before changing code or docs, refresh your understanding of `docs/ARCHITECTURE.md`,
  `PRD.md`, and `README.md`.  Confirm the planned product direction and check
  whether your change fits the staged roadmap they describe.
- Capture the purpose of your change up front.  If it affects domain logic,
  detectors, locking, or health gating, plan the corresponding tests and
  documentation updates before you begin.

## 2. Evolve the product step-by-step
Treat the delivery as an incremental journey toward the PRD goals:
1. **Foundations (current focus)** – keep configuration, detectors, and the health
   runner solid and well-tested.  Avoid regressing existing behaviour.
2. **Orchestration Loop** – when implementing locking, health re-checks, and reboot
   execution, build them behind well-defined interfaces, add integration tests, and
   document new configuration flags.
3. **Operational Hardening** – observability, packaging, systemd integration, and
   security controls follow once the loop is reliable.  Extend the codebase without
   breaking earlier stages.
4. **Release & Automation** – CI/CD, SBOM, signatures, and artefact pipelines round
   out the product.  Keep workflows reproducible and well-documented.

When adding features, state explicitly which stage you are advancing and ensure all
prerequisites from earlier stages remain satisfied.

## 3. Track state and open TODOs
- Maintain the canonical backlog in `docs/STATE.md`.  If you make a change that
  advances progress, introduces a new follow-up, or closes a TODO, update the
  relevant section (Current State, Next Up, Backlog, Open Questions) in the same
  commit.
- If `docs/STATE.md` is missing when you start a task, create it with the above
  sections before proceeding.
- Inline TODO comments in code must have a matching entry in `docs/STATE.md` so
  future agents can see the full picture.

## 4. Quality, safety, and resilience first
- Evaluate security, stability, resilience, and performance implications for every
  change.  Prefer explicit configuration and graceful degradation over quick hacks.
- Add or update tests whenever behaviour changes.  Domain logic without tests is not
  acceptable.
- Keep the code idiomatic Go: run `gofmt` on modified files and execute `go test ./...`
  before finishing.  Fix lint/test failures rather than suppressing them.

## 5. Documentation & communication
- Update `README.md`, architecture docs, and examples when behaviour, CLI flags, or
  configuration change.
- Summarise meaningful design decisions in commit messages and pull request bodies
  so future contributors understand the rationale.

Following these rules keeps the project maintainable and aligned with the long-term
product vision.
