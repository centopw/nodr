package ledger

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
	github "github.com/google/go-github/v75/github"

	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/repository"
	"github.com/centopw/nodr/internal/workspace"
)

// RepositoryRef describes a repository visible to an installation.
type RepositoryRef struct {
	Owner         string
	Name          string
	DefaultBranch string
	Private       bool
}

// Installation is a GitHub App installation and the repositories it can access.
type Installation struct {
	ID           int64
	Account      string
	Repositories []RepositoryRef
}

// ConnectInput names the repository to connect and the installation that can read it.
type ConnectInput struct {
	InstallationID int64
	Owner          string
	Repository     string
}

// Workspace is a connected workspace: its slug is the manifest name of its nodr.yaml.
type Workspace struct {
	Slug           string
	Owner          string
	Repository     string
	DefaultBranch  string
	DefaultSHA     string
	InstallationID int64
}

// Installations lists the app's installations with their accessible repositories.
func (s *Service) Installations(ctx context.Context) ([]Installation, error) {
	list, err := s.deps.GitHub.ListInstallations(ctx)
	if err != nil {
		return nil, fmt.Errorf("ledger: list installations: %w", err)
	}
	out := make([]Installation, 0, len(list))
	for _, inst := range list {
		id := inst.GetID()
		repos, err := s.deps.GitHub.ListRepositories(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("ledger: list repositories of installation %d: %w", id, err)
		}
		out = append(out, Installation{ID: id, Account: inst.GetAccount().GetLogin(), Repositories: repositoryRefs(repos)})
	}
	return out, nil
}

// Connect verifies the repository belongs to the installation, clones its
// mirror, and requires a valid nodr.yaml Workspace manifest. The workspace slug
// is the manifest name.
func (s *Service) Connect(ctx context.Context, in ConnectInput) (Workspace, error) {
	if in.Owner == "" || in.Repository == "" {
		return Workspace{}, fmt.Errorf("ledger: owner and repository are required: %w", ErrRepositoryInaccessible)
	}
	repo, err := s.findRepository(ctx, in)
	if err != nil {
		return Workspace{}, err
	}
	manifestName, probedBranch, err := s.deps.Probe(ctx, in.InstallationID, repo.Owner, repo.Name)
	if err != nil {
		return Workspace{}, err
	}
	branch := repo.DefaultBranch
	if branch == "" {
		branch = probedBranch
	}
	if _, err := s.deps.Meta.Get(ctx, manifestName); err == nil {
		return Workspace{}, fmt.Errorf("ledger: %s: %w", manifestName, ErrWorkspaceExists)
	} else if !errors.Is(err, repository.ErrMetadataNotFound) {
		return Workspace{}, fmt.Errorf("ledger: check workspace %s: %w", manifestName, err)
	}
	created, err := s.deps.Repo.CreateWorkspace(ctx, repository.CreateWorkspaceInput{
		Slug:                 manifestName,
		GitHubInstallationID: in.InstallationID,
		Owner:                repo.Owner,
		Repository:           s.deps.RepositoryURL(repo.Owner, repo.Name),
		DefaultBranch:        branch,
	})
	if err != nil {
		return Workspace{}, fmt.Errorf("ledger: create workspace %s: %w", manifestName, err)
	}
	if err := s.deps.Meta.Put(ctx, repository.WorkspaceMetadata{
		Slug:             manifestName,
		Owner:            repo.Owner,
		Repository:       repo.Name,
		DefaultBranch:    branch,
		InstallationID:   in.InstallationID,
		WebhookSecret:    s.deps.WebhookSecret,
		LastProcessedSHA: created.DefaultSHA,
	}); err != nil {
		return Workspace{}, fmt.Errorf("ledger: record workspace %s: %w", manifestName, err)
	}
	return Workspace{
		Slug:           manifestName,
		Owner:          repo.Owner,
		Repository:     repo.Name,
		DefaultBranch:  branch,
		DefaultSHA:     created.DefaultSHA,
		InstallationID: in.InstallationID,
	}, nil
}

// Workspaces lists every connected workspace, sorted by slug.
func (s *Service) Workspaces(ctx context.Context) ([]Workspace, error) {
	rows, err := s.deps.Meta.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("ledger: list workspaces: %w", err)
	}
	out := make([]Workspace, 0, len(rows))
	for _, md := range rows {
		ws, err := s.Workspace(ctx, md.Slug)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, nil
}

