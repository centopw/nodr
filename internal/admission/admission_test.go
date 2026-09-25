package admission

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/workspace"
)

const manifest = `apiVersion: nodr/v1alpha1
kind: Workspace
metadata: { name: test }
spec:
  environments:
    prod: { vmidRange: [1000, 1003] }
    lab: { vmidRange: [2000, 2999] }
    backwards: { vmidRange: [3000, 2999] }
    bare: {}
`

const platform = `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [https://10.0.10.11:8006]
  credentialsRef: proxmox/pve-main-token
  nodes: [pve3, pve1, pve2]
---
apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-lab }
spec:
  endpoints: [https://10.0.40.11:8006]
  credentialsRef: proxmox/pve-lab-token
---
apiVersion: nodr/v1alpha1
kind: Template
metadata: { name: debian }
spec:
  cluster: pve-main
  image: { url: https://example.com/debian.qcow2, checksum: "sha256:0000000000000000000000000000000000000000000000000000000000000000" }
  storage: local-lvm
  identity: { vmid: 1000 }
`

const networks = `apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: dmz }
spec:
  vlan: 20
  ipv4:
    subnet: 10.0.20.0/24
    gateway: 10.0.20.1
    static: { range: 10.0.20.1-10.0.20.4 }
---
apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: lan }
spec:
  ipv4: { subnet: 10.0.10.0/24, gateway: 10.0.10.1 }
`

// web01 is an admitted VM on pve1 with guest ID 1001 and 10.0.20.2.
const web01 = `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-01
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C2D3
  labels: { nodr/environment: prod }
spec:
  placement: { cluster: pve-main, assignedNode: pve1 }
  identity: { vmid: 1001 }
  resources: { cpu: { cores: 1 }, memory: { size: 4Gi } }
  nics: [{ network: dmz, ipv4: { mode: static, address: 10.0.20.2/24 } }]
`

// vm returns a VirtualMachine document with the given labels and spec
// entries.
func vm(name, labels string, spec ...string) string {
	return fmt.Sprintf("apiVersion: nodr/v1alpha1\nkind: VirtualMachine\nmetadata:\n  name: %s\n  labels: %s\nspec:\n  %s\n",
		name, labels, strings.Join(spec, "\n  "))
}

// resources is the spec entry for resources with the given memory size.
func resources(memory string) string {
	return "resources: { cpu: { cores: 1 }, memory: { size: " + memory + " } }"
}

// load writes a workspace with the manifest, the platform, the networks
// and web-01 above, and with files, which can replace them, and loads it.
// The workspace must be valid.
func load(t *testing.T, files map[string]string) *workspace.Workspace {
	t.Helper()
	all := map[string]string{
		workspace.ManifestFile:       manifest,
		"intent/platform/pve.yaml":   platform,
		"intent/network/nets.yaml":   networks,
		"intent/compute/web-01.yaml": web01,
	}
	for name, content := range files {
		all[name] = content
	}
	root := t.TempDir()
	for name, content := range all {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return reload(t, root)
}

func reload(t *testing.T, root string) *workspace.Workspace {
	t.Helper()
	ws, diags := workspace.Load(root)
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if diags := ws.Validate(reg); diags.HasErrors() {
		t.Fatalf("the workspace is invalid: %v", diags.Err())
	}
	return ws
}

// uids returns a generator of UIDs that counts up.
func uids() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("01J9Z3K4T7M2Q8V5X6N0B1C2%02d", n)
	}
}

// vms returns the VMs with the given names, or all of them.
func vms(t *testing.T, ws *workspace.Workspace, names ...string) []*v1alpha1.VirtualMachine {
	t.Helper()
	var out []*v1alpha1.VirtualMachine
	for _, d := range ws.OfKind(v1alpha1.KindVirtualMachine) {
		if len(names) > 0 && !slices.Contains(names, d.Metadata.Name) {
			continue
		}
		vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](d)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, vm)
	}
	return out
}

