# ADR-0007: A single Go binary, with SQLite by default and PostgreSQL for HA

- Status: Proposed
- Date: 2026-09-25
- Related: [§2.6 Process model](../design/02-architecture.md#26-process-model-and-deployment-topologies),
  [§2.7 Technology stack](../design/02-architecture.md#27-technology-stack)

## Context

Homelab installations need a small footprint and a trivial install. SMEs need
high availability. The core of nodr edits HCL while preserving comments and
formatting, and talks to the Kubernetes API.

## Decision

The target hosted product uses a Go control-plane service with an embedded
TypeScript/React web UI and SQLite in WAL mode for one VPS instance. M0 deploys
the control plane and OpenTofu runner as separate OCI images/services. The
runner is isolated from control-plane secrets; its precise local job protocol
is an implementation gate and is intentionally not specified here.

This ADR is proposed, not an implementation-complete claim. HA/PostgreSQL,
OIDC, Kubernetes, remote runners and user-facing terminal CLI administration
are outside M0. Existing internal packages and CLI implementation may remain
until browser/API replacements make their removal safe.

## Consequences

Positive:

- A small, static binary for amd64 and arm64 that is easy to distribute.
- First-class libraries: `hcl/v2` and `hclwrite` for structure-preserving HCL
  edits, `client-go` and the Helm SDK for Kubernetes.

Negative, with mitigations:

- Two database backends must be tested. The storage layer is shared and CI
  runs the full suite against both.
- Ansible still needs Python, which lives in the runner image and not in the
  server.
- SQLite limits the all-in-one topology to one active server. HA deployments
  switch to PostgreSQL.

## Alternatives considered

- **Python.** Native to Ansible, but a heavier runtime and weaker HCL
  round-trip tooling.
- **TypeScript on Node.js for the whole stack.** Immature libraries for
  structure-preserving HCL edits.
- **Rust.** A smaller ecosystem for HCL and Kubernetes at the level needed,
  and slower iteration.
- **Microservices.** An operational burden that homelabs cannot carry.
