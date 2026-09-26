package v1alpha1

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/nrm"
)

func loadTestdata(t *testing.T, name string) []*nrm.Document {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	docs, diags := nrm.Parse(name, data)
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	return docs
}

func registry(t *testing.T) *nrm.Registry {
	t.Helper()
	r, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

func TestValidDocuments(t *testing.T) {
	r := registry(t)
	docs := loadTestdata(t, "valid.yaml")
	if diags := r.Validate(docs); len(diags) > 0 {
		t.Errorf("unexpected diagnostics:\n%v", diags.Err())
		for _, d := range diags {
			t.Log(d)
		}
	}
	manifest := loadTestdata(t, "workspace.yaml")[0]
	if diags := r.ValidateManifest(manifest); len(diags) > 0 {
		t.Errorf("unexpected diagnostics for the manifest: %v", diags)
	}
}

func TestDecodeEveryKind(t *testing.T) {
	docs := append(loadTestdata(t, "valid.yaml"), loadTestdata(t, "workspace.yaml")...)
	for _, d := range docs {
		var err error
		switch d.Kind {
		case KindProxmoxCluster:
			_, err = Decode[ProxmoxClusterSpec](d)
		case KindNetwork:
			_, err = Decode[NetworkSpec](d)
		case KindTemplate:
			_, err = Decode[TemplateSpec](d)
		case KindSSHKey:
			_, err = Decode[SSHKeySpec](d)
		case KindVirtualMachine:
			_, err = Decode[VirtualMachineSpec](d)
		case KindWorkspace:
			_, err = Decode[WorkspaceSpec](d)
		default:
			t.Fatalf("no decoder for %s", d.Kind)
		}
		if err != nil {
			t.Errorf("Decode %s: %v", d.Ref(), err)
		}
	}
}

// TestTypesMirrorSchemas checks that every object in each kind's schema has
// exactly the properties that the corresponding Go struct declares.
func TestTypesMirrorSchemas(t *testing.T) {
	specs := map[string]reflect.Type{
		"workspace.json":      reflect.TypeOf(WorkspaceSpec{}),
		"proxmoxcluster.json": reflect.TypeOf(ProxmoxClusterSpec{}),
		"network.json":        reflect.TypeOf(NetworkSpec{}),
		"template.json":       reflect.TypeOf(TemplateSpec{}),
		"sshkey.json":         reflect.TypeOf(SSHKeySpec{}),
		"virtualmachine.json": reflect.TypeOf(VirtualMachineSpec{}),
	}
	schemas := map[string]map[string]any{}
	files, _ := fs.Glob(Schemas(), "*.json")
	for _, f := range files {
		data, _ := fs.ReadFile(Schemas(), f)
		var s map[string]any
		if err := json.Unmarshal(data, &s); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		schemas[f] = s
	}
	for file, typ := range specs {
		spec, specFile := resolve(schemas, file, map[string]any{"$ref": "#/$defs/spec"})
		compareObject(t, schemas, specFile, "spec", spec, typ)
	}
}

// resolve follows $ref until it reaches a schema without one, and returns
// that schema with the name of the file that contains it.
func resolve(schemas map[string]map[string]any, file string, s map[string]any) (map[string]any, string) {
	for {
		ref, ok := s["$ref"].(string)
		if !ok {
			return s, file
		}
		target, pointer, _ := strings.Cut(ref, "#")
		if target != "" {
			file = target
		}
		node := any(schemas[file])
		for _, seg := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
			if seg == "" {
				continue
			}
			node = node.(map[string]any)[seg]
		}
		s = node.(map[string]any)
	}
}

