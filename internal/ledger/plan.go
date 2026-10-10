package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/operations"
	"github.com/centopw/nodr/internal/planapply"
	"github.com/centopw/nodr/internal/repository"
)

// planFailureLimit bounds the text stored as the result of a failed operation.
const planFailureLimit = 512

var errNoRunner = errors.New("ledger: no runner configured")

// PlanInput requests a plan of a changeset at the draft revision ExpectedSHA.
type PlanInput struct{ Workspace, ChangesetID, ExpectedSHA, Actor, IdempotencyKey string }

// ApplyInput requests applying the latest plan of a changeset.
type ApplyInput struct {
	Workspace, ChangesetID, ExpectedSHA, Actor, IdempotencyKey string
	AllowDestroy                                               bool
}

// Plan queues a plan of the changeset's draft and returns the queued
// operation. The plan runs in the background on the service lifetime context,
// so it outlives ctx. A repeated IdempotencyKey for the workspace returns the
// operation created first.
func (s *Service) Plan(ctx context.Context, in PlanInput) (operations.Operation, error) {
	if s.deps.Runner == nil {
		return operations.Operation{}, errNoRunner
	}
	scope := "plan:" + in.Workspace
	if op, found, err := s.planReplay(ctx, scope, in.IdempotencyKey); err != nil || found {
		return op, err
	}
	cs, err := s.planChangeset(ctx, in.Workspace, in.ChangesetID)
	if err != nil {
		return operations.Operation{}, err
	}
	if cs.DraftSHA != in.ExpectedSHA {
		return operations.Operation{}, fmt.Errorf("changeset %s: draft %s, expected %s: %w", cs.ID, cs.DraftSHA, in.ExpectedSHA, ErrStaleRevision)
	}
	id := nrm.NewUID()
	op, err := s.deps.Ops.ClaimOperation(ctx, operations.Operation{ID: id, ChangesetID: cs.ID, Kind: "plan", PlanSHA: cs.DraftSHA}, scope, in.IdempotencyKey)
	if err != nil {
		return operations.Operation{}, fmt.Errorf("ledger: claim plan: %w", err)
	}
	if op.ID != id {
		// A concurrent request claimed the same key first; it owns the run.
		return op, nil
	}
	s.wg.Add(1)
	go s.runPlan(op, in.Workspace, cs.ID)
	return op, nil
}

// Apply queues the apply of the changeset's latest plan and returns the queued
// operation. In the background it promotes the draft to the default branch and
// then applies the saved plan. A repeated IdempotencyKey for the workspace
// returns the operation created first.
func (s *Service) Apply(ctx context.Context, in ApplyInput) (operations.Operation, error) {
	if s.deps.Runner == nil {
		return operations.Operation{}, errNoRunner
	}
	scope := "apply:" + in.Workspace
	if op, found, err := s.planReplay(ctx, scope, in.IdempotencyKey); err != nil || found {
		return op, err
	}
	cs, err := s.planChangeset(ctx, in.Workspace, in.ChangesetID)
	if err != nil {
		return operations.Operation{}, err
	}
	if cs.DraftSHA != in.ExpectedSHA {
		return operations.Operation{}, fmt.Errorf("changeset %s: draft %s, expected %s: %w", cs.ID, cs.DraftSHA, in.ExpectedSHA, ErrStaleRevision)
	}
	planOp, art, err := s.deps.Ops.LatestPlan(ctx, cs.ID)
	if errors.Is(err, operations.ErrNotFound) {
		return operations.Operation{}, fmt.Errorf("changeset %s: %w", cs.ID, ErrPlanRequired)
	}
	if err != nil {
		return operations.Operation{}, fmt.Errorf("ledger: load latest plan: %w", err)
	}
	if art.DraftSHA != cs.DraftSHA {
		return operations.Operation{}, fmt.Errorf("changeset %s: plan of %s, draft %s: %w", cs.ID, art.DraftSHA, cs.DraftSHA, ErrPlanChanged)
	}
	if cs.Status != operations.ChangesetPlanned {
		return operations.Operation{}, fmt.Errorf("changeset %s: %s: %w", cs.ID, cs.Status, ErrPlanRequired)
	}
	sum, err := decodePlanSummary(art.JSON)
	if err != nil {
		return operations.Operation{}, err
	}
	if sum.HasDestructive && !in.AllowDestroy {
		return operations.Operation{}, fmt.Errorf("changeset %s: %w", cs.ID, ErrDestructiveNeedsApproval)
	}
	id := nrm.NewUID()
	op, err := s.deps.Ops.ClaimOperation(ctx, operations.Operation{ID: id, ChangesetID: cs.ID, Kind: "apply", PlanSHA: cs.DraftSHA}, scope, in.IdempotencyKey)
	if err != nil {
		return operations.Operation{}, fmt.Errorf("ledger: claim apply: %w", err)
	}
	if op.ID != id {
		return op, nil
	}
	s.wg.Add(1)
	go s.runApply(op, planOp, in.Workspace, cs.ID, in.AllowDestroy)
	return op, nil
}

