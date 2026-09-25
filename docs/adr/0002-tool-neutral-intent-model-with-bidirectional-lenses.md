# ADR-0002: Tool-neutral intent model with bidirectional lenses

- Status: Proposed
- Date: 2026-09-25
- Related: [§3 Resource model](../design/03-resource-model.md),
  [§4.5 Lenses](../design/04-dual-mode-and-sync.md#45-lenses),
  [§5 Intent compiler and engines](../design/05-compiler-and-engines.md)

## Context

GUI actions must become standard IaC code, and workflows must be able to move
between Terraform, Ansible and other tools. The GUI needs structured data
described by schemas. A single resource is often realized by several tools: a
VM is provisioned through the Proxmox API, configured over SSH and registered
in DNS on the router.

## Decision

nodr defines a tool-neutral, versioned and schema-described intent model. It
is the GUI's native format and the compiler's input. Each kind is split into
aspects, and each aspect is bound to one engine. For every kind, aspect and
engine, a lens provides `render`, `put` and `lift`, and must satisfy the
stability and fidelity laws of bidirectional transformations. Lenses are pure
functions that run in a WebAssembly sandbox.

## Consequences

Positive:

- Engines can be switched per aspect with a verifiable handoff.
- Forms, validation and editor completion come from the same schemas.
- Compilation is pure and deterministic, so it is testable and cacheable.

Negative, with mitigations:

- Two representations must stay consistent. This is handled by field-level
  ownership ([ADR-0003](0003-field-level-ownership-for-gui-and-code-sync.md)).
- Writing lenses takes effort. Most are declarative mapping tables, and the
  conformance kit tests the laws automatically.
- Schemas will have gaps. Unmodeled attributes remain usable as code
  extensions.

## Alternatives considered

- **HCL as the model.** It ties nodr to Terraform, forms cannot represent
  expressions, and Ansible could not be supported.
- **A separate GUI per engine.** It offers no interoperability and no single
  view of a resource.
- **CUE or KCL as the model.** Powerful, but a high barrier for beginners.
  They may become optional authoring formats later.
