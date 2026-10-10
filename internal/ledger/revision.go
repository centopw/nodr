package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/centopw/nodr/internal/operations"
)

// WithRevision checks out revision (a SHA, or "" for the default branch HEAD),
// calls fn with the checkout root, and always removes the checkout afterwards.
func (s *Service) WithRevision(ctx context.Context, slug, revision string, fn func(root string) error) error {
	if revision == "" {
		ws, err := s.Workspace(ctx, slug)
		if err != nil {
			return err
		}
		revision = ws.DefaultSHA
	}
	checkout, err := s.deps.Repo.Checkout(ctx, slug, revision)
	if err != nil {
		return fmt.Errorf("ledger: checkout %s at %s: %w", slug, revision, err)
	}
	defer func() { _ = checkout.Close() }()
	return fn(checkout.Path())
}

// RevisionOf resolves a changeset ID to its draft SHA. An empty ID resolves to
// the default-branch HEAD.
func (s *Service) RevisionOf(ctx context.Context, slug, changesetID string) (string, error) {
	if changesetID == "" {
		ws, err := s.Workspace(ctx, slug)
		if err != nil {
			return "", err
		}
		return ws.DefaultSHA, nil
	}
	cs, err := s.changeset(ctx, slug, changesetID)
	if err != nil {
		return "", err
	}
	return cs.DraftSHA, nil
}

// changeset loads a changeset and verifies it belongs to workspace slug.
func (s *Service) changeset(ctx context.Context, slug, id string) (operations.Changeset, error) {
	cs, err := s.deps.Ops.Changeset(ctx, id)
	if errors.Is(err, operations.ErrNotFound) {
		return operations.Changeset{}, fmt.Errorf("ledger: %s: %w", id, ErrChangesetNotFound)
	}
	if err != nil {
		return operations.Changeset{}, fmt.Errorf("ledger: load changeset %s: %w", id, err)
	}
	if cs.Workspace != slug {
		return operations.Changeset{}, fmt.Errorf("ledger: %s is not in workspace %s: %w", id, slug, ErrChangesetNotFound)
	}
	return cs, nil
}