func compareObject(t *testing.T, schemas map[string]map[string]any, file, path string, s map[string]any, typ reflect.Type) {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
		typ = typ.Elem()
	}
	props, _ := s["properties"].(map[string]any)
	if typ.Kind() != reflect.Struct {
		if props != nil {
			t.Errorf("%s: %s has properties but %s is not a struct", file, path, typ)
		}
		return
	}
	fields := map[string]reflect.StructField{}
	for i := range typ.NumField() {
		f := typ.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		fields[name] = f
	}
	var names []string
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f, ok := fields[name]
		if !ok {
			t.Errorf("%s: %s.%s is in the schema but not in %s", file, path, name, typ)
			continue
		}
		child, childFile := resolve(schemas, file, props[name].(map[string]any))
		if items, ok := child["items"].(map[string]any); ok {
			child, childFile = resolve(schemas, childFile, items)
		}
		if _, isQuantity := f.Type.MethodByName("MarshalText"); isQuantity {
			continue
		}
		compareObject(t, schemas, childFile, path+"."+name, child, f.Type)
	}
	for name := range fields {
		if _, ok := props[name]; !ok {
			t.Errorf("%s: %s field %q of %s is not in the schema", file, path, name, typ)
		}
	}
}

func TestSemanticChecks(t *testing.T) {
	r := registry(t)
	base := loadTestdata(t, "valid.yaml")
	tests := []struct {
		name string
		doc  string
		want []string
	}{
		{
			name: "virtual machine",
			doc: `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: bad-vm
spec:
  placement:
    cluster: pve-main
    node: pve1
    assignedNode: pve2
    affinity:
      separateFrom: [bad-vm, web-01]
      keepWith: [web-01]
  resources:
    cpu: { cores: 2 }
    memory: { size: 1000k, minimum: 2Gi }
  disks:
    - { name: root, storage: local-lvm, size: 1536Mi }
    - { name: root, storage: local-lvm, size: "0" }
  nics:
    - network: dmz
      ipv4: { mode: dhcp, address: 10.0.20.30/24 }
    - network: dmz
      ipv4: { mode: static, address: 10.0.20.0/24 }
`,
			want: []string{
				"bad.yaml:9: error: spec.placement.assignedNode: must match the pinned node pve1",
				"bad.yaml:11: error: spec.placement.affinity.separateFrom[0]: a guest cannot be separated from itself",
				"bad.yaml:11: error: spec.placement.affinity.separateFrom[1]: web-01 is also listed in keepWith",
				"bad.yaml:15: error: spec.resources.memory.minimum: cannot be larger than the memory size 1000000",
				"bad.yaml:15: error: spec.resources.memory.size: must be a whole number of MiB: 1000000 is not a whole number of 1Mi",
				"bad.yaml:17: error: spec.disks[0].size: must be a whole number of GiB: 1536Mi is not a whole number of 1Gi",
				`bad.yaml:18: error: spec.disks[1].name: disk name "root" is already used by spec.disks[0]`,
				"bad.yaml:18: error: spec.disks[1].size: must be greater than zero",
				"bad.yaml:21: error: spec.nics[0].ipv4.address: an address cannot be combined with mode dhcp",
				"bad.yaml:23: error: spec.nics[1].ipv4.address: 10.0.20.0 is the network address of its subnet, not a host address",
			},
		},
		{
			name: "network",
			doc: `apiVersion: nodr/v1alpha1
kind: Network
metadata:
  name: bad-net
spec:
  ipv4:
    subnet: 10.0.30.0/24
    gateway: 10.0.31.1
    dhcp: { range: 10.0.30.100-10.0.30.200 }
    static: { range: 10.0.30.150-10.0.31.10 }
`,
			want: []string{
				"bad.yaml:8: error: spec.ipv4.gateway: must be an address inside 10.0.30.0/24",
				"bad.yaml:10: error: spec.ipv4.static.range: must be inside 10.0.30.0/24",
			},
		},
		{
			name: "network with host bits",
			doc: `apiVersion: nodr/v1alpha1
kind: Network
metadata:
  name: bad-net
spec:
  ipv4:
    subnet: 10.0.30.1/24
`,
			want: []string{
				"bad.yaml:7: error: spec.ipv4.subnet: has host bits set; did you mean 10.0.30.0/24?",
			},
		},
		{
			name: "overlapping ranges and gateway inside a range",
			doc: `apiVersion: nodr/v1alpha1
kind: Network
metadata:
  name: bad-net
spec:
  ipv4:
    subnet: 10.0.30.0/24
    gateway: 10.0.30.100
    dhcp: { range: 10.0.30.100-10.0.30.200 }
    static: { range: 10.0.30.200-10.0.30.250 }
`,
			want: []string{
				"bad.yaml:9: warning: spec.ipv4.dhcp.range: contains the gateway 10.0.30.100",
				"bad.yaml:10: error: spec.ipv4.static.range: overlaps the range 10.0.30.100-10.0.30.200",
			},
		},
		{
			name: "template checksum length",
			doc: `apiVersion: nodr/v1alpha1
kind: Template
metadata:
  name: bad-template
spec:
  cluster: pve-main
  image: { url: https://example.com/image.qcow2, checksum: "sha256:abc" }
  storage: local-lvm
`,
			want: []string{
				"bad.yaml:7: error: spec.image.checksum: a sha256 checksum has 64 hexadecimal digits, not 3",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			docs, diags := nrm.Parse("bad.yaml", []byte(tt.doc))
			if diags.HasErrors() {
				t.Fatal(diags.Err())
			}
			got := r.Validate(append(append([]*nrm.Document{}, base...), docs...))
			assertDiags(t, got, tt.want)
		})
	}
}

