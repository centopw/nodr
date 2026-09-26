package proxmoxbootstrap_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/proxmox/proxmoxtest"
	"github.com/centopw/nodr/internal/proxmoxbootstrap"
)

func TestBootstrap_Sequence(t *testing.T) {
	var calls []string
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"POST /api2/json/access/users": func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, "CreateUser")
			proxmoxtest.JSONResponse(w, http.StatusOK, "")
		},
		"POST /api2/json/access/roles": func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, "CreateRole")
			proxmoxtest.JSONResponse(w, http.StatusOK, "")
		},
		"PUT /api2/json/access/acl": func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, "UpdateACL")
			proxmoxtest.JSONResponse(w, http.StatusOK, "")
		},
		"POST /api2/json/access/users/nodr@pve/token/nodr": func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, "CreateAPIToken")
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"value":"bootstrap-secret-abc"}`)
		},
	})
	defer srv.Close()

	client := proxmox.NewClient(srv.URL, nil)
	client.SetAPIToken("root@pam", "admin-token", "admin-secret")

	result, err := proxmoxbootstrap.Bootstrap(context.Background(), client, []string{"VM.Audit"})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if result.TokenSecret != "bootstrap-secret-abc" {
		t.Errorf("TokenSecret = %q", result.TokenSecret)
	}
	if result.User != "nodr@pve" || result.Role != "NodrOperator" || result.TokenID != "nodr" {
		t.Errorf("result = %+v", result)
	}
	want := []string{"CreateUser", "CreateRole", "UpdateACL", "CreateAPIToken"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("calls[%d] = %q, want %q", i, calls[i], want[i])
		}
	}
}
