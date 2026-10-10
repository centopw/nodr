package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	github "github.com/google/go-github/v75/github"

	"github.com/centopw/nodr/internal/authn"
	"github.com/centopw/nodr/internal/ledger"
	"github.com/centopw/nodr/internal/operations"
	"github.com/centopw/nodr/internal/repository"
	"github.com/centopw/nodr/internal/secrets"
)

const (
	e2eOwner   = "acme"
	e2eRepo    = "homelab-intent"
	e2eInstall = int64(42)
	e2eVMParam = `{"name":"web-03","environment":"prod","cluster":"pve-main","template":"debian-12-cloud","network":"dmz","storage":"local-lvm","size":"M"}`
	e2eZeroSHA = "0000000000000000000000000000000000000000"
)

const e2eManifest = `apiVersion: nodr/v1alpha1
kind: Workspace
metadata: { name: homelab }
spec:
  environments:
    prod: { vmidRange: [1012, 1040] }
    templates: { vmidRange: [9000, 9099] }
`

const e2ePlatform = `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [https://10.0.20.11:8006]
  credentialsRef: proxmox/pve-main-token
  nodes: [pve1, pve2]
---
apiVersion: nodr/v1alpha1
kind: Template
metadata: { name: debian-12-cloud }
spec:
  cluster: pve-main
  image: { url: https://example.com/debian.qcow2, checksum: "sha256:0000000000000000000000000000000000000000000000000000000000000000" }
  storage: local-lvm
  identity: { vmid: 9000 }
`

const e2eNetwork = `apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: dmz }
spec:
  ipv4:
    subnet: 10.0.20.0/24
    gateway: 10.0.20.1
    static: { range: 10.0.20.23-10.0.20.60 }
`

const e2eVM = `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-01
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C2D3
  labels: { nodr/environment: prod }
spec:
  placement: { cluster: pve-main, assignedNode: pve1 }
  identity: { vmid: 1012 }
  resources: { cpu: { cores: 2 }, memory: { size: 8Gi } }
  nics:
    - network: dmz
      mac: BC:24:11:3A:5E:01
      ipv4: { mode: static, address: 10.0.20.21/24 }
`

// e2eExternalFiles is an external commit to main adding a VM that compile turns
// into new intent output. Its vmid, address and MAC avoid web-03's allocation.
var e2eExternalFiles = map[string]string{
	"intent/compute/web-04.yaml": `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-04
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C4E5
  labels: { nodr/environment: prod }
spec:
  placement: { cluster: pve-main, assignedNode: pve2 }
  identity: { vmid: 1030 }
  resources: { cpu: { cores: 2 }, memory: { size: 8Gi } }
  nics:
    - network: dmz
      mac: BC:24:11:3A:5E:F4
      ipv4: { mode: static, address: 10.0.20.50/24 }
`,
}

// e2eFakeTofuScript stands in for OpenTofu: plan writes its -out file, apply
// requires that saved plan, and show reports the plan JSON staged for the unit.
const e2eFakeTofuScript = `#!/bin/sh
unit=$(pwd -P)
unit=${unit##*/}
echo "$unit $*" >> "$FAKE_TOFU_LOG"
for arg in "$@"; do last=$arg; done
case $1 in
init)
	echo "initialized $unit"
	;;
plan)
	for arg in "$@"; do
		case $arg in -out=*) plan=${arg#-out=} ;; esac
	done
	echo "plan of $unit" > "$plan"
	echo "planned $unit"
	;;
show | apply)
	if [ "$(cat "$last")" != "plan of $unit" ]; then
		echo "Error: $last is not a saved plan of $unit" >&2
		exit 1
	fi
	if [ "$1" = apply ]; then
		echo "applied $unit"
	elif [ -f "$FAKE_TOFU_PLANS/$unit.json" ]; then
		cat "$FAKE_TOFU_PLANS/$unit.json"
	else
		echo '{"format_version": "1.2", "resource_changes": []}'
	fi
	;;
*)
	echo "Error: unexpected command $1" >&2
	exit 1
	;;
esac
`

