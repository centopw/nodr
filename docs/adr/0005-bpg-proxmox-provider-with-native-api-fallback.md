# ADR-0005: The `bpg/proxmox` provider with a native API fallback

- Status: Proposed
- Date: 2026-09-25
- Related: [§6 Proxmox VE orchestration](../design/06-proxmox.md),
  [§5.4 Engine bindings](../design/05-compiler-and-engines.md#54-engine-bindings-and-the-capability-matrix)

## Context

Two Terraform providers for Proxmox VE are widely used: `bpg/proxmox`, which
is actively maintained, covers most of the API and supports OpenTofu, and
`Telmate/proxmox`, the older provider. No provider covers every Proxmox
feature as soon as it ships (for example the HA rules introduced in Proxmox
VE 9), and no provider covers cluster bootstrap, which needs root access over
SSH.

## Decision

Terraform-rendered Proxmox aspects use `bpg/proxmox`, pinned per workspace.
Features the pinned provider does not cover are routed through the
capability matrix to nodr's native Proxmox API executor. Host-level work
(cluster formation, packages, Ceph) uses Ansible.

## Consequences

Positive:

- Generated HCL uses the provider most homelab and SME users already know.
- Gaps in provider coverage never block a feature in nodr.

Negative, with mitigations:

- nodr depends on a community provider's roadmap, including the resource
  renames planned for its 1.0 release. Renderer upgrades are migration change
  sets that must pass the zero-diff gate
  ([§13.4](../design/13-operations-and-delivery.md#134-upgrades)).
- The native executor duplicates part of the provider's logic. It is limited
  to the gaps and covered by contract tests against nested Proxmox VE.

## Alternatives considered

- **`Telmate/proxmox`.** Less complete coverage.
- **Native API only.** It would drop Terraform, which the requirements ask
  for explicitly.
- **A nodr-specific provider.** High cost, and the resulting code would be
  unfamiliar to users.
