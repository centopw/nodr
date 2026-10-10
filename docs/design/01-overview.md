# 1. Overview

> Part of the [nodr technical design](../README.md).

## 1.1 Problem statement

Small businesses and homelab operators run surprisingly capable infrastructure:
a Proxmox VE cluster, an OpenWrt router with several VLANs, a few Docker hosts
and often a K3s cluster. Today they manage it in one of two ways, and both hurt.

**Vendor GUIs** (the Proxmox web UI, LuCI, Portainer) are quick to learn, but
each covers one system, changes are applied imperatively with no review step,
and nothing records why something is configured the way it is. After a year
the real configuration lives only in the running systems and in someone's
memory. Rebuilding after a failure means clicking everything again from
screenshots.

**IaC tools** (Terraform/OpenTofu, Ansible, Helm) give reproducibility, review
and history, but they have a steep learning curve and need glue between tools.
As soon as anyone makes a quick fix in a vendor GUI, the code is silently out of
date. The next `apply` either reverts the fix or fails.

Most teams pick one side and pay the other side's costs. Teams that mix them
end up with configuration drift they cannot see.

## 1.2 Vision

**One browser-managed control plane, with Git as the desired-state ledger.**

nodr is installed on a dedicated VPS. Administrators complete one-time browser
setup, connect a repository through a GitHub App, create draft changes, review
plans, and apply reviewed revisions. GitHub repositories hold desired state;
the VPS database holds only operational state. Every UI change becomes a Git
commit and every eligible signed GitHub push enters the same sync pipeline.

The browser is the M0 end-user administration surface. The HTTP API remains
available for automation under the same commit, concurrency, review and apply
rules. Current local-workspace CLI paths are migration-deprecated documentation
and implementation, not a supported administrator workflow.

## 1.3 Goals

| ID  | Goal |
| --- | ---- |
| G1  | Keep all desired state in a GitHub repository; Git history and refs are authoritative. |
| G2  | Provide browser-only setup and administration on one dedicated VPS, with a one-time bootstrap token. |
| G3  | Synchronize browser drafts and signed GitHub pushes through one field-ownership pipeline without silent overwrite. |
| G4  | Retain an HTTP API for automation; it uses the same draft, CAS and saved-plan rules as the browser. |
| G5  | Deliver M0 Proxmox VM management with isolated OpenTofu execution. |
| G6  | Keep intent tool-neutral and generated code readable and portable. |

## 1.4 Non-goals

| ID | Non-goal | Rationale |
| -- | -------- | --------- |
| N1 | Terminal CLI or shell setup for administrators/users in M0 | Browser setup and the API are the supported hosted surfaces. |
| N2 | Kubernetes, HA, OIDC, remote runners, Git smart HTTP or other forge integrations in M0 | These are later milestones, not partially exposed M0 features. |
| N3 | Hosted multi-tenant SaaS | nodr is a self-hosted VPS control plane. |
| N4 | Presenting arbitrary code in forms | Code the GUI cannot represent faithfully remains code-owned. |

## 1.5 Personas

| Persona | Context | What they need from nodr |
| ------- | ------- | ------------------------ |
| **P1 Administrator** | Deploys nodr on a dedicated VPS for a homelab or small organization. | Browser setup, a one-time bootstrap secret, GitHub App connection, and no shell-based account provisioning. |
| **P2 Operator** | Reviews inventory and performs authorized infrastructure work. | Browser drafts, visible plans, explicit apply and safe ownership conflicts. |
| **P3 Automation author** | Integrates existing tooling. | Stable HTTP API semantics without an interactive CLI dependency. |

## 1.6 Key scenarios

