package ledger

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	github "github.com/google/go-github/v75/github"

	"github.com/centopw/nodr/internal/operations"
	"github.com/centopw/nodr/internal/repository"
)

// planNoGitHub is a GitHub surface that is never reached by plan and apply tests.
type planNoGitHub struct{}

func (planNoGitHub) ListInstallations(context.Context) ([]*github.Installation, error) {
	return nil, nil
}

func (planNoGitHub) ListRepositories(context.Context, int64) ([]*github.Repository, error) {
	return nil, nil
}

func (planNoGitHub) VerifyWebhookSignature([]byte, string) error { return nil }

func (planNoGitHub) GitAuth(context.Context, int64) (githttp.BasicAuth, error) {
	return githttp.BasicAuth{}, nil
}

// planFailingApply forwards plan and discard to the real runner but fails the
// apply, to exercise a failure after the default branch was promoted.
type planFailingApply struct{ Runner }

func (planFailingApply) Apply(context.Context, ApplyRequest) error {
	return errors.New("tofu exploded")
}

// planVMWithCores returns the test VM with its CPU core count replaced.
func planVMWithCores(cores string) string {
	return strings.Replace(runnerVM, "cores: 2", "cores: "+cores, 1)
}

// planOrigin creates a bare origin whose main branch holds the test workspace
// and returns its path.
func planOrigin(t *testing.T) string {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	bare, err := git.PlainInit(origin, true)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := git.PlainInit(runnerWorkspace(t), false)
	if err != nil {
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
	return origin
}

// planOriginMain returns the commit that origin's main branch points at.
func planOriginMain(t *testing.T, origin string) string {
	t.Helper()
	repo, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	return ref.Hash().String()
}

// planEnv is a real Service over a local origin, real stores, and the fake tofu.
type planEnv struct {
	svc    *Service
	repo   *repository.LocalStore
	ops    *operations.Store
	origin string
	tofu   *runnerFakeTofu
}

// newPlanEnv builds the environment. The fake tofu initially plans one create.
func newPlanEnv(t *testing.T, runner Runner) *planEnv {
	t.Helper()
	ctx := t.Context()
	tofu := runnerInstallFakeTofu(t)
	tofu.runnerSetPlan(t, "create proxmox_virtual_environment_vm.web_01")
	origin := planOrigin(t)
	repo, err := repository.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateWorkspace(ctx, repository.CreateWorkspaceInput{Slug: "homelab", Owner: "acme", Repository: origin, DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	ops, err := operations.Open(filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ops.Close() })
	meta, err := repository.OpenMetadataStore(filepath.Join(t.TempDir(), "metadata.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(ctx, Deps{Repo: repo, Meta: meta, Ops: ops, GitHub: planNoGitHub{}, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return &planEnv{svc: svc, repo: repo, ops: ops, origin: origin, tofu: tofu}
}

// settle waits for background plan and apply operations.
func (e *planEnv) settle(t *testing.T) {
	t.Helper()
	if err := e.svc.Close(); err != nil {
		t.Fatal(err)
	}
}

// commit commits the test VM with cores on top of base in the workspace draft
// and returns the new commit SHA.
func (e *planEnv) commit(t *testing.T, base, cores string) string {
	t.Helper()
	ctx := t.Context()
	co, err := e.repo.Checkout(ctx, "homelab", base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = co.Close() }()
	c, err := e.repo.Commit(ctx, co, repository.CommitInput{
		Message:     "scale web-01",
		Author:      "Ada",
		ExpectedSHA: base,
		Mutate: func(root string) error {
			return os.WriteFile(filepath.Join(root, "intent", "compute", "web-01.yaml"), []byte(planVMWithCores(cores)), 0o644)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.SHA
}

// newChangeset creates an open changeset whose draft is one commit above main.
func (e *planEnv) newChangeset(t *testing.T) operations.Changeset {
	t.Helper()
	ctx := t.Context()
	base, err := e.repo.Workspace(ctx, "homelab")
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.repo.CreateDraft(ctx, "homelab", base.DefaultSHA, "scale web-01", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	head := e.commit(t, d.HeadSHA, "3")
	// Advancing the draft ref is what brings the commit object into the mirror,
	// exactly as RunCommand does.
	if err := e.repo.CompareAndSwap(ctx, "homelab", d.Ref, d.HeadSHA, head); err != nil {
		t.Fatal(err)
	}
	cs, err := e.ops.CreateChangeset(ctx, operations.Changeset{ID: path.Base(d.Ref), Workspace: "homelab", BaseSHA: base.DefaultSHA, DraftSHA: head})
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// plan queues a plan of cs at its current draft.
func (e *planEnv) plan(t *testing.T, cs operations.Changeset, key string) operations.Operation {
	t.Helper()
	op, err := e.svc.Plan(t.Context(), PlanInput{Workspace: "homelab", ChangesetID: cs.ID, ExpectedSHA: cs.DraftSHA, IdempotencyKey: key})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return op
}

// applyAt queues an apply of cs with ExpectedSHA expected.
func (e *planEnv) applyAt(cs operations.Changeset, expected, key string) (operations.Operation, error) {
	return e.svc.Apply(context.Background(), ApplyInput{Workspace: "homelab", ChangesetID: cs.ID, ExpectedSHA: expected, IdempotencyKey: key})
}

func (e *planEnv) operation(t *testing.T, id string) operations.Operation {
	t.Helper()
	op, err := e.ops.Operation(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func (e *planEnv) changesetStatus(t *testing.T, id string) operations.ChangesetStatus {
	t.Helper()
	cs, err := e.ops.Changeset(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return cs.Status
}

func TestPlanSucceedsStoresArtifact(t *testing.T) {
	e := newPlanEnv(t, &LocalRunner{Dir: t.TempDir()})
	cs := e.newChangeset(t)
	op := e.plan(t, cs, "plan-1")
	e.settle(t)

	got := e.operation(t, op.ID)
	if got.Status != operations.OperationSucceeded {
		t.Fatalf("plan status = %s, result %q", got.Status, got.Result)
	}
	if st := e.changesetStatus(t, cs.ID); st != operations.ChangesetPlanned {
		t.Fatalf("changeset status = %s, want planned", st)
	}
	sum, err := e.svc.PlanSummary(t.Context(), op.ID)
	if err != nil {
		t.Fatalf("PlanSummary: %v", err)
	}
	if !sum.HasChanges || sum.HasDestructive || len(sum.Units) != 1 || sum.Units[0].Create != 1 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestPlanStaleExpectedSHA(t *testing.T) {
	e := newPlanEnv(t, &LocalRunner{Dir: t.TempDir()})
	cs := e.newChangeset(t)
	_, err := e.svc.Plan(t.Context(), PlanInput{Workspace: "homelab", ChangesetID: cs.ID, ExpectedSHA: planOriginMain(t, e.origin), IdempotencyKey: "plan-stale"})
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("Plan err = %v, want ErrStaleRevision", err)
	}
}

func TestPlanIdempotentReplayReturnsSameOperation(t *testing.T) {
	e := newPlanEnv(t, &LocalRunner{Dir: t.TempDir()})
	cs := e.newChangeset(t)
	first := e.plan(t, cs, "plan-same")
	second := e.plan(t, cs, "plan-same")
	e.settle(t)
	if first.ID != second.ID {
		t.Fatalf("replayed plan ID = %s, want %s", second.ID, first.ID)
	}
}

func TestPlanOutlivesRequestContext(t *testing.T) {
	e := newPlanEnv(t, &LocalRunner{Dir: t.TempDir()})
	cs := e.newChangeset(t)
	ctx, cancel := context.WithCancel(t.Context())
	op, err := e.svc.Plan(ctx, PlanInput{Workspace: "homelab", ChangesetID: cs.ID, ExpectedSHA: cs.DraftSHA, IdempotencyKey: "plan-cancel"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	cancel()
	e.settle(t)
	if got := e.operation(t, op.ID); got.Status != operations.OperationSucceeded {
		t.Fatalf("plan status after request cancel = %s, result %q", got.Status, got.Result)
	}
}

func TestApplyBeforePlanNeedsPlan(t *testing.T) {
	e := newPlanEnv(t, &LocalRunner{Dir: t.TempDir()})
	cs := e.newChangeset(t)
	_, err := e.applyAt(cs, cs.DraftSHA, "apply-early")
	if !errors.Is(err, ErrPlanRequired) {
		t.Fatalf("Apply err = %v, want ErrPlanRequired", err)
	}
}

func TestApplyAfterDraftAdvancedIsPlanChanged(t *testing.T) {
	e := newPlanEnv(t, &LocalRunner{Dir: t.TempDir()})
	cs := e.newChangeset(t)
	e.plan(t, cs, "plan-adv")
	e.settle(t)

	next := e.commit(t, cs.DraftSHA, "4")
	if _, err := e.ops.AdvanceDraft(t.Context(), cs.ID, cs.DraftSHA, next); err != nil {
		t.Fatalf("AdvanceDraft: %v", err)
	}
	_, err := e.applyAt(cs, next, "apply-adv")
	if !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("Apply err = %v, want ErrPlanChanged", err)
	}
}

func TestApplyDestructiveNeedsApproval(t *testing.T) {
	e := newPlanEnv(t, &LocalRunner{Dir: t.TempDir()})
	e.tofu.runnerSetPlan(t, "delete proxmox_virtual_environment_vm.web_01")
	cs := e.newChangeset(t)
	e.plan(t, cs, "plan-del")
	e.settle(t)

	_, err := e.applyAt(cs, cs.DraftSHA, "apply-del")
	if !errors.Is(err, ErrDestructiveNeedsApproval) {
		t.Fatalf("Apply err = %v, want ErrDestructiveNeedsApproval", err)
	}
	if got := planOriginMain(t, e.origin); got == cs.DraftSHA {
		t.Fatal("default branch moved although apply was refused")
	}
}

func TestApplyPromotesDefaultBranchAndMarksApplied(t *testing.T) {
	e := newPlanEnv(t, &LocalRunner{Dir: t.TempDir()})
	cs := e.newChangeset(t)
	e.plan(t, cs, "plan-ok")
	e.settle(t)

	op, err := e.applyAt(cs, cs.DraftSHA, "apply-ok")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	e.settle(t)
	if got := e.operation(t, op.ID); got.Status != operations.OperationSucceeded {
		t.Fatalf("apply status = %s, result %q", got.Status, got.Result)
	}
	if got := planOriginMain(t, e.origin); got != cs.DraftSHA {
		t.Fatalf("origin main = %s, want draft %s", got, cs.DraftSHA)
	}
	if st := e.changesetStatus(t, cs.ID); st != operations.ChangesetApplied {
		t.Fatalf("changeset status = %s, want applied", st)
	}
}

func TestApplyStaleDefaultFailsWithoutApplying(t *testing.T) {
	e := newPlanEnv(t, &LocalRunner{Dir: t.TempDir()})
	cs := e.newChangeset(t)
	e.plan(t, cs, "plan-stale-default")
	e.settle(t)

	// Move the default branch to a commit the planned draft does not contain.
	ctx := t.Context()
	mainBefore := planOriginMain(t, e.origin)
	other, err := e.repo.CreateDraft(ctx, "homelab", mainBefore, "other change", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	moved := e.commit(t, other.HeadSHA, "4")
	if err := e.repo.Promote(ctx, "homelab", moved, mainBefore); err != nil {
		t.Fatalf("Promote other draft: %v", err)
	}

	op, err := e.applyAt(cs, cs.DraftSHA, "apply-stale-default")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	e.settle(t)
	got := e.operation(t, op.ID)
	if got.Status != operations.OperationFailed || got.Result == "" {
		t.Fatalf("apply status = %s, result %q; want failed with text", got.Status, got.Result)
	}
	if main := planOriginMain(t, e.origin); main != moved {
		t.Fatalf("origin main = %s, want unchanged %s", main, moved)
	}
	if st := e.changesetStatus(t, cs.ID); st != operations.ChangesetPlanned {
		t.Fatalf("changeset status = %s, want planned", st)
	}
}

func TestApplyExpiredPlanDoesNotPromote(t *testing.T) {
	runner := &LocalRunner{Dir: t.TempDir()}
	e := newPlanEnv(t, runner)
	cs := e.newChangeset(t)
	planOp := e.plan(t, cs, "plan-expire")
	e.settle(t)
	mainBefore := planOriginMain(t, e.origin)
	runner.Discard(planOp.ID)

	op, err := e.applyAt(cs, cs.DraftSHA, "apply-expired")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	e.settle(t)
	got := e.operation(t, op.ID)
	if got.Status != operations.OperationFailed || !strings.Contains(got.Result, "plan expired") {
		t.Fatalf("apply status = %s, result %q; want failed with plan expired text", got.Status, got.Result)
	}
	if main := planOriginMain(t, e.origin); main != mainBefore {
		t.Fatalf("origin main = %s, want unchanged %s (expired plan must not promote)", main, mainBefore)
	}
	if st := e.changesetStatus(t, cs.ID); st != operations.ChangesetPlanned {
		t.Fatalf("changeset status = %s, want planned", st)
	}
}

func TestApplyFailureAfterPromoteSaysSo(t *testing.T) {
	runner := &LocalRunner{Dir: t.TempDir()}
	e := newPlanEnv(t, planFailingApply{Runner: runner})
	cs := e.newChangeset(t)
	e.plan(t, cs, "plan-fail")
	e.settle(t)

	op, err := e.applyAt(cs, cs.DraftSHA, "apply-fail")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	e.settle(t)
	got := e.operation(t, op.ID)
	if got.Status != operations.OperationFailed || !strings.Contains(got.Result, "promoted") {
		t.Fatalf("apply status = %s, result %q; want failed mentioning the promote", got.Status, got.Result)
	}
	if main := planOriginMain(t, e.origin); main != cs.DraftSHA {
		t.Fatalf("origin main = %s, want promoted draft %s", main, cs.DraftSHA)
	}
}
