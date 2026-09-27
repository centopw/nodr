# Slice C: Cluster discovery

> Third of four sequenced slices toward real-Proxmox-node adoption (§6.3 of
> `docs/design/06-proxmox.md`). Order: A (Proxmox client + secrets store,
> merged) → B (cluster-connect wizard, credential wiring, server hardening,
> merged) → **C (this doc)** → D (adoption).

## Goal

1. **Read-only observer.** For a connected cluster (a `ProxmoxCluster` intent
   document written by `nodr cluster connect`), enumerate its live QEMU
   guests via the Proxmox API: `GetClusterResources(ctx, "vm")` for the
   cluster-wide overview, `GetQEMUConfig(ctx, node, vmid)` per guest for tags
   and description. §6.3 also names node network/storage configuration, HA
   configuration, backup jobs and pools as observer inputs; this slice reads
   only what step 1-2 below need — guest inventory, tags and description.
   Reading the rest is deferred to whichever later slice needs it (HA/backup
   config isn't needed for adoption decisions).
2. **Classify against workspace intent (§6.3 step 1).** For every live guest,
   decide whether nodr already manages it — a `VirtualMachine` document in
   the workspace whose `placement.cluster` and `identity.vmid` match — or
   whether it's undiscovered.
3. **Flag other-tool ownership (§6.3 step 2).** Among undiscovered guests,
   flag the ones whose tags or description mention another IaC tool
   (Terraform, Ansible, Packer, Pulumi, OpenTofu), matching §6.3's own
   example ("guests whose tags or descriptions mention Terraform").
4. **Report the result.** `nodr cluster discover <cluster>` prints a table:
   VMID, name, node, status, CPU, memory and a status column (`managed`,
   `discovered`, `discovered (terraform?)`). The API exposes the same data
   as `cluster.discover`, a `runCommand` command returning JSON, for the web
   UI or scripting to consume later.

This slice is entirely read-only: no intent is generated, no VM ID is
adopted, no file is written. §6.3 steps 3-5 (choose what to adopt, generate
intent/code/import blocks, run the zero-diff plan gate and commit) are Slice
D.

## Non-goals (deferred to later slices or out of scope entirely)

- **Adoption itself** (§6.3 steps 3-5: selection, intent/code/import
  generation, the zero-diff plan gate, commit) — Slice D.
- **LXC containers.** `internal/proxmox` has no `GetNodeLXC`/`GetLXCConfig`
  methods, and `internal/nrm/v1alpha1` has no container kind (only
  `Workspace`, `ProxmoxCluster`, `Network`, `Template`, `SSHKey`,
  `VirtualMachine`, plus four unimplemented placeholders —
  `ConfigProfile`, `BackupPolicy`, `UpdatePolicy`, `Router` — none of which
  is a container). Discovering LXC guests needs both a client method and a
  kind; out of scope for this repo entirely until that lands.
- **Node network/storage/HA/backup/pool discovery.** §6.3's observer reads
  more than guest inventory; this slice reads only what guest
  classification and other-tool flagging need. The rest is added by
  whichever future slice consumes it.
- **Web UI.** Same precedent as `cluster connect`: no dashboard surface
  ships in the slice that adds the CLI command and API command; only CLI +
  API this time too.
- **REST resource shape from `docs/design/11-api-and-cli.md`.** §11 names an
  aspirational `POST /workspaces/{ws}/imports` REST resource for "Discovery
  and adoption" and a `nodr import <platform>` CLI command. Slice B already
  established the shipped convention of a `commands` sub-resource
  (`POST /workspaces/{ws}/commands` with a `command` field) instead of a
  resource-per-verb REST surface, and a `cluster` CLI subcommand instead of
  a bare verb. This slice follows that same shipped convention: `nodr
  cluster discover <cluster>` and `case "cluster.discover":` in
  `runCommand`'s switch, not `nodr import` or a new REST resource. Framed
  as a deliberate, documented deviation, matching how Slice B's design doc
  documented its own deviations from §10.3's aspirational auth model.

## `internal/proxmoxbootstrap`

Two unexported constants become exported so `internal/proxmoxdiscovery` (and
`internal/cli`, `internal/api`) can build a Proxmox client authenticated as
the bootstrapped operator without redeclaring its identity:

```go
// internal/proxmoxbootstrap/bootstrap.go
const (
	BootstrapUser    = "nodr@pve" // was bootstrapUser
	bootstrapRole    = "NodrOperator"
	BootstrapTokenID = "nodr" // was bootstrapTokenID
)
```

`bootstrapRole` stays unexported: only the bootstrap flow itself needs the
role name; discovery only needs the user and token ID to authenticate as
the token Slice B already created (`proxmoxbootstrap.Bootstrap`'s return
value is a one-time secret, not persisted — the workspace's secrets store
holds it under `credentialsRef`, and discovery must resolve it the same
way `cluster connect` wrote it: `SetAPIToken(user, tokenID, secret)`).
`bootstrap.go`'s own four call sites of these constants (lines 24-51) move
to the exported names with no other change; a grep across the repository
confirms no other package references them today, so this is a pure rename,
zero behavior change.

## `internal/proxmoxdiscovery` (new package)

```go
// internal/proxmoxdiscovery/discovery.go
package proxmoxdiscovery

import (
	"context"
	"fmt"
	"strings"

	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/quantity"
	"github.com/centopw/nodr/internal/workspace"
)

// Status classifies one discovered guest against workspace intent.
type Status string

const (
	StatusManaged        Status = "managed"
	StatusDiscovered      Status = "discovered"
	StatusOtherToolTagged Status = "discovered (other tool?)"
)

// Guest is one live QEMU guest discovered on a cluster, classified against
// the workspace's existing intent.
type Guest struct {
	VMID        int
	Name        string
	Node        string
	Status      string // Proxmox power status: "running", "stopped"
	CPUs        int
	Memory      string // formatted with internal/quantity, e.g. "4Gi"
	Disk        string
	Tags        []string
	Description string
	Classified  Status
}

// otherTools are the substrings, matched case-insensitively against a
// guest's tags and description, that flag it as owned by another IaC tool
// (§6.3 step 2's own example: "tags or descriptions mention Terraform").
var otherTools = []string{"terraform", "ansible", "packer", "pulumi", "opentofu"}

// Discover reads the live QEMU guests of cluster from client, and
// classifies each against ws's existing VirtualMachine intent.
func Discover(ctx context.Context, client *proxmox.Client, ws *workspace.Workspace, cluster string) ([]Guest, error) {
	resources, err := client.GetClusterResources(ctx, "vm")
	if err != nil {
		return nil, fmt.Errorf("proxmoxdiscovery: list cluster resources: %w", err)
	}

	managed := managedVMIDs(ws, cluster)

	guests := make([]Guest, 0, len(resources))
	for _, r := range resources {
		if r.Type != "qemu" || r.Template == 1 {
			continue
		}
		tags, description, err := guestTags(ctx, client, r.Node, r.VMID)
		if err != nil {
			return nil, err
		}
		mem, err := quantity.FromUnits(r.MaxMem, 1)
		if err != nil {
			return nil, fmt.Errorf("proxmoxdiscovery: guest %d memory: %w", r.VMID, err)
		}
		disk, err := quantity.FromUnits(r.MaxDisk, 1)
		if err != nil {
			return nil, fmt.Errorf("proxmoxdiscovery: guest %d disk: %w", r.VMID, err)
		}
		g := Guest{
			VMID:        r.VMID,
			Name:        r.Name,
			Node:        r.Node,
			Status:      r.Status,
			CPUs:        r.MaxCPU,
			Memory:      mem.String(),
			Disk:        disk.String(),
			Tags:        tags,
			Description: description,
		}
		g.Classified = classify(g, managed)
		guests = append(guests, g)
	}
	return guests, nil
}

// managedVMIDs returns the VM IDs that ws already manages on cluster.
func managedVMIDs(ws *workspace.Workspace, cluster string) map[int]bool {
	managed := make(map[int]bool)
	for _, d := range ws.OfKind(v1alpha1.KindVirtualMachine) {
		vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](d)
		if err != nil {
			continue
		}
		if vm.Spec.Placement.Cluster == cluster {
			managed[vm.Spec.Identity.VMID] = true
		}
	}
	return managed
}

func classify(g Guest, managed map[int]bool) Status {
	if managed[g.VMID] {
		return StatusManaged
	}
	if mentionsOtherTool(g.Tags, g.Description) {
		return StatusOtherToolTagged
	}
	return StatusDiscovered
}

func mentionsOtherTool(tags []string, description string) bool {
	haystacks := append([]string{description}, tags...)
	for _, h := range haystacks {
		lower := strings.ToLower(h)
		for _, tool := range otherTools {
			if strings.Contains(lower, tool) {
				return true
			}
		}
	}
	return false
}

// guestTags fetches a guest's raw configuration and returns its Proxmox
// tags (semicolon-separated in the "tags" setting) and description.
func guestTags(ctx context.Context, client *proxmox.Client, node string, vmid int) (tags []string, description string, err error) {
	config, _, err := client.GetQEMUConfig(ctx, node, vmid)
	if err != nil {
		return nil, "", fmt.Errorf("proxmoxdiscovery: guest %d config: %w", vmid, err)
	}
	if raw, ok := config.RawSettings["tags"]; ok && raw != "" {
		tags = strings.Split(raw, ";")
	}
	description = config.RawSettings["description"]
	return tags, description, nil
}
```

`QEMUConfig.RawSettings` (`internal/proxmox/types.go:53-56`) is
`map[string]string` decoded with `json:"-"` — the client leaves per-VM
settings undecoded because their key set varies per guest; `guestTags`
reads the two keys discovery needs (`tags`, `description`) directly out of
that map, the same pattern Proxmox's own API uses (semicolon-separated tag
strings, a free-text description field).

