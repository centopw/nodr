package operations

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestStoreSubscriptionReceivesCommittedEvent(t *testing.T) {
	store := openStore(t)
	events, cancel := store.Subscribe()
	t.Cleanup(cancel)
	op := claimPlanOperation(t, store)
	select {
	case got := <-events:
		if got.OperationID != op.ID || got.ChangesetID != op.ChangesetID || got.Type != "operation.created" {
			t.Fatalf("event = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for committed event")
	}
}

func TestStoreEventsAfterReturnsPersistedEventsInOrder(t *testing.T) {
	store := openStore(t)
	op := claimPlanOperation(t, store)
	if _, err := store.TransitionOperation(t.Context(), op.ID, OperationRunning); err != nil {
		t.Fatal(err)
	}
	events, err := store.EventsAfter(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ID >= events[1].ID {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Type != "operation.created" || events[1].Type != "operation.running" {
		t.Fatalf("event types = %q, %q", events[0].Type, events[1].Type)
	}
}

func TestStoreSlowSubscriberDoesNotBlockMutation(t *testing.T) {
	store := openStore(t)
	slow, cancelSlow := store.Subscribe()
	t.Cleanup(cancelSlow)
	fast, cancelFast := store.Subscribe()
	t.Cleanup(cancelFast)
	for range subscriberBuffer + 1 {
		op := claimPlanOperation(t, store)
		if op.ID == "" {
			t.Fatal("empty operation ID")
		}
	}
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-slow:
			if !ok {
				goto dropped
			}
		case <-deadline:
			t.Fatal("slow subscriber was not dropped")
		}
	}

dropped:
	select {
	case <-fast:
	case <-time.After(time.Second):
		t.Fatal("fast subscriber did not receive an event")
	}
}

func TestStoreCloseWaitsForMutation(t *testing.T) {
	store := openStore(t)
	store.mutateMu.Lock()
	closed := make(chan error, 1)
	go func() {
		closed <- store.Close()
	}()
	select {
	case err := <-closed:
		t.Fatalf("Close returned while a mutation held the lifecycle lock: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	store.mutateMu.Unlock()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not return after the mutation completed")
	}
}

func TestStoreSubscriberReceivesConcurrentMutationsInEventIDOrder(t *testing.T) {
	store := openStore(t)
	events, cancel := store.Subscribe()
	t.Cleanup(cancel)
	const mutations = 4
	changesets := make([]Changeset, mutations)
	for i := range changesets {
		var err error
		changesets[i], err = store.CreateChangeset(t.Context(), Changeset{
			ID: fmt.Sprintf("ordered-changeset-%d", i), Workspace: "home", BaseSHA: "base", DraftSHA: "draft",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := range changesets {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := store.ClaimOperation(context.Background(), Operation{
				ID: fmt.Sprintf("ordered-operation-%d", index), ChangesetID: changesets[index].ID, Kind: "plan", PlanSHA: "draft",
			}, "workspace/home", fmt.Sprintf("ordered-key-%d", index))
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	var previous int64
	for range mutations {
		select {
		case event := <-events:
			if event.ID <= previous {
				t.Fatalf("event ID %d followed %d", event.ID, previous)
			}
			previous = event.ID
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for committed event")
		}
	}
}

func TestStoreCloseWaitsForChangesetCreation(t *testing.T) {
	store := openStore(t)
	store.mutateMu.Lock()
	created := make(chan error, 1)
	go func() {
		_, err := store.CreateChangeset(context.Background(), Changeset{
			ID: "blocked-create", Workspace: "home", BaseSHA: "base", DraftSHA: "draft",
		})
		created <- err
	}()
	select {
	case err := <-created:
		t.Fatalf("CreateChangeset returned while Close lifecycle lock was held: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	store.mutateMu.Unlock()
	if err := <-created; err != nil {
		t.Fatal(err)
	}
}
