// Package repository manages Git-backed nodr workspaces.
package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ErrStaleRevision reports that a ref or checkout no longer has its expected revision.
var ErrStaleRevision = errors.New("repository: stale revision")

// ErrNotFastForward reports that a revision does not descend from the branch it would move.
var ErrNotFastForward = errors.New("repository: not a fast-forward")

// Store manages workspace mirrors and short-lived operation checkouts.
type Store interface {
	CreateWorkspace(context.Context, CreateWorkspaceInput) (Workspace, error)
	Workspace(context.Context, string) (Workspace, error)
	CreateDraft(context.Context, string, string, string, string) (Draft, error)
	Checkout(context.Context, string, string) (Checkout, error)
	Commit(context.Context, Checkout, CommitInput) (Commit, error)
	CompareAndSwap(context.Context, string, string, string, string) error
	Fetch(context.Context, string) (FetchResult, error)
	Ref(ctx context.Context, slug, ref string) (sha string, err error)
	Diff(ctx context.Context, slug, baseSHA, headSHA string) ([]FileDiff, error)
	Push(ctx context.Context, slug, ref string) error
	Promote(ctx context.Context, slug, draftSHA, expectedDefaultSHA string) error
}

// CreateWorkspaceInput identifies a GitHub-backed workspace and its default branch.
type CreateWorkspaceInput struct {
	Slug                 string
	GitHubInstallationID int64
	Owner                string
	Repository           string
	DefaultBranch        string
}

// Workspace identifies a workspace and its local bare mirror.
type Workspace struct {
	Slug                 string
	GitHubInstallationID int64
	Owner                string
	Repository           string
	DefaultBranch        string
	DefaultSHA           string
}

// Draft is a draft ref rooted at a supplied base revision.
type Draft struct {
	Ref     string
	HeadSHA string
	Title   string
	Author  string
}

// Checkout is an isolated temporary checkout. Close removes it.
type Checkout interface {
	Path() string
	Revision() string
	Close() error
}

// CommitInput supplies the commit metadata and operation-local filesystem mutation.
type CommitInput struct {
	Message     string
	Author      string
	ExpectedSHA string
	Mutate      func(root string) error
}

// Commit describes a newly created Git commit.
type Commit struct{ SHA string }

// FetchResult reports the workspace after fetching its origin.
type FetchResult struct{ DefaultSHA string }

// Option configures LocalStore.
type Option func(*LocalStore)

// WithAuth supplies per-installation Git credentials for clone/fetch/push.
func WithAuth(f func(ctx context.Context, installationID int64) (transport.AuthMethod, error)) Option {
	return func(s *LocalStore) {
		s.auth = f
	}
}

// LocalStore stores bare mirrors below a server data directory.
type LocalStore struct {
	root string
	auth func(ctx context.Context, installationID int64) (transport.AuthMethod, error)
	mu   sync.Mutex
}

type localCheckout struct {
	path     string
	revision string
	once     sync.Once
	err      error
}

// NewLocalStore creates a local Git workspace store rooted at dataRoot.
func NewLocalStore(dataRoot string, opts ...Option) (*LocalStore, error) {
	if dataRoot == "" {
		return nil, errors.New("repository: data root is required")
	}
	if err := os.MkdirAll(filepath.Join(dataRoot, "repositories"), 0o700); err != nil {
		return nil, fmt.Errorf("repository: create data directory: %w", err)
	}
	store := &LocalStore{root: dataRoot}
	for _, opt := range opts {
		opt(store)
	}
	return store, nil
}

