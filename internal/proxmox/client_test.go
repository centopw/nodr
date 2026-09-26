package proxmox_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/proxmox/proxmoxtest"
)

func TestClient_Login_Success(t *testing.T) {
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"POST /api2/json/access/ticket": func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			if r.FormValue("username") != "root@pam" || r.FormValue("password") != "secret" {
				proxmoxtest.ErrorResponse(w, http.StatusUnauthorized, "login failed")
				return
			}
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"ticket":"TICKET:123","CSRFPreventionToken":"CSRF:456"}`)
		},
		"GET /api2/json/version": func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("PVEAuthCookie")
			if err != nil || cookie.Value != "TICKET:123" {
				t.Errorf("cookie = %v, want TICKET:123", cookie)
			}
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"version":"8.2"}`)
		},
	})
	defer srv.Close()

	c := proxmox.NewClient(srv.URL, nil)
	ctx := context.Background()
	if err := c.Login(ctx, "pam", "root", "secret"); err != nil {
		t.Fatalf("Login: %v", err)
	}

	var v proxmox.Version
	if err := c.Get(ctx, "/version", &v); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestClient_APIToken_Header(t *testing.T) {
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"GET /api2/json/version": func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			want := "PVEAPIToken=root@pam!token1=abc-def"
			if auth != want {
				t.Errorf("Authorization = %q, want %q", auth, want)
			}
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"version":"8.2"}`)
		},
	})
	defer srv.Close()

	c := proxmox.NewClient(srv.URL, nil)
	c.SetAPIToken("root@pam", "token1", "abc-def")

	var v proxmox.Version
	if err := c.Get(context.Background(), "/version", &v); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestClient_RedactsCredentialsInErrors(t *testing.T) {
	tokenSecret := "super-secret-token-uuid"
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"GET /api2/json/fail": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"invalid request containing super-secret-token-uuid"}`))
		},
	})
	defer srv.Close()

	c := proxmox.NewClient(srv.URL, nil)
	c.SetAPIToken("root@pam", "token1", tokenSecret)

	err := c.Get(context.Background(), "/fail", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if strings.Contains(err.Error(), tokenSecret) {
		t.Fatalf("error leaked secret: %v", err)
	}
}
