package proxmox_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/proxmox/proxmoxtest"
)

func TestEndpoints(t *testing.T) {
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"GET /api2/json/version": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"release":"8.2","repoid":"1","version":"8.2.4"}`)
		},
		"GET /api2/json/cluster/status": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `[{"id":"node/pve1","name":"pve1","type":"node","online":1}]`)
		},
		"GET /api2/json/cluster/resources": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("type") != "vm" {
				t.Errorf("query type = %s, want vm", r.URL.Query().Get("type"))
			}
			proxmoxtest.JSONResponse(w, http.StatusOK, `[{"id":"qemu/100","vmid":100,"name":"test-vm","node":"pve1","type":"qemu","status":"running"}]`)
		},
		"GET /api2/json/nodes/pve1/qemu": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `[{"vmid":100,"name":"test-vm","status":"running","cpus":2}]`)
		},
		"GET /api2/json/nodes/pve1/qemu/100/config": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"digest":"abc123sha1","cores":2,"memory":2048,"name":"test-vm"}`)
		},
		"GET /api2/json/nodes/pve1/certificates/info": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `[{"fingerprint":"AA:BB:CC:DD"}]`)
		},
		"POST /api2/json/access/users": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, "")
		},
		"POST /api2/json/access/roles": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, "")
		},
		"PUT /api2/json/access/acl": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, "")
		},
		"POST /api2/json/access/users/nodr@pve/token/nodr-token": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"value":"token-secret-uuid-1234"}`)
		},
	})
	defer srv.Close()

	c := proxmox.NewClient(srv.URL, nil)
	ctx := context.Background()

	// 1. GetVersion
	ver, err := c.GetVersion(ctx)
	if err != nil || ver.Version != "8.2.4" {
		t.Fatalf("GetVersion: %v, %v", ver, err)
	}

	// 2. GetClusterStatus
	status, err := c.GetClusterStatus(ctx)
	if err != nil || len(status) != 1 || status[0].Name != "pve1" {
		t.Fatalf("GetClusterStatus: %v, %v", status, err)
	}

	// 3. GetClusterResources
	res, err := c.GetClusterResources(ctx, "vm")
	if err != nil || len(res) != 1 || res[0].VMID != 100 {
		t.Fatalf("GetClusterResources: %v, %v", res, err)
	}

	// 4. GetNodeQEMU
	qemu, err := c.GetNodeQEMU(ctx, "pve1")
	if err != nil || len(qemu) != 1 || qemu[0].VMID != 100 {
		t.Fatalf("GetNodeQEMU: %v, %v", qemu, err)
	}

	// 5. GetQEMUConfig
	cfg, digest, err := c.GetQEMUConfig(ctx, "pve1", 100)
	if err != nil || digest != "abc123sha1" || cfg.Digest != "abc123sha1" {
		t.Fatalf("GetQEMUConfig: %v, %s, %v", cfg, digest, err)
	}

	// 6. GetCertFingerprint
	fp, err := c.GetCertFingerprint(ctx, "pve1")
	if err != nil || fp != "AA:BB:CC:DD" {
		t.Fatalf("GetCertFingerprint: %v, %v", fp, err)
	}

	// 7. CreateUser
	if err := c.CreateUser(ctx, "nodr@pve", proxmox.UserOptions{Comment: "nodr"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// 8. CreateRole
	if err := c.CreateRole(ctx, "NodrOperator", []string{"VM.Audit", "VM.Config.Disk"}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	// 9. UpdateACL
	if err := c.UpdateACL(ctx, "/", map[string][]string{"nodr@pve": {"NodrOperator"}}); err != nil {
		t.Fatalf("UpdateACL: %v", err)
	}

	// 10. CreateAPIToken
	secret, err := c.CreateAPIToken(ctx, "nodr@pve", "nodr-token")
	if err != nil || secret != "token-secret-uuid-1234" {
		t.Fatalf("CreateAPIToken: %s, %v", secret, err)
	}
}