// Operation returns a plan or apply operation by ID.
func (s *Service) Operation(ctx context.Context, id string) (operations.Operation, error) {
	return s.deps.Ops.Operation(ctx, id)
}

// PlanSummary returns the normalized plan stored for a plan operation. It wraps
// operations.ErrNotFound when the operation has no plan artifact.
func (s *Service) PlanSummary(ctx context.Context, operationID string) (PlanSummary, error) {
	art, err := s.deps.Ops.PlanArtifact(ctx, operationID)
	if err != nil {
		return PlanSummary{}, fmt.Errorf("ledger: plan summary: %w", err)
	}
	return decodePlanSummary(art.JSON)
}

// Subscribe returns a stream of operation and changeset events and a function
// that cancels the subscription.
func (s *Service) Subscribe() (<-chan operations.Event, func()) {
	return s.deps.Ops.Subscribe()
}

// EventsAfter returns persisted events with IDs greater than afterID.
func (s *Service) EventsAfter(ctx context.Context, afterID int64) ([]operations.Event, error) {
	return s.deps.Ops.EventsAfter(ctx, afterID)
}

// runPlan executes a queued plan operation on the service context. It must
// run as a goroutine after s.wg.Add(1).
func (s *Service) runPlan(op operations.Operation, slug, changesetID string) {
	defer s.wg.Done()
	ctx := s.ctx
	if _, err := s.deps.Ops.TransitionOperation(ctx, op.ID, operations.OperationRunning); err != nil {
		return
	}
	co, err := s.deps.Repo.Checkout(ctx, slug, op.PlanSHA)
	if err != nil {
		s.failOperation(op.ID, planFailureText("checkout draft", err))
		return
	}
	var resolve planapply.Resolver
	if s.deps.Secrets != nil {
		resolve = s.deps.Secrets.Resolve
	}
	res, err := s.deps.Runner.Plan(ctx, PlanRequest{OperationID: op.ID, Workspace: slug, DraftSHA: op.PlanSHA, Root: co.Path(), Checkout: co}, resolve)
	if err != nil {
		_ = co.Close()
		s.deps.Runner.Discard(op.ID)
		s.failOperation(op.ID, planFailureText("plan", err))
		return
	}
	if _, err := s.deps.Ops.PutPlanArtifact(ctx, operations.PlanArtifact{OperationID: op.ID, DraftSHA: op.PlanSHA, JSON: res.JSON}); err != nil {
		_ = co.Close()
		s.deps.Runner.Discard(op.ID)
		s.failOperation(op.ID, planFailureText("store plan", err))
		return
	}
	if _, err := s.deps.Ops.FinishOperation(ctx, op.ID, operations.OperationSucceeded, planSummaryText(res)); err != nil {
		return
	}
	// A draft that moved during the plan refuses this transition; the stored
	// artifact no longer matches, so Apply reports ErrPlanChanged.
	_, _ = s.deps.Ops.TransitionChangeset(ctx, changesetID, operations.ChangesetPlanned)
}

