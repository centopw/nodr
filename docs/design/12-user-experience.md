# 12. User Experience

> Part of the [nodr technical design](../README.md).

## 12.1 Principles

1. **Progressive disclosure.** Beginners see what they need to succeed.
   Experts can reach every setting without switching tools.
2. **Explain before acting.** Every change shows its impact in plain language
   before anything happens.
3. **Show the code.** Every GUI action can show the code it produced, which
   gives users a gradual path from clicking to IaC.
4. **Everything is undoable.** Change sets can be reverted, risky steps take
   snapshots, and deleted guests are retained for a while.
5. **One vocabulary.** The GUI, code, API and documentation use the same names
   for the same things.
6. **Fast for experts.** Command palette, keyboard shortcuts, bulk actions,
   and the API behind every button.
7. **Honest status.** Failures, drift, pending reboots and code ownership are
   always visible, never hidden to make the dashboard look green.

## 12.2 Information architecture

| Section | Contents |
| ------- | -------- |
| **Home** | Health summary, alerts, pending changes, drift, updates, backup status, upcoming maintenance |
| **Infrastructure** | Clusters, nodes, virtual machines, LXC containers, storage, templates, backups |
| **Network** | Topology map, networks, routers and access points, firewall, DNS and DHCP, VPN |
| **Apps** | Catalog, Docker hosts, Compose projects, Kubernetes clusters, applications |
| **Automation** | Policies, schedules, workflows, maintenance windows, runs |
| **Changes** | Change sets, history, drift inbox |
| **Code** | The Advanced Mode IDE |
| **Settings** | Connections, users and access, secrets, runners, plugins, notifications, workspace |

## 12.3 Modes and disclosure mechanics

