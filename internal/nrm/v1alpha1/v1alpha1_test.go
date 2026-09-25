package v1alpha1

import (
	"encoding/json"
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
