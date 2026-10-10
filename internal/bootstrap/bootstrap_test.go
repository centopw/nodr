package bootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestDataDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	if got, want := (Config{}).DataRoot(), filepath.Join(os.Getenv("XDG_STATE_HOME"), "nodr"); got != want {
		t.Fatalf("DataRoot() = %q, want %q", got, want)
	}
	t.Setenv("XDG_STATE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := (Config{}).DataRoot(), filepath.Join(home, ".local", "state", "nodr"); got != want {
		t.Fatalf("DataRoot() = %q, want %q", got, want)
	}
}

func TestStateRoundTripAndMode(t *testing.T) {
	dataDir := t.TempDir()
	want := State{Version: 1, Workspace: "workspaces/home"}
	if err := WriteState(dataDir, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("LoadState() = %#v, want %#v", got, want)
	}
	info, err := os.Stat(filepath.Join(dataDir, "bootstrap.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
}

func TestStateRejectsUnsafeWorkspace(t *testing.T) {
	for _, workspace := range []string{"/tmp/workspace", "workspaces/../escape", "../workspaces/home", "workspace/home", "workspaces/home/", "workspaces/home/nested"} {
		t.Run(workspace, func(t *testing.T) {
			dataDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dataDir, "bootstrap.json"), []byte(`{"version":1,"workspace":"`+workspace+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadState(dataDir); err == nil {
				t.Fatal("LoadState() succeeded for unsafe workspace")
			}
		})
	}
}

func TestWorkspaceTargetRejectsExistingAndSymlink(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "workspaces", "home"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := WorkspaceTarget(dataDir, "home"); err == nil {
		t.Fatal("WorkspaceTarget() accepted existing target")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(dataDir, "workspaces", "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := WorkspaceTarget(dataDir, "linked"); err == nil {
		t.Fatal("WorkspaceTarget() accepted symlink target")
	}
}

func TestResolveKEK(t *testing.T) {
	dataDir := t.TempDir()
	fileKey := bytes.Repeat([]byte{1}, 32)
	keyFile := filepath.Join(dataDir, "configured-kek")
	if err := os.WriteFile(keyFile, fileKey, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODR_TEST_KEK", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	key, err := ResolveKEK(Config{DataDir: dataDir, KEKFile: keyFile, KEKEnv: "NODR_TEST_KEK"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, fileKey) {
		t.Fatal("configured KEK file did not take precedence")
	}
	key, err = ResolveKEK(Config{DataDir: dataDir, KEKEnv: "NODR_TEST_KEK"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, bytes.Repeat([]byte{2}, 32)) {
		t.Fatal("environment KEK was not loaded")
	}
	var warning bytes.Buffer
	key, err = ResolveKEK(Config{DataDir: dataDir}, &warning)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Fatalf("generated KEK length = %d, want 32", len(key))
	}
	if warning.Len() == 0 || bytes.Contains(warning.Bytes(), key) {
		t.Fatal("generated KEK warning missing or leaked key")
	}
	info, err := os.Stat(filepath.Join(dataDir, "kek"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
	reused, err := ResolveKEK(Config{DataDir: dataDir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reused, key) {
		t.Fatal("generated KEK was not reused")
	}
}

func TestResolveKEKReusesGeneratedKeyAfterBootstrapFailure(t *testing.T) {
	dataDir := t.TempDir()
	first, err := ResolveKEK(Config{DataDir: dataDir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dataDir, "kek")
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, rollback, err := CreateWorkspace(context.Background(), dataDir, "bad/name"); err == nil || rollback != nil {
		t.Fatal("simulated bootstrap workspace creation failure did not fail")
	}
	retry, err := ResolveKEK(Config{DataDir: dataDir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, retry) || !bytes.Equal(before, after) {
		t.Fatal("retry did not reuse the durable generated KEK")
	}
}

func TestResolveKEKRejectsMalformedConfiguredFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad-kek")
	if err := os.WriteFile(path, []byte("short"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveKEK(Config{KEKFile: path}, nil); err == nil {
		t.Fatal("ResolveKEK accepted malformed configured file")
	}
}