// e2eGitHub is a GitHub App fake with one installation that lists acme/homelab-intent.
type e2eGitHub struct {
	installations []*github.Installation
	repos         map[int64][]*github.Repository
}

func (g *e2eGitHub) ListInstallations(context.Context) ([]*github.Installation, error) {
	return g.installations, nil
}

func (g *e2eGitHub) ListRepositories(_ context.Context, id int64) ([]*github.Repository, error) {
	return g.repos[id], nil
}

// VerifyWebhookSignature accepts only the fixed signature "sha256=ok".
func (g *e2eGitHub) VerifyWebhookSignature(_ []byte, signature string) error {
	if signature != "sha256=ok" {
		return errors.New("e2e: bad webhook signature")
	}
	return nil
}

func (g *e2eGitHub) GitAuth(context.Context, int64) (githttp.BasicAuth, error) {
	return githttp.BasicAuth{}, nil
}

func e2eNewGitHub() *e2eGitHub {
	return &e2eGitHub{
		installations: []*github.Installation{{ID: github.Ptr(e2eInstall)}},
		repos: map[int64][]*github.Repository{
			e2eInstall: {{
				Name:          github.Ptr(e2eRepo),
				Owner:         &github.User{Login: github.Ptr(e2eOwner)},
				DefaultBranch: github.Ptr("main"),
			}},
		},
	}
}

// e2eService is the LedgerService under test: the real Service plus the fake
// GitHub's webhook verification.
type e2eService struct {
	*ledger.Service
	gh *e2eGitHub
}

func (s e2eService) VerifyWebhookSignature(payload []byte, signature string) error {
	return s.gh.VerifyWebhookSignature(payload, signature)
}

func e2eSignature() *object.Signature {
	return &object.Signature{Name: "fixture", Email: "fixture@localhost", When: time.Unix(1700000000, 0)}
}

// e2eSeedOrigin creates a bare origin with files committed on main and returns its path.
func e2eSeedOrigin(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	bare, err := git.PlainInit(origin, true)
	if err != nil {
		t.Fatalf("init origin: %v", err)
	}
	if err := bare.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	repo, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatalf("init work: %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))); err != nil {
		t.Fatal(err)
	}
	e2eWriteFiles(t, work, files)
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("seed", &git.CommitOptions{Author: e2eSignature()}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{origin}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{"refs/heads/main:refs/heads/main"}}); err != nil {
		t.Fatalf("push seed: %v", err)
	}
	return origin
}

func e2eWriteFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// e2eCommitOnOrigin simulates an external push: it commits files on branch of
// origin and returns the new head SHA.
func e2eCommitOnOrigin(t *testing.T, origin, branch string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainClone(dir, false, &git.CloneOptions{URL: origin, ReferenceName: plumbing.NewBranchReferenceName(branch), SingleBranch: true})
	if err != nil {
		t.Fatalf("clone origin: %v", err)
	}
	e2eWriteFiles(t, dir, files)
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	sha, err := wt.Commit("external", &git.CommitOptions{Author: e2eSignature()})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec("refs/heads/" + branch + ":refs/heads/" + branch)}}); err != nil {
		t.Fatalf("push external commit: %v", err)
	}
	return sha.String()
}

// e2eOriginRef resolves a ref in origin, returning "" when it does not exist.
func e2eOriginRef(t *testing.T, origin, ref string) string {
	t.Helper()
	repo, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatal(err)
	}
	r, err := repo.Reference(plumbing.ReferenceName(ref), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return r.Hash().String()
}

// e2eCountCommits counts the commits reachable from hash in origin.
func e2eCountCommits(t *testing.T, origin, hash string) int {
	t.Helper()
	repo, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatal(err)
	}
	iter, err := repo.Log(&git.LogOptions{From: plumbing.NewHash(hash)})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	if err := iter.ForEach(func(*object.Commit) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	return n
}

