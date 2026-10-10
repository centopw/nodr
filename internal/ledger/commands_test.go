package ledger

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/centopw/nodr/internal/commands"
	"github.com/centopw/nodr/internal/operations"
)

// vmCreateParams returns valid vm.create params for a named VM.
func vmCreateParams(name string) json.RawMessage {
	return json.RawMessage(`{"name":"` + name + `","environment":"prod","cluster":"pve-main","template":"debian-12-cloud","network":"dmz","storage":"local-lvm","size":"M"}`)
}

func runVMCreate(t *testing.T, env *testEnv, ws Workspace, changesetID, expected, key, name string) (CommandResult, error) {
	t.Helper()
	return env.svc.RunCommand(t.Context(), CommandInput{
		Workspace:      ws.Slug,
		ChangesetID:    changesetID,
		ExpectedSHA:    expected,
		Actor:          testActor,
		Command:        "vm.create",
		IdempotencyKey: key,
		Params:         vmCreateParams(name),
	})
}

func TestRunCommandCreatesChangesetAndPushesDraft(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	ws := env.connect(t)

	res, err := runVMCreate(t, env, ws, "", ws.DefaultSHA, "k1", "web-03")
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if res.Changeset.Status != operations.ChangesetOpen || res.Changeset.BaseSHA != ws.DefaultSHA {
		t.Fatalf("changeset = %+v, want open based on %s", res.Changeset, ws.DefaultSHA)
	}
	if res.Changeset.DraftSHA == ws.DefaultSHA || res.Changeset.DraftSHA == "" {
		t.Fatalf("draft SHA %q did not advance", res.Changeset.DraftSHA)
	}
	if len(res.Diff) == 0 {
		t.Fatal("expected a non-empty diff for vm.create")
	}
	if got := originRef(t, env.origin, "refs/heads/nodr/drafts/"+res.Changeset.ID); got != res.Changeset.DraftSHA {
		t.Fatalf("origin draft ref = %s, want %s", got, res.Changeset.DraftSHA)
	}
}

func TestRunCommandStaleExpectedSHA(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	ws := env.connect(t)
	_, err := runVMCreate(t, env, ws, "", "0000000000000000000000000000000000000000", "stale", "web-03")
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("RunCommand error = %v, want ErrStaleRevision", err)
	}
}

func TestRunCommandIdempotentReplayCreatesOneCommit(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	ws := env.connect(t)

	first, err := runVMCreate(t, env, ws, "", ws.DefaultSHA, "replay", "web-03")
	if err != nil {
		t.Fatalf("first RunCommand: %v", err)
	}
	second, err := runVMCreate(t, env, ws, "", ws.DefaultSHA, "replay", "web-03")
	if err != nil {
		t.Fatalf("replayed RunCommand: %v", err)
	}
	if second.Changeset.ID != first.Changeset.ID || second.Changeset.DraftSHA != first.Changeset.DraftSHA {
		t.Fatalf("replay = %+v, want same changeset %+v", second.Changeset, first.Changeset)
	}
	list, err := env.svc.Changesets(t.Context(), ws.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("changesets = %d, want exactly 1", len(list))
	}
	if got := originRef(t, env.origin, "refs/heads/nodr/drafts/"+first.Changeset.ID); got != first.Changeset.DraftSHA {
		t.Fatalf("draft moved on replay: %s", got)
	}
}

func TestRunCommandSecondCommandAdvancesRevision(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	ws := env.connect(t)

	first, err := runVMCreate(t, env, ws, "", ws.DefaultSHA, "first", "web-03")
	if err != nil {
		t.Fatalf("first RunCommand: %v", err)
	}
	second, err := runVMCreate(t, env, ws, first.Changeset.ID, first.Changeset.DraftSHA, "second", "web-04")
	if err != nil {
		t.Fatalf("second RunCommand: %v", err)
	}
	if second.Changeset.ID != first.Changeset.ID {
		t.Fatalf("changeset changed: %s -> %s", first.Changeset.ID, second.Changeset.ID)
	}
	if second.Changeset.Revision <= first.Changeset.Revision {
		t.Fatalf("revision %d did not advance past %d", second.Changeset.Revision, first.Changeset.Revision)
	}
	if second.Changeset.DraftSHA == first.Changeset.DraftSHA {
		t.Fatal("draft SHA did not move")
	}
	if got := originRef(t, env.origin, "refs/heads/nodr/drafts/"+first.Changeset.ID); got != second.Changeset.DraftSHA {
		t.Fatalf("origin draft ref = %s, want %s", got, second.Changeset.DraftSHA)
	}
}

func TestRunCommandOnClosedChangeset(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	ws := env.connect(t)
	first, err := runVMCreate(t, env, ws, "", ws.DefaultSHA, "closed-create", "web-03")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := env.ops.TransitionChangeset(t.Context(), first.Changeset.ID, operations.ChangesetAbandoned); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	_, err = runVMCreate(t, env, ws, first.Changeset.ID, first.Changeset.DraftSHA, "closed-edit", "web-04")
	if !errors.Is(err, ErrChangesetClosed) {
		t.Fatalf("RunCommand on abandoned changeset error = %v, want ErrChangesetClosed", err)
	}
}

func TestRunCommandValidationFailureIs422AndAbandonsNewChangeset(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	ws := env.connect(t)
	// An unknown template makes vm.create fail before any commit is made.
	_, err := env.svc.RunCommand(t.Context(), CommandInput{
		Workspace:      ws.Slug,
		ExpectedSHA:    ws.DefaultSHA,
		Actor:          testActor,
		Command:        "vm.create",
		IdempotencyKey: "invalid",
		Params:         json.RawMessage(`{"name":"web-05","environment":"prod","cluster":"pve-main","template":"ubuntu","network":"dmz","storage":"local-lvm","size":"M"}`),
	})
	var ce *CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("RunCommand error = %v, want *CommandError", err)
	}
	var cerr *commands.Error
	if !errors.As(err, &cerr) || cerr.Status != 400 {
		t.Fatalf("command error = %v, want status 400", err)
	}
	list, err := env.svc.Changesets(t.Context(), ws.Slug)
	if err != nil {
		t.Fatal(err)
	}
	for _, cs := range list {
		if cs.Status == operations.ChangesetOpen {
			t.Fatalf("open changeset %s left behind after failed command", cs.ID)
		}
	}
}

func TestChangesetDetailIncludesDiff(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	ws := env.connect(t)
	created, err := runVMCreate(t, env, ws, "", ws.DefaultSHA, "detail", "web-03")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	detail, err := env.svc.Changeset(t.Context(), ws.Slug, created.Changeset.ID)
	if err != nil {
		t.Fatalf("Changeset: %v", err)
	}
	if len(detail.Diff) == 0 || detail.DraftSHA != created.Changeset.DraftSHA {
		t.Fatalf("detail = %+v", detail)
	}
	if _, err := env.svc.Changeset(t.Context(), ws.Slug, "missing"); !errors.Is(err, ErrChangesetNotFound) {
		t.Fatalf("Changeset(missing) error = %v, want ErrChangesetNotFound", err)
	}
}
