package commands

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
)

const testManifest = `apiVersion: nodr/v1alpha1
kind: Workspace
metadata: { name: homelab }
spec:
  environments:
    prod: { vmidRange: [1012, 1013] }
    lab: { vmidRange: [2000, 2999] }
    templates: { vmidRange: [9000, 9099] }
`

const testPlatform = `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [https://10.0.20.11:8006]
  credentialsRef: proxmox/pve-main-token
  nodes: [pve1, pve2]
---
apiVersion: nodr/v1alpha1
kind: Template
metadata: { name: debian-12-cloud }
spec:
  cluster: pve-main
  image: { url: https://example.com/debian.qcow2, checksum: "sha256:0000000000000000000000000000000000000000000000000000000000000000" }
  storage: local-lvm
  identity: { vmid: 9000 }
`

const testNetwork = `apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: dmz }
spec:
  ipv4:
    subnet: 10.0.20.0/24
    gateway: 10.0.20.1
    static: { range: 10.0.20.23-10.0.20.23 }
`

const testVM = `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-01
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C2D3
  labels: { nodr/environment: prod }
spec:
  placement: { cluster: pve-main, assignedNode: pve1 }
  identity: { vmid: 1012 }
  resources: { cpu: { cores: 2 }, memory: { size: 8Gi } }
  nics:
    - network: dmz
      mac: BC:24:11:3A:5E:01
      ipv4: { mode: static, address: 10.0.20.21/24 }
`

// testWorkspace writes the minimal workspace fixture used by the api tests
// into a fresh temporary directory and returns its root.
func testWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"nodr.yaml":                  testManifest,
		"intent/platform/pve.yaml":   testPlatform,
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
	return root
}

func validCreate(name string) VMCreateParams {
	return VMCreateParams{
		Name:        name,
		Environment: "prod",
		Cluster:     "pve-main",
		Template:    "debian-12-cloud",
		Network:     "dmz",
		Storage:     "local-lvm",
		Size:        "S",
	}
}

func asError(t *testing.T, err error) *Error {
	t.Helper()
	var cerr *Error
	if !errors.As(err, &cerr) {
		t.Fatalf("error = %v (%T), want *Error", err, err)
	}
	return cerr
}

func TestSizeCatalogAndSummaryWithoutAddress(t *testing.T) {
	wantSizes := map[string]size{
		"S": {CPU: 1, Memory: "2Gi", Disk: "20Gi"},
		"M": {CPU: 2, Memory: "4Gi", Disk: "40Gi"},
		"L": {CPU: 4, Memory: "8Gi", Disk: "80Gi"},
	}
	if !reflect.DeepEqual(sizes, wantSizes) {
		t.Errorf("sizes = %#v, want %#v", sizes, wantSizes)
	}

	documents, diags := nrm.Parse("intent/compute/no-ip.yaml", []byte(`apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata: { name: no-ip }
spec:
  placement: { cluster: pve-main, assignedNode: pve1 }
  identity: { vmid: 1013 }
  resources: { cpu: { cores: 1 }, memory: { size: 2Gi } }
  nics: [{ network: dmz, mac: BC:24:11:00:00:01, ipv4: { mode: dhcp } }]
`))
	if diags.HasErrors() || len(documents) != 1 {
		t.Fatalf("parse VM: %v", diags.Err())
	}
	vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](documents[0])
	if err != nil {
		t.Fatal(err)
	}
	response := createResult(vm)
	if response.Summary != "Creates no-ip with 1 vCPUs and 2 GiB memory on pve1." {
		t.Errorf("summary = %q", response.Summary)
	}
	if response.Addresses == nil || response.MACs == nil {
		t.Errorf("network arrays must not be nil: addresses=%v macs=%v", response.Addresses, response.MACs)
	}
}

