# Homelab example workspace

A small nodr workspace: a three-node Proxmox VE cluster, three networks, a
cloud-image template, an SSH key and two virtual machines. It follows the
layout of [the resource model](../../docs/design/03-resource-model.md#312-workspace-repository-layout).

```text
homelab/
├── nodr.yaml                       # workspace manifest
├── intent/                         # what the infrastructure should be
│   ├── access/ssh-keys.yaml
│   ├── compute/dns-01.yaml
│   ├── compute/web-01.yaml
│   ├── network/networks.yaml
│   └── platform/{pve-main,templates}.yaml
└── terraform/pve-main-compute/     # OpenTofu code for the cluster's VMs
    ├── versions.tf, providers.tf   # managed by nodr
    ├── vms.tf                      # managed blocks, one per VM
    └── custom.tf                   # owned by the team
```

`vms.tf` holds the code nodr renders for each VM. The block of `web-01`
carries two changes made in code, as in the
[worked example of the design](../../docs/design/04-dual-mode-and-sync.md#49-worked-example):

- `cores = var.web_cores` makes the core count code-owned: the GUI shows it
  read-only, and nodr does not overwrite the expression.
- The `smbios` block is an extension: nodr has no intent field for it and
  keeps it as it is.

A test (`examples/examples_test.go`) checks that the code and the intent of
this workspace agree, so that syncing it would change neither.

Some values are placeholders: the image checksum, the SSH public key and the
cluster endpoints. The unit has no `backend.tf`, so OpenTofu keeps state
locally; with nodr, the state lives in nodr's state service
([design §5.5](../../docs/design/05-compiler-and-engines.md#55-engine-code-conventions)).
