package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/centopw/nodr/internal/commands"
	"github.com/centopw/nodr/internal/compile"
	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/operations"
	"github.com/centopw/nodr/internal/repository"
	"github.com/centopw/nodr/internal/workspace"
)

// CommandInput is one command request against a workspace.
type CommandInput struct {
	Workspace      string
	ChangesetID    string
	ExpectedSHA    string
	Actor          string
	Command        string
	IdempotencyKey string
	Params         json.RawMessage
}

// CommandResult is the outcome of a command: the changeset after the commit,
// the diff against the workspace base, and the command's own result.
type CommandResult struct {
	Changeset operations.Changeset
	Diff      []FileDiff
	Result    any
}

// FileDiff is one changed file of a changeset.
type FileDiff = repository.FileDiff

// ChangesetDetail is a changeset with the diff from its base to its draft.
type ChangesetDetail struct {
	operations.Changeset
	Diff []FileDiff
}

// RunCommand applies a command as one commit on a changeset draft.
//
// An empty ChangesetID creates a new changeset from the default-branch HEAD;
// ExpectedSHA must then equal that HEAD. Otherwise ExpectedSHA must equal the
// changeset's draft SHA. A repeated IdempotencyKey for the same workspace
// returns the recorded result without a second commit.
func (s *Service) RunCommand(ctx context.Context, in CommandInput) (CommandResult, error) {
	scope := "cmd:" + in.Workspace
	prior, err := s.deps.Ops.OperationByKey(ctx, scope, in.IdempotencyKey)
	switch {
	case err == nil:
		return s.replay(in, prior)
	case !errors.Is(err, operations.ErrNotFound):
		return CommandResult{}, fmt.Errorf("ledger: look up command %s: %w", in.IdempotencyKey, err)
	}

	ws, err := s.Workspace(ctx, in.Workspace)
	if err != nil {
		return CommandResult{}, err
	}
	target, err := s.prepareTarget(ctx, in, ws)
	if err != nil {
		return CommandResult{}, err
	}

	op, err := s.deps.Ops.ClaimOperation(ctx, operations.Operation{
		ID:          nrm.NewUID(),
		ChangesetID: target.cs.ID,
		Kind:        "command",
	}, scope, in.IdempotencyKey)
	if err != nil {
		s.discardNew(target)
		return CommandResult{}, fmt.Errorf("ledger: claim command: %w", err)
	}
	if op.Status != operations.OperationQueued {
		// Another request claimed this key after our lookup; replay its outcome.
		s.discardNew(target)
		return s.replay(in, op)
	}
	if _, err := s.deps.Ops.TransitionOperation(ctx, op.ID, operations.OperationRunning); err != nil {
		s.discardNew(target)
		return CommandResult{}, fmt.Errorf("ledger: start command: %w", err)
	}

	result, newSHA, err := s.commit(ctx, in, target)
	if err != nil {
		s.fail(op.ID, err)
		s.discardNew(target)
		return CommandResult{}, err
	}
	cs, err := s.deps.Ops.AdvanceDraft(ctx, target.cs.ID, target.oldSHA, newSHA)
	if err != nil {
		s.fail(op.ID, err)
		return CommandResult{}, fmt.Errorf("ledger: advance draft: %w", err)
	}
	diff, err := s.deps.Repo.Diff(ctx, in.Workspace, target.cs.BaseSHA, newSHA)
	if err != nil {
		s.fail(op.ID, err)
		return CommandResult{}, fmt.Errorf("ledger: diff draft: %w", err)
	}
	out := CommandResult{Changeset: cs, Diff: diff, Result: result}
	if err := s.finish(ctx, op.ID, out); err != nil {
		return CommandResult{}, err
	}
	return out, nil
}

// commandTarget is the changeset and draft ref a command commits to.
type commandTarget struct {
	cs      operations.Changeset
	ref     string
	oldSHA  string
	created bool
}

