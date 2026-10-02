package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/centopw/nodr/internal/bootstrap"
)

func fakeGit(t *testing.T, log string, fail bool) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake git requires a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(log) + "\n"
	if fail {
		script += "if [ \"$1\" = commit ]; then exit 1; fi\n"
	}
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestServerPartialBootstrapEnvironmentFailsBeforeListen(t *testing.T) {
	t.Setenv("NODR_BOOTSTRAP_WORKSPACE_NAME", "home")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), nil, []string{"server", "--data-dir", t.TempDir(), "--addr", "127.0.0.1:0"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitError {
		t.Fatalf("exit = %d, want %d", code, exitError)
	}
	for _, name := range []string{"NODR_BOOTSTRAP_ADMIN_USERNAME", "NODR_BOOTSTRAP_ADMIN_PASSWORD", "NODR_BOOTSTRAP_TOKEN"} {
		if !strings.Contains(stderr.String(), name) {
			t.Errorf("stderr = %q, want missing %s", stderr.String(), name)
		}
	}
	if strings.Contains(stderr.String(), "home") || strings.Contains(stdout.String(), "listening on") {
		t.Errorf("output exposed configured value or listened")
	}
}

func TestServerBrowserBootstrapWarnsForNonLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	code := Run(ctx, nil, []string{"server", "--data-dir", t.TempDir(), "--addr", "0.0.0.0:0"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "remotely reachable") {
		t.Errorf("stderr = %q, want remote exposure warning", stderr.String())
	}
}

func TestServerBootstrapHandlerCutsOverAfterSetup(t *testing.T) {
	dataDir := t.TempDir()
	oldGit := bootstrap.GitExecutable
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"), false)
	t.Cleanup(func() { bootstrap.GitExecutable = oldGit })
	kek, err := bootstrap.ResolveKEK(bootstrap.Config{DataDir: dataDir}, nil)
	if err != nil {
		t.Fatalf("ResolveKEK: %v", err)
	}
	switcher := &switchingHandler{}
	setup := &bootstrapSetup{service: bootstrap.Service{Config: bootstrap.Config{DataDir: dataDir}}, token: "bootstrap-token-1", onSuccess: func() error {
		state, err := bootstrap.LoadState(dataDir)
		if err != nil {
			return err
		}
		return switcher.swapNormal(context.Background(), filepath.Join(dataDir, state.Workspace), kek)
	}}
	switcher.handler = bootstrapHandler(setup)
	rec := httptest.NewRecorder()
	switcher.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	switcher.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("workspace route before setup = %d, want 404", rec.Code)
	}
	body := `{"token":"bootstrap-token-1","workspaceName":"home","username":"admin","password":"password12345"}`
	rec = httptest.NewRecorder()
	switcher.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/setup", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("setup = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "bootstrap-token-1") {
		t.Fatal("setup response reflected bootstrap token")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookie count = %d", len(cookies))
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	req.AddCookie(cookies[0])
	switcher.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session after cutover = %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	switcher.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/setup", strings.NewReader(body)))
	if rec.Code == http.StatusOK {
		t.Fatal("setup remained available after cutover")
	}
}
