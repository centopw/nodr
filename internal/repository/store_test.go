package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestCreateWorkspaceClonesLocalRepository(t *testing.T) {
	ctx := context.Background()
	origin, mainSHA := createOrigin(t)
	store, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}

	workspace, err := store.CreateWorkspace(ctx, CreateWorkspaceInput{
		Slug:                 "home",
		GitHubInstallationID: 42,
		Owner:                "acme",
		Repository:           origin,
		DefaultBranch:        "main",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if workspace.DefaultBranch != "main" || workspace.Slug != "home" {
		t.Fatalf("workspace = %+v", workspace)
	}
	if workspace.GitHubInstallationID != 42 {
		t.Fatalf("GitHubInstallationID = %d, want 42", workspace.GitHubInstallationID)
	}
	if workspace.Repository != origin {
		t.Fatalf("Repository = %q, want %q", workspace.Repository, origin)
	}
	stored, err := store.Workspace(ctx, "home")
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if stored.GitHubInstallationID != 42 {
		t.Fatalf("stored GitHubInstallationID = %d, want 42", stored.GitHubInstallationID)
	}
	if stored.Repository != origin {
		t.Fatalf("stored Repository = %q, want %q", stored.Repository, origin)
	}

	checkout, err := store.Checkout(ctx, "home", mainSHA)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	defer checkout.Close()
	if _, err := os.Stat(filepath.Join(checkout.Path(), "nodr.yaml")); err != nil {
		t.Fatalf("checkout missing cloned file: %v", err)
	}
}

func TestCheckoutCloseRemovesIsolatedTemporaryDirectory(t *testing.T) {
	ctx := context.Background()
	origin, mainSHA := createOrigin(t)
	store := createWorkspace(t, origin)

	first, err := store.Checkout(ctx, "home", mainSHA)
	if err != nil {
		t.Fatalf("first Checkout: %v", err)
	}
	second, err := store.Checkout(ctx, "home", mainSHA)
	if err != nil {
		t.Fatalf("second Checkout: %v", err)
	}
	if first.Path() == second.Path() {
		t.Fatal("checkouts share a directory")
	}
	if err := os.WriteFile(filepath.Join(first.Path(), "only-first"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(second.Path(), "only-first")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second checkout sees first's uncommitted write: %v", err)
	}
	path := first.Path()
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout directory remains after Close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestCommitPublishesOnlyThroughCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	origin, mainSHA := createOrigin(t)
	store := createWorkspace(t, origin)
	draft, err := store.CreateDraft(ctx, "home", mainSHA, "add VM", "Ada")
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	checkout, err := store.Checkout(ctx, "home", draft.HeadSHA)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	defer checkout.Close()

	commit, err := store.Commit(ctx, checkout, CommitInput{
		Message:     "nodr: Ada vm.create",
		Author:      "Ada",
		ExpectedSHA: draft.HeadSHA,
		Mutate: func(root string) error {
			if err := os.MkdirAll(filepath.Join(root, "intent"), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(root, "intent", "web-01.yaml"), []byte("kind: VirtualMachine\n"), 0o644); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(root, "terraform", "pve-main-compute"), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(root, "terraform", "pve-main-compute", "vms.tf"), []byte("# nodr:managed vm/web-01\n"), 0o644)
		},
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if commit.SHA == draft.HeadSHA {
		t.Fatal("Commit did not create a new SHA")
	}

	beforeCAS, err := store.Checkout(ctx, "home", draft.Ref)
	if err != nil {
		t.Fatalf("draft Checkout before CAS: %v", err)
	}
	defer beforeCAS.Close()
	if _, err := os.Stat(filepath.Join(beforeCAS.Path(), "intent", "web-01.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("draft ref changed before CompareAndSwap: %v", err)
	}
	if err := store.CompareAndSwap(ctx, "home", draft.Ref, draft.HeadSHA, commit.SHA); err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}

	result, err := store.Checkout(ctx, "home", draft.Ref)
	if err != nil {
		t.Fatalf("result Checkout: %v", err)
	}
	defer result.Close()
	if _, err := os.Stat(filepath.Join(result.Path(), "intent", "web-01.yaml")); err != nil {
		t.Fatalf("result checkout lacks committed mutation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.Path(), "terraform", "pve-main-compute", "vms.tf")); err != nil {
		t.Fatalf("result checkout lacks managed HCL mutation: %v", err)
	}
	workspace, err := store.Workspace(ctx, "home")
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if workspace.DefaultSHA != mainSHA {
		t.Fatalf("default branch changed to %s, want %s", workspace.DefaultSHA, mainSHA)
	}
}

func TestCompareAndSwapRejectsStaleRevision(t *testing.T) {
	ctx := context.Background()
	origin, mainSHA := createOrigin(t)
	store := createWorkspace(t, origin)

	if err := store.CompareAndSwap(ctx, "home", "refs/heads/main", "deadbeef", mainSHA); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("CompareAndSwap stale error = %v, want ErrStaleRevision", err)
	}
}

func TestCommitFailedMutationLeavesNoCommit(t *testing.T) {
	ctx := context.Background()
	origin, mainSHA := createOrigin(t)
	store := createWorkspace(t, origin)
	checkout, err := store.Checkout(ctx, "home", mainSHA)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	checkoutPath := checkout.Path()
	_, err = store.Commit(ctx, checkout, CommitInput{
		Message:     "nodr: Ada vm.create",
		Author:      "Ada",
		ExpectedSHA: mainSHA,
		Mutate: func(root string) error {
			if err := os.WriteFile(filepath.Join(root, "partial"), []byte("partial"), 0o644); err != nil {
				return err
			}
			return errors.New("mutation failed")
		},
	})
	if err == nil {
		t.Fatal("Commit succeeded after failed mutation")
	}
	if _, err := os.Stat(checkoutPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed mutation checkout remains: %v", err)
	}

	result, err := store.Checkout(ctx, "home", mainSHA)
	if err != nil {
		t.Fatalf("result Checkout: %v", err)
	}
	defer result.Close()
	if _, err := os.Stat(filepath.Join(result.Path(), "partial")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed mutation escaped checkout: %v", err)
	}
}

func TestCreateDraftLeavesDefaultBranchUnchanged(t *testing.T) {
	ctx := context.Background()
	origin, mainSHA := createOrigin(t)
	store := createWorkspace(t, origin)
	draft, err := store.CreateDraft(ctx, "home", mainSHA, "change", "Ada")
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if draft.HeadSHA != mainSHA {
		t.Fatalf("draft base = %s, want %s", draft.HeadSHA, mainSHA)
	}
	workspace, err := store.Workspace(ctx, "home")
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if workspace.DefaultSHA != mainSHA {
		t.Fatalf("default branch changed to %s, want %s", workspace.DefaultSHA, mainSHA)
	}
}

func createWorkspace(t *testing.T, origin string) *LocalStore {
	t.Helper()
	store, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorkspace(context.Background(), CreateWorkspaceInput{Slug: "home", Owner: "acme", Repository: origin, DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	return store
}

func createOrigin(t *testing.T) (string, string) {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	bare, err := git.PlainInit(origin, true)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "seed")
	seed, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "intent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "nodr.yaml"), []byte("apiVersion: nodr/v1alpha1\nkind: Workspace\nmetadata:\n  name: home\nspec: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, err := seed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	commit, err := wt.Commit("initial", &git.CommitOptions{Author: &object.Signature{Name: "Test", Email: "test@example.invalid"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{origin}}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), commit)); err != nil {
		t.Fatal(err)
	}
	if err := seed.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{"refs/heads/main:refs/heads/main"}}); err != nil {
		t.Fatal(err)
	}
	if err := bare.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))); err != nil {
		t.Fatal(err)
	}
	return origin, commit.String()
}