// prepareTarget resolves or creates the changeset a command commits to and
// checks the caller's expected revision against it.
func (s *Service) prepareTarget(ctx context.Context, in CommandInput, ws Workspace) (commandTarget, error) {
	if in.ChangesetID == "" {
		if in.ExpectedSHA != ws.DefaultSHA {
			return commandTarget{}, ErrStaleRevision
		}
		draft, err := s.deps.Repo.CreateDraft(ctx, in.Workspace, ws.DefaultSHA, in.Command, in.Actor)
		if err != nil {
			return commandTarget{}, fmt.Errorf("ledger: create draft: %w", err)
		}
		cs, err := s.deps.Ops.CreateChangeset(ctx, operations.Changeset{
			ID:        path.Base(draft.Ref),
			Workspace: in.Workspace,
			BaseSHA:   ws.DefaultSHA,
			DraftSHA:  draft.HeadSHA,
		})
		if err != nil {
			return commandTarget{}, fmt.Errorf("ledger: create changeset: %w", err)
		}
		return commandTarget{cs: cs, ref: draft.Ref, oldSHA: draft.HeadSHA, created: true}, nil
	}
	cs, err := s.changeset(ctx, in.Workspace, in.ChangesetID)
	if err != nil {
		return commandTarget{}, err
	}
	if cs.Status != operations.ChangesetOpen {
		return commandTarget{}, fmt.Errorf("changeset %s is %s: %w", cs.ID, cs.Status, ErrChangesetClosed)
	}
	if in.ExpectedSHA != cs.DraftSHA {
		return commandTarget{}, ErrStaleRevision
	}
	return commandTarget{cs: cs, ref: "refs/heads/nodr/drafts/" + cs.ID, oldSHA: cs.DraftSHA}, nil
}

// commit runs the command on a checkout of the target draft, compiles and
// writes the workspace, commits, and moves the draft ref to the new commit.
func (s *Service) commit(ctx context.Context, in CommandInput, target commandTarget) (any, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	checkout, err := s.deps.Repo.Checkout(ctx, in.Workspace, target.oldSHA)
	if err != nil {
		return nil, "", fmt.Errorf("ledger: checkout draft: %w", err)
	}
	var result any
	commit, err := s.deps.Repo.Commit(ctx, checkout, repository.CommitInput{
		Message:     fmt.Sprintf("nodr: %s %s", in.Actor, in.Command),
		Author:      in.Actor,
		ExpectedSHA: target.oldSHA,
		Mutate: func(root string) error {
			r, err := commands.Run(root, in.Command, in.Params)
			if err != nil {
				return &CommandError{Err: asCommandError(err)}
			}
			result = r
			if _, err := compileWorkspace(root, true); err != nil {
				return commandFailure(err)
			}
			return nil
		},
	})
	if err != nil {
		if errors.Is(err, repository.ErrStaleRevision) {
			return nil, "", ErrStaleRevision
		}
		var ce *CommandError
		if errors.As(err, &ce) {
			return nil, "", ce
		}
		return nil, "", fmt.Errorf("ledger: commit %s: %w", in.Command, err)
	}
	if err := s.deps.Repo.CompareAndSwap(ctx, in.Workspace, target.ref, target.oldSHA, commit.SHA); err != nil {
		if errors.Is(err, repository.ErrStaleRevision) {
			return nil, "", ErrStaleRevision
		}
		return nil, "", fmt.Errorf("ledger: move draft: %w", err)
	}
	return result, commit.SHA, nil
}

// compileWorkspace loads and validates the workspace at root, compiles it, and
// writes the changed files when write is true. It returns the changed file
// paths. Validation or compile errors are returned as a diagnostic list.
func compileWorkspace(root string, write bool) ([]string, error) {
	ws, diags := workspace.Load(root)
	if diags.HasErrors() {
		return nil, &diagError{diags: diags}
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		return nil, fmt.Errorf("ledger: build registry: %w", err)
	}
	diags.Append(ws.Validate(reg))
	if diags.HasErrors() {
		return nil, &diagError{diags: diags}
	}
	files, _, cdiags := compile.Compile(ws)
	if cdiags.HasErrors() {
		return nil, &diagError{diags: cdiags}
	}
	changed := make([]string, 0, len(files))
	for p, content := range files {
		existing, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err == nil && bytes.Equal(existing, content) {
			continue
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("ledger: read compiled unit %s: %w", p, err)
		}
		changed = append(changed, p)
	}
	sort.Strings(changed)
	if write && len(changed) > 0 {
		changedFiles := make(map[string][]byte, len(changed))
		for _, p := range changed {
			changedFiles[p] = files[p]
		}
		if err := compile.Write(ws, changedFiles); err != nil {
			return nil, fmt.Errorf("ledger: write compiled units: %w", err)
		}
	}
	return changed, nil
}