// CreateWorkspace clones input.Repository into the workspace's bare mirror.
func (s *LocalStore) CreateWorkspace(ctx context.Context, input CreateWorkspaceInput) (Workspace, error) {
	if err := ctx.Err(); err != nil {
		return Workspace{}, err
	}
	if input.Slug == "" || input.Repository == "" || input.DefaultBranch == "" {
		return Workspace{}, errors.New("repository: slug, repository, and default branch are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mirror := s.mirrorPath(input.Slug)
	if _, err := os.Stat(mirror); err == nil {
		return Workspace{}, fmt.Errorf("repository: workspace %q already exists", input.Slug)
	}
	auth, err := s.authFor(ctx, input.GitHubInstallationID)
	if err != nil {
		return Workspace{}, err
	}
	if _, err := git.PlainClone(mirror, true, &git.CloneOptions{URL: input.Repository, Mirror: true, Auth: auth}); err != nil {
		return Workspace{}, fmt.Errorf("repository: clone mirror: %w", err)
	}
	return s.workspace(input.Slug, input)
}

// Workspace returns metadata and the current default-branch revision.
func (s *LocalStore) Workspace(ctx context.Context, slug string) (Workspace, error) {
	if err := ctx.Err(); err != nil {
		return Workspace{}, err
	}
	repo, err := git.PlainOpen(s.mirrorPath(slug))
	if err != nil {
		return Workspace{}, fmt.Errorf("repository: open workspace: %w", err)
	}
	cfg, err := repo.Config()
	if err != nil {
		return Workspace{}, fmt.Errorf("repository: read config: %w", err)
	}
	branch := cfg.Raw.Section("nodr").Option("defaultBranch")
	if branch == "" {
		return Workspace{}, fmt.Errorf("repository: workspace %q has no metadata", slug)
	}
	return s.workspaceFromRepo(slug, repo, branch)
}

// CreateDraft creates a unique nodr draft ref at baseSHA without moving the default branch.
func (s *LocalStore) CreateDraft(ctx context.Context, slug, baseSHA, title, author string) (Draft, error) {
	if err := ctx.Err(); err != nil {
		return Draft{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := git.PlainOpen(s.mirrorPath(slug))
	if err != nil {
		return Draft{}, fmt.Errorf("repository: open workspace: %w", err)
	}
	hash := plumbing.NewHash(baseSHA)
	if _, err := repo.CommitObject(hash); err != nil {
		return Draft{}, fmt.Errorf("repository: resolve draft base: %w", err)
	}
	ref := fmt.Sprintf("refs/heads/nodr/drafts/%d", time.Now().UnixNano())
	if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(ref), hash)); err != nil {
		return Draft{}, fmt.Errorf("repository: create draft ref: %w", err)
	}
	return Draft{Ref: ref, HeadSHA: baseSHA, Title: title, Author: author}, nil
}

// Checkout creates an isolated temporary clone at revision.
func (s *LocalStore) Checkout(ctx context.Context, slug, revision string) (Checkout, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mirror, err := git.PlainOpen(s.mirrorPath(slug))
	if err != nil {
		return nil, fmt.Errorf("repository: open workspace: %w", err)
	}
	hash := plumbing.NewHash(revision)
	if _, err := mirror.CommitObject(hash); err != nil {
		ref, refErr := mirror.Reference(plumbing.ReferenceName(revision), true)
		if refErr != nil {
			return nil, fmt.Errorf("repository: resolve revision: %w", err)
		}
		hash = ref.Hash()
		if _, err := mirror.CommitObject(hash); err != nil {
			return nil, fmt.Errorf("repository: resolve revision commit: %w", err)
		}
	}
	dir, err := os.MkdirTemp(filepath.Join(s.root, "repositories"), "checkout-")
	if err != nil {
		return nil, fmt.Errorf("repository: create checkout directory: %w", err)
	}
	repo, err := git.PlainClone(dir, false, &git.CloneOptions{URL: s.mirrorPath(slug), NoCheckout: true})
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("repository: clone checkout: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("repository: get checkout worktree: %w", err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{Hash: hash}); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("repository: checkout revision: %w", err)
	}
	return &localCheckout{path: dir, revision: hash.String()}, nil
}

// Commit mutates, stages, and commits an isolated checkout. Failed mutations close the checkout.
func (s *LocalStore) Commit(ctx context.Context, checkout Checkout, input CommitInput) (Commit, error) {
	local, ok := checkout.(*localCheckout)
	if !ok {
		return Commit{}, errors.New("repository: checkout was not created by this store")
	}
	if err := ctx.Err(); err != nil {
		_ = local.Close()
		return Commit{}, err
	}
	if input.ExpectedSHA != local.revision {
		_ = local.Close()
		return Commit{}, ErrStaleRevision
	}
	if input.Mutate == nil {
		_ = local.Close()
		return Commit{}, errors.New("repository: mutation is required")
	}
	if err := input.Mutate(local.path); err != nil {
		_ = local.Close()
		return Commit{}, fmt.Errorf("repository: mutate checkout: %w", err)
	}
	repo, err := git.PlainOpen(local.path)
	if err != nil {
		_ = local.Close()
		return Commit{}, fmt.Errorf("repository: open checkout: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		_ = local.Close()
		return Commit{}, fmt.Errorf("repository: worktree: %w", err)
	}
	if _, err := wt.Add("."); err != nil {
		_ = local.Close()
		return Commit{}, fmt.Errorf("repository: stage changes: %w", err)
	}
	sha, err := wt.Commit(input.Message, &git.CommitOptions{Author: signature(input.Author)})
	if err != nil {
		_ = local.Close()
		return Commit{}, fmt.Errorf("repository: commit: %w", err)
	}
	temporaryRef := plumbing.ReferenceName("refs/nodr/commits/" + sha.String())
	if err := repo.Storer.SetReference(plumbing.NewHashReference(temporaryRef, sha)); err != nil {
		_ = local.Close()
		return Commit{}, fmt.Errorf("repository: stage commit: %w", err)
	}
	if err := repo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(temporaryRef + ":" + temporaryRef)}}); err != nil {
		_ = local.Close()
		return Commit{}, fmt.Errorf("repository: publish commit object: %w", err)
	}
	return Commit{SHA: sha.String()}, nil
}