// vm returns a VirtualMachine document on a cluster. The lines of spec
// follow from line 7 on.
func vm(name, cluster string, spec ...string) string {
	return "apiVersion: nodr/v1alpha1\nkind: VirtualMachine\nmetadata: { name: " + name + " }\nspec:\n" +
		"  placement: { cluster: " + cluster + " }\n" +
		"  resources: { cpu: { cores: 1 }, memory: { size: 1Gi } }\n" +
		"  " + strings.Join(spec, "\n  ") + "\n"
}

// template returns a Template document of a cluster with a guest ID on
// line 8.
func template(name, cluster string, vmid int) string {
	return fmt.Sprintf(`apiVersion: nodr/v1alpha1
kind: Template
metadata: { name: %s }
spec:
  cluster: %s
  image: { url: https://example.com/image.qcow2, checksum: "sha256:%s" }
  storage: local-lvm
  identity: { vmid: %d }
`, name, cluster, strings.Repeat("0", 64), vmid)
}

// Documents that the tests of unique values add to valid.yaml, which has
// the cluster pve-main with the endpoints 10.0.10.11 and 10.0.10.12, the
// network dmz, the template debian-12-cloud with guest ID 9001, and web-01
// with guest ID 1012 and an interface on dmz with 10.0.20.21 and
// BC:24:11:3A:5E:01.
const (
	labCluster = `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-lab }
spec:
  endpoints: [https://10.0.40.11:8006, https://pve-lab.home.arpa:8006]
  credentialsRef: proxmox/pve-lab-token
`
	lanNetwork = `apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: lan }
spec:
  ipv4: { subnet: 10.0.10.0/24 }
`
	// labNetwork is a separate network that reuses the subnet of dmz.
	labNetwork = `apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: lab }
spec:
  vlan: 40
  ipv4: { subnet: 10.0.20.0/24 }
`
)