func TestCreateVMHappyPathAdmitsVM(t *testing.T) {
	root := testWorkspace(t)
	result, err := CreateVM(root, validCreate("db-01"))
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if result.Name != "db-01" || result.Cluster != "pve-main" || result.CPU != 1 || result.Memory != "2Gi" {
		t.Errorf("result = %#v", result)
	}
	if result.VMID == 0 {
		t.Errorf("vmid not assigned: %#v", result)
	}
	if len(result.MACs) != 1 || result.MACs[0] == "" {
		t.Errorf("macs = %v, want one admitted MAC", result.MACs)
	}
	if !strings.HasPrefix(result.Summary, "Creates db-01 with 1 vCPUs and 2 GiB memory on ") {
		t.Errorf("summary = %q", result.Summary)
	}
	content, err := os.ReadFile(filepath.Join(root, "intent", "compute", "db-01.yaml"))
	if err != nil {
		t.Fatalf("intent file: %v", err)
	}
	if !strings.Contains(string(content), "name: db-01") {
		t.Errorf("intent file missing name: %s", content)
	}
}

func TestCreateVMDuplicateIsConflict(t *testing.T) {
	root := testWorkspace(t)
	_, err := CreateVM(root, validCreate("web-01"))
	cerr := asError(t, err)
	if cerr.Status != http.StatusConflict || cerr.Title != "Resource conflict" {
		t.Errorf("status/title = %d %q, want 409 Resource conflict", cerr.Status, cerr.Title)
	}
	if len(cerr.Fields) != 1 || cerr.Fields[0].Path != "params.name" || !strings.Contains(cerr.Fields[0].Message, "already exists") {
		t.Errorf("fields = %#v", cerr.Fields)
	}
}

func TestCreateVMValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*VMCreateParams)
		status  int
		fields  []string
		message string
	}{
		{
			name:   "missing name",
			mutate: func(p *VMCreateParams) { p.Name = "" },
			status: http.StatusBadRequest,
			fields: []string{"params.name"},
		},
		{
			name:   "unknown environment",
			mutate: func(p *VMCreateParams) { p.Environment = "staging" },
			status: http.StatusBadRequest,
			fields: []string{"params.environment"},
		},
		{
			name:   "missing cluster",
			mutate: func(p *VMCreateParams) { p.Cluster = "" },
			status: http.StatusBadRequest,
			fields: []string{"params.cluster"},
		},
		{
			name:   "unknown template",
			mutate: func(p *VMCreateParams) { p.Template = "nope" },
			status: http.StatusBadRequest,
			fields: []string{"params.template"},
		},
		{
			name:   "bad size",
			mutate: func(p *VMCreateParams) { p.Size = "XL" },
			status: http.StatusBadRequest,
			fields: []string{"params.size"},
		},
		{
			name:   "missing storage",
			mutate: func(p *VMCreateParams) { p.Storage = " " },
			status: http.StatusBadRequest,
			fields: []string{"params.storage"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := testWorkspace(t)
			p := validCreate("db-01")
			tt.mutate(&p)
			_, err := CreateVM(root, p)
			cerr := asError(t, err)
			if cerr.Status != tt.status {
				t.Errorf("status = %d, want %d", cerr.Status, tt.status)
			}
			if cerr.Title != "Invalid vm.create parameters" {
				t.Errorf("title = %q", cerr.Title)
			}
			var got []string
			for _, f := range cerr.Fields {
				got = append(got, f.Path)
			}
			if !reflect.DeepEqual(got, tt.fields) {
				t.Errorf("field paths = %v, want %v", got, tt.fields)
			}
			if _, statErr := os.Stat(filepath.Join(root, "intent", "compute", "db-01.yaml")); !os.IsNotExist(statErr) {
				t.Errorf("validation failure must not write a file, stat err = %v", statErr)
			}
		})
	}
}