// CompareAndSwap changes ref from oldSHA to newSHA only when it still points at oldSHA.
// The new value is then pushed to origin when the mirror has one; if the push fails the
// local ref is restored to its previous value and the push error is returned.
func (s *LocalStore) CompareAndSwap(ctx context.Context, slug, ref, oldSHA, newSHA string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := git.PlainOpen(s.mirrorPath(slug))
	if err != nil {
		return fmt.Errorf("repository: open workspace: %w", err)
	}
	name := plumbing.ReferenceName(ref)
	actual, err := repo.Reference(name, true)
	if err != nil || actual.Hash().String() != oldSHA {
		return ErrStaleRevision
	}
	if _, err := repo.CommitObject(plumbing.NewHash(newSHA)); err != nil {
		if err := s.fetchCommitFromOrigin(ctx, repo, newSHA); err != nil {
			return fmt.Errorf("repository: resolve staged commit: %w", err)
		}
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(name, plumbing.NewHash(newSHA))); err != nil {
		return fmt.Errorf("repository: update ref: %w", err)
	}
	if err := s.publish(ctx, repo, name); err != nil {
		if restoreErr := repo.Storer.SetReference(plumbing.NewHashReference(name, actual.Hash())); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("repository: restore ref: %w", restoreErr))
		}
		return err
	}
	return nil
}

