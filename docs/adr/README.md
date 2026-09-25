# Architecture Decision Records

This directory records the architecture decisions behind nodr: decisions that
are expensive to reverse, such as storage formats, public interfaces, security
boundaries and default engines. Each record explains the context, the decision,
its consequences and the alternatives that were considered.

| ADR | Title | Status |
| --- | ----- | ------ |
| [0001](0001-use-git-as-the-desired-state-ledger.md) | Use Git as the desired-state ledger | Proposed |
| [0002](0002-tool-neutral-intent-model-with-bidirectional-lenses.md) | Tool-neutral intent model with bidirectional lenses | Proposed |
| [0003](0003-field-level-ownership-for-gui-and-code-sync.md) | Field-level ownership for GUI and code synchronization | Proposed |
| [0004](0004-opentofu-as-default-terraform-compatible-engine.md) | OpenTofu as the default Terraform-compatible engine | Proposed |
| [0005](0005-bpg-proxmox-provider-with-native-api-fallback.md) | The `bpg/proxmox` provider with a native API fallback | Proposed |
| [0006](0006-connectivity-safe-openwrt-apply-via-rpcd.md) | Connectivity-safe OpenWrt changes through rpcd | Proposed |
| [0007](0007-go-single-binary-control-plane.md) | A single Go binary, with SQLite by default and PostgreSQL for HA | Proposed |
| [0008](0008-separate-day-2-operations-from-desired-state.md) | Separate day-2 operations from desired state | Proposed |
| [0009](0009-prefer-native-schedulers-where-more-resilient.md) | Prefer native schedulers where they are more resilient | Proposed |

## Statuses

- **Proposed:** part of the design under review.
- **Accepted:** agreed and binding for the implementation.
- **Superseded by ADR-NNNN:** replaced by a later decision. The record is kept
  for history.
- **Rejected:** considered and declined. The record is kept so the discussion
  does not have to be repeated.

## Writing a new record

Copy the template below into `NNNN-short-title.md`, using the next free number.
Records are never rewritten after acceptance. A changed decision gets a new
record that supersedes the old one.

```markdown
# ADR-NNNN: Title in sentence case

- Status: Proposed
- Date: YYYY-MM-DD
- Related: links to design chapters and other ADRs

## Context

What forces are at play: requirements, constraints, facts.

## Decision

What we will do, stated in full sentences.

## Consequences

What becomes easier and what becomes harder, including risks and mitigations.

## Alternatives considered

Each option with the reason it was not chosen.
```
