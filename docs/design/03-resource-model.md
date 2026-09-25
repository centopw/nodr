# 3. Resource Model

> Part of the [nodr technical design](../README.md).

## 3.1 Purpose

The nodr Resource Model (NRM) is the tool-neutral description of desired
state. It is the contract between every part of the system:

- The GUI renders its forms from NRM schemas and edits NRM documents.
- The intent compiler turns NRM documents into engine code
  ([§5](05-compiler-and-engines.md)).
- The sync engine lifts code edits back into NRM documents
  ([§4](04-dual-mode-and-sync.md)).
- The API and CLI read and write NRM documents directly.

NRM documents describe *what* should exist ("a VM with 4 GiB of memory on the
`dmz` network, backed up nightly"), not *how* a particular tool creates it.
That separation is what allows the engine behind a resource to change
([§5.9](05-compiler-and-engines.md#59-workflow-migration-between-tools),
[ADR-0002](../adr/0002-tool-neutral-intent-model-with-bidirectional-lenses.md)).

NRM is neutral about tools, not about platforms. v1 targets Proxmox VE for
virtualization, so platform-specific settings are modeled directly. Portable
concepts (CPU, memory, disks, network interfaces, cloud-init) are kept
separate from Proxmox-only settings, which live under a `proxmox` key. That
leaves room for other hypervisors later without complicating v1.

## 3.2 Document structure

NRM documents are YAML files in the workspace's `intent/` directory. The
structure follows the familiar Kubernetes shape:

```yaml
apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-01                        # unique per kind in the workspace
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C2D3     # assigned by nodr, survives renames
  labels:
    nodr/environment: prod
    app: website
  annotations:
    nodr/description: Public website frontend
spec:
  # desired state, see the examples in section 3.13
```

| Field | Rules |
| ----- | ----- |
| `apiVersion` | `nodr/<version>` for built-in kinds, `<plugin>.nodr/<version>` for plugin kinds |
| `metadata.name` | Lowercase letters, digits and hyphens, at most 63 characters, unique per kind in a workspace. Renaming is allowed. |
| `metadata.uid` | A ULID assigned when the resource is created. It is immutable and is the identity that code provenance, allocations and history refer to. A hand-written document without a `uid` gets one in the next sync commit. |
| `metadata.labels` | Key-value pairs used by selectors in policies, schedules, bindings and RBAC scopes. The `nodr/` prefix is reserved. |
| `metadata.annotations` | Free-form metadata. The `nodr/` prefix is reserved. |
| `spec` | The desired state, defined by the kind's schema. |

**Status is not stored in Git.** Runtime information such as conditions,
outputs and the observed revision lives in the database and is returned by the
API next to the document:

```yaml
status:
  observedRevision: 3f9c2e1
  conditions:
    - { type: Synced,  status: "True" }     # intent and code agree
    - { type: Applied, status: "False", reason: PendingChangeSet }
    - { type: Healthy, status: "True" }
    - { type: Drifted, status: "False" }
  outputs:
    vmid: 1012
    node: pve2
    ipv4: [10.0.20.21]
```

## 3.3 Kinds

| Domain | Kind | Purpose |
| ------ | ---- | ------- |
| Platform | `ProxmoxCluster` | A Proxmox VE cluster, or a single node treated as a cluster of one: endpoints, credentials reference, cluster options, HA and Ceph settings |
| | `ProxmoxNode` | Node-level settings: host networking, package repositories, storage, maintenance state |
| | `StoragePool` | A Proxmox storage definition (directory, LVM-thin, ZFS, NFS, CIFS, CephFS, RBD, PBS) |
| | `BackupTarget` | Where backups go: a PBS datastore and namespace, an NFS or CIFS share, or a local directory |
| | `Template` | A VM or LXC template built from a cloud image, an ISO or a Packer build, with a version |
| Compute | `VirtualMachine` | A QEMU/KVM guest |
| | `LinuxContainer` | An LXC guest |
| | `Host` | An existing machine that nodr configures but did not provision: bare metal, a Raspberry Pi, a VPS |
| Configuration | `ConfigProfile` | An operating-system configuration bundle (packages, users, SSH hardening, time sync, agents), realized by Ansible roles by default |
| | `SSHKey` | A public key and where it is authorized |
| Network | `Network` | A layer-2 segment with its subnet, gateway, DHCP, DNS domain, security zone and optional Wi-Fi |
| | `Router` | An OpenWrt device and its role (gateway, access point) |
| | `FirewallPolicy` | Zone-to-zone rules and exceptions |
| | `PortForward` | Exposure of an internal service through the gateway |
| | `DNSRecord` | A local or external DNS record |
| | `WireGuardTunnel` | A site-to-site or road-warrior VPN |
| Containers | `DockerHost` | A host running Docker Engine |
| | `ComposeProject` | A Docker Compose project deployed to a Docker host |
| | `K3sCluster` | A K3s cluster, including its nodes |
| | `KubernetesCluster` | A cluster built with another distribution, or an existing cluster registered by kubeconfig |
| | `Application` | A workload deployed from a Helm chart, Kustomize path, manifests or catalog entry |
| Automation | `UpdatePolicy`, `BackupPolicy`, `SnapshotPolicy` | High-level maintenance intent that compiles into schedules and workflows |
| | `Schedule`, `Workflow`, `MaintenanceWindow` | Timing, procedures and allowed time ranges ([§7](07-scheduler.md)) |
| | `NotificationChannel` | A destination for alerts and reports |
| Composition | `Blueprint`, `BlueprintInstance` | Reusable, parameterized groups of resources ([§3.10](#310-blueprints-and-composition)) |

Secrets are never NRM documents. Intent refers to secrets by name (for example
`secretRef: proxmox/pve-main-token`), and the values live in the secrets
service ([§10.5](10-security.md#105-secrets-management)).

## 3.4 Aspects

One resource is often realized by several engines. A VM is created through
the Proxmox API, configured over SSH, gets a DHCP reservation and DNS record on
the router and joins a backup job. NRM therefore splits every kind into
**aspects**, and each aspect is bound to exactly one engine
([§5.4](05-compiler-and-engines.md#54-engine-bindings-and-the-capability-matrix)).

| `VirtualMachine` aspect | Covers | Default engine | Alternatives |
| ----------------------- | ------ | -------------- | ------------ |
| `provision` | Guest definition: CPU, memory, disks, network interfaces, cloud-init, boot options | OpenTofu with `bpg/proxmox` | Ansible (`community.proxmox`), native Proxmox API |
| `ha` | HA resource, node and resource affinity | OpenTofu | Native Proxmox API |
| `guest` | Operating-system configuration from `ConfigProfile` | Ansible | Plugins such as NixOS; cloud-init only |
| `network-services` | DHCP reservation and DNS record | UCI on OpenWrt | DNS plugins (Pi-hole, AdGuard Home, Technitium, PowerDNS) |
| `backup` | Membership in backup jobs | Native Proxmox backup job | Central scheduler ([§7.6](07-scheduler.md#76-native-delegation)) |
| `updates` | Patching per `UpdatePolicy` | Scheduler with Ansible | `unattended-upgrades` on the guest |

Aspects are also the unit of ownership transfer when a resource moves between
engines.

## 3.5 References and the dependency graph

Fields that point to other resources are typed references. The schema declares
the target kind (`x-nodr-ref: Network`), so a bare name is enough:

```yaml
spec:
  nics:
    - network: dmz                    # a Network in the same workspace
  source:
    template: debian-12-cloud         # a Template
  policies:
    backup: nightly-7d4w              # a BackupPolicy
```

References are resolved within one workspace only. From references and the
implicit dependencies that plugins declare (a VM depends on its cluster, a K3s
cluster depends on its node VMs), nodr builds a **dependency graph**. The graph
is used to:

- order execution across engines ([§5.6](05-compiler-and-engines.md#56-cross-engine-orchestration)),
- analyze impact ("12 VMs and 1 K3s cluster use the `lab` network"),
- block deletions that would leave dangling references,
- limit re-rendering to the resources a change can affect.

Cycles are rejected at admission.

## 3.6 Schemas and field tiers

Each kind version has a JSON Schema (2020-12) with nodr extensions. The same
schema drives GUI forms, editor completion, API validation and documentation.

```yaml
# Excerpt from the VirtualMachine schema
properties:
  resources:
    properties:
      memory:
        properties:
          size:
            type: string
            description: Memory assigned to the guest.
            x-nodr-unit: bytes
            x-nodr-tier: basic
            x-nodr-impact: { increase: hot-plug-if-enabled, decrease: reboot }
            x-nodr-ui: { widget: quantity, presets: [1Gi, 2Gi, 4Gi, 8Gi, 16Gi] }
          minimum:
            type: string
            description: Ballooning floor. Leave empty to disable ballooning.
            x-nodr-unit: bytes
            x-nodr-tier: standard
x-nodr-validations:
  - rule: >-
      !has(self.resources.memory.minimum) ||
      quantity(self.resources.memory.minimum).compareTo(quantity(self.resources.memory.size)) <= 0
    message: The ballooning minimum cannot be larger than the memory size.
```

| Extension | Meaning |
| --------- | ------- |
| `x-nodr-tier` | `basic`, `standard` or `expert`: controls where a field appears in the GUI |
| `x-nodr-impact` | What changing the field does to a running resource: `none`, `in-place`, `hot-plug`, `restart-service`, `reboot`, `replace` |
| `x-nodr-immutable` | The field cannot change after creation. A change means replacement and is flagged as destructive. |
| `x-nodr-ref` | The kind a reference field points to |
| `x-nodr-unit` | Quantity semantics used for parsing, display and conversion |
| `x-nodr-sensitive` | The field holds a secret reference and is masked everywhere |
| `x-nodr-allocated` | nodr fills the field at admission when it is empty ([§3.9](#39-allocations)) |
| `x-nodr-runtime` | Under certain settings the field reflects runtime state and is excluded from drift, for example the node of an HA guest with automatic placement |
| `x-nodr-ui` | Widget, grouping, ordering and preset hints |
| `x-nodr-validations` | CEL rules with error messages |

**Field tiers** implement progressive disclosure ([§12.3](12-user-experience.md#123-modes-and-disclosure-mechanics)):

| Tier | Shown | Examples for a VM |
| ---- | ----- | ----------------- |
| `basic` | Wizards and default forms | Name, template, size, network, backup and update policies |
| `standard` | Behind "More settings" | Ballooning, disk options, boot order, tags, start on boot, HA |
| `expert` | Advanced Mode, or by searching settings | CPU flags and models, NUMA, hugepages, PCI and USB passthrough, machine type, SCSI controller, cloud-init vendor data |

**Coverage rule:** a setting that the platform supports is either modeled in
some tier or reachable as a code extension
([§4.4](04-dual-mode-and-sync.md#44-field-level-ownership)). Power users are
never blocked by gaps in the schema.

## 3.7 Admission

Every write, from any source, passes through the same admission pipeline
before it is committed:

1. **Defaulting.** Values come from schema defaults, workspace defaults in
   `nodr.yaml` and referenced profiles. Identity and safety fields (VM ID,
   storage, protection) are written into the document. Policy-like defaults,
   such as the default backup policy, stay inherited and are shown in the GUI
   as "inherited from workspace defaults". Changing a workspace default
   produces one change set that lists every affected resource.
2. **Allocation.** Fields marked `x-nodr-allocated` are filled ([§3.9](#39-allocations)).
3. **Validation.** Schema checks and CEL rules run first, then semantic checks
   contributed by plugins, which use inventory facts: node capacity, name
   collisions on the cluster, VLANs that exist on the bridge, IP conflicts,
   HA readiness. Findings are *errors* (block), *warnings* (allowed after
   acknowledgment) or *info*.
4. **Policy.** Optional, workspace-defined CEL rule packs such as "production
   VMs need a backup policy" or "LXC containers must be unprivileged". Each
   rule can deny, warn or require approval.

Admission can run in dry-run mode, which is what the GUI uses while someone is
still typing in a form.

## 3.8 Quantities and normalization

- **Sizes** use binary (`Ki`, `Mi`, `Gi`, `Ti`) or decimal (`k`, `M`, `G`)
  suffixes. The canonical form is the largest binary unit that represents the
  value exactly.
- **Conversions are exact or rejected.** Lenses convert to engine units, such
  as MiB for Proxmox memory or whole GiB for `bpg/proxmox` disk sizes. A value
  the engine cannot represent exactly (a 1.5 GiB disk) is rejected at admission
  with a suggestion. Silent rounding would break the round-trip guarantees in
  [§4.5](04-dual-mode-and-sync.md#45-lenses).
- **Durations** use `90s`, `15m`, `3h` notation. **Times** carry an IANA time
  zone.
- **Enumerations that depend on the platform**, such as CPU models or storage
  content types, come from platform facts, so a form only offers values the
  target supports.

## 3.9 Allocations

Several identifiers must be chosen before anything exists, and they must be
stable so that rendering stays deterministic.

| Allocated value | Source | Rule |
| --------------- | ------ | ---- |
| Proxmox VM ID | Per-environment ranges in `nodr.yaml` | Lowest free ID in the range that is unused in the cluster inventory. Checked again at plan time. |
| IPv4 or IPv6 address | IPAM pool of the referenced `Network` | Lowest free address in the static range, excluding reservations and addresses seen on the network |
| MAC address | Derived from the resource `uid` and interface index | Deterministic, using the cluster's MAC prefix. This lets a DHCP reservation exist before the VM does. |
| Node assignment (`placement.assignedNode`) | Placement engine ([§6.7](06-proxmox.md#67-vm-and-container-lifecycle)) | Chosen once when `placement.node` is `auto`. For HA guests it then follows relocations as a runtime value. |
| API VIP, load-balancer pools | IPAM pools marked for the purpose | Reserved per cluster |

Allocations are written into the intent document during admission, so Git
alone is enough to reproduce the rendered code. The database keeps an index for
fast collision checks. When two drafts allocate the same address, validation of
the merged result catches it. If the later allocation was automatic and not yet
applied, nodr re-allocates it; otherwise the conflict is shown to the user.
Allocations are released when the resource is deleted, after a configurable
grace period that prevents immediate reuse of an address still cached by
clients.

## 3.10 Blueprints and composition

A **Blueprint** is a reusable, versioned template that expands into several
intent documents. A **BlueprintInstance** applies a blueprint with parameters.
Built-in high-level kinds such as `K3sCluster` are compositions provided by
plugins; blueprints let users define their own.

```yaml
apiVersion: nodr/v1alpha1
kind: Blueprint
metadata:
  name: k3s-ha
spec:
  version: 1.2.0
  parameters:                          # JSON Schema, drives the wizard
    type: object
    required: [network]
    properties:
      network: { type: string, x-nodr-ref: Network }
      servers: { type: integer, enum: [1, 3, 5], default: 3 }
      agents:  { type: integer, minimum: 0, default: 2 }
      size:    { type: string, enum: [small, medium, large], default: medium }
  resources:
    - template: templates/cluster.yaml
    - template: templates/server-vm.yaml
      forEach: range(params.servers)
    - template: templates/agent-vm.yaml
      forEach: range(params.agents)
---
apiVersion: nodr/v1alpha1
kind: BlueprintInstance
metadata:
  name: k3s-home
spec:
  blueprint: { name: k3s-ha, version: ~1.2 }
  parameters:
    network: lab
    servers: 3
    agents: 2
```

Design rules:

- Templates are structured: expressions are written as `${{ <CEL> }}` inside
  YAML values and evaluated on the parsed document, never by text
  substitution, so indentation and quoting errors cannot occur.
- Expanded resources are committed to Git as normal intent documents with an
  owner reference to the instance. They appear in the GUI and can be edited.
- Re-expansion after a parameter change or a blueprint upgrade is merged with
  a three-way merge against the previous expansion, which is the same
  mechanism used for code regeneration ([§4.6](04-dual-mode-and-sync.md#46-provenance-and-three-way-regeneration)).
  Local edits to expanded resources survive.
- Built-in blueprints for v1: K3s cluster (single server or HA), Docker host,
  Home Assistant OS VM, DNS filter (Pi-hole or AdGuard Home), WireGuard
  road-warrior VPN, media stack, and an SME baseline (backup, update and
  firewall policies).

## 3.11 Versioning and evolution

- Each kind is versioned independently: `v1alpha1`, then `v1beta1`, then `v1`.
- A workspace stores each kind in one **storage version**. Plugins provide pure
  conversion functions between versions.
- Moving to a new storage version is a change set that rewrites intent
  documents. It must pass the zero-diff gate: the engine plan after the
  rewrite must be empty ([§13.4](13-operations-and-delivery.md#134-upgrades)).
- Compatibility: alpha versions can change with an automated migration; beta
  versions are supported for at least two minor releases after deprecation;
  `v1` changes only through a new version with automated conversion.

## 3.12 Workspace repository layout

```text
<workspace>/
├── nodr.yaml               # workspace manifest: environments, engines, bindings, defaults
├── nodr.lock               # pinned toolchain, plugin and renderer versions with checksums
├── intent/                 # NRM documents: edited by the GUI, and by hand if preferred
│   ├── platform/           #   clusters, nodes, storage, backup targets, templates
│   ├── compute/            #   VMs, LXC containers, hosts
│   ├── network/            #   networks, routers, firewall, DNS, VPN
│   ├── apps/               #   Docker hosts, Compose projects, clusters, applications
│   └── automation/         #   policies, schedules, workflows, maintenance windows
├── terraform/              # rendered and customized HCL, one directory per state unit
│   └── pve-main-compute/
├── ansible/                # inventory, host_vars, group_vars, playbooks, requirements.yml
├── openwrt/                # UCI files, one directory per device
│   └── gw-01/
├── kubernetes/             # manifests and Kustomize overlays, one directory per cluster
│   └── k3s-home/
├── compose/                # Compose projects, one directory per host and project
│   └── docker-01/media/
├── custom/                 # user-owned code nodr runs but never rewrites
├── secrets/                # optional SOPS-encrypted secret files (age recipients)
└── .nodr/
    ├── render.lock         # managed block index: uid, file, address, renderer version, hashes
    └── schemas/            # generated JSON Schemas for editors outside nodr
```

The workspace manifest ties it together:

```yaml
apiVersion: nodr/v1alpha1
kind: Workspace
metadata:
  name: home
spec:
  environments:
    prod: { vmidRange: [1000, 1999] }
    lab:  { vmidRange: [2000, 2999] }
    templates: { vmidRange: [9000, 9099] }
  engines:
    opentofu: { version: ">= 1.11, < 2.0" }
    ansible:  { core: ">= 2.19, < 3.0" }
  bindings:                              # see section 5.4
    VirtualMachine/provision: opentofu
    VirtualMachine/guest: ansible
    Router/config: uci
  stateUnits:
    strategy: per-cluster-and-environment
  defaults:
    VirtualMachine:
      policies: { backup: nightly-7d4w, updates: security-weekly }
      lifecycle: { protection: true }
  git:
    mirror: { url: https://git.example.com/home/infra.git, direction: push }
```

## 3.13 Examples

### VirtualMachine

```yaml
apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-01
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C2D3
  labels: { nodr/environment: prod, app: website }
spec:
  placement:
    cluster: pve-main
    node: auto                    # auto, or the name of a node to pin the guest
    assignedNode: pve2            # allocated; follows HA relocations at runtime
    affinity:
      separateFrom: [web-02]      # never on the same node as web-02
  identity:
    vmid: 1012                    # allocated
  source:
    template: debian-12-cloud
  resources:
    cpu: { cores: 2, type: x86-64-v2-AES }
    memory: { size: 4Gi, minimum: 2Gi }
  disks:
    - name: root
      storage: ceph-vm
      size: 32Gi
      options: { discard: true, ssd: true, iothread: true }
  nics:
    - network: dmz
      mac: BC:24:11:3A:5E:01      # allocated
      ipv4: { mode: auto, address: 10.0.20.21/24 }   # address allocated from IPAM
  guest:
    agent: true
    cloudInit:
      user: ops
      authorizedKeys: [ops-team]  # SSHKey references
    profile: linux-baseline       # ConfigProfile
  ha:
    enabled: true
    maxRestart: 2
    maxRelocate: 1
  policies:
    backup: nightly-7d4w
    updates: security-weekly
  lifecycle:
    powerState: running           # running | stopped | unmanaged
    protection: true
    startOnBoot: true
  proxmox:                        # Proxmox-only settings, expert tier
    machine: q35
    bios: ovmf
    scsiController: virtio-scsi-single
```

### Network

```yaml
apiVersion: nodr/v1alpha1
kind: Network
metadata:
  name: iot
spec:
  vlan: 30
  zone: iot                       # security zone used by FirewallPolicy
  ipv4:
    subnet: 10.0.30.0/24
    gateway: 10.0.30.1
    dhcp: { range: 10.0.30.100-10.0.30.249, leaseTime: 12h }
    static: { range: 10.0.30.10-10.0.30.99 }
  dns:
    domain: iot.home.arpa
  wireless:
    ssid: Home-IoT
    security: wpa2-wpa3-mixed
    passphraseRef: wifi/home-iot
  realizeOn:
    routers: [gw-01, ap-attic]
    clusters: [pve-main]
```
