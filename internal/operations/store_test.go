package operations

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOperationStatusTransition(t *testing.T) {
	cases := []struct {
		from OperationStatus
		to   OperationStatus
		want bool
	}{
		{OperationQueued, OperationRunning, true},
		{OperationRunning, OperationSucceeded, true},
		{OperationRunning, OperationFailed, true},
		{OperationSucceeded, OperationRunning, false},
		{OperationFailed, OperationSucceeded, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			if got := canTransitionOperation(tc.from, tc.to); got != tc.want {
				t.Fatalf("canTransitionOperation(%q, %q) = %t, want %t", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestChangesetStatusTransition(t *testing.T) {
	cases := []struct {
		from ChangesetStatus
		to   ChangesetStatus
		want bool
	}{
		{ChangesetOpen, ChangesetPlanned, true},
		{ChangesetPlanned, ChangesetApplied, true},
		{ChangesetOpen, ChangesetAbandoned, true},
		{ChangesetApplied, ChangesetOpen, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			if got := canTransitionChangeset(tc.from, tc.to); got != tc.want {
				t.Fatalf("canTransitionChangeset(%q, %q) = %t, want %t", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func openStore(t testing.TB) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func TestStoreCreateAndGetChangeset(t *testing.T) {
	store := openStore(t)
	got, err := store.CreateChangeset(t.Context(), Changeset{
		ID: "changeset-1", Workspace: "home", BaseSHA: "base", DraftSHA: "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ChangesetOpen {
		t.Fatalf("status = %q, want %q", got.Status, ChangesetOpen)
	}
	got, err = store.Changeset(t.Context(), "changeset-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.DraftSHA != "draft" {
		t.Fatalf("draft SHA = %q, want draft", got.DraftSHA)
	}
}

func TestStoreChangesetTransitionRejectsInvalidWithoutMutation(t *testing.T) {
	store := openStore(t)
	created, err := store.CreateChangeset(t.Context(), Changeset{
		ID: "changeset-1", Workspace: "home", BaseSHA: "base", DraftSHA: "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionChangeset(t.Context(), created.ID, ChangesetAbandoned); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionChangeset(t.Context(), created.ID, ChangesetOpen); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
	got, err := store.Changeset(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ChangesetAbandoned {
		t.Fatalf("status = %q, want unchanged %q", got.Status, ChangesetAbandoned)
	}
}

func TestStoreClaimOperationIsAtomic(t *testing.T) {
	store := openStore(t)
	changeset, err := store.CreateChangeset(t.Context(), Changeset{
		ID: "changeset-1", Workspace: "home", BaseSHA: "base", DraftSHA: "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	const callers = 16
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			op, err := store.ClaimOperation(context.Background(), Operation{
				ID: newOperationID(), ChangesetID: changeset.ID, Kind: "plan", PlanSHA: "draft",
			}, "workspace/home", "request-key")
			if err != nil {
				errs <- err
				return
			}
			ids <- op.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("claim returned IDs %q and %q", first, id)
		}
	}
}

func TestStoreOperationTransitionRejectsInvalidWithoutMutation(t *testing.T) {
	store := openStore(t)
	op := claimPlanOperation(t, store)
	if _, err := store.TransitionOperation(t.Context(), op.ID, OperationRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionOperation(t.Context(), op.ID, OperationQueued); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
	got, err := store.Operation(t.Context(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != OperationRunning {
		t.Fatalf("status = %q, want unchanged %q", got.Status, OperationRunning)
	}
}

func claimPlanOperation(t testing.TB, store *Store) Operation {
	t.Helper()
	changeset, err := store.CreateChangeset(t.Context(), Changeset{
		ID: "changeset-" + newOperationID(), Workspace: "home", BaseSHA: "base", DraftSHA: "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	op, err := store.ClaimOperation(t.Context(), Operation{
		ID: newOperationID(), ChangesetID: changeset.ID, Kind: "plan", PlanSHA: "draft",
	}, "workspace/home", "key-"+changeset.ID)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

var nextOperationID atomic.Int64

func newOperationID() string {
	return strconv.FormatInt(nextOperationID.Add(1), 10)
}

func TestStorePutPlanArtifactRequiresOperationPlanSHA(t *testing.T) {
	store := openStore(t)
	op := claimPlanOperation(t, store)
	if _, err := store.TransitionOperation(t.Context(), op.ID, OperationRunning); err != nil {
		t.Fatal(err)
	}
	_, err := store.PutPlanArtifact(t.Context(), PlanArtifact{
		OperationID: op.ID, DraftSHA: "other-sha", JSON: []byte(`{"format":1}`),
	})
	if !errors.Is(err, ErrPlanSHAMismatch) {
		t.Fatalf("err = %v, want ErrPlanSHAMismatch", err)
	}
}

func TestStorePutPlanArtifactStoresExactBytes(t *testing.T) {
	store := openStore(t)
	op := claimPlanOperation(t, store)
	if _, err := store.TransitionOperation(t.Context(), op.ID, OperationRunning); err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"format":1,"resource_changes":[]}`)
	if _, err := store.PutPlanArtifact(t.Context(), PlanArtifact{OperationID: op.ID, DraftSHA: "draft", JSON: want}); err != nil {
		t.Fatal(err)
	}
	got, err := store.PlanArtifact(t.Context(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.JSON, want) {
		t.Fatalf("JSON = %s, want %s", got.JSON, want)
	}
}

func TestStorePutPlanArtifactRejectsDuplicateWithoutSecondEvent(t *testing.T) {
	store := openStore(t)
	op := claimPlanOperation(t, store)
	if _, err := store.TransitionOperation(t.Context(), op.ID, OperationRunning); err != nil {
		t.Fatal(err)
	}
	artifact := PlanArtifact{OperationID: op.ID, DraftSHA: "draft", JSON: []byte(`{"format":1}`)}
	if _, err := store.PutPlanArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	before, err := store.EventsAfter(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutPlanArtifact(t.Context(), artifact); !errors.Is(err, ErrPlanArtifactExists) {
		t.Fatalf("err = %v, want ErrPlanArtifactExists", err)
	}
	after, err := store.EventsAfter(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("event count = %d, want unchanged %d", len(after), len(before))
	}
}

func TestStorePlanOperationLifecycle(t *testing.T) {
	store := openStore(t)
	changeset, err := store.CreateChangeset(t.Context(), Changeset{
		ID: "lifecycle-changeset", Workspace: "home", BaseSHA: "base", DraftSHA: "exact-draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	op, err := store.ClaimOperation(t.Context(), Operation{
		ID: "lifecycle-operation", ChangesetID: changeset.ID, Kind: "plan", PlanSHA: "exact-draft",
	}, "workspace/home", "lifecycle-key")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.ClaimOperation(t.Context(), Operation{
		ID: "ignored-duplicate", ChangesetID: changeset.ID, Kind: "apply", PlanSHA: "other",
	}, "workspace/home", "lifecycle-key")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != op.ID {
		t.Fatalf("duplicate ID = %q, want %q", duplicate.ID, op.ID)
	}
	if _, err := store.TransitionOperation(t.Context(), op.ID, OperationRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutPlanArtifact(t.Context(), PlanArtifact{
		OperationID: op.ID, DraftSHA: "exact-draft", JSON: []byte(`{"format":1}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionOperation(t.Context(), op.ID, OperationSucceeded); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionChangeset(t.Context(), changeset.ID, ChangesetPlanned); err != nil {
		t.Fatal(err)
	}
	events, err := store.EventsAfter(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	wantTypes := []string{"operation.created", "operation.running", "plan.artifact.stored", "operation.succeeded", "changeset.planned"}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d: %+v", len(events), len(wantTypes), events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Fatalf("event %d type = %q, want %q", i, events[i].Type, want)
		}
		if i > 0 && events[i-1].ID >= events[i].ID {
			t.Fatalf("event IDs not increasing: %d then %d", events[i-1].ID, events[i].ID)
		}
	}
	if events[4].ChangesetID != changeset.ID || events[4].OperationID != "" {
		t.Fatalf("changeset event entity IDs = %+v", events[4])
	}
}

func TestStoreClaimPlanOperationRequiresChangesetDraftSHA(t *testing.T) {
	store := openStore(t)
	changeset, err := store.CreateChangeset(t.Context(), Changeset{
		ID: "sha-changeset", Workspace: "home", BaseSHA: "base", DraftSHA: "draft-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ClaimOperation(t.Context(), Operation{
		ID: "sha-operation", ChangesetID: changeset.ID, Kind: "plan", PlanSHA: "draft-b",
	}, "workspace/home", "sha-key")
	if !errors.Is(err, ErrPlanSHAMismatch) {
		t.Fatalf("err = %v, want ErrPlanSHAMismatch", err)
	}
}

func TestStoreClaimOperationRequiresExistingChangeset(t *testing.T) {
	store := openStore(t)
	_, err := store.ClaimOperation(t.Context(), Operation{
		ID: "orphan-operation", ChangesetID: "missing", Kind: "plan", PlanSHA: "draft",
	}, "workspace/home", "orphan-key")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestStoreTransitionChangesetToPlannedRequiresSucceededExactArtifact(t *testing.T) {
	store := openStore(t)
	changeset, err := store.CreateChangeset(t.Context(), Changeset{
		ID: "unplanned-changeset", Workspace: "home", BaseSHA: "base", DraftSHA: "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.TransitionChangeset(t.Context(), changeset.ID, ChangesetPlanned)
	if !errors.Is(err, ErrPlanSHAMismatch) {
		t.Fatalf("err = %v, want ErrPlanSHAMismatch", err)
	}
}
