package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
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

type synchronizedBuffer struct {
	mu      sync.Mutex
	Buffer  bytes.Buffer
	written chan struct{}
}

func newSynchronizedBuffer() *synchronizedBuffer {
	return &synchronizedBuffer{written: make(chan struct{}, 1)}
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.Buffer.Write(p)
	b.mu.Unlock()
	select {
	case b.written <- struct{}{}:
	default:
	}
	return n, err
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
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

func TestServerBrowserBootstrapAcceptsGeneratedToken(t *testing.T) {
	dataDir := t.TempDir()
	oldGit := bootstrap.GitExecutable
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"), false)
	t.Cleanup(func() { bootstrap.GitExecutable = oldGit })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := newSynchronizedBuffer()
	var stderr bytes.Buffer
	exit := make(chan int, 1)
	go func() {
		exit <- Run(ctx, nil, []string{"server", "--data-dir", dataDir, "--addr", "127.0.0.1:0"}, strings.NewReader(""), stdout, &stderr)
	}()

	var token, addr string
	deadline := time.After(time.Second)
	for token == "" || addr == "" {
		select {
		case <-stdout.written:
			output := stdout.String()
			if fields := strings.Fields(output); len(fields) >= 5 && strings.Contains(output, "generated bootstrap token") {
				token = strings.TrimSuffix(fields[4], ";")
			}
			if index := strings.LastIndex(output, "listening on "); index >= 0 {
				addr = strings.TrimSpace(output[index+len("listening on "):])
			}
		case <-deadline:
			t.Fatalf("server output = %q, stderr = %q", stdout.String(), stderr.String())
		}
	}

	body := fmt.Sprintf(`{"token":%q,"workspaceName":"home","username":"admin","password":%q}`, token, strings.Repeat("p", 16))
	response, err := http.Post("http://"+addr+"/api/v1/setup", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("setup status = %d", response.StatusCode)
	}
	if strings.Contains(response.Request.URL.String(), token) || strings.Contains(string(responseBody), token) {
		t.Fatal("setup response reflected token")
	}
	if response.Header.Get("Set-Cookie") == "" {
		t.Fatal("setup did not reach coordinator")
	}

	cancel()
	if code := <-exit; code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
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
	token := strings.Repeat("t", 32)
	switcher := &switchingHandler{}
	setup := &bootstrapSetup{
		service: bootstrap.Service{Config: bootstrap.Config{DataDir: dataDir}, Prepare: switcher.prepareNormal(context.Background())},
		token:   token,
	}
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

func TestServerBootstrapHandlerPreparationFailureDoesNotCommitOrSetCookie(t *testing.T) {
	dataDir := t.TempDir()
	oldGit := bootstrap.GitExecutable
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"), false)
	t.Cleanup(func() { bootstrap.GitExecutable = oldGit })
	token := strings.Repeat("t", 32)
	prepareErr := errors.New("cannot prepare normal handler")
	service := bootstrap.Service{
		Config: bootstrap.Config{DataDir: dataDir},
		Prepare: func(context.Context, string, []byte) (func(), func(), error) {
			return nil, nil, prepareErr
		},
	}
	switcher := &switchingHandler{}
	setup := &bootstrapSetup{service: service, token: token}
	switcher.handler = bootstrapHandler(setup)
	body := fmt.Sprintf(`{"token":%q,"workspaceName":"home","username":"admin","password":%q}`, token, strings.Repeat("p", 16))

	rec := httptest.NewRecorder()
	switcher.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/setup", strings.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("setup = %d, body = %s", rec.Code, rec.Body.String())
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("cookies = %#v, want none", cookies)
	}
	if _, err := bootstrap.LoadState(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap state = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "workspaces", "home")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed attempt workspace remains: %v", err)
	}

	setup.service.Prepare = switcher.prepareNormal(context.Background())
	rec = httptest.NewRecorder()
	switcher.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/setup", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("retry setup = %d, body = %s", rec.Code, rec.Body.String())
	}
}
