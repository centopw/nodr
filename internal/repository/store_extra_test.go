package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// draftCommit creates a draft at base, commits mutate on it, and publishes it with CompareAndSwap.
// It returns the draft ref and the new commit SHA.
func draftCommit(t *testing.T, store *LocalStore, base string, mutate func(root string) error) (string, string) {
	t.Helper()
	ctx := context.Background()
	draft, err := store.CreateDraft(ctx, "home", base, "change", "Ada")
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	checkout, err := store.Checkout(ctx, "home", base)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	commit, err := store.Commit(ctx, checkout, CommitInput{Message: "nodr: Ada change", Author: "Ada", ExpectedSHA: base, Mutate: mutate})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := store.CompareAndSwap(ctx, "home", draft.Ref, base, commit.SHA); err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	return draft.Ref, commit.SHA
}

func writeFileMutation(name, content string) func(root string) error {
	return func(root string) error {
		return os.WriteFile(filepath.Join(root, name), []byte(content), 0o644)
	}
}

func originRefHash(t *testing.T, origin, ref string) (string, bool) {
	t.Helper()
	bare, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatalf("open origin: %v", err)
	}
	reference, err := bare.Reference(plumbing.ReferenceName(ref), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return "", false
	}
	if err != nil {
		t.Fatalf("origin reference %s: %v", ref, err)
	}
	return reference.Hash().String(), true
}

func mirrorRefHash(t *testing.T, store *LocalStore, ref string) string {
	t.Helper()
	hash, err := store.Ref(context.Background(), "home", ref)
	if err != nil {
		t.Fatalf("Ref(%s): %v", ref, err)
	}
	return hash
}

func TestCompareAndSwapPushesDraftToOrigin(t *testing.T) {
	origin, mainSHA := createOrigin(t)
	store := createWorkspace(t, origin)

	ref, sha := draftCommit(t, store, mainSHA, writeFileMutation("intent.yaml", "kind: VirtualMachine\n"))

	got, ok := originRefHash(t, origin, ref)
	if !ok {
		t.Fatalf("origin lacks draft ref %s", ref)
	}
	if got != sha {
		t.Fatalf("origin draft = %s, want %s", got, sha)
	}
}

func TestCompareAndSwapStaleLeavesRefAndOriginUnchanged(t *testing.T) {
	ctx := context.Background()
	origin, mainSHA := createOrigin(t)
	store := createWorkspace(t, origin)
	draft, err := store.CreateDraft(ctx, "home", mainSHA, "change", "Ada")
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	checkout, err := store.Checkout(ctx, "home", mainSHA)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	commit, err := store.Commit(ctx, checkout, CommitInput{Message: "nodr: change", Author: "Ada", ExpectedSHA: mainSHA, Mutate: writeFileMutation("x.yaml", "x\n")})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := store.CompareAndSwap(ctx, "home", draft.Ref, "deadbeef", commit.SHA); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("CompareAndSwap stale error = %v, want ErrStaleRevision", err)
	}
	if got := mirrorRefHash(t, store, draft.Ref); got != mainSHA {
		t.Fatalf("mirror draft = %s, want unchanged %s", got, mainSHA)
	}
	if _, ok := originRefHash(t, origin, draft.Ref); ok {
		t.Fatalf("origin gained draft ref %s after stale CompareAndSwap", draft.Ref)
	}
}

func TestCompareAndSwapPushFailureRollsBackRef(t *testing.T) {
	ctx := context.Background()
	origin, mainSHA := createOrigin(t)
	store := createWorkspace(t, origin)
	draft, err := store.CreateDraft(ctx, "home", mainSHA, "change", "Ada")
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	checkout, err := store.Checkout(ctx, "home", mainSHA)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	commit, err := store.Commit(ctx, checkout, CommitInput{Message: "nodr: change", Author: "Ada", ExpectedSHA: mainSHA, Mutate: writeFileMutation("x.yaml", "x\n")})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// Block writes into origin's refs/heads so the draft push fails while the
	// commit object (published under refs/nodr/commits) is still fetchable.
	heads := filepath.Join(origin, "refs", "heads")
	if err := os.Chmod(heads, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(heads, 0o700) })

	err = store.CompareAndSwap(ctx, "home", draft.Ref, mainSHA, commit.SHA)
	if err == nil {
		t.Fatal("CompareAndSwap succeeded although origin refs are read-only")
	}
	if errors.Is(err, ErrStaleRevision) {
		t.Fatalf("push failure reported as stale revision: %v", err)
	}
	if got := mirrorRefHash(t, store, draft.Ref); got != mainSHA {
		t.Fatalf("mirror draft = %s after push failure, want rolled back to %s", got, mainSHA)
	}
}

func TestPromoteFastForwardUpdatesMirrorAndOrigin(t *testing.T) {
	origin, mainSHA := createOrigin(t)
	store := createWorkspace(t, origin)
	_, sha := draftCommit(t, store, mainSHA, writeFileMutation("intent.yaml", "kind: VirtualMachine\n"))

	if err := store.Promote(context.Background(), "home", sha, mainSHA); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if got := mirrorRefHash(t, store, "refs/heads/main"); got != sha {
		t.Fatalf("mirror main = %s, want %s", got, sha)
	}
	got, ok := originRefHash(t, origin, "refs/heads/main")
	if !ok || got != sha {
		t.Fatalf("origin main = %s (found %v), want %s", got, ok, sha)
	}
}