// Fetch updates a workspace bare mirror from its configured origin.
func (s *LocalStore) Fetch(ctx context.Context, slug string) (FetchResult, error) {
	if err := ctx.Err(); err != nil {
		return FetchResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := git.PlainOpen(s.mirrorPath(slug))
	if err != nil {
		return FetchResult{}, fmt.Errorf("repository: open workspace: %w", err)
	}
	installationID, err := installationOf(repo)
	if err != nil {
		return FetchResult{}, err
	}
	auth, err := s.authFor(ctx, installationID)
	if err != nil {
		return FetchResult{}, err
	}
	if err := repo.FetchContext(ctx, &git.FetchOptions{RemoteName: "origin", Auth: auth}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return FetchResult{}, fmt.Errorf("repository: fetch origin: %w", err)
	}
	cfg, err := repo.Config()
	if err != nil {
		return FetchResult{}, fmt.Errorf("repository: read metadata: %w", err)
	}
	branch := cfg.Raw.Section("nodr").Option("defaultBranch")
	workspace, err := s.workspaceFromRepo(slug, repo, branch)
	if err != nil {
		return FetchResult{}, err
	}
	return FetchResult{DefaultSHA: workspace.DefaultSHA}, nil
}

// Ref resolves a full ref name (for example refs/heads/main) or a revision in the mirror to a commit SHA.
func (s *LocalStore) Ref(ctx context.Context, slug, ref string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	repo, err := git.PlainOpen(s.mirrorPath(slug))
	if err != nil {
		return "", fmt.Errorf("repository: open workspace: %w", err)
	}
	if reference, err := repo.Reference(plumbing.ReferenceName(ref), true); err == nil {
		return reference.Hash().String(), nil
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return "", fmt.Errorf("repository: resolve ref %q: %w", ref, err)
	}
	return hash.String(), nil
}

// Push publishes one mirror ref to origin using the workspace's installation credentials.
func (s *LocalStore) Push(ctx context.Context, slug, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := git.PlainOpen(s.mirrorPath(slug))
	if err != nil {
		return fmt.Errorf("repository: open workspace: %w", err)
	}
	return s.pushRef(ctx, repo, plumbing.ReferenceName(ref))
}

// Promote fast-forwards the default branch to draftSHA iff the branch still points at
// expectedDefaultSHA and expectedDefaultSHA is an ancestor of draftSHA, then pushes the
// branch to origin. If the push fails the previous branch value is restored.
// ErrStaleRevision reports a moved default branch; ErrNotFastForward reports divergence.
func (s *LocalStore) Promote(ctx context.Context, slug, draftSHA, expectedDefaultSHA string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := git.PlainOpen(s.mirrorPath(slug))
	if err != nil {
		return fmt.Errorf("repository: open workspace: %w", err)
	}
	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("repository: read metadata: %w", err)
	}
	branch := cfg.Raw.Section("nodr").Option("defaultBranch")
	if branch == "" {
		return fmt.Errorf("repository: workspace %q has no metadata", slug)
	}
	name := plumbing.NewBranchReferenceName(branch)
	current, err := repo.Reference(name, true)
	if err != nil {
		return fmt.Errorf("repository: resolve default branch: %w", err)
	}
	if current.Hash().String() != expectedDefaultSHA {
		return ErrStaleRevision
	}
	expected, err := repo.CommitObject(plumbing.NewHash(expectedDefaultSHA))
	if err != nil {
		return fmt.Errorf("repository: resolve expected default commit: %w", err)
	}
	draft, err := repo.CommitObject(plumbing.NewHash(draftSHA))
	if err != nil {
		return fmt.Errorf("repository: resolve draft commit: %w", err)
	}
	ancestor, err := expected.IsAncestor(draft)
	if err != nil {
		return fmt.Errorf("repository: check fast-forward: %w", err)
	}
	if !ancestor {
		return ErrNotFastForward
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(name, draft.Hash)); err != nil {
		return fmt.Errorf("repository: update default branch: %w", err)
	}
	if err := s.pushRef(ctx, repo, name); err != nil {
		if restoreErr := repo.Storer.SetReference(plumbing.NewHashReference(name, current.Hash())); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("repository: restore default branch: %w", restoreErr))
		}
		return err
	}
	return nil
}

func (s *LocalStore) workspace(slug string, input CreateWorkspaceInput) (Workspace, error) {
	repo, err := git.PlainOpen(s.mirrorPath(slug))
	if err != nil {
		return Workspace{}, fmt.Errorf("repository: open mirror: %w", err)
	}
	cfg, err := repo.Config()
	if err != nil {
		return Workspace{}, err
	}
	cfg.Raw.Section("nodr").SetOption("defaultBranch", input.DefaultBranch)
	cfg.Raw.Section("nodr").SetOption("installationID", fmt.Sprint(input.GitHubInstallationID))
	cfg.Raw.Section("nodr").SetOption("owner", input.Owner)
	cfg.Raw.Section("nodr").SetOption("repository", input.Repository)
	if err := repo.SetConfig(cfg); err != nil {
		return Workspace{}, fmt.Errorf("repository: write metadata: %w", err)
	}
	return s.workspaceFromRepo(slug, repo, input.DefaultBranch)
}
func (s *LocalStore) workspaceFromRepo(slug string, repo *git.Repository, branch string) (Workspace, error) {
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return Workspace{}, fmt.Errorf("repository: resolve default branch: %w", err)
	}
	cfg, _ := repo.Config()
	installationID, err := strconv.ParseInt(cfg.Raw.Section("nodr").Option("installationID"), 10, 64)
	if err != nil {
		return Workspace{}, fmt.Errorf("repository: parse installation ID: %w", err)
	}
	return Workspace{Slug: slug, GitHubInstallationID: installationID, Owner: cfg.Raw.Section("nodr").Option("owner"), Repository: cfg.Raw.Section("nodr").Option("repository"), DefaultBranch: branch, DefaultSHA: ref.Hash().String()}, nil
}