// plan admits the VMs with the given names, or all of them, and returns
// the assignments as text.
func plan(t *testing.T, ws *workspace.Workspace, names ...string) ([]string, diag.List) {
	t.Helper()
	assignments, diags := Plan(ws, vms(t, ws, names...), Options{NewUID: uids()})
	lines := make([]string, len(assignments))
	for i, a := range assignments {
		lines[i] = a.String()
	}
	return lines, diags
}

func check(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("assignments:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func checkDiags(t *testing.T, diags diag.List, want ...string) {
	t.Helper()
	got := make([]string, len(diags))
	for i, d := range diags {
		got[i] = d.String()
	}
	if !slices.Equal(got, want) {
		t.Errorf("diagnostics:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPlan(t *testing.T) {
	ws := load(t, map[string]string{
		"intent/compute/web-02.yaml": vm("web-02", "{ nodr/environment: prod }",
			"placement: { cluster: pve-main }", resources("2Gi"),
			"nics: [{ network: dmz, ipv4: { mode: auto } }, { network: lan }]"),
	})
	got, diags := plan(t, ws)
	checkDiags(t, diags)
	// The node is the first of the nodes without guests, the guest ID the
	// first one that neither the template nor web-01 uses, and the address
	// the first in the static range that is not the gateway or web-01's.
	check(t, got, []string{
		"vm/web-02: metadata.uid = 01J9Z3K4T7M2Q8V5X6N0B1C201 (intent/compute/web-02.yaml)",
		"vm/web-02: spec.placement.assignedNode = pve2 (intent/compute/web-02.yaml)",
		"vm/web-02: spec.identity.vmid = 1002 (intent/compute/web-02.yaml)",
		"vm/web-02: spec.nics[0].ipv4.address = 10.0.20.3/24 (intent/compute/web-02.yaml)",
	})
}

func TestPlanSeveralVMs(t *testing.T) {
	ws := load(t, map[string]string{
		"intent/compute/a.yaml": vm("web-04", "{ nodr/environment: prod }",
			"placement: { cluster: pve-main }", resources("1Gi"),
			"nics: [{ network: dmz, ipv4: { mode: auto } }]") +
			"---\n" + vm("web-03", "{ nodr/environment: prod }",
			"placement: { cluster: pve-main }", resources("2Gi"),
			"nics: [{ network: dmz, ipv4: { mode: auto } }]"),
		"intent/compute/b.yaml": vm("lab-01", "{ nodr/environment: lab }",
			"placement: { cluster: pve-main }", resources("512Mi")),
	})
	got, diags := plan(t, ws)
	checkDiags(t, diags)
	// File order, then name order: web-03, web-04, lab-01. Each VM goes to
	// the node with the least memory at its turn: pve1 has 4Gi from web-01.
	check(t, got, []string{
		"vm/web-03: metadata.uid = 01J9Z3K4T7M2Q8V5X6N0B1C201 (intent/compute/a.yaml)",
		"vm/web-03: spec.placement.assignedNode = pve2 (intent/compute/a.yaml)",
		"vm/web-03: spec.identity.vmid = 1002 (intent/compute/a.yaml)",
		"vm/web-03: spec.nics[0].ipv4.address = 10.0.20.3/24 (intent/compute/a.yaml)",
		"vm/web-04: metadata.uid = 01J9Z3K4T7M2Q8V5X6N0B1C202 (intent/compute/a.yaml)",
		"vm/web-04: spec.placement.assignedNode = pve3 (intent/compute/a.yaml)",
		"vm/web-04: spec.identity.vmid = 1003 (intent/compute/a.yaml)",
		"vm/web-04: spec.nics[0].ipv4.address = 10.0.20.4/24 (intent/compute/a.yaml)",
		"vm/lab-01: metadata.uid = 01J9Z3K4T7M2Q8V5X6N0B1C203 (intent/compute/b.yaml)",
		"vm/lab-01: spec.placement.assignedNode = pve3 (intent/compute/b.yaml)",
		"vm/lab-01: spec.identity.vmid = 2000 (intent/compute/b.yaml)",
	})

	// Admitting only some VMs gives them the same values, whatever order
	// they are named in.
	selected, _ := plan(t, ws, "web-04", "web-03")
	check(t, selected, got[:8])
}

func TestPlanKeepsSetValues(t *testing.T) {
	ws := load(t, nil)
	got, diags := plan(t, ws)
	checkDiags(t, diags)
	check(t, got, []string{})
}

func TestPlanGuestIDs(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
		diag  string
	}{
		{
			name: "other clusters do not count",
			files: map[string]string{
				"intent/compute/lab.yaml": vm("lab-01", "{ nodr/environment: prod }",
					"placement: { cluster: pve-lab, node: lab1, assignedNode: lab1 }",
					"identity: { vmid: 1002 }", resources("1Gi")),
				"intent/compute/web-02.yaml": vm("web-02", "{ nodr/environment: prod }",
					"placement: { cluster: pve-main, assignedNode: pve1 }", resources("1Gi")),
			},
			want: "vm/web-02: spec.identity.vmid = 1002 (intent/compute/web-02.yaml)",
		},
		{
			name: "missing environment label",
			files: map[string]string{
				"intent/compute/web-02.yaml": vm("web-02", "{ app: web }",
					"placement: { cluster: pve-main, assignedNode: pve1 }", resources("1Gi")),
			},
			diag: "intent/compute/web-02.yaml:6: error: spec.identity.vmid: cannot allocate a guest ID: the label nodr/environment is missing, so the range of IDs is unknown",
		},
		{
			name: "unknown environment",
			files: map[string]string{
				"intent/compute/web-02.yaml": vm("web-02", "{ nodr/environment: staging }",
					"placement: { cluster: pve-main, assignedNode: pve1 }", resources("1Gi")),
			},
			diag: `intent/compute/web-02.yaml:6: error: spec.identity.vmid: cannot allocate a guest ID: environment "staging" is not defined in nodr.yaml`,
		},
		{
			name: "environment without a range",
			files: map[string]string{
				"intent/compute/web-02.yaml": vm("web-02", "{ nodr/environment: bare }",
					"placement: { cluster: pve-main, assignedNode: pve1 }", resources("1Gi")),
			},
			diag: `intent/compute/web-02.yaml:6: error: spec.identity.vmid: cannot allocate a guest ID: environment "bare" has no vmidRange in nodr.yaml`,
		},
		{
			name: "range that ends before it starts",
			files: map[string]string{
				"intent/compute/web-02.yaml": vm("web-02", "{ nodr/environment: backwards }",
					"placement: { cluster: pve-main, assignedNode: pve1 }", resources("1Gi")),
			},
			diag: `intent/compute/web-02.yaml:6: error: spec.identity.vmid: cannot allocate a guest ID: the vmidRange of environment "backwards" in nodr.yaml ends before it starts`,
		},
		{
			name: "exhausted range",
			files: map[string]string{
				"intent/compute/full.yaml": vm("web-02", "{ nodr/environment: prod }",
					"placement: { cluster: pve-main, assignedNode: pve1 }", "identity: { vmid: 1002 }", resources("1Gi")) +
					"---\n" + vm("web-03", "{ nodr/environment: prod }",
					"placement: { cluster: pve-main, assignedNode: pve1 }", "identity: { vmid: 1003 }", resources("1Gi")) +
					"---\n" + vm("web-04", "{ nodr/environment: prod }",
					"placement: { cluster: pve-main, assignedNode: pve1 }", resources("1Gi")),
			},
			diag: `intent/compute/full.yaml:26: error: spec.identity.vmid: cannot allocate a guest ID: cluster "pve-main" uses every ID in the range 1000-1003 of environment "prod"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := load(t, tt.files)
			got, diags := plan(t, ws)
			var ids []string
			for _, line := range got {
				if strings.Contains(line, "vmid") {
					ids = append(ids, line)
				}
			}
			if tt.diag != "" {
				checkDiags(t, diags, tt.diag)
				check(t, ids, nil)
				return
			}
			checkDiags(t, diags)
			check(t, ids, []string{tt.want})
		})
	}
}

func TestPlanNodes(t *testing.T) {
	tests := []struct {
		name string
		vms  []string
		want []string
		diag string
	}{
		{
			name: "pinned",
			vms: []string{vm("web-02", "{ nodr/environment: prod }",
				"placement: { cluster: pve-main, node: pve1 }", "identity: { vmid: 1002 }", resources("1Gi"))},
			want: []string{"pve1"},
		},
		{
			name: "pinned on a cluster that lists no nodes",
			vms: []string{vm("lab-01", "{ nodr/environment: lab }",
				"placement: { cluster: pve-lab, node: lab1 }", "identity: { vmid: 2000 }", resources("1Gi"))},
			want: []string{"lab1"},
		},
		{
			name: "pinned to a node the cluster does not list",
			vms: []string{vm("web-02", "{ nodr/environment: prod }",
				"placement: { cluster: pve-main, node: pve9 }", "identity: { vmid: 1002 }", resources("1Gi"))},
			diag: `intent/compute/vms.yaml:7: error: spec.placement.node: node "pve9" is not in spec.nodes of cluster "pve-main"`,
		},
		{
			name: "automatic on a cluster that lists no nodes",
			vms: []string{vm("lab-01", "{ nodr/environment: lab }",
				"placement: { cluster: pve-lab, node: auto }", "identity: { vmid: 2000 }", resources("1Gi"))},
			diag: `intent/compute/vms.yaml:7: error: spec.placement.assignedNode: cannot choose a node: cluster "pve-lab" lists no nodes in spec.nodes`,
		},
		{
			name: "least memory, then name",
			vms: []string{
				vm("a", "{ nodr/environment: prod }", "placement: { cluster: pve-main }", "identity: { vmid: 1002 }", resources("5Gi")),
				vm("b", "{ nodr/environment: prod }", "placement: { cluster: pve-main }", "identity: { vmid: 1003 }", resources("2Gi")),
				vm("c", "{ nodr/environment: prod }", "placement: { cluster: pve-main }", "identity: { vmid: 1004 }", resources("1Gi")),
				vm("d", "{ nodr/environment: prod }", "placement: { cluster: pve-main }", "identity: { vmid: 1005 }", resources("1Gi")),
			},
			// pve1 starts with 4Gi. a: pve2 (tie with pve3), b: pve3,
			// c: pve3 (2Gi against 4Gi and 5Gi), d: pve3 (3Gi) and not
			// pve1 (4Gi).
			want: []string{"pve2", "pve3", "pve3", "pve3"},
		},
		{
			name: "affinity",
			vms: []string{
				// web-01 runs on pve1 and would get no guest by memory.
				vm("a", "{ nodr/environment: prod }", "placement: { cluster: pve-main, affinity: { keepWith: [web-01] } }", "identity: { vmid: 1002 }", resources("1Gi")),
				// b must avoid a and web-01, so pve3 although it holds c.
				vm("b", "{ nodr/environment: prod }", "placement: { cluster: pve-main, affinity: { separateFrom: [a, c] } }", "identity: { vmid: 1003 }", resources("1Gi")),
				vm("c", "{ nodr/environment: prod }", "placement: { cluster: pve-main, assignedNode: pve2, affinity: { separateFrom: [web-01] } }", "identity: { vmid: 1004 }", resources("1Gi")),
			},
			want: []string{"pve1", "pve3"},
		},
		{
			// z comes last, but its memory counts on pve2 from the start.
			name: "memory of a pinned guest that is not admitted yet",
			vms: []string{
				vm("a", "{ nodr/environment: prod }", "placement: { cluster: pve-main }", "identity: { vmid: 1002 }", resources("1Gi")),
				vm("z", "{ nodr/environment: prod }", "placement: { cluster: pve-main, node: pve2 }", "identity: { vmid: 1003 }", resources("8Gi")),
			},
			want: []string{"pve3", "pve2"},
		},
		{
			// y and z come last, but they run on the nodes they are
			// pinned to: b avoids pve2 and k joins z on pve1.
			name: "affinity with pinned guests that are not admitted yet",
			vms: []string{
				vm("b", "{ nodr/environment: prod }", "placement: { cluster: pve-main, affinity: { separateFrom: [y] } }", "identity: { vmid: 1002 }", resources("1Gi")),
				vm("k", "{ nodr/environment: prod }", "placement: { cluster: pve-main, affinity: { keepWith: [z] } }", "identity: { vmid: 1003 }", resources("1Gi")),
				vm("y", "{ nodr/environment: prod }", "placement: { cluster: pve-main, node: pve2 }", "identity: { vmid: 1004 }", resources("1Gi")),
				vm("z", "{ nodr/environment: prod }", "placement: { cluster: pve-main, node: pve1 }", "identity: { vmid: 1005 }", resources("1Gi")),
			},
			want: []string{"pve3", "pve1", "pve2", "pve1"},
		},
		{
			name: "affinity leaves no node",
			vms: []string{
				vm("a", "{ nodr/environment: prod }", "placement: { cluster: pve-main, assignedNode: pve2 }", "identity: { vmid: 1002 }", resources("1Gi")),
				vm("b", "{ nodr/environment: prod }", "placement: { cluster: pve-main, assignedNode: pve3, affinity: { separateFrom: [c] } }", "identity: { vmid: 1003 }", resources("1Gi")),
				vm("c", "{ nodr/environment: prod }", "placement: { cluster: pve-main, affinity: { separateFrom: [web-01, a] } }", "identity: { vmid: 1004 }", resources("1Gi")),
			},
			diag: `intent/compute/vms.yaml:27: error: spec.placement.assignedNode: cannot choose a node: every node of cluster "pve-main" runs a guest that c must be separated from`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := load(t, map[string]string{"intent/compute/vms.yaml": strings.Join(tt.vms, "---\n")})
			got, diags := plan(t, ws)
			var nodes []string
			for _, line := range got {
				if _, rest, ok := strings.Cut(line, "spec.placement.assignedNode = "); ok {
					node, _, _ := strings.Cut(rest, " ")
					nodes = append(nodes, node)
				}
			}
			if tt.diag != "" {
				checkDiags(t, diags, tt.diag)
				return
			}
			checkDiags(t, diags)
			check(t, nodes, tt.want)
		})
	}
}

func TestPlanAddresses(t *testing.T) {
	nets := `apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: dmz }
spec:
  vlan: 20
  ipv4:
    subnet: 10.0.20.0/24
    gateway: 10.0.20.1
    static: { range: 10.0.20.1-10.0.20.4 }
---
apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: small }
spec:
  vlan: 30
  ipv4:
    subnet: 10.0.30.0/30
    static: { range: 10.0.30.0-10.0.30.3 }
---
apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: lan }
spec:
  ipv4: { subnet: 10.0.10.0/24 }
---
apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: servers }
spec:
  vlan: 40
  ipv4:
    subnet: 10.0.40.0/24
    static: { range: 10.0.40.10-10.0.40.12 }
`
	tests := []struct {
		name string
		nics string
		want []string
		diag string
	}{
		{
			// pve-lab has the endpoint https://10.0.40.11:8006.
			name: "not the address of a cluster endpoint",
			nics: "nics: [{ network: servers, ipv4: { mode: auto } }, { network: servers, ipv4: { mode: auto } }]",
			want: []string{"spec.nics[0].ipv4.address = 10.0.40.10/24", "spec.nics[1].ipv4.address = 10.0.40.12/24"},
		},
		{
			name: "not the gateway, and not used by another interface",
			nics: "nics: [{ network: dmz, ipv4: { mode: auto } }]",
			want: []string{"spec.nics[0].ipv4.address = 10.0.20.3/24"},
		},
		{
			name: "several interfaces on one network",
			nics: "nics: [{ network: dmz, ipv4: { mode: auto } }, { network: dmz, ipv4: { mode: auto } }]",
			want: []string{"spec.nics[0].ipv4.address = 10.0.20.3/24", "spec.nics[1].ipv4.address = 10.0.20.4/24"},
		},
		{
			name: "not the network or broadcast address, with the prefix length of the subnet",
			nics: "nics: [{ network: small, ipv4: { mode: auto } }, { network: small, ipv4: { mode: auto } }]",
			want: []string{"spec.nics[0].ipv4.address = 10.0.30.1/30", "spec.nics[1].ipv4.address = 10.0.30.2/30"},
		},
		{
			name: "only mode auto without an address",
			nics: "nics: [{ network: dmz, ipv4: { mode: dhcp } }, { network: dmz, ipv4: { mode: static, address: 10.0.20.3/24 } }, { network: dmz, ipv4: { mode: auto, address: 10.0.20.4/24 } }, { network: dmz }]",
		},
		{
			name: "exhausted range",
			nics: "nics: [{ network: dmz, ipv4: { mode: auto } }, { network: dmz, ipv4: { mode: auto } }, { network: dmz, ipv4: { mode: auto } }]",
			want: []string{"spec.nics[0].ipv4.address = 10.0.20.3/24", "spec.nics[1].ipv4.address = 10.0.20.4/24"},
			diag: `intent/compute/web-02.yaml:10: error: spec.nics[2].ipv4.address: cannot allocate an address: every address in the static range 10.0.20.1-10.0.20.4 of network "dmz" is in use`,
		},
		{
			name: "network without a static range",
			nics: "nics: [{ network: lan, ipv4: { mode: auto } }]",
			diag: `intent/compute/web-02.yaml:10: error: spec.nics[0].ipv4.address: cannot allocate an address: network "lan" has no static range in spec.ipv4.static.range`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := load(t, map[string]string{
				"intent/network/nets.yaml": nets,
				"intent/compute/web-02.yaml": vm("web-02", "{ nodr/environment: prod }",
					"placement: { cluster: pve-main, assignedNode: pve1 }", "identity: { vmid: 1002 }",
					resources("1Gi"), tt.nics),
			})
			got, diags := plan(t, ws)
			var addresses []string
			for _, line := range got {
				if strings.Contains(line, ".ipv4.address") {
					addresses = append(addresses, strings.TrimSuffix(strings.TrimPrefix(line, "vm/web-02: "), " (intent/compute/web-02.yaml)"))
				}
			}
			if tt.diag != "" {
				checkDiags(t, diags, tt.diag)
			} else {
				checkDiags(t, diags)
			}
			check(t, addresses, tt.want)
		})
	}
}

func TestApply(t *testing.T) {
	const web02 = `# A second web server.
apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-02
  labels:
    nodr/environment: prod   # production
spec:
  placement:
    cluster: pve-main
  resources:
    cpu: { cores: 1 }
    memory: { size: 2Gi }
  nics:
    - network: dmz
      ipv4: { mode: auto }
`
	const databases = `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata: { name: db-01, uid: 01J9Z3K4T7M2Q8V5X6N0B1C2D4, labels: { nodr/environment: lab } }
spec:
  placement: { cluster: pve-main, node: pve3 }
  identity: { vmid: 2001 }
  resources: { cpu: { cores: 1 }, memory: { size: 1Gi } }
---
# Not admitted yet.
apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata: { name: db-02, uid: 01J9Z3K4T7M2Q8V5X6N0B1C2D5, labels: { nodr/environment: lab } }
spec: { placement: { cluster: pve-main, assignedNode: pve3 }, resources: { cpu: { cores: 1 }, memory: { size: 1Gi } } }
`
	ws := load(t, map[string]string{"intent/compute/web-02.yaml": web02, "intent/compute/db.yaml": databases})
	assignments, diags := Plan(ws, vms(t, ws), Options{NewUID: uids()})
	checkDiags(t, diags)
	if err := Apply(ws, assignments, false); err != nil {
		t.Fatal(err)
	}

	for file, want := range map[string]string{
		"intent/compute/web-02.yaml": `# A second web server.
apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-02
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C201
  labels:
    nodr/environment: prod   # production
spec:
  placement:
    cluster: pve-main
    assignedNode: pve2
  identity:
    vmid: 1002
  resources:
    cpu: { cores: 1 }
    memory: { size: 2Gi }
  nics:
    - network: dmz
      ipv4: { mode: auto, address: 10.0.20.3/24 }
`,
		"intent/compute/db.yaml": strings.NewReplacer(
			"node: pve3 }", "node: pve3, assignedNode: pve3 }",
			"assignedNode: pve3 }, resources", "assignedNode: pve3 }, identity: { vmid: 2000 }, resources",
		).Replace(databases),
		"intent/compute/web-01.yaml": web01,
	} {
		got, err := os.ReadFile(filepath.Join(ws.Root, filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s =\n%s\nwant\n%s", file, got, want)
		}
	}

	// The admitted workspace is valid, and admitting it again changes
	// nothing.
	ws = reload(t, ws.Root)
	again, diags := plan(t, ws)
	checkDiags(t, diags)
	check(t, again, nil)
}

func TestApplyWritesAllOrNothing(t *testing.T) {
	ws := load(t, map[string]string{
		"intent/compute/a.yaml": vm("a", "{ nodr/environment: prod }", "placement: { cluster: pve-main }", resources("1Gi")),
		"intent/compute/b.yaml": vm("b", "{ nodr/environment: prod }", "placement: { cluster: pve-main }", resources("1Gi")),
	})
	assignments, diags := Plan(ws, vms(t, ws), Options{NewUID: uids()})
	checkDiags(t, diags)
	// b.yaml changes after planning, so its document moves.
	b := filepath.Join(ws.Root, "intent", "compute", "b.yaml")
	changed := "# Edited meanwhile.\n" + vm("b", "{ nodr/environment: prod }", "placement: { cluster: pve-main }", resources("1Gi"))
	if err := os.WriteFile(b, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Apply(ws, assignments, false)
	if err == nil || !strings.Contains(err.Error(), "intent/compute/b.yaml: no document starts at line 1") {
		t.Fatalf("Apply = %v, want an error for b.yaml", err)
	}
	a, _ := os.ReadFile(filepath.Join(ws.Root, "intent", "compute", "a.yaml"))
	if string(a) != vm("a", "{ nodr/environment: prod }", "placement: { cluster: pve-main }", resources("1Gi")) {
		t.Errorf("Apply wrote a.yaml although it failed:\n%s", a)
	}
}

func TestKeyOrder(t *testing.T) {
	tests := map[string][]string{
		"":                 {"apiVersion", "kind", "metadata", "spec"},
		"metadata":         {"name", "uid", "labels", "annotations"},
		"spec.placement":   {"cluster", "node", "assignedNode", "affinity"},
		"spec.nics.0.ipv4": {"mode", "address"},
		"metadata.labels":  nil,
		"spec.unknown":     nil,
	}
	for path, want := range tests {
		var segs []string
		if path != "" {
			segs = strings.Split(path, ".")
		}
		if got := keyOrder(segs); !slices.Equal(got, want) {
			t.Errorf("keyOrder(%s) = %v, want %v", path, got, want)
		}
	}
}