`quantity.FromUnits(n, 1)` (`internal/quantity/quantity.go:92-101`) turns a
raw byte count into a `Quantity` for display: `unit=1` satisfies its
`unit <= 0` guard, so the returned `Quantity` holds exactly `n` bytes, and
`.String()` formats it (e.g. `4294967296` bytes → `4Gi`).

## `internal/cli`

`cluster.go` gains one line in `clusterCommand` and a new subcommand:

```go
func (a *app) clusterCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage Proxmox cluster connections",
	}
	cmd.AddCommand(a.clusterConnectCommand())
	cmd.AddCommand(a.clusterDiscoverCommand())
	return cmd
}

func (a *app) clusterDiscoverCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "discover <cluster>",
		Short: "List live QEMU guests on a connected Proxmox VE cluster and classify them against workspace intent",
		Long: `Discover connects to a cluster nodr already manages (docs/design/06-proxmox.md
§6.3) using its stored API token, lists every live QEMU guest, and reports
whether nodr already manages it, it looks undiscovered, or its tags or
description mention another infrastructure-as-code tool. Discover makes no
changes: it neither writes intent nor touches the cluster.`,
		Args: exactlyOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.clusterDiscover(cmd.Context(), args[0])
		},
	}
	return cmd
}

func (a *app) clusterDiscover(ctx context.Context, clusterName string) error {
	loaded, err := a.mustLoad()
	if err != nil {
		return err
	}

	credentialsRef, ok := planapply.CredentialsRefFor(loaded.ws, clusterName)
	if !ok {
		return fmt.Errorf("nodr: cluster %q not found, or has no credentialsRef; run 'nodr cluster connect' first", clusterName)
	}
	resolve, err := a.secretsResolve(loaded.ws.Root)
	if err != nil {
		return err
	}
	if resolve == nil {
		return fmt.Errorf("nodr: no secrets store found at %s; run 'nodr cluster connect' first", filepath.Join(loaded.ws.Root, ".nodr", "secrets.db"))
	}
	secret, err := resolve(ctx, credentialsRef)
	if err != nil {
		return fmt.Errorf("nodr: resolve %s: %w", credentialsRef, err)
	}

	d := loaded.ws.Find(nrm.Ref{Kind: v1alpha1.KindProxmoxCluster, Name: clusterName})
	spec, err := v1alpha1.Decode[v1alpha1.ProxmoxClusterSpec](d)
	if err != nil {
		return fmt.Errorf("nodr: decode cluster %q: %w", clusterName, err)
	}
	if len(spec.Spec.Endpoints) == 0 {
		return fmt.Errorf("nodr: cluster %q has no endpoints", clusterName)
	}
	var httpClient *http.Client
	if spec.Spec.TLS != nil && spec.Spec.TLS.Fingerprint != "" {
		httpClient = proxmox.NewPinnedHTTPClient(spec.Spec.TLS.Fingerprint)
	}
	client := proxmox.NewClient(spec.Spec.Endpoints[0], httpClient)
	client.SetAPIToken(proxmoxbootstrap.BootstrapUser, proxmoxbootstrap.BootstrapTokenID, string(secret))

	guests, err := proxmoxdiscovery.Discover(ctx, client, loaded.ws, clusterName)
	if err != nil {
		return err
	}

	rows := [][]string{{"VMID", "NAME", "NODE", "STATUS", "CPU", "MEMORY", "NODR"}}
	for _, g := range guests {
		rows = append(rows, []string{
			strconv.Itoa(g.VMID), g.Name, g.Node, g.Status,
			strconv.Itoa(g.CPUs), g.Memory, string(g.Classified),
		})
	}
	return writeTable(a.stdout, 2, rows)
}
```

