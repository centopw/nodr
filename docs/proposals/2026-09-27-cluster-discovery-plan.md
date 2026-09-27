# Slice C: Cluster Discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Working Directory:** All work must take place in the isolated git worktree:
> `/Users/cento/git/personal/nodr/.worktrees/cluster-discovery`
> on branch `feat/cluster-discovery`.
> Never modify files in the root repository checkout.

**Goal:** Add read-only Proxmox guest discovery (docs/design/06-proxmox.md §6.3 steps 1-2): list every live QEMU guest on a connected cluster, classify it against workspace intent as already managed or undiscovered, and flag undiscovered guests whose tags or description mention another IaC tool. Ship as `nodr cluster discover <cluster>` (CLI, text table) and `cluster.discover` (API command, JSON). No intent is written, no VM is adopted — that's Slice D.

**Architecture:** A new `internal/proxmoxdiscovery` package holds `Discover(ctx, client, ws, cluster) ([]Guest, error)`: it calls `client.GetClusterResources(ctx, "vm")` for the cluster-wide overview, then `client.GetQEMUConfig(ctx, node, vmid)` per guest for tags/description, and classifies each guest against `ws.OfKind(v1alpha1.KindVirtualMachine)` by cluster+VMID match, flagging other-tool ownership by substring match. `internal/proxmoxbootstrap` exports two previously-unexported constants (`BootstrapUser`, `BootstrapTokenID`) so callers outside the bootstrap flow can authenticate as the token it already created. `internal/cli` gains `nodr cluster discover`, reusing Slice B's `secretsResolve`/`planapply.CredentialsRefFor` plumbing to resolve the cluster's stored token, then builds a pinned `proxmox.Client` and calls `Discover`, printing the result with the existing `writeTable` helper. `internal/api` gains a `cluster.discover` case in `runCommand`'s switch, with the same credential-resolution and client-construction logic, returning JSON.

**Tech Stack:** Go 1.24, existing `internal/proxmox`, `internal/quantity`, `internal/nrm/v1alpha1`, `internal/workspace`, `internal/planapply` packages. No new external dependencies.

---

## File Structure

```
.worktrees/cluster-discovery/
├── internal/
│   ├── proxmoxbootstrap/
│   │   └── bootstrap.go                  # bootstrapUser -> BootstrapUser, bootstrapTokenID -> BootstrapTokenID
│   ├── proxmoxdiscovery/
│   │   ├── discovery.go                  # Guest, Status, Discover, classify, mentionsOtherTool, guestTags
│   │   └── discovery_test.go             # proxmoxtest.NewServer-backed classification tests
│   ├── cli/
│   │   ├── cluster.go                    # clusterDiscoverCommand, clusterDiscover; clusterCommand adds it
│   │   └── cluster_test.go               # TestClusterDiscover_ReportsGuests
│   └── api/
│       ├── api.go                        # cluster.discover case, clusterDiscoverParams, discoveredGuest, (*server).clusterDiscover
│       └── api_test.go                   # TestClusterDiscover, TestClusterDiscover_UnknownCluster
└── CHANGELOG.md                          # Unreleased entry for this slice
```

---

### Task 1: `internal/proxmoxbootstrap` — export bootstrap identity constants

**Files:**
- Modify: `internal/proxmoxbootstrap/bootstrap.go`

- [ ] **Step 1: Rename the two constants**

Read the current file first to confirm line numbers haven't shifted, then rename `bootstrapUser` to `BootstrapUser` and `bootstrapTokenID` to `BootstrapTokenID` everywhere they appear in `internal/proxmoxbootstrap/bootstrap.go` (the const block and all four use sites inside `Bootstrap`). `bootstrapRole` stays unchanged (unexported, used only inside this file). The const block becomes:
```go
const (
	// BootstrapUser is the dedicated Proxmox VE user nodr creates and
	// authenticates as after onboarding a cluster.
	BootstrapUser = "nodr@pve"
	bootstrapRole = "NodrOperator"
	// BootstrapTokenID is the API token ID nodr creates under BootstrapUser.
	BootstrapTokenID = "nodr"
)
```
Every use of `bootstrapUser` and `bootstrapTokenID` inside `Bootstrap` (the `CreateUser`, `UpdateACL` map key, `CreateAPIToken` call, and the returned `Result{User: ..., TokenID: ...}`) becomes `BootstrapUser` / `BootstrapTokenID`.