// e2eInstallFakeTofu installs a fake tofu through NODR_TOFU whose plan for the
// workspace's unit holds one non-destructive create.
func e2eInstallFakeTofu(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tofu is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "tofu")
	if err := os.WriteFile(bin, []byte(e2eFakeTofuScript), 0o755); err != nil {
		t.Fatal(err)
	}
	plans := filepath.Join(dir, "plans")
	if err := os.Mkdir(plans, 0o755); err != nil {
		t.Fatal(err)
	}
	const plan = `{"format_version":"1.2","resource_changes":[{"address":"proxmox_virtual_environment_vm.web_03","type":"proxmox_virtual_environment_vm","change":{"actions":["create"]}}]}`
	if err := os.WriteFile(filepath.Join(plans, "pve-main-compute.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODR_TOFU", bin)
	t.Setenv("FAKE_TOFU_LOG", filepath.Join(dir, "calls.log"))
	t.Setenv("FAKE_TOFU_PLANS", plans)
	t.Setenv("FAKE_TOFU_FAIL", "")
}

// e2eStatus fails the test unless rec has the wanted status code.
func e2eStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, want, rec.Body.String())
	}
}

func e2eDecode(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

// e2eWaitFor polls done every 20ms until it reports true or 10s pass.
func e2eWaitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func e2eGetOperation(t *testing.T, h http.Handler, cookie *http.Cookie, id string) ledgerOperation {
	t.Helper()
	rec := ledgerDo(h, http.MethodGet, apiPrefix+"/operations/"+id, "", cookie, "", nil)
	e2eStatus(t, rec, http.StatusOK)
	var op ledgerOperation
	e2eDecode(t, rec, &op)
	return op
}

func e2eGetChangeset(t *testing.T, h http.Handler, cookie *http.Cookie, id string) ledgerChangeset {
	t.Helper()
	rec := ledgerDo(h, http.MethodGet, apiPrefix+"/workspaces/homelab/changesets/"+id, "", cookie, "", nil)
	e2eStatus(t, rec, http.StatusOK)
	var body struct {
		Changeset ledgerChangeset `json:"changeset"`
	}
	e2eDecode(t, rec, &body)
	return body.Changeset
}

func e2eListChangesets(t *testing.T, h http.Handler, cookie *http.Cookie) []ledgerChangeset {
	t.Helper()
	rec := ledgerDo(h, http.MethodGet, apiPrefix+"/workspaces/homelab/changesets", "", cookie, "", nil)
	e2eStatus(t, rec, http.StatusOK)
	var body struct {
		Items []ledgerChangeset `json:"items"`
	}
	e2eDecode(t, rec, &body)
	return body.Items
}

// TestLedgerEndToEnd drives the real stack over HTTP: auth, GitHub setup,
// workspace connect, command commit, plan, apply, webhook sync and events.
func TestLedgerEndToEnd(t *testing.T) {
	e2eInstallFakeTofu(t)
	origin := e2eSeedOrigin(t, map[string]string{
		"nodr.yaml":                  e2eManifest,
		"intent/platform/pve.yaml":   e2ePlatform,
		"intent/network/dmz.yaml":    e2eNetwork,
		"intent/compute/web-01.yaml": e2eVM,
	})

	dataRoot := t.TempDir()
	repo, err := repository.NewLocalStore(dataRoot)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	sec, err := secrets.Open(filepath.Join(dataRoot, "secrets.db"), make([]byte, 32))
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	t.Cleanup(func() { _ = sec.Close() })
	if err := sec.Put(t.Context(), "proxmox/pve-main-token", []byte("root@pam!nodr=e2e")); err != nil {
		t.Fatalf("secrets.Put: %v", err)
	}
	meta, err := repository.OpenMetadataStore(filepath.Join(dataRoot, "metadata.db"), sec)
	if err != nil {
		t.Fatalf("OpenMetadataStore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	ops, err := operations.Open(filepath.Join(dataRoot, "operations.db"))
	if err != nil {
		t.Fatalf("operations.Open: %v", err)
	}
	t.Cleanup(func() { _ = ops.Close() })

	gh := e2eNewGitHub()
	svc, err := ledger.New(t.Context(), ledger.Deps{
		Repo:          repo,
		Meta:          meta,
		Ops:           ops,
		GitHub:        gh,
		Secrets:       sec,
		Runner:        &ledger.LocalRunner{Dir: t.TempDir()},
		WebhookSecret: "whsec",
		RepositoryURL: func(string, string) string { return origin },
	})
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	// Registered last so it runs first, before the stores close.
	t.Cleanup(func() { _ = svc.Close() })

	store := ledgerAuthStore(t)
	next := http.NewServeMux()
	next.Handle("POST "+apiPrefix+"/auth/login", authn.LoginHandler(store, apiPrefix+"/auth/login"))
	next.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := LedgerHandler(LedgerConfig{
		Service: e2eService{Service: svc, gh: gh},
		Auth:    store,
		Ready:   func(context.Context) []string { return nil },
	}, next)

	// 1) login yields a session cookie and CSRF token.
	cookie, csrf := ledgerLogin(t, h)
	if csrf == "" {
		t.Fatal("login returned an empty CSRF token")
	}

	// 2) the installation lists acme/homelab-intent.
	rec := ledgerDo(h, http.MethodPost, apiPrefix+"/setup/github/installations", "", cookie, csrf, nil)
	e2eStatus(t, rec, http.StatusOK)
	var installs struct {
		Installations []struct {
			ID           int64 `json:"id"`
			Repositories []struct {
				Owner         string `json:"owner"`
				Name          string `json:"name"`
				DefaultBranch string `json:"defaultBranch"`
			} `json:"repositories"`
		} `json:"installations"`
	}
	e2eDecode(t, rec, &installs)
	if len(installs.Installations) != 1 || installs.Installations[0].ID != e2eInstall {
		t.Fatalf("installations = %+v, want installation %d", installs.Installations, e2eInstall)
	}
	repos := installs.Installations[0].Repositories
	if len(repos) != 1 || repos[0].Owner != e2eOwner || repos[0].Name != e2eRepo || repos[0].DefaultBranch != "main" {
		t.Fatalf("repositories = %+v, want %s/%s on main", repos, e2eOwner, e2eRepo)
	}

	// 3) connecting the repository creates the workspace at the default HEAD.
	rec = ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces",
		`{"installationId":42,"owner":"acme","repository":"homelab-intent"}`, cookie, csrf, nil)
	e2eStatus(t, rec, http.StatusCreated)
	var ws ledgerWorkspace
	e2eDecode(t, rec, &ws)
	if ws.Name != "homelab" || ws.HeadSHA == "" {
		t.Fatalf("workspace = %+v, want homelab with a head SHA", ws)
	}
	if head := e2eOriginRef(t, origin, "refs/heads/main"); ws.HeadSHA != head {
		t.Fatalf("headSha = %s, want origin main %s", ws.HeadSHA, head)
	}

	// 4) vm.create commits one command onto a new changeset draft.
	commandBody := `{"command":"vm.create","params":` + e2eVMParam + `}`
	createHeaders := map[string]string{"If-Match": quoteETag(ws.HeadSHA), "Idempotency-Key": "vm-create-web-03"}
	rec = ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces/homelab/commands", commandBody, cookie, csrf, createHeaders)
	e2eStatus(t, rec, http.StatusOK)
	var created struct {
		Changeset ledgerChangeset  `json:"changeset"`
		Diff      []ledgerFileDiff `json:"diff"`
	}
	e2eDecode(t, rec, &created)
	cs := created.Changeset
	if cs.ID == "" {
		t.Fatalf("changeset = %+v, want an id", cs)
	}
	if cs.Status != string(operations.ChangesetOpen) || cs.BaseSHA != ws.HeadSHA {
		t.Fatalf("changeset = %+v, want open on %s", cs, ws.HeadSHA)
	}
	if cs.DraftSHA == "" || cs.DraftSHA == ws.HeadSHA {
		t.Fatalf("draft SHA %q did not advance from %s", cs.DraftSHA, ws.HeadSHA)
	}
	if etag := rec.Header().Get("ETag"); etag != quoteETag(cs.DraftSHA) {
		t.Fatalf("ETag = %q, want %q", etag, quoteETag(cs.DraftSHA))
	}
	if len(created.Diff) == 0 {
		t.Fatal("expected a non-empty diff for vm.create")
	}
	draftRef := "refs/heads/nodr/drafts/" + cs.ID
	if got := e2eOriginRef(t, origin, draftRef); got != cs.DraftSHA {
		t.Fatalf("origin draft ref = %s, want %s", got, cs.DraftSHA)
	}
	baseCommits := e2eCountCommits(t, origin, ws.HeadSHA)

	// 5) replaying the same Idempotency-Key returns the recorded draft, with no new commit.
	rec = ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces/homelab/commands", commandBody, cookie, csrf, createHeaders)
	e2eStatus(t, rec, http.StatusOK)
	var replayed struct {
		Changeset ledgerChangeset `json:"changeset"`
	}
	e2eDecode(t, rec, &replayed)
	if replayed.Changeset.ID != cs.ID || replayed.Changeset.DraftSHA != cs.DraftSHA {
		t.Fatalf("replay changeset = %+v, want %+v", replayed.Changeset, cs)
	}
	if got := e2eOriginRef(t, origin, draftRef); got != cs.DraftSHA {
		t.Fatalf("draft moved on replay: %s, want %s", got, cs.DraftSHA)
	}
	if n := e2eCountCommits(t, origin, cs.DraftSHA); n != baseCommits+1 {
		t.Fatalf("draft commits = %d, want exactly one above base (%d)", n, baseCommits)
	}

	// 6) a stale If-Match against the changeset draft is 412.
	staleBody := `{"command":"vm.create","params":` + e2eVMParam + `,"changeset":"` + cs.ID + `"}`
	rec = ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces/homelab/commands", staleBody, cookie, csrf,
		map[string]string{"If-Match": quoteETag(e2eZeroSHA), "Idempotency-Key": "vm-create-stale"})
	e2eStatus(t, rec, http.StatusPreconditionFailed)
	if p := ledgerDecodeProblem(t, rec); p.Title != "PRECONDITION_FAILED" {
		t.Fatalf("stale problem title = %q, want PRECONDITION_FAILED", p.Title)
	}
	if got := e2eOriginRef(t, origin, draftRef); got != cs.DraftSHA {
		t.Fatalf("draft moved on stale request: %s, want %s", got, cs.DraftSHA)
	}

	// 7) plan the draft; the operation and the changeset both reach planned.
	planHeaders := map[string]string{"If-Match": quoteETag(cs.DraftSHA), "Idempotency-Key": "plan-1"}
	rec = ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces/homelab/changesets/"+cs.ID+":plan", "", cookie, csrf, planHeaders)
	e2eStatus(t, rec, http.StatusAccepted)
	var planStarted struct {
		Operation ledgerOperation `json:"operation"`
	}
	e2eDecode(t, rec, &planStarted)
	planID := planStarted.Operation.ID
	if planID == "" || planStarted.Operation.Kind != "plan" {
		t.Fatalf("plan operation = %+v", planStarted.Operation)
	}
	var planned ledgerOperation
	e2eWaitFor(t, "plan operation to succeed", func() bool {
		planned = e2eGetOperation(t, h, cookie, planID)
		return planned.Status == string(operations.OperationSucceeded)
	})
	if planned.Plan == nil || !planned.Plan.HasChanges {
		t.Fatalf("plan = %+v, want hasChanges", planned.Plan)
	}
	e2eWaitFor(t, "changeset to be planned", func() bool {
		return e2eGetChangeset(t, h, cookie, cs.ID).Status == string(operations.ChangesetPlanned)
	})

	// 8) apply promotes the draft to main and the changeset is applied.
	applyHeaders := map[string]string{"If-Match": quoteETag(cs.DraftSHA), "Idempotency-Key": "apply-1"}
	rec = ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces/homelab/changesets/"+cs.ID+":apply", "", cookie, csrf, applyHeaders)
	e2eStatus(t, rec, http.StatusAccepted)
	var applyStarted struct {
		Operation ledgerOperation `json:"operation"`
	}
	e2eDecode(t, rec, &applyStarted)
	applyID := applyStarted.Operation.ID
	if applyID == "" || applyStarted.Operation.Kind != "apply" {
		t.Fatalf("apply operation = %+v", applyStarted.Operation)
	}
	e2eWaitFor(t, "apply operation to succeed", func() bool {
		return e2eGetOperation(t, h, cookie, applyID).Status == string(operations.OperationSucceeded)
	})
	if got := e2eOriginRef(t, origin, "refs/heads/main"); got != cs.DraftSHA {
		t.Fatalf("origin main = %s, want draft %s", got, cs.DraftSHA)
	}
	e2eWaitFor(t, "changeset to be applied", func() bool {
		return e2eGetChangeset(t, h, cookie, cs.ID).Status == string(operations.ChangesetApplied)
	})

	// 9) an external push to main creates one sync changeset; repeats and bad signatures do not.
	ext := e2eCommitOnOrigin(t, origin, "main", e2eExternalFiles)
	pushBody, err := json.Marshal(map[string]any{
		"ref":   "refs/heads/main",
		"after": ext,
		"repository": map[string]any{
			"name":  e2eRepo,
			"owner": map[string]any{"login": e2eOwner},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pushHeaders := map[string]string{
		"X-Hub-Signature-256": "sha256=ok",
		"X-GitHub-Event":      "push",
		"X-GitHub-Delivery":   "d1",
	}
	rec = ledgerDo(h, http.MethodPost, apiPrefix+"/integrations/github/webhook", string(pushBody), nil, "", pushHeaders)
	e2eStatus(t, rec, http.StatusAccepted)
	items := e2eListChangesets(t, h, cookie)
	if len(items) != 2 {
		t.Fatalf("changesets after push = %d, want 2 (applied + sync)", len(items))
	}
	var sync ledgerChangeset
	for _, item := range items {
		if item.ID != cs.ID {
			sync = item
		}
	}
	if sync.ID == "" || sync.BaseSHA != ext || sync.Status != string(operations.ChangesetOpen) {
		t.Fatalf("sync changeset = %+v, want open on %s", sync, ext)
	}

	rec = ledgerDo(h, http.MethodPost, apiPrefix+"/integrations/github/webhook", string(pushBody), nil, "", pushHeaders)
	e2eStatus(t, rec, http.StatusAccepted)
	if n := len(e2eListChangesets(t, h, cookie)); n != 2 {
		t.Fatalf("changesets after repeated delivery = %d, want 2", n)
	}

	badHeaders := map[string]string{
		"X-Hub-Signature-256": "sha256=bad",
		"X-GitHub-Event":      "push",
		"X-GitHub-Delivery":   "d2",
	}
	rec = ledgerDo(h, http.MethodPost, apiPrefix+"/integrations/github/webhook", string(pushBody), nil, "", badHeaders)
	e2eStatus(t, rec, http.StatusUnauthorized)
	if n := len(e2eListChangesets(t, h, cookie)); n != 2 {
		t.Fatalf("changesets after bad signature = %d, want 2", n)
	}

	// 10) the SSE stream replays from the start and carries the succeeded operation.
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+apiPrefix+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	req.Header.Set("Last-Event-ID", "0")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d, want 200", resp.StatusCode)
	}
	seen := false
	scanner := bufio.NewScanner(resp.Body)
	for !seen && scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var env ledgerCloudEvent
		if err := json.Unmarshal([]byte(data), &env); err != nil {
			t.Fatalf("decode event %q: %v", data, err)
		}
		seen = env.Type == "nodr.operation.succeeded"
	}
	if !seen {
		t.Fatalf("event stream ended without nodr.operation.succeeded: %v", scanner.Err())
	}

	// 11) readiness reports 200 when every check passes.
	rec = ledgerDo(h, http.MethodGet, "/readyz", "", nil, "", nil)
	e2eStatus(t, rec, http.StatusOK)
}