// Workspace returns a connected workspace with its current default-branch SHA.
func (s *Service) Workspace(ctx context.Context, slug string) (Workspace, error) {
	md, err := s.deps.Meta.Get(ctx, slug)
	if errors.Is(err, repository.ErrMetadataNotFound) {
		return Workspace{}, fmt.Errorf("ledger: %s: %w", slug, ErrWorkspaceNotFound)
	}
	if err != nil {
		return Workspace{}, fmt.Errorf("ledger: load workspace %s: %w", slug, err)
	}
	mirror, err := s.deps.Repo.Workspace(ctx, slug)
	if err != nil {
		return Workspace{}, fmt.Errorf("ledger: open workspace %s: %w", slug, err)
	}
	return Workspace{
		Slug:           slug,
		Owner:          md.Owner,
		Repository:     md.Repository,
		DefaultBranch:  md.DefaultBranch,
		DefaultSHA:     mirror.DefaultSHA,
		InstallationID: md.InstallationID,
	}, nil
}

// findRepository looks up owner/name among the installation's repositories.
func (s *Service) findRepository(ctx context.Context, in ConnectInput) (RepositoryRef, error) {
	repos, err := s.deps.GitHub.ListRepositories(ctx, in.InstallationID)
	if err != nil {
		return RepositoryRef{}, fmt.Errorf("ledger: list repositories: %w", err)
	}
	for _, r := range repositoryRefs(repos) {
		if r.Owner == in.Owner && r.Name == in.Repository {
			return r, nil
		}
	}
	return RepositoryRef{}, fmt.Errorf("ledger: %s/%s: %w", in.Owner, in.Repository, ErrRepositoryInaccessible)
}

func repositoryRefs(repos []*github.Repository) []RepositoryRef {
	out := make([]RepositoryRef, 0, len(repos))
	for _, r := range repos {
		out = append(out, RepositoryRef{
			Owner:         r.GetOwner().GetLogin(),
			Name:          r.GetName(),
			DefaultBranch: r.GetDefaultBranch(),
			Private:       r.GetPrivate(),
		})
	}
	return out
}

// defaultProbe shallow-clones the repository's default branch into a temporary
// directory, validates its nodr.yaml, and returns the manifest name and branch.
func defaultProbe(gh GitHub, url func(owner, repo string) string) func(context.Context, int64, string, string) (string, string, error) {
	return func(ctx context.Context, installationID int64, owner, repo string) (string, string, error) {
		auth, err := gh.GitAuth(ctx, installationID)
		if err != nil {
			return "", "", fmt.Errorf("ledger: git credentials: %w", err)
		}
		dir, err := os.MkdirTemp("", "nodr-probe-")
		if err != nil {
			return "", "", fmt.Errorf("ledger: create probe directory: %w", err)
		}
		defer os.RemoveAll(dir)
		return probeClone(ctx, dir, url(owner, repo), &auth)
	}
}

// probeClone shallow-clones url into dir and reads the manifest of its default branch.
func probeClone(ctx context.Context, dir, url string, auth transport.AuthMethod) (string, string, error) {
	repo, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{URL: url, Depth: 1, Auth: auth})
	if err != nil {
		return "", "", fmt.Errorf("%w: clone: %w", ErrRepositoryInaccessible, err)
	}
	head, err := repo.Head()
	if err != nil {
		return "", "", fmt.Errorf("ledger: read HEAD: %w", err)
	}
	name, err := manifestName(dir)
	if err != nil {
		return "", "", err
	}
	return name, head.Name().Short(), nil
}

// manifestName loads the workspace at root and returns its manifest name. The
// manifest must be a valid Workspace document; any failure is ErrNotNodrRepository.
func manifestName(root string) (string, error) {
	ws, diags := workspace.Load(root)
	if diags.HasErrors() {
		return "", fmt.Errorf("%w: %w", ErrNotNodrRepository, diags.Err())
	}
	if ws.Manifest == nil || ws.Manifest.Kind != v1alpha1.KindWorkspace {
		return "", fmt.Errorf("%w: %s must declare kind %s", ErrNotNodrRepository, workspace.ManifestFile, v1alpha1.KindWorkspace)
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		return "", fmt.Errorf("ledger: build registry: %w", err)
	}
	if diags := ws.Validate(reg); diags.HasErrors() {
		return "", fmt.Errorf("%w: %w", ErrNotNodrRepository, diags.Err())
	}
	name := ws.Manifest.Metadata.Name
	if name == "" {
		return "", fmt.Errorf("%w: manifest has no metadata.name", ErrNotNodrRepository)
	}
	return name, nil
}
