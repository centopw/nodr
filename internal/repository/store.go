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
)

// ErrStaleRevision reports that a ref or checkout no longer has its expected revision.
var ErrStaleRevision = errors.New("repository: stale revision")

// Store manages workspace mirrors and short-lived operation checkouts.
type Store interface {
	CreateWorkspace(context.Context, CreateWorkspaceInput) (Workspace, error)
	Workspace(context.Context, string) (Workspace, error)
	CreateDraft(context.Context, string, string, string, string) (Draft, error)
	Checkout(context.Context, string, string) (Checkout, error)
	Commit(context.Context, Checkout, CommitInput) (Commit, error)
	CompareAndSwap(context.Context, string, string, string, string) error
	Fetch(context.Context, string) (FetchResult, error)
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

// LocalStore stores bare mirrors below a server data directory.
type LocalStore struct {
	root string
	mu   sync.Mutex
}

type localCheckout struct {
	path     string
	revision string
	once     sync.Once
	err      error
}

// NewLocalStore creates a local Git workspace store rooted at dataRoot.
func NewLocalStore(dataRoot string) (*LocalStore, error) {
	if dataRoot == "" {
		return nil, errors.New("repository: data root is required")
	}
	if err := os.MkdirAll(filepath.Join(dataRoot, "repositories"), 0o700); err != nil {
		return nil, fmt.Errorf("repository: create data directory: %w", err)
	}
	return &LocalStore{root: dataRoot}, nil
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
	if _, err := git.PlainClone(mirror, true, &git.CloneOptions{URL: input.Repository, Mirror: true}); err != nil {
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
	actual, err := repo.Reference(plumbing.ReferenceName(ref), true)
	if err != nil || actual.Hash().String() != oldSHA {
		return ErrStaleRevision
	}
	if _, err := repo.CommitObject(plumbing.NewHash(newSHA)); err != nil {
		return fmt.Errorf("repository: resolve staged commit: %w", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(ref), plumbing.NewHash(newSHA))); err != nil {
		return fmt.Errorf("repository: update ref: %w", err)
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
	if err := repo.Fetch(&git.FetchOptions{RemoteName: "origin"}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
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