func TestUniqueValues(t *testing.T) {
	r := registry(t)
	base := loadTestdata(t, "valid.yaml")
	tests := []struct {
		name string
		// docs are added after valid.yaml, each in a file named after
		// the resource.
		docs []string
		want []string
	}{
		{
			name: "guest IDs within a cluster",
			docs: []string{
				vm("web-03", "pve-main", "identity: { vmid: 1012 }"),
				vm("web-04", "pve-main", "identity: { vmid: 1012 }"),
				vm("web-05", "pve-main", "identity: { vmid: 1013 }"),
			},
			want: []string{
				`web-03.yaml:7: error: spec.identity.vmid: guest ID 1012 is used twice in cluster "pve-main"; it is also used by VirtualMachine/web-01 at valid.yaml:78`,
				`web-04.yaml:7: error: spec.identity.vmid: guest ID 1012 is used twice in cluster "pve-main"; it is also used by VirtualMachine/web-01 at valid.yaml:78`,
			},
		},
		{
			name: "templates and virtual machines share guest IDs",
			docs: []string{
				vm("web-03", "pve-main", "identity: { vmid: 9001 }"),
				template("debian-13-cloud", "pve-main", 1012),
			},
			want: []string{
				`web-03.yaml:7: error: spec.identity.vmid: guest ID 9001 is used twice in cluster "pve-main"; it is also used by Template/debian-12-cloud at valid.yaml:52`,
				`debian-13-cloud.yaml:8: error: spec.identity.vmid: guest ID 1012 is used twice in cluster "pve-main"; it is also used by VirtualMachine/web-01 at valid.yaml:78`,
			},
		},
		{
			name: "the same guest IDs in another cluster",
			docs: []string{
				labCluster,
				vm("lab-01", "pve-lab", "identity: { vmid: 1012 }"),
				template("debian-12-lab", "pve-lab", 9001),
			},
		},
		{
			name: "addresses within a network, whatever the prefix length",
			docs: []string{
				labNetwork,
				vm("web-03", "pve-main",
					"nics:",
					"  - { network: dmz, ipv4: { mode: static, address: 10.0.20.21/25 } }",
					"  - { network: lab, ipv4: { mode: static, address: 10.0.20.30/24 } }",
					"  - { network: lab, ipv4: { mode: static, address: 10.0.20.30/24 } }"),
			},
			want: []string{
				`web-03.yaml:8: error: spec.nics[0].ipv4.address: address 10.0.20.21 is used twice in network "dmz"; it is also used by VirtualMachine/web-01 at valid.yaml:92`,
				`web-03.yaml:10: error: spec.nics[2].ipv4.address: address 10.0.20.30 is used twice in network "lab"; it is also used by VirtualMachine/web-03 at web-03.yaml:9`,
			},
		},
		{
			name: "the same address in another network",
			docs: []string{
				labNetwork,
				vm("lab-01", "pve-main", "nics: [{ network: lab, ipv4: { mode: static, address: 10.0.20.21/24 } }]"),
			},
		},
		{
			name: "addresses of the endpoints of the cluster",
			docs: []string{
				lanNetwork,
				vm("web-03", "pve-main", "nics: [{ network: lan, ipv4: { mode: static, address: 10.0.10.12/24 } }]"),
			},
			want: []string{
				`web-03.yaml:7: error: spec.nics[0].ipv4.address: address 10.0.10.12 is used twice; it is also used by an endpoint of ProxmoxCluster/pve-main at valid.yaml:10`,
			},
		},
		{
			name: "addresses of the endpoints of another cluster",
			docs: []string{
				labCluster,
				lanNetwork,
				vm("lab-01", "pve-lab", "nics: [{ network: lan, ipv4: { mode: static, address: 10.0.10.11/24 } }]"),
			},
		},
		{
			name: "MAC addresses within a cluster, whatever the case",
			docs: []string{
				labCluster,
				vm("web-03", "pve-main", "nics: [{ network: dmz, mac: bc:24:11:3a:5e:01 }]"),
				vm("lab-01", "pve-lab", "nics: [{ network: dmz, mac: BC:24:11:3A:5E:01 }]"),
			},
			want: []string{
				`web-03.yaml:7: error: spec.nics[0].mac: MAC address bc:24:11:3a:5e:01 is used twice in cluster "pve-main"; it is also used by VirtualMachine/web-01 at valid.yaml:91`,
			},
		},
		{
			// The invalid power state does not stop the documents from
			// decoding, so only the schema keeps them out.
			name: "only documents that pass their schema",
			docs: []string{
				vm("web-03", "pve-main", "identity: { vmid: 1012 }",
					"nics: [{ network: dmz, mac: BC:24:11:3A:5E:01, ipv4: { mode: static, address: 10.0.20.21/24 } }]",
					"lifecycle: { powerState: paused }"),
				vm("web-04", "pve-main", "identity: { vmid: 1100 }", "lifecycle: { powerState: paused }"),
				vm("web-05", "pve-main", "identity: { vmid: 1100 }"),
			},
			want: []string{
				"web-03.yaml:9: error: spec.lifecycle.powerState: value must be one of 'running', 'stopped', 'unmanaged'",
				"web-04.yaml:8: error: spec.lifecycle.powerState: value must be one of 'running', 'stopped', 'unmanaged'",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			docs := append([]*nrm.Document{}, base...)
			for _, src := range tt.docs {
				parsed, diags := nrm.Parse("new.yaml", []byte(src))
				if diags.HasErrors() || len(parsed) != 1 {
					t.Fatalf("parse: %v (%d documents)", diags.Err(), len(parsed))
				}
				parsed[0].File = parsed[0].Metadata.Name + ".yaml"
				docs = append(docs, parsed[0])
			}
			assertDiags(t, r.Validate(docs), tt.want)
		})
	}
}

