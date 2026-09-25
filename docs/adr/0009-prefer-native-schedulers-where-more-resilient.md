# ADR-0009: Prefer native schedulers where they are more resilient

- Status: Proposed
- Date: 2026-09-25
- Related: [§7.6 Native delegation](../design/07-scheduler.md#76-native-delegation)

## Context

Backups and some maintenance tasks must keep running when nodr is down or
being upgraded. The managed platforms have their own schedulers: Proxmox
backup and replication jobs, PBS prune, garbage collection and verification
jobs, K3s etcd snapshots and the system-upgrade-controller. Multi-step
procedures, such as a rolling cluster update with health checks, still need
central orchestration.

## Decision

Policies compile to native jobs when their semantics can be represented
exactly: the calendar expression, the time zone and the absence of pre or post
hooks. Otherwise they run centrally. Results of native jobs are collected into
the same run history, alerts and reports. `execution: auto` makes this choice
automatically; `native` and `central` force it, and admission explains when
`native` is not possible.

## Consequences

Positive:

- Backups keep running during nodr outages.
- Less load on nodr, and fewer moving parts on the critical path of data
  protection.

Negative, with mitigations:

- There are two execution paths. One run history and one alerting path hide
  the difference from users.
- The exact-representation rules must be precise. Tests convert schedules in
  both directions for every supported native scheduler.
- Each platform needs an integration to observe its jobs. This is part of the
  platform plugins.

## Alternatives considered

- **Central scheduling only.** nodr becomes a single point of failure for
  backups.
- **Native scheduling only.** No rolling updates, health checks, approvals or
  rollback.
