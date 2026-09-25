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

nodr is in the design phase. The technical design is in
[`docs/`](docs/README.md), and architecture decisions are recorded in
[`docs/adr/`](docs/adr/README.md).