- [ ] **Step 2: Verify no other package referenced the old names**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
grep -rn "bootstrapUser\|bootstrapTokenID" internal/
```
Expected: no matches (confirms the rename is complete and was never referenced elsewhere).

- [ ] **Step 3: Run the package's existing tests**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
go test ./internal/proxmoxbootstrap/...
```
Expected: PASS, unchanged behavior (`bootstrap_test.go` asserts on `Result.User`/`Result.TokenID` values, not on the constant names, so it needs no edits).

- [ ] **Step 4: Build the whole module**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
go build ./...
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
git add internal/proxmoxbootstrap/bootstrap.go
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "refactor(proxmoxbootstrap): export BootstrapUser and BootstrapTokenID"
```

---

### Task 2: `internal/proxmoxdiscovery` — new package

**Files:**
- Create: `internal/proxmoxdiscovery/discovery.go`
- Create: `internal/proxmoxdiscovery/discovery_test.go`

- [ ] **Step 1: Write `discovery.go`**

```go
// Package proxmoxdiscovery reads the live QEMU guests of a Proxmox VE
// cluster and classifies them against a workspace's existing
// VirtualMachine intent (docs/design/06-proxmox.md §6.3, steps 1-2).
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

// Guest classifications.
const (
	// StatusManaged is a guest whose cluster and VMID match an existing
	// VirtualMachine document.
	StatusManaged Status = "managed"
	// StatusDiscovered is a guest with no matching intent and no sign
	// another tool manages it.
	StatusDiscovered Status = "discovered"
	// StatusOtherToolTagged is a guest with no matching intent whose tags
	// or description mention another infrastructure-as-code tool.
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
// classifies each against ws's existing VirtualMachine intent. It excludes
// templates (ClusterResource.Template == 1).
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

- [ ] **Step 2: Verify it builds**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
go build ./internal/proxmoxdiscovery/...
```
Expected: PASS.

- [ ] **Step 3: Write `discovery_test.go`**

```go
package proxmoxdiscovery_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/proxmox/proxmoxtest"
	"github.com/centopw/nodr/internal/proxmoxdiscovery"
	"github.com/centopw/nodr/internal/workspace"
)

const discoveryManifest = `apiVersion: nodr/v1alpha1
kind: Workspace
metadata: { name: test }
spec: {}
`

const discoveryPlatform = `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [https://placeholder:8006]
  credentialsRef: proxmox/pve-main-token
  nodes: [pve1]
`

const discoveryVM = `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata: { name: web-01 }
spec:
  placement: { cluster: pve-main, assignedNode: pve1 }
  identity: { vmid: 100 }
  resources: { cpu: { cores: 2 }, memory: { size: 2Gi } }
`

func writeDiscoveryWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"nodr.yaml":                discoveryManifest,
		"intent/platform/pve.yaml": discoveryPlatform,
		"intent/compute/web.yaml":  discoveryVM,
	}
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws, diags := workspace.Load(root)
	if diags.HasErrors() {
		t.Fatalf("workspace.Load: %v", diags)
	}
	return ws
}

