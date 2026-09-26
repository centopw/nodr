package proxmoxtest_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/centopw/nodr/internal/proxmox/proxmoxtest"
)

func TestNewServer_MatchedRoute(t *testing.T) {
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"GET /api2/json/version": func(w http.ResponseWriter, _ *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"version":"8.2.4"}`)
		},
	})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api2/json/version")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, _ := io.ReadAll(resp.Body)
	want := `{"data":{"version":"8.2.4"}}`
	if string(body) != want {
		t.Errorf("body = %q, want %q", string(body), want)
	}
}

func TestNewServer_UnmatchedRoute(t *testing.T) {
	fakeT := &testing.T{}
	srv := proxmoxtest.NewServer(fakeT, map[string]http.HandlerFunc{})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api2/json/unknown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotImplemented)
	}
}