// authFor returns credentials for a GitHub installation. It returns nil (anonymous access)
// when no auth function is configured or the workspace is not GitHub-backed.
func (s *LocalStore) authFor(ctx context.Context, installationID int64) (transport.AuthMethod, error) {
	if s.auth == nil || installationID == 0 {
		return nil, nil
	}
	auth, err := s.auth(ctx, installationID)
	if err != nil {
		return nil, fmt.Errorf("repository: installation credentials: %w", err)
	}
	return auth, nil
}

// installationOf reads the GitHub installation ID stored in the mirror metadata.
func installationOf(repo *git.Repository) (int64, error) {
	cfg, err := repo.Config()
	if err != nil {
		return 0, fmt.Errorf("repository: read metadata: %w", err)
	}
	installationID, err := strconv.ParseInt(cfg.Raw.Section("nodr").Option("installationID"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("repository: parse installation ID: %w", err)
	}
	return installationID, nil
}

// hasOrigin reports whether the mirror has an origin remote configured.
func hasOrigin(repo *git.Repository) (bool, error) {
	_, err := repo.Remote("origin")
	if errors.Is(err, git.ErrRemoteNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("repository: resolve origin: %w", err)
	}
	return true, nil
}

// fetchCommitFromOrigin fetches the commit object that Commit published to origin under
// refs/nodr/commits/<sha> into the mirror, so a CompareAndSwap can reference it.
func (s *LocalStore) fetchCommitFromOrigin(ctx context.Context, repo *git.Repository, sha string) error {
	installationID, err := installationOf(repo)
	if err != nil {
		return err
	}
	auth, err := s.authFor(ctx, installationID)
	if err != nil {
		return err
	}
	src := plumbing.ReferenceName("refs/nodr/commits/" + sha)
	spec := config.RefSpec(src + ":" + src)
	if err := repo.FetchContext(ctx, &git.FetchOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{spec}, Auth: auth}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch commit object: %w", err)
	}
	if _, err := repo.CommitObject(plumbing.NewHash(sha)); err != nil {
		return fmt.Errorf("commit object missing after fetch: %w", err)
	}
	return nil
}

// publish pushes ref to origin when the mirror has an origin remote.
func (s *LocalStore) publish(ctx context.Context, repo *git.Repository, name plumbing.ReferenceName) error {
	ok, err := hasOrigin(repo)
	if err != nil || !ok {
		return err
	}
	return s.pushRef(ctx, repo, name)
}

// pushRef pushes one ref to origin with the workspace's installation credentials.
// Already up-to-date remotes are a success.
func (s *LocalStore) pushRef(ctx context.Context, repo *git.Repository, name plumbing.ReferenceName) error {
	installationID, err := installationOf(repo)
	if err != nil {
		return err
	}
	auth, err := s.authFor(ctx, installationID)
	if err != nil {
		return err
	}
	spec := config.RefSpec(name + ":" + name)
	if err := repo.PushContext(ctx, &git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{spec}, Auth: auth}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("repository: push %s: %w", name, err)
	}
	return nil
}

func (s *LocalStore) mirrorPath(slug string) string {
	return filepath.Join(s.root, "repositories", strings.ReplaceAll(slug, "/", "_"), "mirror.git")
}
func (c *localCheckout) Path() string     { return c.path }
func (c *localCheckout) Revision() string { return c.revision }
func (c *localCheckout) Close() error {
	c.once.Do(func() { c.err = os.RemoveAll(c.path) })
	return c.err
}
func signature(author string) *object.Signature {
	if author == "" {
		author = "nodr"
	}
	return &object.Signature{Name: author, Email: "nodr@localhost", When: time.Now()}
}