func TestDiscover_ClassifiesGuests(t *testing.T) {
	ws := writeDiscoveryWorkspace(t)

	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		// 1. GetClusterResources: three live QEMU guests, one template
		// (excluded), and a non-qemu resource (excluded).
		"GET /api2/json/cluster/resources": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("type") != "vm" {
				t.Errorf("query type = %s, want vm", r.URL.Query().Get("type"))
			}
			proxmoxtest.JSONResponse(w, http.StatusOK, `[
				{"id":"qemu/100","vmid":100,"name":"web-01","node":"pve1","type":"qemu","status":"running","maxmem":2147483648,"maxdisk":10737418240,"maxcpu":2},
				{"id":"qemu/101","vmid":101,"name":"undiscovered","node":"pve1","type":"qemu","status":"stopped","maxmem":1073741824,"maxdisk":5368709120,"maxcpu":1},
				{"id":"qemu/102","vmid":102,"name":"tf-managed","node":"pve1","type":"qemu","status":"running","maxmem":1073741824,"maxdisk":5368709120,"maxcpu":1},
				{"id":"qemu/103","vmid":103,"name":"a-template","node":"pve1","type":"qemu","status":"stopped","maxmem":1073741824,"maxdisk":5368709120,"maxcpu":1,"template":1}
			]`)
		},
		// 2. GetQEMUConfig for the managed guest: no tags.
		"GET /api2/json/nodes/pve1/qemu/100/config": func(w http.ResponseWriter, _ *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"digest":"d1","name":"web-01"}`)
		},
		// 3. GetQEMUConfig for the undiscovered guest: no tags.
		"GET /api2/json/nodes/pve1/qemu/101/config": func(w http.ResponseWriter, _ *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"digest":"d2","name":"undiscovered"}`)
		},
		// 4. GetQEMUConfig for the Terraform-tagged guest.
		"GET /api2/json/nodes/pve1/qemu/102/config": func(w http.ResponseWriter, _ *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"digest":"d3","name":"tf-managed","tags":"env-prod;terraform"}`)
		},
	})
	defer srv.Close()

	client := proxmox.NewClient(srv.URL, nil)
	guests, err := proxmoxdiscovery.Discover(context.Background(), client, ws, "pve-main")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(guests) != 3 {
		t.Fatalf("len(guests) = %d, want 3 (template excluded): %+v", len(guests), guests)
	}

	byVMID := make(map[int]proxmoxdiscovery.Guest, len(guests))
	for _, g := range guests {
		byVMID[g.VMID] = g
	}

	if g := byVMID[100]; g.Classified != proxmoxdiscovery.StatusManaged {
		t.Errorf("guest 100 classified = %q, want %q", g.Classified, proxmoxdiscovery.StatusManaged)
	}
	if g := byVMID[101]; g.Classified != proxmoxdiscovery.StatusDiscovered {
		t.Errorf("guest 101 classified = %q, want %q", g.Classified, proxmoxdiscovery.StatusDiscovered)
	}
	if g := byVMID[102]; g.Classified != proxmoxdiscovery.StatusOtherToolTagged {
		t.Errorf("guest 102 classified = %q, want %q", g.Classified, proxmoxdiscovery.StatusOtherToolTagged)
	}
	if g := byVMID[100]; g.Memory != "2Gi" || g.Disk != "10Gi" {
		t.Errorf("guest 100 memory/disk = %q/%q, want 2Gi/10Gi", g.Memory, g.Disk)
	}

	// Reference check: nrm import is used only to keep this test file
	// self-contained if a future edit needs Ref-based lookups; drop this
	// var if unused after implementation settles.
	_ = nrm.Ref{}
}
```

Note: if `nrm.Ref{}` at the end is unused after you finish (it likely is — `nrm` is not needed by this test as written), delete the import and the trailing reference block entirely rather than leaving a placeholder; it was included above only to flag the decision point, not as code to keep.

- [ ] **Step 4: Remove the placeholder `nrm` reference**

Re-read the file you just wrote, delete the `"github.com/centopw/nodr/internal/nrm"` import and the trailing `_ = nrm.Ref{}` block (both are dead weight — the test never needs `nrm` directly).

- [ ] **Step 5: Run the new test**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
go test -v ./internal/proxmoxdiscovery/...
```
Expected: `TestDiscover_ClassifiesGuests` PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
git add internal/proxmoxdiscovery/
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(proxmoxdiscovery): add read-only guest discovery and classification"
```

---

### Task 3: `internal/cli` — `nodr cluster discover`

**Files:**
- Modify: `internal/cli/cluster.go`
- Modify: `internal/cli/cluster_test.go`

- [ ] **Step 1: Add imports and the subcommand registration**

Re-read `internal/cli/cluster.go` first to get its current tag. In the `import` block, add `"net/http"`, `"strconv"`, `"github.com/centopw/nodr/internal/nrm"`, `"github.com/centopw/nodr/internal/planapply"`, `"github.com/centopw/nodr/internal/proxmoxdiscovery"` alongside the existing imports. In `clusterCommand`, add the second `AddCommand` line:
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
```

- [ ] **Step 2: Add `clusterDiscoverCommand` and `clusterDiscover`**

Append at the end of `internal/cli/cluster.go`:
```go
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
	if d == nil {
		return fmt.Errorf("nodr: cluster %q not found", clusterName)
	}
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

