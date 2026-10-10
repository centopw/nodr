// Package ledger runs nodr's GitHub-backed control plane: it connects GitHub
// repositories as workspaces, applies commands as commits on changeset drafts,
// and reacts to pushes. Desired state lives in Git; operational state (drafts,
// operations, metadata) lives in SQLite.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"sync"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	github "github.com/google/go-github/v75/github"

	"github.com/centopw/nodr/internal/commands"
	"github.com/centopw/nodr/internal/operations"
	"github.com/centopw/nodr/internal/repository"
	"github.com/centopw/nodr/internal/secrets"
)

// Sentinel errors. API handlers map them to HTTP statuses; callers classify
// with errors.Is.
var (
	// ErrRepositoryInaccessible reports that the repository is not visible to the installation.
	ErrRepositoryInaccessible = errors.New("ledger: repository is not accessible to the installation")
	// ErrNotNodrRepository reports that the repository has no valid nodr.yaml Workspace manifest.
	ErrNotNodrRepository = errors.New("ledger: repository is not a nodr workspace")
	// ErrWorkspaceExists reports that a workspace with the manifest name is already connected.
	ErrWorkspaceExists = errors.New("ledger: workspace already exists")
	// ErrWorkspaceNotFound reports an unknown workspace slug.
	ErrWorkspaceNotFound = errors.New("ledger: workspace not found")
	// ErrChangesetNotFound reports an unknown changeset ID for the workspace.
	ErrChangesetNotFound = errors.New("ledger: changeset not found")
	// ErrStaleRevision reports that the caller's expected revision no longer matches.
	ErrStaleRevision = errors.New("ledger: stale revision")
	// ErrPlanChanged reports that the plan no longer matches the changeset's draft.
	ErrPlanChanged = errors.New("ledger: plan changed")
	// ErrPlanRequired reports that a changeset has no successful plan to apply.
	ErrPlanRequired = errors.New("ledger: plan required")
	// ErrChangesetClosed reports that a changeset is applied or abandoned.
	ErrChangesetClosed = errors.New("ledger: changeset closed")
	// ErrCommandInFlight reports that an idempotent command with the same key is still running.
	ErrCommandInFlight = errors.New("ledger: command in flight")
)

// CommandError carries a user-facing command failure from the commands package.
type CommandError struct {
	Err *commands.Error
}

// Error implements the error interface.
func (e *CommandError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the wrapped command error so errors.As can reach *commands.Error.
func (e *CommandError) Unwrap() error {
	return e.Err
}

// Deps are the collaborators of a Service.
type Deps struct {
	Repo    repository.Store
	Meta    *repository.MetadataStore
	Ops     *operations.Store
	GitHub  GitHub
	Secrets *secrets.Store // credentials resolver for tofu; may be nil
	Runner  Runner         // T4
	// WebhookSecret is stored (encrypted) in workspace metadata.
	WebhookSecret string
	// RepositoryURL builds the clone URL of owner/repo. Nil means
	// https://github.com/<owner>/<repo>.git.
	RepositoryURL func(owner, repo string) string
	// Probe reads the manifest name and default branch of a repository. Nil
	// means a shallow clone of the default branch authenticated with GitHub.GitAuth.
	Probe func(ctx context.Context, installationID int64, owner, repo string) (manifestName, defaultBranch string, err error)
}

// GitHub is the GitHub App surface the ledger needs; *githubapp.Service satisfies it.
type GitHub interface {
	ListInstallations(ctx context.Context) ([]*github.Installation, error)
	ListRepositories(ctx context.Context, installationID int64) ([]*github.Repository, error)
	VerifyWebhookSignature(payload []byte, signature string) error
	GitAuth(ctx context.Context, installationID int64) (githttp.BasicAuth, error)
}

// Service owns connected workspaces and their background operations.
type Service struct {
	deps Deps
	ctx  context.Context
	wg   sync.WaitGroup
	// mu serializes per-service mutation of refs.
	mu sync.Mutex
}

// New creates a Service. ctx is the server lifetime; background plan and apply
// operations run on it.
func New(ctx context.Context, d Deps) (*Service, error) {
	if d.Repo == nil || d.Meta == nil || d.Ops == nil || d.GitHub == nil {
		return nil, errors.New("ledger: repository, metadata, operations and GitHub are required")
	}
	if d.RepositoryURL == nil {
		d.RepositoryURL = func(owner, repo string) string {
			return fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
		}
	}
	if d.Probe == nil {
		d.Probe = defaultProbe(d.GitHub, d.RepositoryURL)
	}
	return &Service{deps: d, ctx: ctx}, nil
}

// Close waits for in-flight background operations. It does not cancel them.
func (s *Service) Close() error {
	s.wg.Wait()
	return nil
}
