package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/proxmox/proxmoxtest"
	"github.com/centopw/nodr/internal/secrets"
)

func TestClusterConnect_BootstrapsAndWritesIntent(t *testing.T) {
	t.Setenv("NODR_KEK", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	root := writeWorkspace(t, planFiles)
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"POST /api2/json/access/ticket": func(w http.ResponseWriter, _ *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"ticket":"T","CSRFPreventionToken":"C"}`)
		},
		"GET /api2/json/nodes/pve1/certificates/info": func(w http.ResponseWriter, _ *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `[{"fingerprint":"AA:BB:CC"}]`)
		},
		"POST /api2/json/access/users": func(w http.ResponseWriter, _ *http.Request) { proxmoxtest.JSONResponse(w, http.StatusOK, "") },
		"POST /api2/json/access/roles": func(w http.ResponseWriter, _ *http.Request) { proxmoxtest.JSONResponse(w, http.StatusOK, "") },
		"PUT /api2/json/access/acl":    func(w http.ResponseWriter, _ *http.Request) { proxmoxtest.JSONResponse(w, http.StatusOK, "") },
		"POST /api2/json/access/users/nodr@pve/token/nodr": func(w http.ResponseWriter, _ *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"value":"wizard-secret-xyz"}`)
		},
	})
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	stdin := bytes.NewBufferString("pve-main\n" + srv.URL + "\npve1\nroot\nadminpass\nyes\n")
	code := Run(context.Background(), nil, []string{"--workspace", root, "cluster", "connect"}, stdin, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}

	kek := make([]byte, 32)
	store, err := secrets.Open(filepath.Join(root, ".nodr", "secrets.db"), kek)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	defer store.Close()
	got, err := store.Resolve(context.Background(), "proxmox/pve-main-token")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(got) != "wizard-secret-xyz" {
		t.Errorf("token = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "intent", "platform", "pve-main.yaml")); err != nil {
		t.Errorf("intent file not written: %v", err)
	}
}
func TestClusterDiscover_ReportsGuests(t *testing.T) {
	t.Setenv("NODR_KEK", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	wantAuth := "PVEAPIToken=nodr@pve!nodr=token-from-the-environment"
	handlers := map[string]http.HandlerFunc{
		"GET /api2/json/cluster/resources": func(w http.ResponseWriter, r *http.Request) {
			if auth := r.Header.Get("Authorization"); auth != wantAuth {
				t.Errorf("Authorization = %q, want %q", auth, wantAuth)
			}
			proxmoxtest.JSONResponse(w, http.StatusOK, `[
				{"id":"qemu/1000","vmid":1000,"name":"web-01","node":"pve1","type":"qemu","status":"running","maxmem":2147483648,"maxdisk":10737418240,"maxcpu":2},
				{"id":"qemu/1001","vmid":1001,"name":"stray","node":"pve1","type":"qemu","status":"stopped","maxmem":1073741824,"maxdisk":5368709120,"maxcpu":1}
			]`)
		},
		"GET /api2/json/nodes/pve1/qemu/1000/config": func(w http.ResponseWriter, r *http.Request) {
			if auth := r.Header.Get("Authorization"); auth != wantAuth {
				t.Errorf("Authorization = %q, want %q", auth, wantAuth)
			}
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"digest":"d1","name":"web-01"}`)
		},
		"GET /api2/json/nodes/pve1/qemu/1001/config": func(w http.ResponseWriter, r *http.Request) {
			if auth := r.Header.Get("Authorization"); auth != wantAuth {
				t.Errorf("Authorization = %q, want %q", auth, wantAuth)
			}
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"digest":"d2","name":"stray"}`)
		},
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := fmt.Sprintf("%s %s", r.Method, r.URL.Path)
		h, ok := handlers[key]
		if !ok {
			t.Errorf("unexpected request: %s", key)
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	defer srv.Close()
	fingerprint := proxmox.FormatFingerprint(srv.Certificate())

	files := map[string]string{
		"nodr.yaml": manifest,
		"intent/platform/pve.yaml": fmt.Sprintf(`apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [%s]
  credentialsRef: proxmox/pve-main-token
  nodes: [pve1]
  tls:
    fingerprint: %s
`, srv.URL, fingerprint),
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
	if !strings.Contains(result.stdout, "1000") || !strings.Contains(result.stdout, "managed") {
		t.Errorf("stdout missing managed guest 1000: %s", result.stdout)
	}
	if !strings.Contains(result.stdout, "1001") || !strings.Contains(result.stdout, "discovered") {
		t.Errorf("stdout missing discovered guest 1001: %s", result.stdout)
	}
}
