# 11. API, Events and CLI

> Part of the [nodr technical design](../README.md).

## 11.1 M0 contract

The browser uses the public HTTP API; automation uses that same API. M0 has no
end-user CLI, local-workspace administration, terminal setup, or Git smart HTTP
surface. Existing CLI documentation and implementation are migration-deprecated
until browser replacements exist; they are not deployment prerequisites.

All workspace writes use a draft commit SHA as the ETag. `If-Match` is required
for a mutation and stale values return RFC 9457 `412 PRECONDITION_FAILED`.
`Idempotency-Key` is required for commands, plan and apply requests. A repeated
key returns the recorded response and cannot create a second commit.

## 11.2 M0 routes

All paths are relative to `/api/v1`.

| Path | Methods | Purpose |
| ---- | ------- | ------- |
| `/setup/status`, `/setup` | GET, POST | Browser-only first administrator bootstrap |
| `/workspaces` | GET, POST | List or connect a GitHub App repository |
| `/workspaces/{workspace}` | GET | Workspace and revision details |
| `/workspaces/{workspace}/resources?kind=...` | GET | Read resources at a revision |
| `/workspaces/{workspace}/commands` | POST | Typed draft mutation |
| `/workspaces/{workspace}/changesets` | GET | List drafts/change sets |
| `/workspaces/{workspace}/changesets/{id}` | GET | Draft, SHA, diff and conflict details |
| `/workspaces/{workspace}/changesets/{id}:plan` | POST | Queue a saved plan |
| `/workspaces/{workspace}/changesets/{id}:apply` | POST | Apply the current successfully planned draft |
| `/operations/{id}` | GET | Durable operation state |
| `/events` | GET | SSE topics `operations` and `changesets` |
| `/integrations/github/webhook` | POST | Signature-verified GitHub push ingestion |

GitHub installation selection and repository connection are authenticated API
flows. The server verifies repository access and `nodr.yaml`; an inaccessible
or non-nodr repository returns `422` with field `repository`. The webhook route
is unauthenticated only because it authenticates the GitHub signature before
parsing. Duplicate delivery IDs return `202` without synchronizing again.

## 11.3 Command, plan and apply semantics

A resource-changing command creates or updates a draft from the supplied base
SHA, checks out that revision, synchronizes it, commits `nodr: <actor>
<command>`, and CAS-updates its draft ref. It returns the draft SHA and static
file diff; it never writes the default branch directly.

Plan returns `202` and an operation. The saved plan is keyed to the draft head
SHA. Apply accepts only a successful saved plan for the current draft SHA;
otherwise it returns `409 PLAN_CHANGED`. It CAS-merges that draft once into the
default branch before the separate runner service applies the saved plan. The
runner job interface is an implementation gate, not an established public API.
Today the apply runs in-process (`ledger.LocalRunner`); if the saved plan is no
longer retained, apply fails without promoting the default branch and the
changeset stays `planned`.

## 11.4 Errors and events

Errors are RFC 9457 Problem Details. M0 uses `OWNERSHIP_CONFLICT` (409),
`PLAN_CHANGED` (409), `PRECONDITION_FAILED` (412), and validation failures
(422). Ownership and synchronization conflicts include the affected file/block
location so the browser can link to code.

SSE publishes CloudEvents for `nodr.operation.started`,
`nodr.operation.succeeded`, `nodr.operation.failed`, and
`nodr.changeset.changed`. Events are invalidation signals: clients re-fetch
state rather than treating the event payload as authoritative.

## 11.5 Automation and migration

The API is the sole supported automation interface in M0. It observes the same
repository authorization, draft, CAS, idempotency, synchronization, saved-plan
and apply rules as browser requests. CLI parity is neither promised nor a
prerequisite; removal of user-facing CLI administration follows only after its
browser/API replacement is delivered.
