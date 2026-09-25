# 11. API, Events and CLI

> Part of the [nodr technical design](../README.md).

## 11.1 Principles

- **API first.** The web UI uses only the public API. Anything a user can do
  in the GUI can be automated.
- **Resource-oriented REST** under `/api/v1`, with JSON bodies and an OpenAPI
  3.1 description at `/api/v1/openapi.json`. Actions that do not fit CRUD are
  custom methods with a `:verb` suffix, for example `:plan` and `:migrate`.
- **Optimistic concurrency.** Resources carry an `ETag` (a content hash).
  Writes send `If-Match`, and a stale write fails with `412 Precondition
  Failed`.
- **Idempotency.** `POST` requests accept an `Idempotency-Key` header, so
  retries never duplicate work.
- **Long-running work** returns `202 Accepted` with an operation resource,
  which can be polled or followed through server-sent events (SSE).
- **Errors** use RFC 9457 Problem Details with nodr extensions
  ([§11.4](#114-errors)).
- **Collections** use cursor pagination (`pageToken`) and label selectors
  (`selector=app=website,nodr/environment=prod`).
- **Compatibility.** `v1` only changes additively. Breaking changes require
  `v2`, and both versions run side by side for at least one major release.

## 11.2 Resource map

All paths are relative to `/api/v1`.

| Path | Methods | Purpose |
| ---- | ------- | ------- |
| `/workspaces`, `/workspaces/{ws}` | GET, POST, PATCH, DELETE | Workspaces and their settings |
| `/workspaces/{ws}/resources` | GET | List intent resources, filtered by `kind` and `selector` |
| `/workspaces/{ws}/resources/{kind}/{name}` | GET, PUT, PATCH, DELETE | One intent document, returned together with its status |
| `/workspaces/{ws}/commands` | POST | Typed commands ([§5.2](05-compiler-and-engines.md#52-commands-gui-actions-as-data)) |
| `/workspaces/{ws}/files/{path}` | GET, PUT | Code files, with the base commit for concurrency control |
| `/workspaces/{ws}/changesets`, `/changesets/{id}` | GET, POST, PATCH, DELETE | Change sets |
| `/workspaces/{ws}/changesets/{id}:plan`, `:approve`, `:apply` | POST | Plan, approve and apply a change set |
| `/workspaces/{ws}/resources/{kind}/{name}:{operation}` | POST | Operations such as `:start`, `:shutdown`, `:migrate`, `:snapshot`, `:console` |
| `/workspaces/{ws}/runs`, `/runs/{id}`, `/runs/{id}/logs` | GET | Runs, their steps and logs |
| `/workspaces/{ws}/drift`, `/drift/{id}:resolve` | GET, POST | Drift reports and their resolution |
| `/workspaces/{ws}/imports` | POST | Discovery and adoption |
| `/workspaces/{ws}/exports` | POST | Export bundles |
| `/workspaces/{ws}/bindings:migrate` | POST | Engine handoff ([§5.9.1](05-compiler-and-engines.md#591-engine-handoff-protocol)) |
| `/workspaces/{ws}/secrets`, `/secrets/{name}`, `/secrets/{name}:reveal` | GET, PUT, DELETE, POST | Secret metadata; values are write-only except through `:reveal` |
| `/operations/{id}`, `/operations/{id}:cancel` | GET, POST | Long-running operations |
| `/events` | GET | SSE stream |
| `/state/{ws}/{unit}` | GET, POST, DELETE, LOCK, UNLOCK | OpenTofu and Terraform HTTP state backend |
| `/users`, `/groups`, `/rolebindings`, `/tokens` | Various | Identity and access management |
| `/plugins`, `/runners` | Various | Extension and runner management |
| `/audit` | GET | Audit log |

`/metrics`, `/healthz` and `/readyz` are served outside the versioned API.

## 11.3 Examples

### Changing a resource

```http
PATCH /api/v1/workspaces/home/resources/VirtualMachine/web-01?changeSet=cs-42 HTTP/1.1
Content-Type: application/merge-patch+json
If-Match: "sha256:9b1e40..."
Idempotency-Key: 5f0c1d8e-2a4b-4c1e-9d7a-0b6e3f2a9c11

{ "spec": { "resources": { "memory": { "size": "8Gi" } } } }
```

```http
HTTP/1.1 200 OK
ETag: "sha256:77aa02..."
Content-Type: application/json

{
  "resource": { "apiVersion": "nodr/v1alpha1", "kind": "VirtualMachine", "...": "..." },
  "changeSet": {
    "id": "cs-42",
    "staticDiff": {
      "impact": "reboot",
      "files": ["intent/compute/web-01.yaml", "terraform/pve-main-compute/vms.tf"]
    }
  },
  "warnings": []
}
```

### Planning and following progress

```http
POST /api/v1/workspaces/home/changesets/cs-42:plan HTTP/1.1
Idempotency-Key: 0e7b3a52-6c1f-4d3e-8a90-2f5c7d1b4e66
```

```http
HTTP/1.1 202 Accepted
Location: /api/v1/operations/op-7f3a

{ "id": "op-7f3a", "type": "plan", "state": "running" }
```

```text
GET /api/v1/events?topics=operations,runs,drift

event: nodr.operation.succeeded
data: {"specversion":"1.0","type":"nodr.operation.succeeded","source":"/workspaces/home/operations/op-7f3a","id":"ev-9d2","time":"2026-09-25T08:15:02Z","data":{"plan":"/api/v1/workspaces/home/changesets/cs-42/plan"}}
```

### Running an operation

```http
POST /api/v1/workspaces/home/resources/VirtualMachine/web-01:migrate HTTP/1.1
Content-Type: application/json
Idempotency-Key: 8c2d4e6f-1a3b-4c5d-9e7f-a1b2c3d4e5f6

{ "targetNode": "pve3", "online": true }
```

## 11.4 Errors

```json
{
  "type": "urn:nodr:error:ownership-conflict",
  "title": "Field is managed in code",
  "status": 409,
  "detail": "spec.resources.cpu.cores is set by the expression var.web_cores in terraform/pve-main-compute/vms.tf.",
  "instance": "/api/v1/workspaces/home/resources/VirtualMachine/web-01",
  "code": "OWNERSHIP_CONFLICT",
  "field": "spec.resources.cpu.cores",
  "remediation": [
    { "action": "edit-in-code", "file": "terraform/pve-main-compute/vms.tf", "line": 14 },
    { "action": "take-over", "command": "resource.takeOver" }
  ]
}
```

| Code | HTTP status | Meaning |
| ---- | ----------- | ------- |
| `VALIDATION_FAILED` | 422 | Schema, CEL or semantic validation failed; `errors` lists fields |
| `POLICY_DENIED` | 403 | A workspace policy rule denied the change |
| `OWNERSHIP_CONFLICT` | 409 | The GUI tried to change a code-owned field |
| `PLAN_CHANGED` | 409 | The plan changed since approval; a new review is needed |
| `PRECONDITION_FAILED` | 412 | Stale `If-Match` or base commit |
| `LOCKED` | 423 | A state unit or resource lock is held; `retryAfter` is included |
| `APPROVAL_REQUIRED` | 403 | The change set needs approvals that are still missing |
| `RATE_LIMITED` | 429 | Too many requests |

## 11.5 Events and webhooks

- **Envelope:** CloudEvents 1.0 in JSON.
- **Event types** include `nodr.resource.changed`, `nodr.changeset.created`,
  `nodr.changeset.approved`, `nodr.changeset.applied`, `nodr.run.started`,
  `nodr.run.step.succeeded`, `nodr.run.step.failed`, `nodr.run.finished`,
  `nodr.drift.detected`, `nodr.schedule.paused`, `nodr.node.maintenance` and
  `nodr.update.available`.
- **Outbound webhooks** are subscriptions with filters. Payloads are signed
  with HMAC-SHA256 over a timestamp and the body
  (`Nodr-Signature: t=<unix time>,v1=<hex digest>`). Failed deliveries are
  retried with exponential backoff for 24 hours and then shown in a
  dead-letter view.
- **Inbound webhooks** accept push events from Git remotes and signed custom
  events that can trigger schedules.

## 11.6 Git interface

- **Smart HTTP** at `/git/{workspace}.git`, authenticated with API tokens.
  SSH access is optional.
- **Branches.** `main` is protected. Pushing to `drafts/<name>` creates or
  updates a change set automatically. Direct pushes to `main` need
  `changeset:apply` and pass the same checks.
- **Pre-receive checks:** parsing, the sync invariant, secret scanning and
  policy rules. Rejections explain how to fix the problem
  ([§4.7](04-dual-mode-and-sync.md#47-sync-pipeline)).
- **External forges.** With GitHub, GitLab, Gitea or Forgejo as the remote,
  nodr reports commit statuses on pull requests (`nodr/sync`, `nodr/policy`,
  `nodr/plan`) and posts the plan summary as a comment. Merging the pull
  request creates a change set, which is applied according to the approval
  rules.

## 11.7 CLI

| Command | Purpose |
| ------- | ------- |
| `nodr login`, `nodr context use <workspace>` | Authenticate with the device flow and select a workspace |
| `nodr get <kind> [name] [-l selector] [-o table\|yaml\|json]` | List or show resources |
| `nodr describe <kind>/<name> [--ownership]` | Status, conditions, field ownership, recent runs |
| `nodr edit <kind>/<name>` | Edit intent in `$EDITOR` and submit it as a change set |
| `nodr apply -f <path> [--auto-approve]` | Submit documents as a change set, plan it and apply after confirmation |
| `nodr changeset list\|plan\|approve\|apply <id>` | Work with change sets |
| `nodr op <kind>/<name> <operation> [flags]` | Run an operation, for example `migrate --to pve3` |
| `nodr run <workflow> [--param key=value]` | Start a workflow |
| `nodr logs <run> [-f]` | Follow run logs |
| `nodr drift [--resolve adopt\|revert\|ignore]` | Show and resolve drift |
| `nodr import <platform>`, `nodr export --out <dir>` | Adopt existing systems, export a standalone repository |
| `nodr migrate <kind>/<name> --aspect <aspect> --to <engine>` | Engine handoff |
| `nodr forget <kind>/<name>` | Stop managing a resource without touching it |
| `nodr sync --local`, `nodr validate` | Run sync and validation on a local clone |
| `nodr secret set\|list\|delete` | Manage secrets |
| `nodr merge-driver <format>` | Structural Git merge driver ([§4.6](04-dual-mode-and-sync.md#46-provenance-and-three-way-regeneration)) |
| `nodr plugin test` | Run the plugin conformance kit |

```console
$ nodr get vm -l app=website
NAME     CLUSTER    NODE   STATE     SYNCED   APPLIED   DRIFT
web-01   pve-main   pve2   running   yes      yes       no
web-02   pve-main   pve3   running   yes      yes       no

$ nodr describe vm/web-01 --ownership
FIELD                          OWNER       SOURCE
spec.resources.cpu.cores       synced      terraform/pve-main-compute/vms.tf:14
spec.resources.memory.size     synced      terraform/pve-main-compute/vms.tf:19
smbios                         extension   terraform/pve-main-compute/vms.tf:31
```

## 11.8 Integration surfaces

- The OpenTofu and Terraform HTTP state backend ([§5.5](05-compiler-and-engines.md#55-engine-code-conventions)).
- The `nodr.platform.nodr` Ansible inventory plugin for users' own playbooks.
- Prometheus metrics and health endpoints ([§13.6](13-operations-and-delivery.md#136-observability)).
- OpenID Connect for single sign-on.
- Notification channels and webhooks ([§7.9](07-scheduler.md#79-notifications-and-reporting)).
- An optional Model Context Protocol (MCP) server, disabled by default. It
  exposes read-only tools and a tool that proposes change sets, so AI
  assistants can inspect infrastructure and draft changes that still go
  through review, approval and RBAC like any other change.
