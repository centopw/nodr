package resolve

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/nrm"
)

const fixture = `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [https://10.0.10.11:8006]
  credentialsRef: proxmox/pve-main-token
---
apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-lab }
spec:
  endpoints: [https://10.0.40.11:8006]
  credentialsRef: proxmox/pve-lab-token
  network: { guestBridge: vmbr1 }
---
apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: lan }
spec:
  ipv4: { subnet: 10.0.10.0/24, gateway: 10.0.10.1 }
---
apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: dmz }
spec:
  vlan: 20
  ipv4: { subnet: 10.0.20.0/24, gateway: 10.0.20.1 }
  realizeOn: { clusters: [pve-main] }
---
apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: iot }
spec:
  vlan: 30
---
apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: iot-copy }
spec:
  vlan: 30
  realizeOn: { clusters: [pve-lab] }
---
apiVersion: nodr/v1alpha1
kind: Template
metadata: { name: debian-12-cloud }
spec:
  cluster: pve-main
  node: pve1
  image: { url: https://example.com/d.qcow2, checksum: "sha256:00" }
  storage: local-lvm
  identity: { vmid: 9001 }
---
apiVersion: nodr/v1alpha1
kind: Template
metadata: { name: lab-debian }
spec:
  cluster: pve-lab
  image: { url: https://example.com/d.qcow2, checksum: "sha256:00" }
  storage: shared
  identity: { vmid: 9001 }
---
apiVersion: nodr/v1alpha1
kind: Template
metadata: { name: lab-debian-copy }
spec:
  cluster: pve-lab
  image: { url: https://example.com/d.qcow2, checksum: "sha256:00" }
  storage: shared
  identity: { vmid: 9001 }
---
apiVersion: nodr/v1alpha1
kind: Template
metadata: { name: pending }
spec:
  cluster: pve-main
  image: { url: https://example.com/p.qcow2, checksum: "sha256:00" }
  storage: local-lvm
---
apiVersion: nodr/v1alpha1
kind: SSHKey
metadata: { name: ops-team }
spec:
  publicKey: ssh-ed25519 AAAAops ops@example
---
apiVersion: nodr/v1alpha1
kind: SSHKey
metadata: { name: alice }
spec:
  publicKey: ssh-ed25519 AAAAalice alice@laptop
`

func index(t *testing.T) *Index {
	t.Helper()
	docs, diags := nrm.Parse("fixture.yaml", []byte(fixture))
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	return NewIndex(docs)
}

func TestNetwork(t *testing.T) {
	ix := index(t)
	got, err := ix.Network("pve-main", "dmz")
	if err != nil || got != (Network{Name: "dmz", Bridge: "vmbr0", VLAN: 20, Gateway: "10.0.20.1"}) {
		t.Errorf("Network(dmz) = %+v, %v", got, err)
	}
	got, err = ix.Network("pve-lab", "lan")
	if err != nil || got.Bridge != "vmbr1" || got.VLAN != 0 {
		t.Errorf("Network(lan) on pve-lab = %+v, %v", got, err)
	}
	if _, err := ix.Network("pve-lab", "dmz"); err == nil || !strings.Contains(err.Error(), "not realized") {
		t.Errorf("dmz on pve-lab: err = %v", err)
	}
	if _, err := ix.Network("pve-main", "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown network: err = %v", err)
	}
	if _, err := ix.Network("nowhere", "lan"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown cluster: err = %v", err)
	}
}

func TestNetworkFor(t *testing.T) {
	ix := index(t)
	tests := []struct {
		cluster, bridge string
		vlan            int
		want            string
		err             string
	}{
		{"pve-main", "vmbr0", 20, "dmz", ""},
		{"pve-main", "vmbr0", 0, "lan", ""},
		{"pve-main", "vmbr0", 30, "iot", ""},
		{"pve-lab", "vmbr1", 30, "", "ambiguous"},
		{"pve-main", "vmbr0", 99, "", "no network with VLAN 99"},
		{"pve-main", "vmbr9", 20, "", "not the guest bridge"},
	}
	for _, tt := range tests {
		got, err := ix.NetworkFor(tt.cluster, tt.bridge, tt.vlan)
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("NetworkFor(%s, %s, %d): err = %v, want %q", tt.cluster, tt.bridge, tt.vlan, err, tt.err)
			}
			continue
		}
		if err != nil || got.Name != tt.want {
			t.Errorf("NetworkFor(%s, %s, %d) = %+v, %v; want %s", tt.cluster, tt.bridge, tt.vlan, got, err, tt.want)
		}
	}
}

func TestTemplate(t *testing.T) {
	ix := index(t)
	want := Template{Name: "debian-12-cloud", VMID: 9001, Node: "pve1"}
	if got, err := ix.Template("pve-main", "debian-12-cloud"); err != nil || got != want {
		t.Errorf("Template(debian-12-cloud) = %+v, %v; want %+v", got, err, want)
	}
	if got, err := ix.Template("pve-lab", "lab-debian"); err != nil || got != (Template{Name: "lab-debian", VMID: 9001}) {
		t.Errorf("Template(lab-debian) = %+v, %v", got, err)
	}
	if _, err := ix.Template("pve-lab", "debian-12-cloud"); err == nil || !strings.Contains(err.Error(), `belongs to cluster "pve-main"`) {
		t.Errorf("a template of another cluster: err = %v", err)
	}
	if _, err := ix.Template("pve-main", "pending"); !errors.Is(err, ErrNotAllocated) {
		t.Errorf("a template without a guest ID: err = %v", err)
	}
	if _, err := ix.Template("pve-main", "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown template: err = %v", err)
	}
}

func TestTemplateFor(t *testing.T) {
	ix := index(t)
	if got, err := ix.TemplateFor("pve-main", 9001); err != nil || got.Name != "debian-12-cloud" || got.Node != "pve1" {
		t.Errorf("TemplateFor(pve-main, 9001) = %+v, %v", got, err)
	}
	if _, err := ix.TemplateFor("pve-lab", 9001); err == nil || !strings.Contains(err.Error(), "lab-debian, lab-debian-copy") {
		t.Errorf("a guest ID of two templates: err = %v", err)
	}
	for _, id := range []int{1234, 0} {
		if _, err := ix.TemplateFor("pve-main", id); !errors.Is(err, ErrNotFound) {
			t.Errorf("TemplateFor(pve-main, %d): err = %v", id, err)
		}
	}
}

func TestKeys(t *testing.T) {
	ix := index(t)
	keys, err := ix.PublicKeys([]string{"alice", "ops-team"})
	if err != nil || !reflect.DeepEqual(keys, []string{"ssh-ed25519 AAAAalice alice@laptop", "ssh-ed25519 AAAAops ops@example"}) {
		t.Errorf("PublicKeys = %v, %v", keys, err)
	}
	if _, err := ix.PublicKeys([]string{"bob"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown key: err = %v", err)
	}
	names, err := ix.KeyNames([]string{"ssh-ed25519 AAAAops a different comment", "ssh-ed25519 AAAAalice"})
	if err != nil || !reflect.DeepEqual(names, []string{"ops-team", "alice"}) {
		t.Errorf("KeyNames = %v, %v", names, err)
	}
	if _, err := ix.KeyNames([]string{"ssh-ed25519 AAAAunknown"}); err == nil {
		t.Error("KeyNames of an unknown key succeeded")
	}
}
