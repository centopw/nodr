# 13. Operations and Delivery

> Part of the [nodr technical design](../README.md).

## 13.1 Non-functional requirements

| Category | Requirement |
| -------- | ----------- |
| Footprint | Control plane idles at 512 MiB of RAM or less and under 5% of one vCPU. A job sandbox starts in under 3 seconds. |
| Responsiveness | GUI edit to static diff under 1 second (p95). API reads under 200 ms (p95). nodr adds less than 2 seconds to engine plan and apply times. |
| Scale | As in [§2.9](02-architecture.md#29-scalability-envelope). |
| Availability | Homelab: a single instance that restarts in under 30 seconds. SME: 99.9% monthly API availability with two or more replicas, scheduler failover in under 60 seconds. |
| Durability | No acknowledged desired-state change is lost: the Git write is durable before the API answers. |
| Recovery | Recovery time under one hour from backup. Desired state can always be rebuilt from Git. |
| Compatibility | amd64 and arm64. The latest two versions of Chrome, Firefox, Safari and Edge. Proxmox VE 8 and 9, PBS 3 and 4, OpenWrt 23.05, 24.10 and 25.12, OpenTofu and Terraform 1.8 or later, supported `ansible-core` releases. |
| Accessibility | WCAG 2.2 level AA. |
| Security | As in [§10](10-security.md). |

## 13.2 Packaging and installation

| Artifact | Contents | Use |
| -------- | -------- | --- |
| `nodr` OCI image | The server binary on a distroless base | All topologies |
| `nodr-runner` OCI image | Runner binary, OpenTofu, `ansible-core` with `ansible-runner`, kubectl, Helm, Docker CLI with Compose, OpenSSH, Python | Job sandboxes |
| Proxmox LXC appliance | A script that creates an unprivileged container with nodr and nested container support for sandboxes | The most common homelab install |
| Compose file | Server with embedded runner | Docker hosts |
| Helm chart | HA deployment | SMEs running Kubernetes |

| Topology | Minimum resources |
| -------- | ----------------- |
| All-in-one | 1 vCPU, 1 GiB RAM (2 GiB recommended, because the embedded runner also executes jobs), 10 GiB disk |
| SME HA | Two replicas with 2 vCPUs and 2 GiB each, PostgreSQL, S3-compatible storage, runners with 2 vCPUs and 4 GiB each |

The first-run wizard is described in [§12.4](12-user-experience.md#124-key-flows).

## 13.3 Running nodr in production

- **Leader election** for singleton duties uses a lease row in the database
  with a fencing token. A former leader that resumes after a pause cannot act
  on a stale lease.
- **Git authority.** The HA topology requires an external Git server as the
  authoritative repository ([§2.6](02-architecture.md#26-process-model-and-deployment-topologies)).
- **Runner pools.** Runners carry labels (`site`, `capability`). Each runner
  runs four concurrent jobs by default. An OpenTofu job needs roughly 200 to
  500 MiB, depending on the providers.

## 13.4 Upgrades

### nodr itself

- Semantic versioning.
- Database migrations are forward-only and follow the expand-and-contract
  pattern, so replicas one minor version apart can run side by side during a
  rolling upgrade. A backup is taken automatically before migrating.

### Renderers, plugins and toolchain

Upgrading a renderer, plugin, OpenTofu, a provider or an Ansible collection
can change generated code. Such upgrades are therefore never applied
silently. They produce a **migration change set** with:

- the new `nodr.lock` entries,
- re-rendered code, merged into customized code with three-way regeneration
  ([§4.6](04-dual-mode-and-sync.md#46-provenance-and-three-way-regeneration)),
- `moved` blocks, or `import` and `removed` pairs, for renamed resource types.

The change set must pass the **zero-diff gate**: engine plans after the
upgrade must show no infrastructure changes, or only changes that the
reviewer explicitly acknowledges, such as a new provider default. For
example, when `bpg/proxmox` renames its resource types in a future major
version, the renderer upgrade produces the moves and proves that nothing
changes.

Kind storage versions follow the same process
([§3.11](03-resource-model.md#311-versioning-and-evolution)). Every release
publishes a compatibility matrix.

## 13.5 Backup and disaster recovery of nodr

| Data | Backup method | Target RPO |
| ---- | ------------- | ---------- |
| Workspace repositories | Git mirror, plus the nodr backup | 0 when mirrored |
| Database | SQLite online backup, or `pg_dump` and point-in-time recovery | 24 hours by default, configurable down to 1 hour |
| Engine state | Versioned in the artifact store, included in the backup | As the database |
| Artifact store | Optional; run logs and plans | Best effort |
| KEK | Recovery kit printed or stored in a password manager at setup | Required: without it, stored secrets cannot be recovered |

- nodr backs itself up with its own scheduler: an encrypted system backup to
  PBS (with the Proxmox Backup client), S3 or a local path.
- **Restore procedure:** install nodr, restore the database and repositories,
  provide the KEK, start, run `nodr admin verify` (audit chain, repository
  integrity, state versions), and re-enroll runners if needed.
- **If engine state is lost anyway,** it can be recreated: intent contains the
  identities of every resource (VM IDs, router names, cluster names), so nodr
  can render `import` blocks for everything and verify the result with a
  zero-diff plan.

## 13.6 Observability

- **Metrics** (Prometheus): `nodr_runs_total{type,outcome}`,
  `nodr_run_duration_seconds`, `nodr_scheduler_lag_seconds`,
  `nodr_queue_depth`, `nodr_drift_open{kind}`, `nodr_sync_duration_seconds`,
  `nodr_sync_fallbacks_total` (runtime lens-law failures),
  `nodr_engine_errors_total{engine,class}`, `nodr_runner_jobs_active` and
  `nodr_api_request_duration_seconds`.
- **Logs:** structured JSON with correlation IDs that follow a request from
  the API through the run to the runner.
- **Traces:** OpenTelemetry spans for commands, sync, compilation, runs and
  steps.
- **Health endpoints:** `/healthz` for liveness; `/readyz` checks the
  database, Git and that the KEK is unsealed.
- **Support bundle:** `nodr admin support-bundle` collects redacted
  configuration, versions and recent errors.

## 13.7 Testing strategy

| Level | What it covers |
| ----- | -------------- |
| Unit tests | Go and TypeScript code |
| Lens property tests | The lens laws ([§4.5](04-dual-mode-and-sync.md#45-lenses)) for every lens, with generated intents and random code mutations |
| Golden files | Every renderer: fixed intent in, expected files out; changes are reviewed as diffs |
| Sync scenarios | The twelve conflict scenarios in [§4.8](04-dual-mode-and-sync.md#48-conflict-handling) as executable tests |
| Plugin conformance | The conformance kit for built-in and third-party plugins |
| Proxmox integration | Nested virtualization in CI: Proxmox VE installed unattended in QEMU VMs, as a single node and as a three-node cluster with Ceph |
| OpenWrt integration | x86-64 OpenWrt images in QEMU, including tests that deliberately cut the management path and assert the automatic rollback |
| Kubernetes and Docker | k3d or kind for application delivery, full K3s on nested Proxmox nightly, Docker-in-Docker for Compose |
| End to end | Playwright tests for the key flows in [§12.4](12-user-experience.md#124-key-flows), with automated accessibility checks |
| Upgrades | Upgrading a sample workspace from every supported version; the zero-diff gate must pass |
| Export | An exported sample workspace planned with plain OpenTofu and Ansible; the plan must be empty |
| Chaos | Killing the control plane or runners mid-workflow, network partitions during router applies, full disks, clock skew |
| Performance | Rendering a 1,000-resource workspace in under 2 seconds; 50 concurrent users |
| Security | Static analysis, dependency scanning, fuzzing of the HCL, YAML and UCI parsers and lens inputs, an external penetration test before 1.0 |

The key scenarios S1 to S10 from [§1.6](01-overview.md#16-key-scenarios) run as
automated end-to-end tests.

## 13.8 Delivery roadmap

| Milestone | Scope | Exit criteria |
| --------- | ----- | ------------- |
| **M0 Foundations** | Git-backed workspaces, intent model and admission, OpenTofu engine and state service, VM lifecycle on a single Proxmox node, Simple Mode forms, Advanced Mode editor with sync for VMs, change sets and plans | S2 and S4 pass; lens laws hold for the VM lens |
| **M1 Clusters and automation** | Multi-node clusters, version-aware HA, discovery and adoption, drift detection, scheduler with backup, update and snapshot policies, maintenance windows, notifications | S1, S3, S5 and S9 pass |
| **M2 Networking** | OpenWrt plugin with connectivity-safe apply, IPAM, DHCP and DNS, firewall policy, Proxmox host networking and SDN, WireGuard | S6 passes; rollback chaos tests pass |
| **M3 Containers** | Docker hosts, Compose projects with image locks and updates, K3s clusters, applications with direct and GitOps delivery, catalog | S7 passes |
| **M4 Interoperability** | Ansible engine for VM provisioning, the handoff protocol, importers for Terraform and Ansible repositories, export, plugin SDK beta | S8 and S10 pass |
| **M5 SME readiness (1.0)** | HA topology, OIDC, scoped RBAC and approvals, audit export, remote runners, Talos and Kubespray, hardening pack, external security review | All scenarios pass on the HA topology; security review findings closed |

## 13.9 Risks and mitigations

| Risk | Likelihood | Impact | Mitigation |
| ---- | ---------- | ------ | ---------- |
| Bidirectional lenses are hard to get right | High | High | Declarative mapping tables, property-based law tests, runtime round-trip checks with a conservative fallback to code-owned fields |
| Provider and platform churn (provider renames, Proxmox API changes) | High | Medium | Pinned versions, renderer upgrades as zero-diff change sets, native executor fallback, contract tests against nested Proxmox |
| Scope is broad: Proxmox, OpenWrt, Docker and Kubernetes | High | High | Milestones that each deliver a usable product, the plugin architecture, golden paths before breadth |
| Beginners are overwhelmed | Medium | High | Progressive disclosure, strong defaults, usability tests at every milestone |
| Arbitrary code execution through Advanced Mode | Medium | High | `code:write`, sandboxing, scoped credentials, policy checks, approvals |
| nodr locks itself out by breaking its own network or host | Medium | High | Self-protection rules, connectivity-safe apply, nodr's host last in rolling operations |
| Terraform license terms restrict bundling | Low | Medium | OpenTofu by default; the Terraform CLI is supported only as a binary the user supplies |
| Engine state is lost | Low | High | Versioned state backups, re-adoption from the identities stored in intent |
| Plans become slow in large workspaces | Medium | Medium | State units, incremental plans, static previews that skip refresh |
| Ansible check mode is inaccurate for some modules | Medium | Medium | Effects marked as unknown in plans, idempotent roles, integration tests |

## 13.10 Open questions

1. Should plugins and catalogs have an optional hosted index, or only
   Git-based distribution?
2. How far should the portable part of `VirtualMachine` go toward other
   hypervisors, such as Incus or XCP-ng, before v2?
3. Should staging environments ever be long-lived branches, or stay
   label-based as currently decided?
4. May renderers use OpenTofu-only language features when a workspace is
   pinned to OpenTofu, at the cost of Terraform compatibility?
5. Do air-gapped sites need a bundled Attended Sysupgrade server, or is
   uploading firmware images enough?
6. Which licenses should nodr and the `nodr.platform` Ansible collection use?
   The answer affects the export guarantee and community contributions.
7. Should the MCP server ever offer write tools beyond proposing change sets?