func TestReferencesAreChecked(t *testing.T) {
	r := registry(t)
	docs := loadTestdata(t, "valid.yaml")
	var kept []*nrm.Document
	for _, d := range docs {
		if d.Kind != KindNetwork && d.Kind != KindSSHKey {
			kept = append(kept, d)
		}
	}
	assertDiags(t, r.Validate(kept), []string{
		`valid.yaml:90: error: spec.nics[0].network: Network "dmz" does not exist`,
		`valid.yaml:97: error: spec.guest.cloudInit.authorizedKeys[0]: SSHKey "ops-team" does not exist`,
	})
}

func TestWithDefaults(t *testing.T) {
	spec := VirtualMachineSpec{NICs: []NIC{{Network: "dmz"}}, HA: &HA{Enabled: true}}
	got := spec.WithDefaults()
	if got.Placement.Node != DefaultNode || got.Resources.CPU.Sockets != 1 || got.Resources.CPU.Type != DefaultCPUType {
		t.Errorf("placement or CPU defaults missing: %+v", got)
	}
	if !*got.Guest.Agent || !*got.Lifecycle.Protection || !*got.Lifecycle.StartOnBoot || got.Lifecycle.PowerState != PowerStateRunning {
		t.Errorf("guest or lifecycle defaults missing: %+v %+v", got.Guest, got.Lifecycle)
	}
	if got.NICs[0].IPv4 == nil || got.NICs[0].IPv4.Mode != IPv4ModeDHCP {
		t.Errorf("NIC default missing: %+v", got.NICs[0])
	}
	if *got.HA.MaxRestart != 1 || *got.HA.MaxRelocate != 1 {
		t.Errorf("HA defaults missing: %+v", got.HA)
	}
	if spec.NICs[0].IPv4 != nil || spec.Guest.Agent != nil {
		t.Error("WithDefaults modified its receiver")
	}
}

func TestProxmoxClusterSpecMACPrefix(t *testing.T) {
	tests := []struct {
		name string
		spec ProxmoxClusterSpec
		want string
	}{
		{name: "default", want: "BC:24:11"},
		{name: "explicit", spec: ProxmoxClusterSpec{MacPrefix: "AA:BB:CC"}, want: "AA:BB:CC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.MACPrefix(); got != tt.want {
				t.Errorf("MACPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProxmoxClusterSchemaRejectsMalformedMACPrefix(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "wrong octet count", value: "AA:BB"},
		{name: "one-digit octet", value: "A:BB:CC"},
		{name: "wrong separator", value: "AA-BB-CC"},
		{name: "non-hex", value: "GG:BB:CC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := fmt.Sprintf(`apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata:
  name: bad-prefix
spec:
  endpoints: [https://10.0.10.11:8006]
  credentialsRef: proxmox/bad-prefix-token
  macPrefix: %q
`, tt.value)
			docs, diags := nrm.Parse("bad.yaml", []byte(src))
			if diags.HasErrors() || len(docs) != 1 {
				t.Fatalf("parse: %v (%d documents)", diags.Err(), len(docs))
			}
			want := fmt.Sprintf("bad.yaml:8: error: spec.macPrefix: '%s' does not match pattern '^([0-9A-Fa-f]{2}:){2}[0-9A-Fa-f]{2}$'", tt.value)
			assertDiags(t, registry(t).Validate(docs), []string{want})
		})
	}
}

func assertDiags(t *testing.T, got diag.List, want []string) {
	t.Helper()
	lines := make([]string, len(got))
	for i, d := range got {
		lines[i] = d.String()
	}
	sort.Strings(lines)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("diagnostics:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
