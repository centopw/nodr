# nodr

nodr is a hybrid infrastructure-as-code and web GUI platform for managing
small-business and homelab infrastructure: Proxmox VE, OpenWrt, Docker, K3s
and Kubernetes.

**Click or code: one infrastructure, one source of truth.** Every GUI action
becomes a reviewable change to standard code in Git (OpenTofu/Terraform,
Ansible, OpenWrt UCI, Kubernetes manifests, Docker Compose). Every code change
shows up in the GUI. Changes made outside nodr are detected and can be
reverted or adopted.

## Highlights

- **Dual-mode management.** A Simple Mode for guided, form-based work and an
  Advanced Mode with an in-browser IDE for the generated code. Field-level
  ownership keeps both in sync without overwriting anyone's edits.
- **Proxmox VE orchestration.** From a single node to multi-node HA clusters
  with Ceph, including the full VM and LXC lifecycle.
- **Built-in automation.** Update, backup, snapshot and restart policies,
  maintenance windows and durable workflows, such as rolling cluster updates.
- **Network management.** OpenWrt configuration with automatic rollback,
  integrated IPAM, DHCP and DNS, and networks defined once for routers,
  hypervisors and clusters.
- **Containers and Kubernetes.** Docker hosts, Compose projects, K3s and other
  Kubernetes distributions, applications and a catalog.
- **No lock-in.** A tool-neutral intent model, engines that can be switched
  per resource, and an export to a standalone Terraform and Ansible
  repository.

## Status

nodr is at an early stage. The technical design is in
[`docs/`](docs/README.md), and architecture decisions are recorded in
[`docs/adr/`](docs/adr/README.md). Work on the first milestone,
[M0 Foundations](docs/design/13-operations-and-delivery.md#138-delivery-roadmap),
has started. So far the repository contains:

- **The intent model** `nodr/v1alpha1`: JSON Schemas for each kind, semantic
  checks, and validation of the references between resources.
- **The lens engine** that keeps intent and HCL in sync, with field-level
  ownership. It edits only the bytes it has to, so comments, hand formatting
  and code written by hand survive.
- **The VM lens**, which maps a `VirtualMachine` to a
  `proxmox_virtual_environment_vm` of the `bpg/proxmox` provider and back.
  Property-based tests check the lens laws of
  [§4.5](docs/design/04-dual-mode-and-sync.md#45-lenses).
- **The `nodr` command line**, which validates a workspace, allocates VM
  IDs, nodes and addresses, renders engine code and shows who owns each
  field.

## Try it

With Go 1.24 or later:

```console
$ make build
$ bin/nodr validate -w examples/homelab
8 documents valid
$ bin/nodr render -w examples/homelab vm/web-01
$ bin/nodr describe -w examples/homelab vm/web-01 --ownership
```

The [homelab example](examples/homelab/README.md) explains the workspace.
[CONTRIBUTING.md](CONTRIBUTING.md) covers development.