- [ ] **Step 3: Verify it builds**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
go build ./internal/cli/...
```
Expected: PASS. If it fails on an unused/missing import, re-read the current import block (some names above, like `v1alpha1` and `proxmox` and `proxmoxbootstrap`, are already imported by `clusterConnect`) and only add what's genuinely missing.

- [ ] **Step 4: Add `TestClusterDiscover_ReportsGuests` to `cluster_test.go`**

Re-read `internal/cli/cluster_test.go` first for its current tag. Append:
```go
func TestClusterDiscover_ReportsGuests(t *testing.T) {
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"GET /api2/json/cluster/resources": func(w http.ResponseWriter, _ *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `[
				{"id":"qemu/1000","vmid":1000,"name":"web-01","node":"pve1","type":"qemu","status":"running","maxmem":2147483648,"maxdisk":10737418240,"maxcpu":2},
				{"id":"qemu/1001","vmid":1001,"name":"stray","node":"pve1","type":"qemu","status":"stopped","maxmem":1073741824,"maxdisk":5368709120,"maxcpu":1}
			]`)
		},
		"GET /api2/json/nodes/pve1/qemu/1000/config": func(w http.ResponseWriter, _ *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"digest":"d1","name":"web-01"}`)
		},
		"GET /api2/json/nodes/pve1/qemu/1001/config": func(w http.ResponseWriter, _ *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"digest":"d2","name":"stray"}`)
		},
	})
	defer srv.Close()

	files := map[string]string{
		"nodr.yaml": manifest,
		"intent/platform/pve.yaml": `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [` + srv.URL + `]
  credentialsRef: proxmox/pve-main-token
  nodes: [pve1]
`,
		"intent/compute/web.yaml": `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata: { name: web-01 }
spec:
  placement: { cluster: pve-main, assignedNode: pve1 }
  identity: { vmid: 1000 }
  resources: { cpu: { cores: 2 }, memory: { size: 2Gi } }
`,
	}
	root := writeWorkspace(t, files)

	result := runWithStdin(bytes.NewReader(nil), "--workspace", root, "cluster", "discover", "pve-main")
	result.check(t, exitOK)
	if !bytes.Contains([]byte(result.stdout), []byte("1000")) || !bytes.Contains([]byte(result.stdout), []byte("managed")) {
		t.Errorf("stdout missing managed guest 1000: %s", result.stdout)
	}
	if !bytes.Contains([]byte(result.stdout), []byte("1001")) || !bytes.Contains([]byte(result.stdout), []byte("discovered")) {
		t.Errorf("stdout missing discovered guest 1001: %s", result.stdout)
	}
}
```
`writeWorkspace` (`internal/cli/cli_test.go:44-67`) already seeds `proxmox/pve-main-token` = `"token-from-the-environment"` into the workspace's secrets store, matching this test's `credentialsRef: proxmox/pve-main-token` — no additional secrets setup is needed.

- [ ] **Step 5: Run the new test**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
go test -v -run TestClusterDiscover ./internal/cli/...
```
Expected: `TestClusterDiscover_ReportsGuests` PASS.

- [ ] **Step 6: Run the full `internal/cli` package tests**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
go test ./internal/cli/...
```
Expected: PASS, including the pre-existing `TestClusterConnect_BootstrapsAndWritesIntent`.

- [ ] **Step 7: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
git add internal/cli/cluster.go internal/cli/cluster_test.go
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(cli): add 'nodr cluster discover' command"
```

---

### Task 4: `internal/api` — `cluster.discover` command

**Files:**
- Modify: `internal/api/api.go`
- Modify: `internal/api/api_test.go`

- [ ] **Step 1: Add imports**

Re-read `internal/api/api.go` first to get its current tag. In the `import` block, add `"github.com/centopw/nodr/internal/proxmox"`, `"github.com/centopw/nodr/internal/proxmoxbootstrap"`, `"github.com/centopw/nodr/internal/proxmoxdiscovery"` (`"net/http"`, `"bytes"`, `"encoding/json"`, `"fmt"`, `"github.com/centopw/nodr/internal/nrm"`, `"github.com/centopw/nodr/internal/nrm/v1alpha1"`, `"github.com/centopw/nodr/internal/planapply"` are already imported).

- [ ] **Step 2: Add the `cluster.discover` case to `runCommand`'s switch**

In `runCommand` (`internal/api/api.go`), insert a new case immediately after the `workspace.apply` case's closing brace and before `default:`:
```go
	case "cluster.discover":
		a.clusterDiscover(w, r, request)
```

- [ ] **Step 3: Add the params/response types and handler**

