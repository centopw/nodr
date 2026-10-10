# ADR-0001: Use Git as the desired-state ledger

- Status: Proposed
- Date: 2026-09-25
- Related: [§2.5 Data architecture](../design/02-architecture.md#25-data-architecture),
  [§4 Dual-mode management and sync](../design/04-dual-mode-and-sync.md)

## Context

nodr must give GUI changes the same properties that code changes have in a
mature IaC workflow: history, diffs, review, attribution and rollback. Power
users want to work in their own editors and forges, and GitOps tools expect
desired state in a repository. Homelab installations must stay simple, and
disaster recovery must not depend on one database.

## Decision

All desired state of a workspace lives in one GitHub-connected repository:
intent documents, engine code, render locks, automation definitions and
allocations. Every hosted UI or API mutation will create a commit on a draft
branch; applying a reviewed change set will merge that draft into the default
branch. Git refs are advanced with compare-and-swap. SQLite will hold only
operational data (users, sessions, workspace metadata, webhook delivery
de-duplication, operations, artifacts and rebuildable indexes).

The target integration is a server-side GitHub App. Its short-lived
installation tokens and signed webhook secrets are never stored in Git. UI
writes and eligible signature-verified pushes will enter the same idempotent
sync pipeline. This is a proposed migration contract; it does not state that
the current local-workspace implementation has already been removed.

## Consequences

Positive:

- History, diff, blame and revert come for free, for GUI users too.
- External editors, forges and GitOps tools work without adapters.
- Desired state survives the loss of the database.
- Git authorship adds an independent audit record.

Negative, with mitigations:

- Git operations add latency. Reads are served in-process through go-git and
  writes are batched per command.
- Concurrent edits need merges. Structural merge drivers
  ([§4.6](../design/04-dual-mode-and-sync.md#46-provenance-and-three-way-regeneration))
  and field-level conflicts keep them manageable.
- Secrets must never reach the repository. Intent uses references, and every
  save and push is scanned.
- Large binaries do not belong in Git. Plans, logs and backups go to the
  artifact store.

## Alternatives considered

- **Database as the source of truth, exported to Git.** Git would become a
  lossy mirror, and edits made outside nodr would be hard to accept safely.
- **An event-sourced database.** It would reproduce what Git already offers,
  without the ecosystem around it.
