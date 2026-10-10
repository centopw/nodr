package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
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

	"github.com/centopw/nodr/internal/authn"
	"github.com/centopw/nodr/internal/bootstrap"
)

func fakeGit(t *testing.T, log string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake git requires a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(log) + "\n"
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

// runServerUntilListening runs `nodr server` with args and cancels it once it
// reports that it is listening, so slow startup (Argon2, race detector) cannot
// race a fixed lifetime.
func runServerUntilListening(t *testing.T, args ...string) (code int, stdout string, stderr string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := newSynchronizedBuffer()
	var errBuf bytes.Buffer
	exit := make(chan int, 1)
	go func() {
		exit <- Run(ctx, nil, args, strings.NewReader(""), out, &errBuf)
	}()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case code := <-exit:
			return code, out.String(), errBuf.String()
		case <-out.written:
			if strings.Contains(out.String(), "listening on") {
				cancel()
			}
		case <-timeout:
			cancel()
			t.Fatalf("server did not start: stdout = %q", out.String())
		}
	}
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
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"))
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
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"))
	t.Cleanup(func() { bootstrap.GitExecutable = oldGit })
	setupBootstrap(t, dataDir, "recorded")

	code, stdoutText, stderrText := runServerUntilListening(t, "server", "--data-dir", dataDir, "--addr", "127.0.0.1:0")
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderrText)
	}
	if !strings.Contains(stdoutText, "listening on") {
		t.Fatalf("stdout = %q, want server startup", stdoutText)
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
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"))
	t.Cleanup(func() { bootstrap.GitExecutable = oldGit })
	t.Setenv("NODR_BOOTSTRAP_WORKSPACE_NAME", "unattended")
	t.Setenv("NODR_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("NODR_BOOTSTRAP_ADMIN_PASSWORD", strings.Repeat("p", 16))
	t.Setenv("NODR_BOOTSTRAP_TOKEN", strings.Repeat("t", 32))

	code, stdoutText, stderrText := runServerUntilListening(t, "server", "--data-dir", dataDir, "--addr", "127.0.0.1:0")
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderrText)
	}
	state, err := bootstrap.LoadState(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Workspace != filepath.Join("workspaces", "unattended") || !strings.Contains(stdoutText, "listening on") {
		t.Fatalf("state/stdout = %#v/%q", state, stdoutText)
	}
	if _, err := os.Stat(filepath.Join(dataDir, ".nodr", "authn.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("data root auth database = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, state.Workspace, ".nodr", "authn.db")); err != nil {
		t.Fatalf("workspace auth database = %v", err)
	}
}

func TestServerReloadsCompletedUnattendedBootstrapWithEnvironment(t *testing.T) {
	dataDir := t.TempDir()
	oldGit := bootstrap.GitExecutable
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"))
	t.Cleanup(func() { bootstrap.GitExecutable = oldGit })
	t.Setenv("NODR_BOOTSTRAP_WORKSPACE_NAME", "unattended")
	t.Setenv("NODR_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("NODR_BOOTSTRAP_ADMIN_PASSWORD", strings.Repeat("p", 16))
	t.Setenv("NODR_BOOTSTRAP_TOKEN", strings.Repeat("t", 32))

	for attempt := range 2 {
		code, stdoutText, stderrText := runServerUntilListening(t, "server", "--data-dir", dataDir, "--addr", "127.0.0.1:0")
		if code != exitOK {
			t.Fatalf("attempt %d exit = %d, stderr = %s", attempt+1, code, stderrText)
		}
		if !strings.Contains(stdoutText, "listening on") {
			t.Fatalf("attempt %d stdout = %q, want recorded server startup", attempt+1, stdoutText)
		}
	}

	state, err := bootstrap.LoadState(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, state.Workspace, ".nodr", "authn.db")); err != nil {
		t.Fatalf("workspace auth database = %v", err)
	}
}

func TestServerBrowserBootstrapInvalidKEKFailsBeforeListen(t *testing.T) {
	kekFile := filepath.Join(t.TempDir(), "invalid-kek")
	if err := os.WriteFile(kekFile, []byte("not a key"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), nil, []string{"server", "--data-dir", t.TempDir(), "--kek-file", kekFile, "--addr", addr}, strings.NewReader(""), &stdout, &stderr); code != exitError {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "listening on") {
		t.Fatalf("server listened before rejecting KEK: %q", stdout.String())
	}
	connection, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("listener remained reachable after KEK resolution failure")
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
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"))
	t.Cleanup(func() { bootstrap.GitExecutable = oldGit })
	token := strings.Repeat("t", 32)
	switcher := &switchingHandler{}
	setup := &bootstrapSetup{
		service: bootstrap.Service{Config: bootstrap.Config{DataDir: dataDir}, Prepare: switcher.prepareNormal(context.Background(), nil)},
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
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"))
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

	setup.service.Prepare = switcher.prepareNormal(context.Background(), nil)
	rec = httptest.NewRecorder()
	switcher.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/setup", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("retry setup = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestServerBootstrapHandlerSerializesConcurrentSetup(t *testing.T) {
	dataDir := t.TempDir()
	oldGit := bootstrap.GitExecutable
	bootstrap.GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"))
	t.Cleanup(func() { bootstrap.GitExecutable = oldGit })
	token := strings.Repeat("t", 32)
	// entered is buffered so the barrier signal is never dropped when the
	// setup goroutine arrives before the test starts waiting.
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	service := bootstrap.Service{
		Config: bootstrap.Config{DataDir: dataDir},
		SetupAuth: func(ctx context.Context, store *authn.Store, request bootstrap.Request) (string, string, time.Time, error) {
			entered <- struct{}{}
			<-release
			return store.Setup(ctx, request.Token, request.Username, request.Password)
		},
	}
	handler := bootstrapHandler(&bootstrapSetup{service: service, token: token})
	post := func(workspace string) int {
		body := fmt.Sprintf(`{"token":%q,"workspaceName":%q,"username":"admin","password":%q}`, token, workspace, strings.Repeat("p", 16))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/setup", strings.NewReader(body)))
		return rec.Code
	}
	first := make(chan int, 1)
	go func() { first <- post("first") }()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("first setup did not reach authorization barrier")
	}
	second := make(chan int, 1)
	go func() { second <- post("second") }()
	select {
	case status := <-second:
		t.Fatalf("concurrent setup completed before first setup: %d", status)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if status := <-first; status != http.StatusOK {
		t.Fatalf("first setup = %d", status)
	}
	if status := <-second; status/100 == 2 {
		t.Fatalf("second setup = %d, want non-2xx", status)
	}
	state, err := bootstrap.LoadState(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Workspace != filepath.Join("workspaces", "first") {
		t.Fatalf("workspace = %q", state.Workspace)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "workspaces", "second")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second workspace = %v, want not exist", err)
	}
}
