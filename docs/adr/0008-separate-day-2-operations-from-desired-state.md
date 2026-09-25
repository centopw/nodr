# ADR-0008: Separate day-2 operations from desired state

- Status: Proposed
- Date: 2026-09-25
- Related: [§6.7 VM and container lifecycle](../design/06-proxmox.md#67-vm-and-container-lifecycle),
  [§4.10 Infrastructure drift](../design/04-dual-mode-and-sync.md#410-infrastructure-drift)

## Context

IaC tools are poor at imperative actions such as start, reboot, migrate,
snapshot or opening a console. Modeling such actions as desired state produces
noisy commits and fights with the platform: after an HA failover, Terraform
would try to move a guest back to its old node. Some runtime properties, such
as whether a guest should be running, still matter to users.

## Decision

Operations are a separate, audited API (custom methods such as `:migrate`),
executed by native executors. An operation changes intent only when it affects
a field the user chose to manage, for example `powerState: running` or
`stopped`. Fields that follow runtime state under defined conditions are marked
`x-nodr-runtime` and excluded from drift under those conditions. Adopted
resources start with `powerState: unmanaged`.

## Consequences

Positive:

- IaC, the HA manager and human operators do not fight each other.
- Git history contains only intentional changes.
- The Operator role can run operations without permission to change desired
  state.

Negative, with mitigations:

- Users must understand two paths. The GUI labels actions as "changes
  configuration" or "one-time action", and the review screen explains the
  difference.
- Each runtime field needs precisely defined semantics, which live in the
  schema and are covered by tests.

## Alternatives considered

- **Everything declarative,** for example snapshots as resources. Noisy
  history and the wrong semantics.
- **Everything imperative.** It gives up the benefits of IaC.