`credentialsRef, ok := planapply.CredentialsRefFor(...)` and
`a.secretsResolve(root)` are Slice B's own plumbing
(`internal/planapply/planapply.go:40-50`, `internal/cli/cli.go`'s
`secretsResolve` method) — this reuses them exactly as written, no change
to either. `writeTable` (`internal/cli/commands.go`, `text/tabwriter`
backed) is the same helper `nodr describe`'s text output already uses.

New imports in `cluster.go`: `net/http`, `strconv`,
`github.com/centopw/nodr/internal/nrm`, `github.com/centopw/nodr/internal/planapply`,
`github.com/centopw/nodr/internal/proxmoxdiscovery`.

## `internal/api`

`runCommand`'s switch (`internal/api/api.go`) gains one case, inserted
after the `workspace.apply` case and before `default`:

```go
	case "cluster.discover":
		a.clusterDiscover(w, r, request)
```

```go
type clusterDiscoverParams struct {
	Cluster string `json:"cluster"`
}

type discoveredGuest struct {
	VMID        int      `json:"vmid"`
	Name        string   `json:"name"`
	Node        string   `json:"node"`
	Status      string   `json:"status"`
	CPU         int      `json:"cpu"`
	Memory      string   `json:"memory"`
	Disk        string   `json:"disk"`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description,omitempty"`
	Classified  string   `json:"classified"`
}