// runApply executes a queued apply operation on the service context. It must
// run as a goroutine after s.wg.Add(1). The retained plan is discarded only
// after a successful apply.
func (s *Service) runApply(op, planOp operations.Operation, slug, changesetID string, allowDestroy bool) {
	defer s.wg.Done()
	ctx := s.ctx
	if _, err := s.deps.Ops.TransitionOperation(ctx, op.ID, operations.OperationRunning); err != nil {
		return
	}
	draft := op.PlanSHA
	if !s.deps.Runner.Has(planOp.ID) {
		s.failOperation(op.ID, "plan expired; plan again (nothing was promoted or applied; the changeset stays planned)")
		return
	}
	s.mu.Lock()
	ws, err := s.deps.Repo.Workspace(ctx, slug)
	if err != nil {
		s.mu.Unlock()
		s.failOperation(op.ID, planFailureText("read default branch", err))
		return
	}
	err = s.deps.Repo.Promote(ctx, slug, draft, ws.DefaultSHA)
	s.mu.Unlock()
	if err != nil {
		s.failOperation(op.ID, promoteFailureText(err))
		return
	}
	err = s.deps.Runner.Apply(ctx, ApplyRequest{PlanOperationID: planOp.ID, AllowDestroy: allowDestroy})
	if err != nil {
		if errors.Is(err, ErrPlanExpired) {
			s.failOperation(op.ID, fmt.Sprintf("plan expired; plan again (default branch was already promoted to %s; nothing was applied and the changeset stays planned)", draft))
			return
		}
		s.failOperation(op.ID, truncateText(fmt.Sprintf("default branch promoted to %s before apply; apply failed: %s", draft, err.Error())))
		return
	}
	s.deps.Runner.Discard(planOp.ID)
	_, _ = s.deps.Ops.FinishOperation(ctx, op.ID, operations.OperationSucceeded, "applied "+draft+" to the default branch")
	_, _ = s.deps.Ops.TransitionChangeset(ctx, changesetID, operations.ChangesetApplied)
}

// planReplay returns the operation already claimed under scope and key, if any.
func (s *Service) planReplay(ctx context.Context, scope, key string) (operations.Operation, bool, error) {
	op, err := s.deps.Ops.OperationByKey(ctx, scope, key)
	if err == nil {
		return op, true, nil
	}
	if errors.Is(err, operations.ErrNotFound) {
		return operations.Operation{}, false, nil
	}
	return operations.Operation{}, false, fmt.Errorf("ledger: look up operation: %w", err)
}

// planChangeset loads a changeset of slug that is open or planned.
func (s *Service) planChangeset(ctx context.Context, slug, id string) (operations.Changeset, error) {
	cs, err := s.deps.Ops.Changeset(ctx, id)
	if errors.Is(err, operations.ErrNotFound) {
		return operations.Changeset{}, fmt.Errorf("changeset %s: %w", id, ErrChangesetNotFound)
	}
	if err != nil {
		return operations.Changeset{}, fmt.Errorf("ledger: load changeset: %w", err)
	}
	if cs.Workspace != slug {
		return operations.Changeset{}, fmt.Errorf("changeset %s in workspace %s: %w", id, slug, ErrChangesetNotFound)
	}
	if _, err := s.deps.Repo.Workspace(ctx, slug); err != nil {
		return operations.Changeset{}, fmt.Errorf("workspace %s: %w: %w", slug, ErrWorkspaceNotFound, err)
	}
	if cs.Status != operations.ChangesetOpen && cs.Status != operations.ChangesetPlanned {
		return operations.Changeset{}, fmt.Errorf("changeset %s: %s: %w", id, cs.Status, ErrChangesetClosed)
	}
	return cs, nil
}

// failOperation records a failed operation with text as its result.
func (s *Service) failOperation(id, text string) {
	_, _ = s.deps.Ops.FinishOperation(s.ctx, id, operations.OperationFailed, text)
}

// decodePlanSummary decodes a stored plan artifact.
func decodePlanSummary(data []byte) (PlanSummary, error) {
	var sum PlanSummary
	if err := json.Unmarshal(data, &sum); err != nil {
		return PlanSummary{}, fmt.Errorf("ledger: decode plan summary: %w", err)
	}
	return sum, nil
}

// planSummaryText is the one-line result recorded on a succeeded plan.
func planSummaryText(res PlanResult) string {
	return fmt.Sprintf("plan: hasChanges=%t hasDestructive=%t", res.HasChanges, res.HasDestructive)
}

// planFailureText is the bounded failure result of a plan step.
func planFailureText(step string, err error) string {
	return truncateText(step + ": " + err.Error())
}

// promoteFailureText explains why promoting the default branch failed. Nothing
// is applied when promotion fails.
func promoteFailureText(err error) string {
	switch {
	case errors.Is(err, repository.ErrStaleRevision):
		return "default branch moved since the plan; nothing applied; plan again"
	case errors.Is(err, repository.ErrNotFastForward):
		return "draft is not a fast-forward of the default branch; nothing applied"
	default:
		return truncateText("promote default branch failed; nothing applied: " + err.Error())
	}
}

// truncateText bounds s to planFailureLimit runes.
func truncateText(s string) string {
	r := []rune(s)
	if len(r) <= planFailureLimit {
		return s
	}
	return string(r[:planFailureLimit]) + "..."
}
