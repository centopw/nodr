# nodr technical design

This directory holds the technical design of **nodr**, a hybrid
infrastructure-as-code (IaC) and web GUI platform for small and medium-sized
enterprises (SMEs) and homelabs. nodr lets people manage Proxmox VE, OpenWrt,
Docker, K3s and Kubernetes either by clicking through a web interface or by
editing standard code (Terraform/OpenTofu, Ansible, UCI, Kubernetes manifests).
Both paths change the same desired state, which lives in Git.

| Field        | Value                              |
| ------------ | ---------------------------------- |
| Status       | Draft, open for review             |
| Version      | 0.1                                |
| Last updated | 2026-09-25                         |
| Scope        | Architecture and design for nodr v1 |

## Reading order

Start with the overview and architecture, then read the sync design, which is
the core of the product. The domain chapters can be read in any order.

| #  | Document | What it covers |
| -- | -------- | -------------- |
| 1  | [Overview](design/01-overview.md) | Problem, goals, non-goals, personas, principles, requirements traceability, glossary |
| 2  | [System architecture](design/02-architecture.md) | Context, components, key flows, data architecture, deployment topologies, technology stack |
| 3  | [Resource model](design/03-resource-model.md) | The tool-neutral intent model: kinds, aspects, schema tiers, validation, allocations, blueprints, repository layout |
| 4  | [Dual-mode management and sync](design/04-dual-mode-and-sync.md) | Simple and Advanced Mode, field-level ownership, lenses, three-way regeneration, conflict handling, infrastructure drift |
| 5  | [Intent compiler and engines](design/05-compiler-and-engines.md) | The abstraction layer from GUI actions to IaC, engine bindings, plugin SDK, migration between Terraform, Ansible and other tools |
| 6  | [Proxmox VE orchestration](design/06-proxmox.md) | Single-node and clustered Proxmox, high availability, VM and container lifecycle, storage and backup |
| 7  | [Automation and scheduling](design/07-scheduler.md) | Policies, schedules, maintenance windows, workflows, native delegation, execution engine |
| 8  | [Network management](design/08-networking.md) | OpenWrt integration, connectivity-safe apply, IPAM, DHCP, DNS, firewall, Proxmox host networking |
| 9  | [Containers and Kubernetes](design/09-containers.md) | Docker hosts, Compose projects, K3s, Kubernetes distributions, applications and catalog |
| 10 | [Security architecture](design/10-security.md) | Threat model, identity, RBAC, secrets, runner isolation, supply chain, audit |
| 11 | [API, events and CLI](design/11-api-and-cli.md) | REST API, long-running operations, events and webhooks, Git interface, CLI |
| 12 | [User experience](design/12-user-experience.md) | Progressive disclosure, key flows, change review, Advanced Mode IDE |
| 13 | [Operations and delivery](design/13-operations-and-delivery.md) | Non-functional requirements, packaging, upgrades, backup of nodr itself, testing, roadmap, risks |

Architecture decisions are recorded separately as
[Architecture Decision Records](adr/README.md).

## Conventions

- The key words **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT** and **MAY**
  are used as described in RFC 2119 when they appear in bold.
- YAML examples use the `nodr/v1alpha1` API version. Field names are
  illustrative until the schemas are published with the first implementation.
- Terraform examples target the `bpg/proxmox` provider. Resource type names
  follow the provider version pinned by the workspace.
- Diagrams are written in Mermaid so they render on GitHub and stay diffable.

## Changing this design

Small corrections go straight into the relevant chapter. Decisions that are
expensive to reverse (storage formats, public interfaces, security boundaries,
default engines) need a new ADR, and the chapters that depend on it are updated
in the same change.
