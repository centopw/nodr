package ledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	github "github.com/google/go-github/v75/github"

	"github.com/centopw/nodr/internal/operations"
	"github.com/centopw/nodr/internal/repository"
	"github.com/centopw/nodr/internal/secrets"
)

const (
	testOwner   = "acme"
	testRepo    = "homelab-intent"
	testSlug    = "homelab"
	testInstall = int64(42)
	testActor   = "alice"
)

const testManifest = `apiVersion: nodr/v1alpha1
kind: Workspace
metadata: { name: homelab }
spec:
  environments:
    prod: { vmidRange: [1012, 1040] }
    lab: { vmidRange: [2000, 2999] }
    templates: { vmidRange: [9000, 9099] }
`

const testPlatform = `apiVersion: nodr/v1alpha1
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

const testNetwork = `apiVersion: nodr/v1alpha1
kind: Network
metadata: { name: dmz }
spec:
  ipv4:
    subnet: 10.0.20.0/24
    gateway: 10.0.20.1
    static: { range: 10.0.20.23-10.0.20.60 }
`

const testVM = `apiVersion: nodr/v1alpha1
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

// workspaceFiles is the minimal valid workspace seeded into the origin.
var workspaceFiles = map[string]string{
	"nodr.yaml":                  testManifest,
	"intent/platform/pve.yaml":   testPlatform,
	"intent/network/dmz.yaml":    testNetwork,
	"intent/compute/web-01.yaml": testVM,
}

// fakeGitHub implements GitHub without any network access.
type fakeGitHub struct {
	installations []*github.Installation
	repos         map[int64][]*github.Repository
	gitAuthErr    error
}

func (f *fakeGitHub) ListInstallations(context.Context) ([]*github.Installation, error) {
	return f.installations, nil
}

func (f *fakeGitHub) ListRepositories(_ context.Context, id int64) ([]*github.Repository, error) {
	return f.repos[id], nil
}

func (f *fakeGitHub) VerifyWebhookSignature([]byte, string) error { return nil }

func (f *fakeGitHub) GitAuth(context.Context, int64) (githttp.BasicAuth, error) {
	if f.gitAuthErr != nil {
		return githttp.BasicAuth{}, f.gitAuthErr
	}
	return githttp.BasicAuth{Username: "x-access-token", Password: "test"}, nil
}

// newFakeGitHub lists one installation with testOwner/testRepo on defaultBranch.
func newFakeGitHub(defaultBranch string) *fakeGitHub {
	return &fakeGitHub{
		installations: []*github.Installation{{ID: github.Ptr(testInstall)}},
		repos: map[int64][]*github.Repository{
			testInstall: {{
				Name:          github.Ptr(testRepo),
				Owner:         &github.User{Login: github.Ptr(testOwner)},
				DefaultBranch: github.Ptr(defaultBranch),
			}},
		},
	}
}

// testSignature is the author of fixture commits.
func testSignature() *object.Signature {
	return &object.Signature{Name: "fixture", Email: "fixture@localhost", When: time.Unix(1700000000, 0)}
}

// seedOrigin creates a bare origin holding files on branch main and returns its path.
func seedOrigin(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	bare, err := git.PlainInit(origin, true)
	if err != nil {
		t.Fatalf("init origin: %v", err)
	}
	// A bare repository's HEAD defaults to master; GitHub's default branch here is main.
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
	writeFiles(t, work, files)
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("seed", &git.CommitOptions{Author: testSignature()}); err != nil {
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

// writeFiles writes files below root, creating directories as needed.
func writeFiles(t *testing.T, root string, files map[string]string) {
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

// commitOnOrigin adds a commit to branch of origin, writing files, to simulate
// an external push. It returns the new head SHA.
func commitOnOrigin(t *testing.T, origin, branch string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainClone(dir, false, &git.CloneOptions{URL: origin, ReferenceName: plumbing.NewBranchReferenceName(branch), SingleBranch: true})
	if err != nil {
		t.Fatalf("clone origin: %v", err)
	}
	writeFiles(t, dir, files)
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	sha, err := wt.Commit("external", &git.CommitOptions{Author: testSignature()})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec("refs/heads/" + branch + ":refs/heads/" + branch)}}); err != nil {
		t.Fatalf("push external commit: %v", err)
	}
	return sha.String()
}

// originRef resolves a ref in origin, returning "" when the ref does not exist.
func originRef(t *testing.T, origin, ref string) string {
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

// testEnv is a Service wired to real stores over a local file:// origin.
type testEnv struct {
	svc    *Service
	origin string
	meta   *repository.MetadataStore
	ops    *operations.Store
	repo   *repository.LocalStore
	gh     *fakeGitHub
}

// newTestEnv builds a Service whose origin holds files on main. The GitHub fake
// lists that origin as testOwner/testRepo.
func newTestEnv(t *testing.T, files map[string]string) *testEnv {
	t.Helper()
	origin := seedOrigin(t, files)
	dataRoot := t.TempDir()
	repo, err := repository.NewLocalStore(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.Open(filepath.Join(dataRoot, "secrets.db"), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sec.Close() })
	meta, err := repository.OpenMetadataStore(filepath.Join(dataRoot, "metadata.db"), sec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	ops, err := operations.Open(filepath.Join(dataRoot, "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ops.Close() })
	gh := newFakeGitHub("main")
	svc, err := New(t.Context(), Deps{
		Repo:          repo,
		Meta:          meta,
		Ops:           ops,
		GitHub:        gh,
		Secrets:       sec,
		WebhookSecret: "whsec",
		RepositoryURL: func(string, string) string { return origin },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return &testEnv{svc: svc, origin: origin, meta: meta, ops: ops, repo: repo, gh: gh}
}

// connect connects testOwner/testRepo and returns the workspace.
func (e *testEnv) connect(t *testing.T) Workspace {
	t.Helper()
	ws, err := e.svc.Connect(t.Context(), ConnectInput{InstallationID: testInstall, Owner: testOwner, Repository: testRepo})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return ws
}
