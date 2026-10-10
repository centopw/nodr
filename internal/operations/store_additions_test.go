package operations

import (
	"errors"
	"testing"
	"time"
)

func addtChangeset(t testing.TB, store *Store, id, draft string, created time.Time) Changeset {
	t.Helper()
	changeset, err := store.CreateChangeset(t.Context(), Changeset{
		ID: id, Workspace: "home", BaseSHA: "base", DraftSHA: draft, CreatedAt: created,
	})
	if err != nil {
		t.Fatal(err)
	}
	return changeset
}

// addtPlan claims a plan under key, runs it, and finishes it as succeeded
// (with a stored artifact) or failed (without one).
func addtPlan(t testing.TB, store *Store, changesetID, sha, key string, created time.Time, succeed bool) {
	t.Helper()
	op, err := store.ClaimOperation(t.Context(), Operation{
		ID: key + "-op", ChangesetID: changesetID, Kind: "plan", PlanSHA: sha, CreatedAt: created,
	}, "workspace/home", key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionOperation(t.Context(), op.ID, OperationRunning); err != nil {
		t.Fatal(err)
	}
	if !succeed {
		if _, err := store.FinishOperation(t.Context(), op.ID, OperationFailed, "boom"); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := store.PutPlanArtifact(t.Context(), PlanArtifact{
		OperationID: op.ID, DraftSHA: sha, JSON: []byte(`{"plan":"` + key + `"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishOperation(t.Context(), op.ID, OperationSucceeded, "ok"); err != nil {
		t.Fatal(err)
	}
}

func addtEventTypes(t testing.TB, store *Store) []string {
	t.Helper()
	events, err := store.EventsAfter(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func addtSetupOpen(t *testing.T, store *Store) string {
	t.Helper()
	addtChangeset(t, store, "cs-1", "draft-1", time.Time{})
	return "cs-1"
}

func addtSetupPlanned(t *testing.T, store *Store) string {
	t.Helper()
	addtChangeset(t, store, "cs-1", "draft-1", time.Time{})
	addtPlan(t, store, "cs-1", "draft-1", "plan-1", time.Time{}, true)
	if _, err := store.TransitionChangeset(t.Context(), "cs-1", ChangesetPlanned); err != nil {
		t.Fatal(err)
	}
	return "cs-1"
}

func addtSetupApplied(t *testing.T, store *Store) string {
	t.Helper()
	id := addtSetupPlanned(t, store)
	if _, err := store.TransitionChangeset(t.Context(), id, ChangesetApplied); err != nil {
		t.Fatal(err)
	}
	return id
}

func addtSetupAbandoned(t *testing.T, store *Store) string {
	t.Helper()
	addtChangeset(t, store, "cs-1", "draft-1", time.Time{})
	if _, err := store.TransitionChangeset(t.Context(), "cs-1", ChangesetAbandoned); err != nil {
		t.Fatal(err)
	}
	return "cs-1"
}

func TestStoreAdvanceDraft(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(*testing.T, *Store) string
		expected   string
		newSHA     string
		wantErr    error
		wantStatus ChangesetStatus
	}{
		{name: "open_matching_advances", setup: addtSetupOpen, expected: "draft-1", newSHA: "draft-2", wantStatus: ChangesetOpen},
		{name: "planned_returns_to_open", setup: addtSetupPlanned, expected: "draft-1", newSHA: "draft-2", wantStatus: ChangesetOpen},
		{name: "open_stale_rejected", setup: addtSetupOpen, expected: "old", newSHA: "draft-2", wantErr: ErrStaleDraft, wantStatus: ChangesetOpen},
		{name: "planned_stale_rejected", setup: addtSetupPlanned, expected: "old", newSHA: "draft-2", wantErr: ErrStaleDraft, wantStatus: ChangesetPlanned},
		{name: "applied_rejected", setup: addtSetupApplied, expected: "draft-1", newSHA: "draft-2", wantErr: ErrInvalidTransition, wantStatus: ChangesetApplied},
		{name: "abandoned_rejected", setup: addtSetupAbandoned, expected: "draft-1", newSHA: "draft-2", wantErr: ErrInvalidTransition, wantStatus: ChangesetAbandoned},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t)
			id := tc.setup(t, store)
			before, err := store.Changeset(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			eventsBefore := len(addtEventTypes(t, store))

			got, err := store.AdvanceDraft(t.Context(), id, tc.expected, tc.newSHA)
			stored, loadErr := store.Changeset(t.Context(), id)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			eventsAfter := addtEventTypes(t, store)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if stored.Status != before.Status || stored.DraftSHA != before.DraftSHA || stored.Revision != before.Revision {
					t.Fatalf("stored changeset mutated: before %+v after %+v", before, stored)
				}
				if len(eventsAfter) != eventsBefore {
					t.Fatalf("event count = %d, want %d unchanged", len(eventsAfter), eventsBefore)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.wantStatus || got.DraftSHA != tc.newSHA || got.Revision != before.Revision+1 {
				t.Fatalf("returned changeset = %+v, want status %q draft %q revision %d", got, tc.wantStatus, tc.newSHA, before.Revision+1)
			}
			if stored.Status != tc.wantStatus || stored.DraftSHA != tc.newSHA || stored.Revision != before.Revision+1 {
				t.Fatalf("stored changeset = %+v, want status %q draft %q revision %d", stored, tc.wantStatus, tc.newSHA, before.Revision+1)
			}
			if len(eventsAfter) != eventsBefore+1 || eventsAfter[len(eventsAfter)-1] != "changeset.updated" {
				t.Fatalf("events = %v, want one trailing changeset.updated", eventsAfter)
			}
		})
	}
}

func TestStoreChangesetsNewestFirstPerWorkspace(t *testing.T) {
	store := openStore(t)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	addtChangeset(t, store, "a", "d", base)
	addtChangeset(t, store, "b", "d", base)
	addtChangeset(t, store, "c", "d", base.Add(time.Second))
	if _, err := store.CreateChangeset(t.Context(), Changeset{
		ID: "elsewhere", Workspace: "other", BaseSHA: "base", DraftSHA: "d", CreatedAt: base.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Changesets(t.Context(), "home")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"c", "b", "a"}
	if len(got) != len(want) {
		t.Fatalf("changesets = %+v, want IDs %v", got, want)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("changeset %d = %q, want %q", i, got[i].ID, id)
		}
	}
}

func TestStoreFinishOperation(t *testing.T) {
	cases := []struct {
		name       string
		running    bool
		to         OperationStatus
		wantErr    bool
		wantStatus OperationStatus
	}{
		{name: "succeeded_from_running", running: true, to: OperationSucceeded, wantStatus: OperationSucceeded},
		{name: "failed_from_running", running: true, to: OperationFailed, wantStatus: OperationFailed},
		{name: "queued_target_rejected", running: true, to: OperationQueued, wantErr: true, wantStatus: OperationRunning},
		{name: "running_target_rejected", running: true, to: OperationRunning, wantErr: true, wantStatus: OperationRunning},
		{name: "from_queued_rejected", running: false, to: OperationSucceeded, wantErr: true, wantStatus: OperationQueued},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t)
			addtChangeset(t, store, "cs-1", "draft-1", time.Time{})
			op, err := store.ClaimOperation(t.Context(), Operation{
				ID: "op-1", ChangesetID: "cs-1", Kind: "command", PlanSHA: "",
			}, "workspace/home", "key-1")
			if err != nil {
				t.Fatal(err)
			}
			if tc.running {
				if _, err := store.TransitionOperation(t.Context(), op.ID, OperationRunning); err != nil {
					t.Fatal(err)
				}
			}
			eventsBefore := len(addtEventTypes(t, store))
			got, err := store.FinishOperation(t.Context(), op.ID, tc.to, `{"ok":true}`)
			stored, loadErr := store.Operation(t.Context(), op.ID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("err = %v, want ErrInvalidTransition", err)
				}
				if stored.Status != tc.wantStatus || stored.Result != "" {
					t.Fatalf("stored operation mutated: %+v", stored)
				}
				if n := len(addtEventTypes(t, store)); n != eventsBefore {
					t.Fatalf("event count = %d, want %d unchanged", n, eventsBefore)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.to || got.Result != `{"ok":true}` {
				t.Fatalf("returned operation = %+v", got)
			}
			if stored.Status != tc.to || stored.Result != `{"ok":true}` {
				t.Fatalf("stored operation = %+v, want status %q and result persisted", stored, tc.to)
			}
			types := addtEventTypes(t, store)
			if types[len(types)-1] != "operation."+string(tc.to) {
				t.Fatalf("last event = %q, want operation.%s", types[len(types)-1], tc.to)
			}
		})
	}
}

func TestStoreOperationByKey(t *testing.T) {
	store := openStore(t)
	addtChangeset(t, store, "cs-1", "draft-1", time.Time{})
	claimed, err := store.ClaimOperation(t.Context(), Operation{
		ID: "op-1", ChangesetID: "cs-1", Kind: "plan", PlanSHA: "draft-1",
	}, "workspace/home", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		scope   string
		key     string
		wantID  string
		wantErr error
	}{
		{name: "claimed_key", scope: "workspace/home", key: "key-1", wantID: claimed.ID},
		{name: "other_scope_not_found", scope: "workspace/other", key: "key-1", wantErr: ErrNotFound},
		{name: "unknown_key_not_found", scope: "workspace/home", key: "missing", wantErr: ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.OperationByKey(t.Context(), tc.scope, tc.key)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != tc.wantID || got.Kind != "plan" || got.PlanSHA != "draft-1" {
				t.Fatalf("operation = %+v, want ID %q", got, tc.wantID)
			}
		})
	}
}

func TestStoreLatestPlanPicksNewestSucceededPlan(t *testing.T) {
	store := openStore(t)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	addtChangeset(t, store, "cs-1", "draft-1", time.Time{})
	addtChangeset(t, store, "cs-2", "draft-2", time.Time{})
	addtChangeset(t, store, "cs-empty", "draft-3", time.Time{})

	addtPlan(t, store, "cs-1", "draft-1", "old", base, true)
	addtPlan(t, store, "cs-1", "draft-1", "new", base.Add(time.Hour), true)
	addtPlan(t, store, "cs-1", "draft-1", "newest-failed", base.Add(2*time.Hour), false)
	addtPlan(t, store, "cs-2", "draft-2", "other", base.Add(3*time.Hour), true)

	op, artifact, err := store.LatestPlan(t.Context(), "cs-1")
	if err != nil {
		t.Fatal(err)
	}
	if op.ID != "new-op" || op.Status != OperationSucceeded || op.PlanSHA != "draft-1" {
		t.Fatalf("operation = %+v, want succeeded new-op", op)
	}
	if artifact.OperationID != "new-op" || artifact.DraftSHA != "draft-1" || string(artifact.JSON) != `{"plan":"new"}` {
		t.Fatalf("artifact = %+v", artifact)
	}

	if _, _, err := store.LatestPlan(t.Context(), "cs-empty"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for changeset without plans", err)
	}
}