func (a *server) clusterDiscover(w http.ResponseWriter, r *http.Request, request commandRequest) {
	ws, ok := a.workspace(w, r)
	if !ok {
		return
	}

	var params clusterDiscoverParams
	if len(request.Params) > 0 {
		dec := json.NewDecoder(bytes.NewReader(request.Params))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&params); err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid request", fmt.Sprintf("decode params: %v", err), nil)
			return
		}
	}
	if params.Cluster == "" && request.Target != "" {
		params.Cluster = request.Target
	}
	if params.Cluster == "" {
		message := "cluster is required"
		writeProblem(w, http.StatusBadRequest, "Invalid request", message, []fieldError{{Path: "cluster", Message: message}})
		return
	}

	credentialsRef, ok := planapply.CredentialsRefFor(ws, params.Cluster)
	if !ok {
		writeProblem(w, http.StatusNotFound, "Cluster not found", fmt.Sprintf("cluster %q not found, or has no credentialsRef", params.Cluster), nil)
		return
	}
	var resolve planapply.Resolver
	if a.secrets != nil {
		resolve = a.secrets.Resolve
	}
	if resolve == nil {
		writeProblem(w, http.StatusServiceUnavailable, "Secrets store unavailable", "no secrets store is configured", nil)
		return
	}
	secret, err := resolve(r.Context(), credentialsRef)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "Cannot resolve credentials", err.Error(), nil)
		return
	}

	d := ws.Find(nrm.Ref{Kind: v1alpha1.KindProxmoxCluster, Name: params.Cluster})
	spec, err := v1alpha1.Decode[v1alpha1.ProxmoxClusterSpec](d)
	if err != nil || len(spec.Spec.Endpoints) == 0 {
		writeProblem(w, http.StatusInternalServerError, "Invalid cluster", fmt.Sprintf("cluster %q has no endpoints", params.Cluster), nil)
		return
	}
	var httpClient *http.Client
	if spec.Spec.TLS != nil && spec.Spec.TLS.Fingerprint != "" {
		httpClient = proxmox.NewPinnedHTTPClient(spec.Spec.TLS.Fingerprint)
	}
	client := proxmox.NewClient(spec.Spec.Endpoints[0], httpClient)
	client.SetAPIToken(proxmoxbootstrap.BootstrapUser, proxmoxbootstrap.BootstrapTokenID, string(secret))

	guests, err := proxmoxdiscovery.Discover(r.Context(), client, ws, params.Cluster)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "Discovery failed", err.Error(), nil)
		return
	}

	out := make([]discoveredGuest, len(guests))
	for i, g := range guests {
		out[i] = discoveredGuest{
			VMID: g.VMID, Name: g.Name, Node: g.Node, Status: g.Status,
			CPU: g.CPUs, Memory: g.Memory, Disk: g.Disk,
			Tags: g.Tags, Description: g.Description, Classified: string(g.Classified),
		}
	}
	writeJSON(w, http.StatusOK, out)
}
```

`params.Cluster` falling back to `request.Target` mirrors
`decodeActionParams`'s own `params.Name`/`req.Target` fallback exactly
(`internal/api/api.go`), so `cluster.discover` can be invoked either with
`{"command":"cluster.discover","target":"pve-main"}` or
`{"command":"cluster.discover","params":{"cluster":"pve-main"}}`.
`var resolve planapply.Resolver; if a.secrets != nil { resolve = a.secrets.Resolve }`
is copied verbatim from `planWorkspace`'s own resolver setup.

New imports in `api.go`: `net/http` (already imported), plus
`github.com/centopw/nodr/internal/planapply` (already imported for
`planWorkspace`), `github.com/centopw/nodr/internal/proxmox`,
`github.com/centopw/nodr/internal/proxmoxbootstrap`,
`github.com/centopw/nodr/internal/proxmoxdiscovery`.

## Testing

- **`internal/proxmoxbootstrap`**: existing `bootstrap_test.go` updated for
  the renamed constants only where it references them directly (grep
  confirms it currently doesn't reference `bootstrapUser`/`bootstrapTokenID`
  by name, only by the resulting `Result.User`/`.TokenID` values, so no
  change is expected there — verified during implementation before editing).
- **`internal/proxmoxdiscovery`**: new `discovery_test.go` using
  `proxmoxtest.NewServer`, following `endpoints_test.go`'s style —
  numbered-comment routes, one server per test. Cases: a cluster resource
  list mixing a managed guest (VMID matches a `VirtualMachine` fixture), an
  untagged discoverable guest, and a guest whose `tags` setting contains
  `terraform` — asserting each ends up with the right `Status`. A template
  guest (`ClusterResource.Template == 1`) is asserted excluded from the
  result. `proxmoxtest.NewServer` matches routes on `r.URL.Path` only
  (`internal/proxmox/proxmoxtest/server.go:24`), so
  `"GET /api2/json/cluster/resources"` is registered without a query
  string even though `GetClusterResources` appends `?type=vm`; the handler
  may still assert `r.URL.Query().Get("type")` itself, matching
  `endpoints_test.go`'s own pattern for that same route.
- **`internal/cli`**: new `TestClusterDiscover_ReportsGuests` in
  `cluster_test.go`. No existing fixture combines a live
  `proxmoxtest.NewServer` cluster with a workspace whose `ProxmoxCluster`
  document points at that server's URL and whose secrets store holds that
  cluster's token — `TestClusterConnect_BootstrapsAndWritesIntent` only
  exercises the wizard that *creates* such a setup, it doesn't start from
  one. This test builds its own combined fixture: a `proxmoxtest.NewServer`
  serving `cluster/resources` and `qemu/{vmid}/config`, a workspace written
  via `writeWorkspace` with an intent file whose `ProxmoxCluster.endpoints`
  is `[]string{srv.URL}` and `credentialsRef: proxmox/pve-main-token`
  (matching `writeWorkspace`'s own pre-seeded secret,
  `internal/cli/cli_test.go:62`), and a `VirtualMachine` document for the
  "already managed" case. Asserts the table's `NODR` column values for all
  three classifications.
- **`internal/api`**: new `TestClusterDiscover` in `api_test.go`, mirroring
  the CLI test's combined fixture (workspace + live `proxmoxtest.NewServer`
  cluster + seeded secret), issuing
  `POST /api/v1/workspaces/test/commands` with
  `{"command":"cluster.discover","target":"pve-main"}` via `authedRequest`,
  asserting the JSON body's `classified` fields. A second case posts an
  unknown cluster name and asserts 404.
- Full suite: `go build ./... && go test ./... && golangci-lint run` (per
  `.golangci.yml`: `enable: [errname, errorlint, gocritic, misspell,
  nolintlint, revive, unconvert, unparam]`, `goimports` with
  `local-prefixes: github.com/centopw/nodr`).

## Dependencies added

None. `internal/proxmoxdiscovery` uses only the standard library and
existing internal packages (`internal/proxmox`, `internal/quantity`,
`internal/nrm/v1alpha1`, `internal/workspace`).

## Open risks

- **N+1 API calls.** `Discover` issues one `GetQEMUConfig` request per live
  guest (Proxmox has no batch endpoint that returns tags/description
  alongside the cluster-resources overview). For a cluster with hundreds of
  guests this is hundreds of sequential HTTP round-trips. Acceptable for
  this slice's scope (typical homelab/small-cluster guest counts per
  §6.1's stated topology range); revisit with bounded concurrency if a
  larger deployment reports slow `cluster discover` runs.
- **`internal/proxmoxbootstrap`'s public surface widens.** Exporting
  `BootstrapUser`/`BootstrapTokenID` lets any future package construct a
  client authenticated as the bootstrapped operator without going through
  `Bootstrap` itself. Mitigated by keeping the rename minimal (two
  constants, not the whole file) and by `bootstrapRole` staying unexported
  since nothing outside `bootstrap.go` needs it.
</content>