These scenarios drive the design and become end-to-end acceptance tests
([§13.7](13-operations-and-delivery.md#137-testing-strategy)).

| ID  | Scenario |
| --- | -------- |
| S1  | Connect an existing Proxmox cluster, discover 25 running VMs and adopt them without changing any of them. |
| S2  | Create a VM from a cloud image in three clicks. nodr assigns the VM ID, IP address, DNS name, backup policy and update policy. |
| S3  | Enable HA for a VM. nodr first checks quorum, shared storage and fencing, and explains anything that blocks HA. |
| S4  | Add a PCI passthrough device by editing the generated Terraform. The GUI keeps working and shows the customization. |
| S5  | Run a weekly rolling update of a Ceph-backed Proxmox cluster with no downtime for HA guests. |
| S6  | Add an IoT VLAN end to end: OpenWrt interface, DHCP, firewall zone, Wi-Fi SSID, Proxmox VLAN and IP address management (IPAM). |
| S7  | Deploy a K3s cluster with three server nodes and an HA API endpoint, spread across three Proxmox nodes. |
| S8  | Move VM provisioning from Terraform to Ansible, and back, with no change to the running infrastructure. |
| S9  | Someone edits a VM in the Proxmox UI. nodr detects the drift and offers to revert or adopt the change. |
| S10 | Export the workspace as a standalone Terraform and Ansible repository that works without nodr. |

## 1.7 Design principles

1. **Git is the ledger.** Desired state changes are durable only after a
   compare-and-swap Git ref update succeeds.
2. **Browser for people; API for automation.** M0 has no end-user CLI or shell
   setup path. Existing local CLI paths are migration-deprecated until replaced.
3. **One synchronization pipeline.** UI commits and signature-verified GitHub
   pushes are idempotently reconciled by the same ownership-aware sync path.
4. **SQLite is operational, not desired-state authority.** It stores users,
   metadata, delivery de-duplication, operations and artifacts for one VPS.
5. **Plan before apply.** A saved plan is bound to one draft HEAD and is
   applied only after that exact reviewed revision is merged.
6. **Explicit ownership.** Conflicts and code-owned fields are shown, never
   silently overwritten.
7. **Separate control plane and runner.** M0 deploys distinct OCI services;
   the runner's concrete internal job protocol remains an implementation gate.
8. **Small, concrete M0.** GitHub App, Proxmox VMs, SQLite and one VPS precede
   HA, OIDC, Kubernetes and remote runners.

## 1.8 Requirements traceability

| Requirement | Summary | Addressed in |
| ----------- | ------- | ------------ |
| 1.1 | User-friendly web GUI for infrastructure orchestration | [§12 User experience](12-user-experience.md), [§4.1 Modes](04-dual-mode-and-sync.md#41-management-modes) |
| 1.2 | Developer/Advanced Mode with in-browser editing of generated Terraform and other code | [§4.2 Advanced Mode](04-dual-mode-and-sync.md#42-advanced-mode-in-the-browser), [§12.6 IDE](12-user-experience.md#126-advanced-mode-ide) |
| 1.3 | Synchronization between GUI changes and manual code edits to prevent drift | [§4 Dual-mode management and sync](04-dual-mode-and-sync.md), [ADR-0003](../adr/0003-field-level-ownership-for-gui-and-code-sync.md) |
| 2.1 | Proxmox single-node and multi-node HA clusters, full VM lifecycle | [§6 Proxmox VE orchestration](06-proxmox.md) |
| 2.2 | Scheduler for updates, restarts and backups | [§7 Automation and scheduling](07-scheduler.md) |
| 2.3 | OpenWrt routing and network configuration | [§8 Network management](08-networking.md) |
| 2.4 | Docker, K3s and Kubernetes deployment and management | [§9 Containers and Kubernetes](09-containers.md) |
| 3.1 | Modular architecture to migrate workflows between Terraform, Ansible and other tools | [§5.9 Workflow migration](05-compiler-and-engines.md#59-workflow-migration-between-tools), [§5.8 Plugins](05-compiler-and-engines.md#58-plugin-architecture) |
| 3.2 | Abstraction layer that translates GUI actions into standardized IaC | [§3 Resource model](03-resource-model.md), [§5 Intent compiler](05-compiler-and-engines.md) |
| 4   | Easy for beginners, granular for power users | [§12 User experience](12-user-experience.md), [§3.6 Field tiers](03-resource-model.md#36-schemas-and-field-tiers) |

## 1.9 Glossary

| Term | Meaning |
| ---- | ------- |
| **Workspace** | The unit of management: one Git repository plus its connections, settings and permissions. A homelab usually has one workspace. An MSP has one per customer. |
| **Environment** | A label-based grouping inside a workspace (for example `prod`, `lab`). Environments are not Git branches. |
| **Intent** | The tool-neutral desired state, written as YAML documents in the workspace's `intent/` directory. Also called the nodr Resource Model (NRM). |
| **Kind** | The type of an intent document, for example `VirtualMachine`, `Network` or `K3sCluster`. |
| **Aspect** | One facet of a resource that a single engine realizes. A `VirtualMachine` has `provision`, `ha`, `guest`, `dns` and `backup` aspects. |
| **Engine** | A tool that realizes aspects: OpenTofu/Terraform, Ansible, UCI, Kubernetes, Docker Compose, or a native API driver. |
| **Binding** | The mapping from a kind and aspect to the engine that realizes it. Bindings can be changed per workspace or per resource. |
| **Plugin** | A packaged extension that contributes kinds, lenses, executors, observers, operations and workflow steps. |
| **Lens** | A bidirectional mapping between intent and engine code for one kind, aspect and engine. |
| **Managed block** | A block of engine code that nodr generated for an intent resource and keeps in sync. |
| **Pristine output** | The exact output of the renderers for a given revision, kept on a hidden Git ref and used as the base for three-way merges. |
| **Field ownership** | The state of each field of a managed resource: *synced*, *code-owned*, *extension* or *ignored*. See [§4.4](04-dual-mode-and-sync.md#44-field-level-ownership). |
| **User-owned code** | Code without a provenance marker, such as custom modules or roles. nodr runs it but never rewrites it. |
| **Change set** | A draft of intent and code changes on a Git branch, together with its plan. Applying a change set merges it and executes it. |
| **Plan** | The engine-independent preview of a change set: which resources change, how, and with what impact. |
| **Run** | One execution of a plan, a workflow or an operation. |
| **Operation** | An imperative action such as start, migrate, snapshot or console. Operations are not desired state. |
| **State unit** | One OpenTofu/Terraform root module with its own state. nodr partitions engine code into state units to keep plans small. |
| **Runner** | The process that executes engine jobs in an isolated sandbox. It can be embedded in the server or run at a remote site. |
| **Observer** | The component that reads the actual state of managed systems for drift detection and discovery. |
| **Drift** | A difference between desired and actual state. *Representation drift* is intent versus code. *Infrastructure drift* is declared versus running. |
| **Blueprint** | A reusable, parameterized template that expands into several intent resources, for example a three-node K3s cluster. |
| **Policy** | A high-level automation intent, such as `UpdatePolicy` or `BackupPolicy`, that compiles into schedules and workflows. |
| **Maintenance window** | A recurring time range in which disruptive automation may run. |
