package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/centopw/nodr/internal/operations"
	"github.com/centopw/nodr/internal/repository"
)

// draftRefPrefix is the namespace of nodr-owned draft refs.
const draftRefPrefix = "refs/heads/nodr/drafts/"

// PushEvent is a parsed GitHub push delivery.
type PushEvent struct {
	DeliveryID string
	Owner      string
	Repository string
	Ref        string
	After      string
}

// pushPayload is the subset of GitHub's push payload that nodr reads.
type pushPayload struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
			Name  string `json:"name"`
		} `json:"owner"`
	} `json:"repository"`
}

// ParsePushEvent parses a GitHub push webhook body. The owner is the owner
// login, or the owner name when the login is absent.
func ParsePushEvent(deliveryID string, body []byte) (PushEvent, error) {
	var p pushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return PushEvent{}, fmt.Errorf("ledger: decode push payload: %w", err)
	}
	owner := p.Repository.Owner.Login
	if owner == "" {
		owner = p.Repository.Owner.Name
	}
	if owner == "" || p.Repository.Name == "" || p.Ref == "" || p.After == "" {
		return PushEvent{}, errors.New("ledger: push payload is missing ref, after, or repository")
	}
	return PushEvent{
		DeliveryID: deliveryID,
		Owner:      owner,
		Repository: p.Repository.Name,
		Ref:        p.Ref,
		After:      p.After,
	}, nil
}

// HandlePush reacts to a push. The API verifies the signature first. An unknown
// repository, a repeated delivery, or an unrelated ref is ignored. The delivery
// is recorded last so a failed push can be retried.
func (s *Service) HandlePush(ctx context.Context, ev PushEvent) error {
	md, ok, err := s.pushTarget(ctx, ev)
	if err != nil || !ok {
		return err
	}
	seen, err := s.deps.Meta.DeliverySeen(ctx, ev.DeliveryID)
	if err != nil {
		return fmt.Errorf("ledger: check delivery %s: %w", ev.DeliveryID, err)
	}
	if seen {
		return nil
	}
	switch {
	case ev.Ref == "refs/heads/"+md.DefaultBranch:
		if err := s.processDefaultPush(ctx, md, ev.After); err != nil {
			return err
		}
	case strings.HasPrefix(ev.Ref, draftRefPrefix):
		if _, err := s.deps.Repo.Fetch(ctx, md.Slug); err != nil {
			return fmt.Errorf("ledger: fetch %s: %w", md.Slug, err)
		}
	default:
		return nil
	}
	if err := s.deps.Meta.RecordDelivery(ctx, ev.DeliveryID); err != nil {
		return fmt.Errorf("ledger: record delivery %s: %w", ev.DeliveryID, err)
	}
	return nil
}

// pushTarget returns the metadata of the workspace a push belongs to. ok is
// false for an unknown repository.
func (s *Service) pushTarget(ctx context.Context, ev PushEvent) (repository.WorkspaceMetadata, bool, error) {
	list, err := s.deps.Meta.List(ctx)
	if err != nil {
		return repository.WorkspaceMetadata{}, false, fmt.Errorf("ledger: list workspaces: %w", err)
	}
	for _, md := range list {
		if md.Owner == ev.Owner && md.Repository == ev.Repository {
			return md, true, nil
		}
	}
	return repository.WorkspaceMetadata{}, false, nil
}

// processDefaultPush fetches the default branch and, when After is new, compiles
// it. Changed output becomes a server-owned sync changeset.
func (s *Service) processDefaultPush(ctx context.Context, md repository.WorkspaceMetadata, after string) error {
	if _, err := s.deps.Repo.Fetch(ctx, md.Slug); err != nil {
		return fmt.Errorf("ledger: fetch %s: %w", md.Slug, err)
	}
	if after == md.LastProcessedSHA {
		return nil
	}
	if err := s.createSyncDraft(ctx, md.Slug, after); err != nil {
		return err
	}
	if err := s.deps.Meta.SetLastProcessed(ctx, md.Slug, after); err != nil {
		return fmt.Errorf("ledger: record processed %s: %w", after, err)
	}
	return nil
}

// createSyncDraft checks out the pushed default-branch commit, and if compile
// would change files there, opens a sync changeset that carries those changes.
func (s *Service) createSyncDraft(ctx context.Context, slug, after string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	checkout, err := s.deps.Repo.Checkout(ctx, slug, after)
	if err != nil {
		return fmt.Errorf("ledger: checkout %s: %w", after, err)
	}
	defer func() { _ = checkout.Close() }()
	changed, err := compileWorkspace(checkout.Path(), false)
	if err != nil {
		return err
	}
	if len(changed) == 0 {
		return nil
	}
	draft, err := s.deps.Repo.CreateDraft(ctx, slug, after, "nodr: sync", "nodr")
	if err != nil {
		return fmt.Errorf("ledger: create sync draft: %w", err)
	}
	cs, err := s.deps.Ops.CreateChangeset(ctx, operations.Changeset{
		ID:        path.Base(draft.Ref),
		Workspace: slug,
		BaseSHA:   after,
		DraftSHA:  draft.HeadSHA,
	})
	if err != nil {
		return fmt.Errorf("ledger: create sync changeset: %w", err)
	}
	if err := s.commitSync(ctx, slug, draft, cs); err != nil {
		_, _ = s.deps.Ops.TransitionChangeset(ctx, cs.ID, operations.ChangesetAbandoned)
		return err
	}
	return nil
}

// commitSync writes the compiled changes onto the sync draft, moves its ref, and
// advances the changeset to the new commit.
func (s *Service) commitSync(ctx context.Context, slug string, draft repository.Draft, cs operations.Changeset) error {
	checkout, err := s.deps.Repo.Checkout(ctx, slug, draft.HeadSHA)
	if err != nil {
		return fmt.Errorf("ledger: checkout sync draft: %w", err)
	}
	commit, err := s.deps.Repo.Commit(ctx, checkout, repository.CommitInput{
		Message:     "nodr: sync",
		Author:      "nodr",
		ExpectedSHA: draft.HeadSHA,
		Mutate: func(root string) error {
			_, err := compileWorkspace(root, true)
			return err
		},
	})
	if err != nil {
		return fmt.Errorf("ledger: commit sync: %w", err)
	}
	if err := s.deps.Repo.CompareAndSwap(ctx, slug, draft.Ref, draft.HeadSHA, commit.SHA); err != nil {
		return fmt.Errorf("ledger: move sync draft: %w", err)
	}
	if _, err := s.deps.Ops.AdvanceDraft(ctx, cs.ID, draft.HeadSHA, commit.SHA); err != nil {
		return fmt.Errorf("ledger: advance sync draft: %w", err)
	}
	return nil
}
