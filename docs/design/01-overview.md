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

**Click or code: one infrastructure, one source of truth.**

nodr is a web GUI and an IaC platform at the same time:

- Every GUI action becomes a reviewable change to standard, readable code in Git.
- Every code change, whether made in the built-in editor or pushed from a
  laptop, shows up in the GUI.
- Every change is carried out by standard tools: OpenTofu or Terraform,
  Ansible, OpenWrt UCI, Kubernetes and Docker Compose.
- Changes made outside nodr, such as a quick edit in the Proxmox UI, are
  detected and can be reverted or adopted.

A beginner can run a backed-up, patched homelab without ever opening the code
view. A power user can treat nodr as a GitOps control plane and never touch the
GUI. Both can work on the same infrastructure at the same time.

## 1.3 Goals

| ID  | Goal |
| --- | ---- |
| G1  | Keep all desired state in Git, whether it was created through the GUI, the code editor, the API, the CLI or an import. |
| G2  | Synchronize GUI and code in both directions with explicit, field-level ownership, so that nothing is silently overwritten. |
| G3  | Manage the full Proxmox VE lifecycle, from a single node to a multi-node high-availability (HA) cluster, including VMs and LXC containers from creation to decommissioning. |
| G4  | Provide built-in scheduling for maintenance (updates, restarts, backups, snapshots, firmware and cluster upgrades) with safety rails such as pre-change snapshots, health checks and maintenance windows. |
| G5  | Manage OpenWrt routing and networking with connectivity-safe apply, and define networks once for routers, hypervisors and clusters. |
| G6  | Treat Docker, K3s and Kubernetes environments as first-class resources, provisioned end to end. |
| G7  | Keep the intent model tool-neutral so the engine behind a resource (Terraform, Ansible, native API) can be switched without changing infrastructure. |
| G8  | Avoid lock-in: generated code is idiomatic, human-readable and runnable without nodr. |
| G9  | Use progressive disclosure: defaults are enough for beginners, and experts can reach every setting. |
| G10 | Stay light enough for homelab hardware: the control plane idles in at most 512 MiB of RAM on one vCPU. |

## 1.4 Non-goals

| ID | Non-goal | Rationale |
| -- | -------- | --------- |
| N1 | Public-cloud management (AWS, Azure, GCP) in v1 | The plugin model allows it later; v1 focuses on self-hosted infrastructure. |
| N2 | Replacing monitoring stacks | nodr runs the health checks its own workflows need and integrates with Prometheus, Grafana and Uptime Kuma instead of rebuilding them. |
| N3 | General-purpose CI/CD for application code | nodr deploys infrastructure and packaged applications, not source code pipelines. |
| N4 | Re-implementing every vendor GUI feature | Low-level diagnostics (SMART data, Ceph internals, packet captures) deep-link to the vendor UI. |
| N5 | Hosted multi-tenant SaaS | nodr is self-hosted. Managed service providers are supported through multiple workspaces in one installation. |
| N6 | Presenting arbitrary code in the GUI | Code that the GUI cannot represent faithfully stays code and is shown as code, instead of being approximated in a form. |

## 1.5 Personas

| Persona | Context | What they need from nodr |
| ------- | ------- | ------------------------ |
| **P1 Homelab beginner** | One Proxmox node, a consumer router flashed with OpenWrt. Wants Home Assistant, a DNS ad-blocker and a media server. | Wizards and defaults, backups and updates that just happen, a clear warning before anything risky. No need to learn Terraform. |
| **P2 Homelab power user** | Three-node Proxmox cluster with Ceph, K3s, VLANs, their own Terraform modules. | GitOps, full access to every Proxmox and OpenWrt setting, editable code, API and CLI, no lock-in. |
| **P3 SME IT generalist** | One to three people running IT for a 20 to 200 person company. | Reliable backups with restore tests, patching with approvals, an audit trail, low day-to-day effort. |
| **P4 DevOps engineer or MSP technician** | Several sites or customers, strong IaC background. | Multiple workspaces, pull-request workflows, RBAC, remote runners, automation through the API. |

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

1. **Git is the ledger.** Every change to desired state is a commit. History,
   diff, review and revert come for free.
2. **One model, many views.** The GUI, the code, the API and the CLI are
   projections of the same model. The GUI has no private capabilities.
3. **Explicit ownership.** Every setting has exactly one owner at any time.
   Conflicts are shown to a person and never resolved silently.
4. **Plan before apply.** Every change produces a preview in plain language
   before anything touches infrastructure.
5. **Safe by default.** Protection flags, pre-change snapshots,
   connectivity-safe network changes and tested rollback paths are on by
   default.
6. **Standard tools, idiomatic output.** Execution uses established tools, and
   generated code looks like a careful engineer wrote it.
7. **Native first.** When a platform has a more resilient built-in mechanism
   (Proxmox backup jobs, K3s etcd snapshots, OpenWrt apply with rollback),
   nodr configures that mechanism instead of reinventing it.
8. **Progressive disclosure.** Simple by default, complete on demand.
9. **Small footprint.** Homelab hardware is the baseline, not an afterthought.

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
