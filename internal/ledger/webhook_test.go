package ledger

import (
	"testing"

	"github.com/centopw/nodr/internal/operations"
)

// externalVMFiles is an external commit to main that adds a VM intent file,
// which compile turns into changed units the server has not yet processed.
var externalVMFiles = map[string]string{
	"intent/compute/web-04.yaml": `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-04
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C4E5
  labels: { nodr/environment: prod }
spec:
  placement: { cluster: pve-main, assignedNode: pve2 }
  identity: { vmid: 1013 }
  resources: { cpu: { cores: 2 }, memory: { size: 8Gi } }
  nics:
    - network: dmz
      mac: BC:24:11:3A:5E:04
      ipv4: { mode: static, address: 10.0.20.24/24 }
`,
}

func TestParsePushEvent(t *testing.T) {
	t.Parallel()
	body := []byte(`{"ref":"refs/heads/main","after":"abc123","repository":{"name":"homelab-intent","owner":{"login":"acme","name":"acme-org"}}}`)
	ev, err := ParsePushEvent("d1", body)
	if err != nil {
		t.Fatalf("ParsePushEvent: %v", err)
	}
	want := PushEvent{DeliveryID: "d1", Owner: "acme", Repository: testRepo, Ref: "refs/heads/main", After: "abc123"}
	if ev != want {
		t.Fatalf("ParsePushEvent = %+v, want %+v", ev, want)
	}
	if _, err := ParsePushEvent("d2", []byte(`{"ref":"refs/heads/main"}`)); err == nil {
		t.Fatal("ParsePushEvent accepted a payload without repository or after")
	}
}

func TestHandlePushDuplicateDeliveryIgnored(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	ws := env.connect(t)
	after := commitOnOrigin(t, env.origin, "main", externalVMFiles)
	ev := PushEvent{DeliveryID: "dup", Owner: testOwner, Repository: testRepo, Ref: "refs/heads/main", After: after}
	if err := env.svc.HandlePush(t.Context(), ev); err != nil {
		t.Fatalf("first HandlePush: %v", err)
	}
	list, err := env.svc.Changesets(t.Context(), ws.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("changesets after first push = %d, want 1", len(list))
	}
	// Replaying the same delivery must not create a second sync changeset.
	if err := env.svc.HandlePush(t.Context(), ev); err != nil {
		t.Fatalf("duplicate HandlePush: %v", err)
	}
	list, err = env.svc.Changesets(t.Context(), ws.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("changesets after duplicate = %d, want 1", len(list))
	}
}

func TestHandlePushUnknownRepositoryIgnored(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	env.connect(t)
	ev := PushEvent{DeliveryID: "other", Owner: "stranger", Repository: "elsewhere", Ref: "refs/heads/main", After: "deadbeef"}
	if err := env.svc.HandlePush(t.Context(), ev); err != nil {
		t.Fatalf("HandlePush unknown repo: %v", err)
	}
}

func TestHandlePushUnrelatedRefIgnored(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	ws := env.connect(t)
	commitOnOrigin(t, env.origin, "main", externalVMFiles)
	ev := PushEvent{DeliveryID: "feature", Owner: testOwner, Repository: testRepo, Ref: "refs/heads/feature/x", After: "deadbeef"}
	if err := env.svc.HandlePush(t.Context(), ev); err != nil {
		t.Fatalf("HandlePush unrelated ref: %v", err)
	}
	list, err := env.svc.Changesets(t.Context(), ws.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("unrelated ref created %d changesets", len(list))
	}
	md, err := env.meta.Get(t.Context(), testSlug)
	if err != nil {
		t.Fatal(err)
	}
	if md.LastProcessedSHA != ws.DefaultSHA {
		t.Fatalf("LastProcessedSHA = %s, want unchanged %s", md.LastProcessedSHA, ws.DefaultSHA)
	}
}

func TestHandlePushDefaultBranchCreatesSyncChangeset(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	ws := env.connect(t)

	after := commitOnOrigin(t, env.origin, "main", externalVMFiles)
	ev := PushEvent{DeliveryID: "push-1", Owner: testOwner, Repository: testRepo, Ref: "refs/heads/main", After: after}
	if err := env.svc.HandlePush(t.Context(), ev); err != nil {
		t.Fatalf("HandlePush: %v", err)
	}

	list, err := env.svc.Changesets(t.Context(), ws.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Status != operations.ChangesetOpen || list[0].BaseSHA != after {
		t.Fatalf("changesets = %+v, want one open sync changeset based on %s", list, after)
	}
	if got := originRef(t, env.origin, "refs/heads/nodr/drafts/"+list[0].ID); got != list[0].DraftSHA {
		t.Fatalf("origin sync draft = %s, want %s", got, list[0].DraftSHA)
	}
	md, err := env.meta.Get(t.Context(), testSlug)
	if err != nil {
		t.Fatal(err)
	}
	if md.LastProcessedSHA != after {
		t.Fatalf("LastProcessedSHA = %s, want %s", md.LastProcessedSHA, after)
	}
}

func TestHandlePushDraftRefFetchesOnly(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, workspaceFiles)
	ws := env.connect(t)
	ev := PushEvent{DeliveryID: "draft-push", Owner: testOwner, Repository: testRepo, Ref: "refs/heads/nodr/drafts/none", After: ws.DefaultSHA}
	if err := env.svc.HandlePush(t.Context(), ev); err != nil {
		t.Fatalf("HandlePush draft ref: %v", err)
	}
	md, err := env.meta.Get(t.Context(), testSlug)
	if err != nil {
		t.Fatal(err)
	}
	if md.LastProcessedSHA != ws.DefaultSHA {
		t.Fatalf("draft-ref push changed LastProcessedSHA to %s", md.LastProcessedSHA)
	}
}
