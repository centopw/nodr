package cli

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

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
