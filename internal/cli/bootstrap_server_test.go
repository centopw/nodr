package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
			t.Errorf("stderr missing %s", name)
		}
	}
	if strings.Contains(stderr.String(), "home") || strings.Contains(stdout.String(), "listening on") {
		t.Error("output exposed configured value or listened")
	}
}

func TestServerBrowserBootstrapWarnsForNonLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, nil, []string{"server", "--data-dir", t.TempDir(), "--addr", "0.0.0.0:0"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "remotely reachable") {
		t.Errorf("stderr = %q, want remote warning", stderr.String())
	}
	if !strings.Contains(stdout.String(), "generated bootstrap token") {
		t.Error("generated token was not printed")
	}
}

func TestServerReloadsRecordedWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	oldGit := bootstrap.GitExecutable
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"), false)
	t.Cleanup(func() { bootstrap.GitExecutable = oldGit })
	setupBootstrap(t, dataDir, "recorded")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, nil, []string{"server", "--data-dir", dataDir, "--addr", "127.0.0.1:0"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "listening on") {
		t.Fatalf("stdout = %q, want server startup", stdout.String())
	}
}

func TestServerRejectsIncompleteWorkspaceDirectory(t *testing.T) {
	incomplete := t.TempDir()
	if err := os.MkdirAll(filepath.Join(incomplete, "workspaces", "stuck"), 0700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), nil, []string{"server", "--data-dir", incomplete, "--addr", "127.0.0.1:0"}, strings.NewReader(""), &stdout, &stderr); code != exitError || !strings.Contains(stderr.String(), "incomplete workspace directory") {
		t.Fatalf("incomplete exit/output = %d/%q", code, stderr.String())
	}
}

func TestServerCompletesUnattendedBootstrap(t *testing.T) {
	dataDir := t.TempDir()
	oldGit := bootstrap.GitExecutable
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"), false)
	t.Cleanup(func() { bootstrap.GitExecutable = oldGit })
	t.Setenv("NODR_BOOTSTRAP_WORKSPACE_NAME", "unattended")
	t.Setenv("NODR_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("NODR_BOOTSTRAP_ADMIN_PASSWORD", strings.Repeat("p", 16))
	t.Setenv("NODR_BOOTSTRAP_TOKEN", strings.Repeat("t", 32))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, nil, []string{"server", "--data-dir", dataDir, "--addr", "127.0.0.1:0"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	state, err := bootstrap.LoadState(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Workspace != filepath.Join("workspaces", "unattended") || !strings.Contains(stdout.String(), "listening on") {
		t.Fatalf("state/stdout = %#v/%q", state, stdout.String())
	}
}

func setupBootstrap(t *testing.T, dataDir, workspace string) {
	t.Helper()
	_, _, _, err := (bootstrap.Service{Config: bootstrap.Config{DataDir: dataDir}}).Setup(context.Background(), bootstrap.Request{
		Token: strings.Repeat("t", 32), WorkspaceName: workspace, Username: "admin", Password: strings.Repeat("p", 16),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestServerBootstrapHandlerCutsOverAfterSetup(t *testing.T) {
	dataDir := t.TempDir()
	oldGit := bootstrap.GitExecutable
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"), false)
	t.Cleanup(func() { bootstrap.GitExecutable = oldGit })
	kek, err := bootstrap.ResolveKEK(bootstrap.Config{DataDir: dataDir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("t", 32)
	switcher := &switchingHandler{}
	setup := &bootstrapSetup{service: bootstrap.Service{Config: bootstrap.Config{DataDir: dataDir}}, token: token, onSuccess: func() error {
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
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	switcher.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatal(rec.Code)
	}
	body := fmt.Sprintf(`{"token":%q,"workspaceName":"home","username":"admin","password":%q}`, token, strings.Repeat("p", 16))
	rec = httptest.NewRecorder()
	switcher.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/setup", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("setup = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), token) {
		t.Fatal("token reflected")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	req.AddCookie(cookies[0])
	switcher.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	switcher.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/setup", strings.NewReader(body)))
	if rec.Code == http.StatusOK {
		t.Fatal("setup remained available")
	}
}

func TestServerBootstrapHandlerDoesNotReportSuccessBeforeCutover(t *testing.T) {
	dataDir := t.TempDir()
	oldGit := bootstrap.GitExecutable
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"), false)
	t.Cleanup(func() { bootstrap.GitExecutable = oldGit })
	token := strings.Repeat("t", 32)
	switcher := &switchingHandler{}
	setup := &bootstrapSetup{
		service: bootstrap.Service{Config: bootstrap.Config{DataDir: dataDir}},
		token:   token,
		onSuccess: func() error {
			return errors.New("cannot install normal handler")
		},
	}
	switcher.handler = bootstrapHandler(setup)
	body := fmt.Sprintf(`{"token":%q,"workspaceName":"home","username":"admin","password":%q}`, token, strings.Repeat("p", 16))
	rec := httptest.NewRecorder()
	switcher.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/setup", strings.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("setup = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	switcher.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("handler changed after failed cutover: %d", rec.Code)
	}
}