// diagError carries error diagnostics from validation or compilation.
type diagError struct {
	diags diag.List
}

// Error implements the error interface.
func (e *diagError) Error() string {
	return e.diags.Err().Error()
}

// commandFailure converts a compile or validation failure into a 422
// CommandError with one field per located error diagnostic. Other errors pass
// through unchanged.
func commandFailure(err error) error {
	var de *diagError
	if !errors.As(err, &de) {
		return err
	}
	fields := make([]commands.FieldError, 0, len(de.diags))
	for _, d := range de.diags {
		if d.Severity != diag.Error {
			continue
		}
		fields = append(fields, commands.FieldError{Path: d.Path, Message: d.Message})
	}
	return &CommandError{Err: &commands.Error{
		Status: http.StatusUnprocessableEntity,
		Title:  "Workspace validation failed",
		Detail: de.Error(),
		Fields: fields,
	}}
}

// asCommandError returns err's *commands.Error, or wraps any other error as a 500.
func asCommandError(err error) *commands.Error {
	var ce *commands.Error
	if errors.As(err, &ce) {
		return ce
	}
	return &commands.Error{Status: 500, Title: "Command failed", Detail: err.Error()}
}

// discardNew abandons a changeset created by a command that never reached its
// commit, so no empty draft stays open.
func (s *Service) discardNew(target commandTarget) {
	if !target.created {
		return
	}
	_, _ = s.deps.Ops.TransitionChangeset(s.ctx, target.cs.ID, operations.ChangesetAbandoned)
}

// fail marks a running command operation failed with a redacted message.
func (s *Service) fail(opID string, cause error) {
	_, _ = s.deps.Ops.FinishOperation(s.ctx, opID, operations.OperationFailed, redactMessage(cause))
}

// finish marks a running command operation succeeded and stores its result as JSON.
func (s *Service) finish(ctx context.Context, opID string, out CommandResult) error {
	raw, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("ledger: encode command result: %w", err)
	}
	if _, err := s.deps.Ops.FinishOperation(ctx, opID, operations.OperationSucceeded, string(raw)); err != nil {
		return fmt.Errorf("ledger: finish command: %w", err)
	}
	return nil
}

// replay returns the outcome recorded for a command already claimed under the same key.
func (s *Service) replay(in CommandInput, op operations.Operation) (CommandResult, error) {
	switch op.Status {
	case operations.OperationSucceeded:
		var out CommandResult
		if err := json.Unmarshal([]byte(op.Result), &out); err != nil {
			return CommandResult{}, fmt.Errorf("ledger: decode recorded command result: %w", err)
		}
		return out, nil
	case operations.OperationFailed:
		return CommandResult{}, &CommandError{Err: &commands.Error{Status: 409, Title: "Command failed", Detail: op.Result}}
	default:
		return CommandResult{}, fmt.Errorf("command %s: %w", in.IdempotencyKey, ErrCommandInFlight)
	}
}

// redactMessage returns a user-safe description of cause for the operation record.
func redactMessage(cause error) string {
	var ce *CommandError
	if errors.As(cause, &ce) {
		return ce.Err.Error()
	}
	return "command failed"
}

// Changesets lists a workspace's changesets, newest first.
func (s *Service) Changesets(ctx context.Context, slug string) ([]operations.Changeset, error) {
	if _, err := s.Workspace(ctx, slug); err != nil {
		return nil, err
	}
	list, err := s.deps.Ops.Changesets(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("ledger: list changesets: %w", err)
	}
	return list, nil
}

// Changeset returns a changeset with its diff from the base to its draft.
func (s *Service) Changeset(ctx context.Context, slug, id string) (ChangesetDetail, error) {
	cs, err := s.changeset(ctx, slug, id)
	if err != nil {
		return ChangesetDetail{}, err
	}
	diff, err := s.deps.Repo.Diff(ctx, slug, cs.BaseSHA, cs.DraftSHA)
	if err != nil {
		return ChangesetDetail{}, fmt.Errorf("ledger: diff changeset %s: %w", id, err)
	}
	return ChangesetDetail{Changeset: cs, Diff: diff}, nil
}
