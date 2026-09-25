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

The control plane and the runner are written in Go and ship as one binary
with selectable roles. The web UI is a TypeScript and React single-page
application embedded in the binary. SQLite in WAL mode is the default
database. PostgreSQL is required for the HA topology, together with an
external Git server as the authoritative repository.

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