- **Mode toggle.** Simple or Advanced, per user, in the header, remembered
  across sessions ([§4.1](04-dual-mode-and-sync.md#41-management-modes)).
- **Field tiers.** `basic` fields appear in wizards and forms, `standard`
  fields behind "More settings", and `expert` fields in Advanced Mode
  ([§3.6](03-resource-model.md#36-schemas-and-field-tiers)). Searching the
  settings of a resource finds expert fields in Simple Mode too, with a short
  explanation.
- **Show code.** Every form and resource page can open the split view.
- **Inherited values** show their source: workspace defaults, a policy or a
  blueprint.
- **Ownership badges** on fields: "managed in code", "code extension",
  "drift ignored".
- **Command palette** (`Ctrl+K` or `Cmd+K`): navigate, run actions, open files,
  start workflows.
- **Bulk actions** in every table: attach a policy, add a label, start or stop,
  change the maintenance window.

## 12.4 Key flows

### First-run onboarding (about ten minutes)

1. Create the administrator account or connect an OIDC provider. Name the
   workspace and optionally connect a Git remote.
2. Connect Proxmox VE: URL, fingerprint confirmation and a one-time
   administrator credential, which nodr exchanges for a least-privilege token.
3. Review what discovery found and pick what to adopt. Nothing is selected by
   default. Adopted guests keep running exactly as before.
4. Optionally connect an OpenWrt router and a Proxmox Backup Server.
5. Choose starter policies. The "Recommended" set is nightly backups kept 7
   days and 4 weeks, weekly security updates in a Sunday early-morning window,
   and a monthly restore test. Each is explained in one sentence.
6. Review the resulting change set and apply it.
7. Land on the dashboard with a checklist of suggested next steps.

### Creating a VM

- **Simple Mode:** *New VM*, pick a template, a size (S, M or L, with the
  actual numbers shown), a network by name and a name. The review screen
  reads, for example: "Creates web-03 with 2 vCPUs and 4 GiB memory on pve1,
  IP 10.0.20.23, backed up nightly and patched weekly." Users can choose to
  apply small, low-risk changes directly; otherwise a change set is created.
- **Advanced Mode:** the full form with every tier, *Create from YAML*,
  *Duplicate*, or writing the intent document in the IDE.

### Enabling HA

Switching HA on opens the readiness panel from
[§6.6](06-proxmox.md#66-high-availability): each check passes or fails, and each
failure comes with a fix. Confirming creates a change set.

### Adding a network end to end

1. *New network*: name and purpose (IoT, Guests, Servers, DMZ).
2. nodr suggests a free VLAN ID and a non-overlapping subnet.
3. Choose where to realize it: routers, access points, clusters.
4. Optionally add a Wi-Fi SSID.
5. A firewall preset follows from the purpose.
6. The review lists the OpenWrt and Proxmox changes and states that every
   router change is protected by automatic rollback.
7. Applying shows per-device progress, including each confirmation.

### Deploying a K3s cluster

Choose the K3s blueprint, a size (single node or HA), the network and the
component defaults. The review lists five VMs, DNS records, addresses and
schedules. Applying shows the execution graph from
[§5.6](05-compiler-and-engines.md#56-cross-engine-orchestration) with live status,
and ends with a kubeconfig download.

### Scheduling updates

Select resources, attach an `UpdatePolicy` in bulk, pick a maintenance window
and check the calendar preview of the next runs.

### Resolving drift

The drift inbox shows the expected and actual values, and when Proxmox's task
log records it, who made the change and when. The user chooses *Revert*,
*Adopt* or *Ignore*, which creates a change set.

### Resolving a GUI and code conflict

A lock icon marks code-owned fields. Its popover shows the expression and
offers *Edit in code* and *Take over*. *Take over* shows the code diff before
anything changes ([§4.8](04-dual-mode-and-sync.md#48-conflict-handling)).

### Switching engines

On the resource's *Engines* tab, "Provisioned by OpenTofu" has a *Change*
action. nodr runs the preflight, shows blockers and their options, and then
runs the handoff with the zero-diff gate visible as a step.

### Exporting

*Workspace settings*, then *Export*: choose whether to include engine state
and encrypted secrets, then download the bundle.

## 12.5 Change review

The review screen is where beginners build trust and experts check details.

```text
┌ Change set cs-42 · Increase web-01 memory ──────────────── Risk: medium ┐
│ Update 1 VM. web-01 needs a reboot to apply the memory change.          │
│ Author: alex · Approvals: not required                                  │
├─────────────────────────────────────────────────────────────────────────┤
│ ▸ VirtualMachine web-01                  update · reboot required       │
│     Memory                4 GiB → 8 GiB                                 │
├ Intent diff │ Code diff │ Engine plan │ Policy: 2 passed ───────────────┤
│                                                                         │
│ [ Apply now ]  [ Apply in next window, Sun 02:00 ]  [ Request review ]  │
└─────────────────────────────────────────────────────────────────────────┘
```

- **Summary sentence and risk badge** at the top.
- **Per-resource cards** with the action, impact badges (reboot, replace,
  data loss) and field changes in human units.
- **Tabs** for the intent diff, the code diff with ownership annotations, the
  raw engine plan, policy results and validation warnings.
- **Apply options:** now, in the next maintenance window, or after review.
  Changes that need a reboot can apply now and reboot in the window.
- **Live progress** after applying: the execution graph with step status and
  logs. A partial failure explains what completed and offers *Retry* and
  *Resume*.

## 12.6 Advanced Mode IDE

```text
┌ Files ─────────────────┬ terraform/pve-main-compute/vms.tf ─┬ Inspector ─────────────┐
│ intent/                │ 12  # nodr:managed vm/web-01       │ vm/web-01              │
│ terraform/             │ 13  resource "proxmox_virtual_...  │ Synced        14       │
│  └ pve-main-compute/   │ 14    cpu {                        │ Code-owned     0       │
│     ├ vms.tf      ● 1  │ 15      cores = 4         ↔ GUI    │ Extensions     1       │
│     ├ ha.tf            │ 16      type  = "x86-64-v2-AES"    │ [Open in GUI]          │
│     └ custom.tf   user │ 17    }                            │ [Compare to pristine]  │
│ ansible/               │                                    │                        │
├────────────────────────┴────────────────────────────────────┴────────────────────────┤
│ Problems (0) │ Plan │ Runs │ Git history                                            │
└──────────────────────────────────────────────────────────────────────────────────────┘
```

The file tree marks managed files, customized files (with a count) and
user-owned files. The inspector shows the ownership summary of the selected
block. Editor features are described in
[§4.2](04-dual-mode-and-sync.md#42-advanced-mode-in-the-browser).

## 12.7 Dashboards and status

- **Home** stays small: the health of Proxmox clusters, nodes, guests,
  routers and Kubernetes clusters, open alerts, pending changes, drift count,
  available updates, backup success in the last 24 hours with the last restore
  test, upcoming maintenance and capacity per cluster.
- **Resource pages** show the conditions `Synced`, `Applied`, `Healthy` and
  `Drifted` as badges with explanations, plus outputs, recent runs and related
  resources.
- **Topology views:** a network map (routers, VLANs, trunks, access points,
  clusters), a cluster map (nodes, guests, HA rules) and a dependency graph for
  any resource.
- **Monitoring** links to Grafana dashboards when configured. nodr does not
  try to be a monitoring system (non-goal N2).

## 12.8 Guidance and learning

- Every field has help text from its schema, with links to vendor
  documentation.
- Disabled actions explain why: a missing permission, a code-owned field or a
  failed readiness check.
- After each GUI change, a "See the code for this change" link opens the diff.
- Blueprints and catalog entries explain what they create.
- Empty states suggest the next step.
- Engine errors are translated into plain language with a suggested fix, and
  the original error stays one click away.

## 12.9 Accessibility and internationalization

- WCAG 2.2 level AA, full keyboard navigation and screen-reader labels.
- Color is never the only signal; badges always carry text or an icon.
- Diagrams such as the topology map have list or table alternatives.
- Messages use the ICU message format. Units, dates and times follow the
  user's locale, and schedules show both the user's time zone and the
  schedule's own.
- The reduced-motion preference is respected.

## 12.10 Mobile

The layout is responsive for status, alerts, approvals, starting runbooks and
basic console access. The IDE is desktop-only. In v1, push notifications reach
phones through ntfy or Gotify rather than a native app.
