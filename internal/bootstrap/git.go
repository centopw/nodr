package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/workspace"
)

// GitExecutable is the Git binary used by new GitRunner values. Tests may replace it.
var GitExecutable = "git"

// GitRunner runs the fixed commands required to initialize a workspace ledger.
type GitRunner struct{ Executable string }

func (g GitRunner) executable() string {
	if g.Executable != "" {
		return g.Executable
	}
	return GitExecutable
}

func (g GitRunner) run(ctx context.Context, root string, args ...string) error {
	cmd := exec.CommandContext(ctx, g.executable(), args...)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bootstrap: git %v: %w: %s", args, err, output)
	}
	return nil
}

// Init initializes the repository at root.
func (g GitRunner) Init(ctx context.Context, root string) error { return g.run(ctx, root, "init") }

func (g GitRunner) Add(ctx context.Context, root string) error {
	return g.run(ctx, root, "add", "nodr.yaml", "intent")
}

func (g GitRunner) Commit(ctx context.Context, root string) error {
	return g.run(ctx, root, "commit", "-m", "chore: initialize nodr workspace")
}

// CreateWorkspace creates, validates, and commits a newly derived workspace target.
func CreateWorkspace(ctx context.Context, dataDir, name string) (root string, rollback func(), err error) {
	target, err := WorkspaceTarget(dataDir, name)
	if err != nil {
		return "", nil, err
	}
	if err := os.Mkdir(target, 0700); err != nil {
		return "", nil, fmt.Errorf("bootstrap: create workspace: %w", err)
	}
	created := true
	rollback = func() {
		if created {
			_ = os.RemoveAll(target)
		}
	}
	fail := func(err error) (string, func(), error) { rollback(); return "", nil, err }
	if err := os.Mkdir(filepath.Join(target, workspace.IntentDir), 0700); err != nil {
		return fail(fmt.Errorf("bootstrap: create intent directory: %w", err))
	}
	manifest := fmt.Sprintf("apiVersion: nodr/v1alpha1\nkind: Workspace\nmetadata:\n  name: %s\nspec: {}\n", name)
	if err := os.WriteFile(filepath.Join(target, workspace.ManifestFile), []byte(manifest), 0600); err != nil {
		return fail(fmt.Errorf("bootstrap: write manifest: %w", err))
	}
	loaded, diags := workspace.Load(target)
	if diags.HasErrors() {
		return fail(fmt.Errorf("bootstrap: load manifest: %w", diags.Err()))
	}
	registry, err := v1alpha1.NewRegistry()
	if err != nil {
		return fail(fmt.Errorf("bootstrap: create NRM registry: %w", err))
	}
	if diags := loaded.Validate(registry); diags.HasErrors() {
		return fail(fmt.Errorf("bootstrap: validate manifest: %w", diags.Err()))
	}
	git := GitRunner{}
	if err := git.Init(ctx, target); err != nil {
		return fail(err)
	}
	if err := git.Add(ctx, target); err != nil {
		return fail(err)
	}
	if err := git.Commit(ctx, target); err != nil {
		return fail(err)
	}
	return target, rollback, nil
}