func TestSetVMPowerStatePersists(t *testing.T) {
	root := testWorkspace(t)
	file := filepath.Join(root, "intent", "compute", "web-01.yaml")
	tests := []struct {
		name  string
		state string
	}{
		{name: "stop", state: v1alpha1.PowerStateStopped},
		{name: "start", state: v1alpha1.PowerStateRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := SetVMPowerState(root, "web-01", tt.state)
			if err != nil {
				t.Fatalf("SetVMPowerState: %v", err)
			}
			if result.Name != "web-01" || result.PowerState != tt.state {
				t.Errorf("result = %#v", result)
			}
			content, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(content), "powerState: "+tt.state) {
				t.Errorf("file missing powerState: %s", content)
			}
		})
	}
}

func TestSetVMPowerStateNotFound(t *testing.T) {
	root := testWorkspace(t)
	_, err := SetVMPowerState(root, "missing", v1alpha1.PowerStateStopped)
	cerr := asError(t, err)
	if cerr.Status != http.StatusNotFound || cerr.Title != "Resource not found" {
		t.Errorf("status/title = %d %q", cerr.Status, cerr.Title)
	}
}

func TestDeleteVMRemovesFile(t *testing.T) {
	root := testWorkspace(t)
	result, err := DeleteVM(root, "web-01")
	if err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}
	if result.Name != "web-01" || !result.Deleted {
		t.Errorf("result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "intent", "compute", "web-01.yaml")); !os.IsNotExist(err) {
		t.Errorf("file not removed, stat err = %v", err)
	}
	_, err = DeleteVM(root, "web-01")
	if cerr := asError(t, err); cerr.Status != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404", cerr.Status)
	}
}

func TestRunDispatch(t *testing.T) {
	t.Run("unknown command", func(t *testing.T) {
		_, err := Run(t.TempDir(), "vm.destroy", nil)
		cerr := asError(t, err)
		if cerr.Status != http.StatusBadRequest || cerr.Title != "Unknown command" {
			t.Errorf("status/title = %d %q", cerr.Status, cerr.Title)
		}
		if cerr.Detail != `command "vm.destroy" is not supported` {
			t.Errorf("detail = %q", cerr.Detail)
		}
		if len(cerr.Fields) != 1 || cerr.Fields[0].Path != "command" {
			t.Errorf("fields = %#v", cerr.Fields)
		}
	})
	t.Run("bad json params", func(t *testing.T) {
		_, err := Run(t.TempDir(), "vm.create", json.RawMessage(`{"nope":1}`))
		cerr := asError(t, err)
		if cerr.Status != http.StatusBadRequest || cerr.Title != "Invalid request" {
			t.Errorf("status/title = %d %q", cerr.Status, cerr.Title)
		}
		if !strings.HasPrefix(cerr.Detail, "decode params: ") {
			t.Errorf("detail = %q", cerr.Detail)
		}
	})
	t.Run("action requires name", func(t *testing.T) {
		_, err := Run(t.TempDir(), "vm.stop", json.RawMessage(`{}`))
		cerr := asError(t, err)
		if cerr.Detail != "name is required" || len(cerr.Fields) != 1 || cerr.Fields[0].Path != "name" {
			t.Errorf("error = %#v", cerr)
		}
	})
	t.Run("stop then start then delete", func(t *testing.T) {
		root := testWorkspace(t)
		got, err := Run(root, "vm.stop", json.RawMessage(`{"name":"web-01"}`))
		if err != nil {
			t.Fatal(err)
		}
		if got != (VMPowerStateResult{Name: "web-01", PowerState: v1alpha1.PowerStateStopped}) {
			t.Errorf("stop result = %#v", got)
		}
		if _, err := Run(root, "vm.start", json.RawMessage(`{"name":"web-01"}`)); err != nil {
			t.Fatal(err)
		}
		got, err = Run(root, "vm.delete", json.RawMessage(`{"name":"web-01"}`))
		if err != nil {
			t.Fatal(err)
		}
		if got != (VMDeleteResult{Name: "web-01", Deleted: true}) {
			t.Errorf("delete result = %#v", got)
		}
	})
}