func TestPromoteMovedDefaultReturnsStaleRevision(t *testing.T) {
	origin, mainSHA := createOrigin(t)
	store := createWorkspace(t, origin)
	_, first := draftCommit(t, store, mainSHA, writeFileMutation("a.yaml", "a\n"))
	if err := store.Promote(context.Background(), "home", first, mainSHA); err != nil {
		t.Fatalf("first Promote: %v", err)
	}
	_, second := draftCommit(t, store, first, writeFileMutation("b.yaml", "b\n"))

	// The default branch is now first; promoting against the old expectation must fail.
	if err := store.Promote(context.Background(), "home", second, mainSHA); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("Promote with moved default error = %v, want ErrStaleRevision", err)
	}
	if got := mirrorRefHash(t, store, "refs/heads/main"); got != first {
		t.Fatalf("mirror main = %s after stale promote, want %s", got, first)
	}
}

func TestPromoteDivergedDraftReturnsNotFastForward(t *testing.T) {
	origin, mainSHA := createOrigin(t)
	store := createWorkspace(t, origin)
	_, first := draftCommit(t, store, mainSHA, writeFileMutation("a.yaml", "a\n"))
	if err := store.Promote(context.Background(), "home", first, mainSHA); err != nil {
		t.Fatalf("first Promote: %v", err)
	}
	// Second draft descends from mainSHA, so it does not contain the new default first.
	_, diverged := draftCommit(t, store, mainSHA, writeFileMutation("b.yaml", "b\n"))

	if err := store.Promote(context.Background(), "home", diverged, first); !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("Promote diverged error = %v, want ErrNotFastForward", err)
	}
	if got := mirrorRefHash(t, store, "refs/heads/main"); got != first {
		t.Fatalf("mirror main = %s after rejected promote, want %s", got, first)
	}
}

func TestDiffReportsAddedModifiedDeleted(t *testing.T) {
	ctx := context.Background()
	origin, mainSHA := createOrigin(t)
	store := createWorkspace(t, origin)

	_, added := draftCommit(t, store, mainSHA, func(root string) error {
		if err := os.WriteFile(filepath.Join(root, "gone.txt"), []byte("gone\nline\n"), 0o644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, "blob.bin"), []byte{0x00, 0x01, 0x02, 0xff}, 0o644)
	})
	_, next := draftCommit(t, store, added, func(root string) error {
		if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, "nodr.yaml"), []byte("apiVersion: nodr/v1alpha1\nkind: Workspace\nmetadata:\n  name: home\nspec: {n: 1}\n"), 0o644)
	})

	first, err := store.Diff(ctx, "home", mainSHA, added)
	if err != nil {
		t.Fatalf("Diff added: %v", err)
	}
	wantAdded := map[string]string{"blob.bin": "added", "gone.txt": "added"}
	if len(first) != len(wantAdded) {
		t.Fatalf("Diff added = %+v, want %d entries", first, len(wantAdded))
	}
	for _, d := range first {
		if wantAdded[d.Path] != d.Status {
			t.Fatalf("Diff added entry %+v, want status %q", d, wantAdded[d.Path])
		}
		if d.Path == "blob.bin" && d.Patch != "" {
			t.Fatalf("binary file patch = %q, want empty", d.Patch)
		}
		if d.Path == "gone.txt" && !strings.Contains(d.Patch, "+gone") {
			t.Fatalf("added text patch lacks content: %q", d.Patch)
		}
	}

	second, err := store.Diff(ctx, "home", added, next)
	if err != nil {
		t.Fatalf("Diff modified: %v", err)
	}
	wantSecond := map[string]string{"gone.txt": "deleted", "new.txt": "added", "nodr.yaml": "modified"}
	if len(second) != len(wantSecond) {
		t.Fatalf("Diff modified = %+v, want %d entries", second, len(wantSecond))
	}
	for _, d := range second {
		if wantSecond[d.Path] != d.Status {
			t.Fatalf("Diff modified entry %+v, want status %q", d, wantSecond[d.Path])
		}
		if d.Path == "nodr.yaml" && !strings.Contains(d.Patch, "+spec: {n: 1}") {
			t.Fatalf("modified patch lacks change: %q", d.Patch)
		}
	}
}

func TestRefResolvesMirrorRefs(t *testing.T) {
	origin, mainSHA := createOrigin(t)
	store := createWorkspace(t, origin)
	ctx := context.Background()

	got, err := store.Ref(ctx, "home", "refs/heads/main")
	if err != nil {
		t.Fatalf("Ref: %v", err)
	}
	if got != mainSHA {
		t.Fatalf("Ref(main) = %s, want %s", got, mainSHA)
	}
	if _, err := store.Ref(ctx, "home", "refs/heads/missing"); err == nil {
		t.Fatal("Ref(missing) succeeded")
	}
}

func TestWithAuthUsedOnlyForGitHubBackedWorkspaces(t *testing.T) {
	ctx := context.Background()
	origin, _ := createOrigin(t)
	var calls atomic.Int64
	var gotID atomic.Int64
	store, err := NewLocalStore(t.TempDir(), WithAuth(func(_ context.Context, installationID int64) (transport.AuthMethod, error) {
		calls.Add(1)
		gotID.Store(installationID)
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorkspace(ctx, CreateWorkspaceInput{Slug: "home", GitHubInstallationID: 42, Owner: "acme", Repository: origin, DefaultBranch: "main"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if calls.Load() != 1 || gotID.Load() != 42 {
		t.Fatalf("auth calls = %d for installation %d, want 1 for 42", calls.Load(), gotID.Load())
	}

	if _, err := store.CreateWorkspace(ctx, CreateWorkspaceInput{Slug: "local", Owner: "acme", Repository: origin, DefaultBranch: "main"}); err != nil {
		t.Fatalf("CreateWorkspace without installation: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("auth called for workspace without installation: %d calls", calls.Load())
	}
}
