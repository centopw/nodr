package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/centopw/nodr/internal/authn"
)

func TestServiceSetupCreatesWorkspaceAuthAndState(t *testing.T) {
	dataDir := t.TempDir()
	service := Service{Config: Config{DataDir: dataDir}}

	session, csrf, _, err := service.Setup(context.Background(), Request{
		Token: "bootstrap-token-1", WorkspaceName: "home", Username: "admin", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if session == "" || csrf == "" {
		t.Fatal("Setup returned an empty session or CSRF token")
	}
	state, err := LoadState(dataDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state != (State{Version: 1, Workspace: "workspaces/home"}) {
		t.Fatalf("state = %#v", state)
	}
	root := filepath.Join(dataDir, state.Workspace)
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatalf("initial Git repository: %v", err)
	}
	authStore, err := authn.Open(filepath.Join(root, ".nodr", "authn.db"))
	if err != nil {
		t.Fatalf("Open attempt auth store: %v", err)
	}
	t.Cleanup(func() { authStore.Close() })
	if !authStore.ValidateSession(context.Background(), session) {
		t.Error("session was not persisted in attempt-local auth store")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "authn.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("data-root auth DB exists or stat failed: %v", err)
	}
}

func TestServiceSetupStateWriteFailureRemovesAttemptAndAllowsRetry(t *testing.T) {
	dataDir := t.TempDir()
	stateErr := errors.New("state write failed")
	service := Service{
		Config:     Config{DataDir: dataDir},
		WriteState: func(string, State) error { return stateErr },
	}
	req := Request{Token: "bootstrap-token-1", WorkspaceName: "home", Username: "admin", Password: "password12345"}

	if _, _, _, err := service.Setup(context.Background(), req); !errors.Is(err, stateErr) {
		t.Fatalf("Setup error = %v, want state write error", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "workspaces", "home")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed attempt workspace remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "bootstrap.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap state remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "kek")); err != nil {
		t.Fatalf("generated KEK not preserved: %v", err)
	}

	service.WriteState = nil
	if _, _, _, err := service.Setup(context.Background(), req); err != nil {
		t.Fatalf("retry Setup: %v", err)
	}
}

func TestServiceSetupInvalidNameAndAuthFailureRemoveOnlyAttempt(t *testing.T) {
	dataDir := t.TempDir()
	existing := filepath.Join(dataDir, "workspaces", "existing")
	if err := os.MkdirAll(existing, 0700); err != nil {
		t.Fatal(err)
	}
	service := Service{Config: Config{DataDir: dataDir}}

	if _, _, _, err := service.Setup(context.Background(), Request{Token: "token", WorkspaceName: "bad/name", Username: "admin", Password: "password12345"}); err == nil {
		t.Fatal("Setup accepted invalid workspace name")
	}
	if _, err := os.Stat(existing); err != nil {
		t.Fatalf("existing workspace was removed: %v", err)
	}
	if _, _, _, err := service.Setup(context.Background(), Request{Token: "wrong", WorkspaceName: "home", Username: "admin", Password: "password12345"}); err != nil {
		t.Fatalf("Setup with initial token: %v", err)
	}
}

func TestServiceSetupGitFailureRemovesAttemptOnly(t *testing.T) {
	dataDir := t.TempDir()
	existing := filepath.Join(dataDir, "workspaces", "existing")
	if err := os.MkdirAll(existing, 0700); err != nil {
		t.Fatal(err)
	}
	old := GitExecutable
	GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"), true)
	t.Cleanup(func() { GitExecutable = old })
	service := Service{Config: Config{DataDir: dataDir}}
	if _, _, _, err := service.Setup(context.Background(), Request{Token: "bootstrap-token-1", WorkspaceName: "home", Username: "admin", Password: "password12345"}); err == nil {
		t.Fatal("Setup succeeded with Git failure")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "workspaces", "home")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed attempt workspace remains: %v", err)
	}
	if _, err := os.Stat(existing); err != nil {
		t.Fatalf("existing workspace was removed: %v", err)
	}
}

func TestServiceSetupAuthFailureRemovesAttemptOnly(t *testing.T) {
	dataDir := t.TempDir()
	existing := filepath.Join(dataDir, "workspaces", "existing")
	if err := os.MkdirAll(existing, 0700); err != nil {
		t.Fatal(err)
	}
	authErr := errors.New("auth setup failed")
	service := Service{
		Config: Config{DataDir: dataDir},
		SetupAuth: func(context.Context, *authn.Store, Request) (string, string, time.Time, error) {
			return "", "", time.Time{}, authErr
		},
	}
	if _, _, _, err := service.Setup(context.Background(), Request{Token: "bootstrap-token-1", WorkspaceName: "home", Username: "admin", Password: "password12345"}); !errors.Is(err, authErr) {
		t.Fatalf("Setup error = %v, want auth setup error", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "workspaces", "home")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed attempt workspace remains: %v", err)
	}
	if _, err := os.Stat(existing); err != nil {
		t.Fatalf("existing workspace was removed: %v", err)
	}
}
