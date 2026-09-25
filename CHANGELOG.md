# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- The technical design for nodr and the architecture decisions (ADRs)
  behind it, covering the resource model, dual-mode sync between the GUI
  and code, the intent compiler and engines, Proxmox, networking,
  containers, security, and the API and CLI.
- The `nodr/v1alpha1` intent model: JSON Schemas and Go types for
  workspaces, Proxmox clusters, networks, templates, SSH keys and virtual
  machines, with validation of metadata, semantics and the references
  between resources.
- `nodr validate`, which checks a workspace on disk and reports problems
  with their file, line and field.
- A bidirectional HCL lens engine that renders, lifts and puts Go structs
  as HCL blocks, editing only the bytes it has to so comments, hand
  formatting and code written by hand survive.
- A Proxmox VM lens that maps `VirtualMachine` intent to a
  `proxmox_virtual_environment_vm` resource of the `bpg/proxmox`
  provider, and back.
- `nodr render`, which prints the generated OpenTofu code for one or
  more virtual machines.
- `nodr describe`, which shows where a resource is defined and managed,
  and `nodr describe --ownership`, which lists the owner of every field
  as text or JSON.
- A homelab example workspace with a three-node Proxmox cluster, three
  networks, a template, an SSH key and two virtual machines, and
  matching OpenTofu code.
- Admission for virtual machines, which allocates a UID, a Proxmox guest
  ID, a placement node and IPv4 addresses and writes them into the
  intent files with minimal, comment-preserving edits. Run it locally
  with `nodr admit`, or preview the changes with `--dry-run`.
- Compilation of virtual machine intent into the OpenTofu state units
  below `terraform/`. Managed blocks are updated in place, so code-owned
  values, extensions and hand edits stay; a new VM gets a block in
  `terraform/<cluster>-compute/vms.tf`, and a unit gets `versions.tf`
  and `providers.tf` if it lacks them.
- `nodr plan`, which compiles intent, writes the files that change and
  plans each state unit with a local OpenTofu, printing what each unit
  would add, change, replace and destroy, and `nodr apply`, which then
  applies the saved plans after confirmation. Plans that replace or
  destroy resources need `--allow-destroy`, even with `--auto-approve`.
- `nodr validate` reports values that must be unique but are used more
  than once: guest IDs of virtual machines and templates within a
  cluster, IPv4 addresses of network interfaces within a network or
  shared with an endpoint of the guest's cluster, and MAC addresses
  within a cluster.
- A contributor guide covering setup, repository layout, tests and Git
  conventions, and continuous integration that builds, tests, lints and
  validates the example code on every change.