Append near `decodeActionParams` (after it, before `resourceAction`, or at the end of the file — either is fine as long as it's in the `api` package):
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
	if d == nil {
		writeProblem(w, http.StatusNotFound, "Cluster not found", fmt.Sprintf("cluster %q not found", params.Cluster), nil)
		return
	}
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

- [ ] **Step 4: Verify it builds**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
go build ./internal/api/...
```
Expected: PASS.

- [ ] **Step 5: Add `TestClusterDiscover` and `TestClusterDiscover_UnknownCluster` to `api_test.go`**

Re-read `internal/api/api_test.go` first for its current tag. Append:
```go
func TestClusterDiscover(t *testing.T) {
	proxmoxSrv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"GET /api2/json/cluster/resources": func(w http.ResponseWriter, _ *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `[
				{"id":"qemu/1012","vmid":1012,"name":"web-01","node":"pve1","type":"qemu","status":"running","maxmem":2147483648,"maxdisk":10737418240,"maxcpu":2},
				{"id":"qemu/1099","vmid":1099,"name":"tf-guest","node":"pve1","type":"qemu","status":"running","maxmem":1073741824,"maxdisk":5368709120,"maxcpu":1}
			]`)
		},
		"GET /api2/json/nodes/pve1/qemu/1012/config": func(w http.ResponseWriter, _ *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"digest":"d1","name":"web-01"}`)
		},
		"GET /api2/json/nodes/pve1/qemu/1099/config": func(w http.ResponseWriter, _ *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"digest":"d2","name":"tf-guest","tags":"terraform"}`)
		},
	})
	defer proxmoxSrv.Close()

	root := t.TempDir()
	files := map[string]string{
		"nodr.yaml": testManifest,
		"intent/platform/pve.yaml": `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [` + proxmoxSrv.URL + `]
  credentialsRef: proxmox/pve-main-token
  nodes: [pve1]
`,
		"intent/network/dmz.yaml":    testNetwork,
		"intent/compute/web-01.yaml": testVM,
	}
	for name, content := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	handler := testHandler(t, root)

	res := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "cluster.discover",
		"target":  "pve-main",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	var guests []discoveredGuest
	decodeResponse(t, res, &guests)
	if len(guests) != 2 {
		t.Fatalf("len(guests) = %d, want 2: %+v", len(guests), guests)
	}
	byVMID := make(map[int]discoveredGuest, len(guests))
	for _, g := range guests {
		byVMID[g.VMID] = g
	}
	if g := byVMID[1012]; g.Classified != "managed" {
		t.Errorf("guest 1012 classified = %q, want managed", g.Classified)
	}
	if g := byVMID[1099]; g.Classified != "discovered (other tool?)" {
		t.Errorf("guest 1099 classified = %q, want discovered (other tool?)", g.Classified)
	}
}

func TestClusterDiscover_UnknownCluster(t *testing.T) {
	root := testWorkspace(t)
	handler := testHandler(t, root)

	res := request(t, handler, http.MethodPost, "/api/v1/workspaces/homelab/commands", map[string]any{
		"command": "cluster.discover",
		"target":  "does-not-exist",
	})
	checkProblem(t, res, http.StatusNotFound, "Cluster not found", "", "does-not-exist")
}
```

- [ ] **Step 6: Add the `proxmoxtest` import to `api_test.go`**

Re-read the current import block; add `"github.com/centopw/nodr/internal/proxmox/proxmoxtest"` alongside the existing internal imports.

- [ ] **Step 7: Run the new tests**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
go test -v -run TestClusterDiscover ./internal/api/...
```
Expected: `TestClusterDiscover` and `TestClusterDiscover_UnknownCluster` PASS.

- [ ] **Step 8: Run the full `internal/api` package tests**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
go test ./internal/api/...
```
Expected: PASS across the whole package, including pre-existing tests.

- [ ] **Step 9: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
git add internal/api/api.go internal/api/api_test.go
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(api): add cluster.discover command"
```

---

### Task 5: CHANGELOG and full verification

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Update `CHANGELOG.md`**

Re-read `CHANGELOG.md` first to confirm the current line numbers of the `## [Unreleased]` / `### Added` section (it currently ends at the `The dashboard and API now require an authenticated session...` bullet, line 34, immediately before `### Fixed`). Insert a new bullet at the end of that `### Added` block, in the same prose-bullet style as the existing entries:
```markdown
- `nodr cluster discover <cluster>`: lists a connected cluster's live QEMU
  guests, classifies each as already managed by nodr or undiscovered, and
  flags undiscovered guests whose tags or description mention another
  infrastructure-as-code tool. Read-only; adoption is not implemented yet.
```
Do not touch `### Fixed` or either `### Changed` heading further down in the file.

