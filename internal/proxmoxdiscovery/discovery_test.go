package proxmoxdiscovery_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

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
}
