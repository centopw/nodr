package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/workspace"
)

func TestCreateWorkspaceInitializesGitAndValidManifest(t *testing.T) {
	log := filepath.Join(t.TempDir(), "git.log")
	git := fakeGit(t, log, false)
	old := GitExecutable
	GitExecutable = git
	t.Cleanup(func() { GitExecutable = old })
	root, rollback, err := CreateWorkspace(context.Background(), t.TempDir(), "home")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rollback)
	if _, err := os.Stat(filepath.Join(root, "intent")); err != nil {
		t.Fatal(err)
	}
	w, diags := workspace.Load(root)
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	r, err := v1alpha1.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if diags := w.Validate(r); diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Split(strings.TrimSpace(string(data)), "\n"), []string{"init", "add nodr.yaml intent", "commit -m chore: initialize nodr workspace"}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("git calls = %q, want %q", got, want)
	}
}

func TestCreateWorkspaceGitFailureRollsBack(t *testing.T) {
	dataDir := t.TempDir()
	old := GitExecutable
	GitExecutable = fakeGit(t, filepath.Join(t.TempDir(), "git.log"), true)
	t.Cleanup(func() { GitExecutable = old })
	root, rollback, err := CreateWorkspace(context.Background(), dataDir, "home")
	if err == nil {
		t.Fatal("CreateWorkspace() succeeded despite failed Git commit")
	}
	if root != "" || rollback != nil {
		t.Fatal("CreateWorkspace() returned a workspace after Git failure")
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "workspaces", "home")); !os.IsNotExist(statErr) {
		t.Fatalf("failed workspace remains: %v", statErr)
	}
}

func TestGitRunnerMissingExecutable(t *testing.T) {
	if err := (GitRunner{Executable: filepath.Join(t.TempDir(), "missing")}).Init(context.Background(), t.TempDir()); err == nil {
		t.Fatal("Init() succeeded with missing executable")
	}
}

func fakeGit(t *testing.T, log string, failCommit bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(log) + "\n"
	if failCommit {
		script += "if [ \"$1\" = commit ]; then exit 1; fi\n"
	}
	script += "exit 0\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