- [ ] **Step 2: Run the full test suite with race detection**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
go test -v -race ./...
```
Expected: PASS across all packages.

- [ ] **Step 3: Run the linter**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
golangci-lint run ./...
```
Expected: PASS with 0 lint issues. Fix any `unparam`, `revive`, `errorlint`, `gocritic`, `unconvert` findings before proceeding — do not add `//nolint` without a `require-specific`-satisfying explanation, per `.golangci.yml`'s `nolintlint` settings.

- [ ] **Step 4: Manual smoke test against a fake Proxmox server**

This proves the full CLI path — cluster connect against a fake server, then discover against the same server — end to end with a real built binary.

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
go build -o /tmp/nodr-discover ./cmd/nodr
```

Write a tiny throwaway Go program that starts a `proxmoxtest.NewServer`-equivalent fake Proxmox HTTP server on a fixed port and serves the same routes as `TestClusterConnect_BootstrapsAndWritesIntent` plus `cluster/resources` and one `qemu/{vmid}/config` route, then leave it running:
```bash
cat > /tmp/fake_proxmox.go <<'EOF'
package main

import (
	"fmt"
	"net/http"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api2/json/access/ticket", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data":{"ticket":"T","CSRFPreventionToken":"C"}}`)
	})
	mux.HandleFunc("GET /api2/json/nodes/pve1/certificates/info", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data":[{"fingerprint":"AA:BB:CC"}]}`)
	})
	mux.HandleFunc("POST /api2/json/access/users", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"data":null}`) })
	mux.HandleFunc("POST /api2/json/access/roles", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"data":null}`) })
	mux.HandleFunc("PUT /api2/json/access/acl", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"data":null}`) })
	mux.HandleFunc("POST /api2/json/access/users/nodr@pve/token/nodr", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data":{"value":"smoke-secret"}}`)
	})
	mux.HandleFunc("GET /api2/json/cluster/resources", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"qemu/500","vmid":500,"name":"smoke-vm","node":"pve1","type":"qemu","status":"running","maxmem":2147483648,"maxdisk":10737418240,"maxcpu":2}]}`)
	})
	mux.HandleFunc("GET /api2/json/nodes/pve1/qemu/500/config", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data":{"digest":"d1","name":"smoke-vm"}}`)
	})
	fmt.Println("fake proxmox listening on :18443")
	http.ListenAndServe("127.0.0.1:18443", mux)
}
EOF
go run /tmp/fake_proxmox.go &
sleep 1

rm -rf /tmp/smoke-discover-workspace
mkdir -p /tmp/smoke-discover-workspace
cat > /tmp/smoke-discover-workspace/nodr.yaml <<'EOF'
apiVersion: nodr/v1alpha1
kind: Workspace
metadata: { name: smoke }
spec: {}
EOF
cd /tmp/smoke-discover-workspace
export NODR_KEK=$(head -c32 /dev/urandom | base64)
/tmp/nodr-discover cluster connect <<< $'pve-main\nhttp://127.0.0.1:18443\npve1\nroot\nadminpass\nyes\n'
/tmp/nodr-discover cluster discover pve-main
kill %1
```

Expected: `cluster connect` prints `connected cluster "pve-main"; wrote intent/platform/pve-main.yaml`; `cluster discover pve-main` prints a table with one row, VMID `500`, name `smoke-vm`, and `NODR` column `discovered` (the guest has no matching VirtualMachine intent, so it's undiscovered, not managed).

- [ ] **Step 5: Clean up smoke-test scaffolding**

```bash
rm -f /tmp/fake_proxmox.go /tmp/nodr-discover
rm -rf /tmp/smoke-discover-workspace
```

- [ ] **Step 6: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-discovery
git add CHANGELOG.md
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "docs(changelog): document cluster discovery"
```

---

## After the plan: PR workflow

1. Push the branch: `git push -u origin feat/cluster-discovery`.
2. Open a PR against `main` (title: `feat: cluster discovery (Slice C)`), same convention as Slices A and B (PRs #14/#15/#16 for Slice B).
3. Watch CI (`.github/workflows/ci.yml`).
4. Merge once green; no branch protection exists, but PR-based merge is the established convention — never push directly to `main`.
</content>
