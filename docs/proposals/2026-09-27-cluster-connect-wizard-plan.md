# Slice B: Cluster-connect Wizard, Credential Wiring and Server Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Working Directory:** All work must take place in the isolated git worktree:
> `/Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard`
> on branch `feat/cluster-connect-wizard`.
> Never modify files in the root repository checkout.

**Goal:** Wire `credentialsRef` through `secrets.Store` into the OpenTofu child process's environment for both the CLI and the API; add a Proxmox bootstrap wizard (`nodr cluster connect`) that creates a least-privilege user/role/token and pins the cluster's TLS fingerprint; gate every dashboard/API route behind a session-cookie auth (`internal/authn`, Argon2id, single local admin); harden the server's default bind to loopback; and close the two identified token-leak surfaces (OpenTofu output writers, `CommandError.Stderr`) with integration tests proving the token never appears in a response body, log line, or stdout/stderr.

**Architecture:** `internal/compile` gains a second return value mapping unit directory to cluster name. `internal/planapply` gains a `Resolver` parameter (`func(ctx, ref) ([]byte, error)`, satisfied directly by `secrets.Store.Resolve`) threaded into both `PlanUnits` and `Apply`, which sets `Runner.Env` and wraps `Runner.Stdout`/`Stderr` and `CommandError.Stderr` with a redactor. `internal/proxmox` gains a pinned-TLS HTTP client. A new `internal/proxmoxbootstrap` package sequences Slice A's existing `CreateUser`/`CreateRole`/`UpdateACL`/`CreateAPIToken` calls. A new `internal/authn` package implements Argon2id password hashing and SQLite-backed sessions, with `Middleware` wrapping `internal/api`'s mux. `internal/webui` gains a static login page. `internal/cli` gains `nodr auth create-admin` and `nodr cluster connect` subcommands and changes `server`'s default `--addr`.

**Tech Stack:** Go 1.24, `golang.org/x/crypto/argon2`, `modernc.org/sqlite` (already a dependency), `crypto/tls`, `crypto/sha256`, `net/http/httptest`.

---

## File Structure

```
.worktrees/cluster-connect-wizard/
├── go.mod                                          # add golang.org/x/crypto
├── go.sum
├── internal/
│   ├── compile/
│   │   ├── compile.go                             # Compile returns (files, unitClusters, diags); addProviders records unitClusters
│   │   └── compile_test.go                         # 16 call sites updated for second return value; new unitClusters assertions
│   ├── planapply/
│   │   ├── planapply.go                            # Resolver type; PlanUnits/Apply gain resolve param; redactor; remove dead nil-Runner fallback
│   │   ├── planapply_test.go                       # existing tests updated for new param; new credential-wiring + redaction tests
│   │   └── redact.go                               # redactor io.Writer, scrubCommandError helper
│   ├── proxmox/
│   │   ├── tls.go                                  # NewPinnedHTTPClient, fingerprint comparison, producer of ErrFingerprintMismatch
│   │   └── tls_test.go                             # matching/mismatched fingerprint tests against httptest.NewTLSServer
│   ├── proxmoxbootstrap/
│   │   ├── bootstrap.go                            # Result, Bootstrap(ctx, client, privileges)
│   │   └── bootstrap_test.go                       # sequence assertions against proxmoxtest.NewServer
│   ├── authn/
│   │   ├── store.go                                # Store, Open, CreateAccount, Authenticate, ValidateSession, Logout, Close
│   │   ├── store_test.go                           # round-trip, wrong password, expiry, logout
│   │   ├── middleware.go                           # Middleware, LoginHandler, LogoutHandler
│   │   └── middleware_test.go                       # 401 without cookie, 200 with valid cookie, login/logout flow
│   ├── api/
│   │   ├── api.go                                  # Handler gains auth/secrets params; auth routes; middleware wraps mux; planWorkspace/applyWorkspace pass resolver
│   │   └── api_test.go                             # testHandler updated; new auth-gate tests; fake-tofu script echoes token; leak-proof assertions
│   ├── webui/
│   │   ├── server.go                                # LoginHandler wired at GET /login (static form, not SPA fallback)
│   │   └── login.html                              # new embedded static login form
│   └── cli/
│       ├── server.go                                # --addr default "127.0.0.1:8080"; opens authn.Store + secrets.Store; passes into api.Handler
│       ├── auth.go                                  # nodr auth create-admin subcommand
│       ├── auth_test.go
│       ├── cluster.go                               # nodr cluster connect subcommand (bootstrap wizard)
│       ├── cluster_test.go
│       ├── cli.go                                   # rootCommand registers authCommand, clusterCommand
│       └── plan.go                                  # planUnits/apply pass a.secretsStore().Resolve into planapply.PlanUnits/Apply
├── web/
│   └── src/
│       └── api.ts                                   # fetchJSON redirects to /login on 401
└── CHANGELOG.md                                      # Unreleased entries for this slice
```

---

### Task 1: Add `golang.org/x/crypto` dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add the dependency**

Run in worktree:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
go get golang.org/x/crypto@latest
go mod tidy
```

- [ ] **Step 2: Verify dependencies compile**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
go build ./...
```
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
git add go.mod go.sum
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "build(deps): add golang.org/x/crypto dependency"
```

---

### Task 2: `internal/compile` returns unit-to-cluster mapping

**Files:**
- Modify: `internal/compile/compile.go`
- Modify: `internal/compile/compile_test.go`

- [ ] **Step 1: Update the failing call sites**

`Compile`'s signature changes from `func Compile(ws *workspace.Workspace) (map[string][]byte, diag.List)` to `func Compile(ws *workspace.Workspace) (map[string][]byte, map[string]string, diag.List)`. Update every one of the 16 test call sites in `internal/compile/compile_test.go` (currently at lines `224, 230, 240, 242, 270, 286, 301, 318, 327, 350, 363, 379, 386, 394, 415, 427, 451, 463, 477, 490`) to accept the second return value, assigning to `_` unless the specific test asserts on it. Example of the mechanical change:
```go
// before
files, diags := compile.Compile(ws)
// after
files, _, diags := compile.Compile(ws)
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `go build ./internal/compile/...`
Expected: FAIL (`Compile` still returns two values, call sites now expect three — a deliberate intermediate state before Step 3's implementation change; if you prefer, do Steps 1 and 3 as a single atomic edit and skip this deliberately-failing checkpoint)

- [ ] **Step 3: Change `Compile` and `compiler` to expose `unitClusters`**

File: `internal/compile/compile.go`, replace the `Compile` function (lines 56-74) and the `compiler` struct's `units` field:
```go
// Compile writes the VirtualMachines of ws into the OpenTofu state units
// below terraform/ and returns the files that change, by workspace-relative
// path, with their new content, the cluster each unit directory's provider
// block reaches (workspace-relative unit directory -> ProxmoxCluster name,
// omitting units with no managed blocks), and diagnostics. It writes
// nothing itself, so callers can show the changes before they call Write.
//
// ... (rest of existing doc comment unchanged) ...
func Compile(ws *workspace.Workspace) (map[string][]byte, map[string]string, diag.List) {
	c := &compiler{
		ws:     ws,
		lens:   proxmoxvm.Lens{Resolver: resolve.NewIndex(ws.Documents)},
		disk:   map[string][]byte{},
		files:  map[string][]byte{},
		blocks: map[string][]proxmoxvm.ManagedBlock{},
		units:  map[string]map[string]bool{},
	}
	if c.scan() {
		for _, vm := range c.virtualMachines() {
			c.compileVM(vm)
		}
		c.warnOrphans()
		c.addUnitFiles()
	}
	c.diags.Sort()
	return c.changes(), c.unitClusters(), c.diags
}

// unitClusters returns, for each unit directory with managed blocks, the
// single cluster its provider block reaches. addProviders already rejects
// a unit whose VMs span more than one cluster, so this is always at most
// one name per directory.
func (c *compiler) unitClusters() map[string]string {
	out := make(map[string]string, len(c.units))
	for dir, clusters := range c.units {
		for cluster := range clusters {
			out[dir] = cluster
		}
	}
	return out
}
```
No change is needed to `compiler.units` itself (it already holds exactly this data via `addUnit`, in `internal/compile/unit.go:53-58`) — only the new accessor and the `Compile` signature.

- [ ] **Step 4: Add a test asserting `unitClusters`**

File: `internal/compile/compile_test.go`, add near the other `Compile` tests (find one that compiles a single VM against a single cluster, e.g. around line 224, and add alongside it):
```go
func TestCompile_UnitClusters(t *testing.T) {
	ws := testWorkspace(t /* reuse whatever helper builds a workspace with one VM on cluster pve-main in the surrounding tests */)
	_, unitClusters, diags := compile.Compile(ws)
	if diags.HasErrors() {
		t.Fatalf("diags = %v", diags)
	}
	want := map[string]string{"terraform/pve-main-compute": "pve-main"}
	if !reflect.DeepEqual(unitClusters, want) {
		t.Errorf("unitClusters = %v, want %v", unitClusters, want)
	}
}
```
Adjust the workspace-building helper name and cluster/unit names to match whatever fixture the surrounding tests in this file already use (read the file's existing helpers before writing this test; do not invent a fixture that duplicates one already present).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -v ./internal/compile/...`
Expected: PASS.

- [ ] **Step 6: Update the one production call site**

File: `internal/planapply/planapply.go`, line 142 changes from `files, diags := compile.Compile(ws)` to `files, unitClusters, diags := compile.Compile(ws)`. `unitClusters` is threaded into `PlanUnits` in Task 3; if Task 3 has not landed yet in this same worktree session, assign to `_` as a placeholder and revisit in Task 3 (do not leave an unused-variable compile error between tasks — prefer doing Task 2 and Task 3 as one commit if that is simpler for the implementing agent).

- [ ] **Step 7: Run full test suite**

Run: `go test ./...`
Expected: PASS (or a clearly-scoped failure isolated to `internal/planapply` if Step 6 was done as a placeholder — resolve before committing).

- [ ] **Step 8: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
git add internal/compile/ internal/planapply/planapply.go
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(compile): return unit-to-cluster mapping from Compile"
```

---

### Task 3: `internal/planapply` credential wiring and redaction

**Files:**
- Modify: `internal/planapply/planapply.go`
- Create: `internal/planapply/redact.go`
- Modify: `internal/planapply/planapply_test.go`
- Modify: `internal/cli/plan.go` (update the two callers)
- Modify: `internal/api/api.go` (update the two callers — full wiring completed in Task 7, but the signature must compile now)

- [ ] **Step 1: Write the failing tests**

File: `internal/planapply/planapply_test.go`, add new tests near the existing `PlanUnits`/`Apply` tests (read the file's existing fake-tofu-script setup helper before writing these, and reuse it rather than duplicating the script inline):
```go
func TestPlanUnits_ResolvesCredentials(t *testing.T) {
	// ws has one ProxmoxCluster "pve-main" with credentialsRef "proxmox/pve-main-token"
	// and one VM placed on it, using the shared test fixture helper this file
	// already provides.
	ws := testWorkspaceWithCluster(t)
	fake := installFakeTofu(t) // existing helper; extend its script (Step 3 below)
	resolve := func(_ context.Context, ref string) ([]byte, error) {
		if ref != "proxmox/pve-main-token" {
			t.Fatalf("ref = %q, want proxmox/pve-main-token", ref)
		}
		return []byte("test-token-secret-xyz"), nil
	}
	planDir := t.TempDir()
	_, plans, err := PlanUnits(t.Context(), ws, nil, planDir, nil, nil, resolve)
	if err != nil {
		t.Fatalf("PlanUnits: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans = %d, want 1", len(plans))
	}
	log, err := os.ReadFile(fake.log)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(log), "test-token-secret-xyz") {
		t.Errorf("log = %q, want it to contain the resolved token (the fake tofu script must echo $PROXMOX_VE_API_TOKEN on init)", log)
	}
}

func TestPlanUnits_NilResolverUnchanged(t *testing.T) {
	ws := testWorkspaceWithCluster(t)
	installFakeTofu(t)
	planDir := t.TempDir()
	_, plans, err := PlanUnits(t.Context(), ws, nil, planDir, nil, nil, nil)
	if err != nil {
		t.Fatalf("PlanUnits: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans = %d, want 1", len(plans))
	}
	// no assertion on env content: nil resolver means today's unchanged behavior.
}

func TestPlanUnits_ScrubsTokenFromCommandError(t *testing.T) {
	ws := testWorkspaceWithCluster(t)
	installFakeTofu(t)
	t.Setenv("FAKE_TOFU_FAIL", "pve-main-compute init") // adjust unit name to fixture
	resolve := func(context.Context, string) ([]byte, error) { return []byte("leak-me-token"), nil }
	planDir := t.TempDir()
	var out bytes.Buffer
	_, _, err := PlanUnits(t.Context(), ws, nil, planDir, &out, nil, resolve)
	if err == nil {
		t.Fatal("expected error")
	}
	var cmdErr *opentofu.CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("error = %v, want *opentofu.CommandError", err)
	}
	if strings.Contains(cmdErr.Stderr, "leak-me-token") {
		t.Errorf("CommandError.Stderr leaked the token: %q", cmdErr.Stderr)
	}
	if strings.Contains(out.String(), "leak-me-token") {
		t.Errorf("out leaked the token: %q", out.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/planapply/... -run TestPlanUnits_`
Expected: FAIL (`PlanUnits` does not yet accept a 7th parameter; fake-tofu script does not yet echo the token — extend it in Step 3 alongside the implementation, or as its own preparatory sub-step if the shared helper lives in a file this task does not otherwise touch).

- [ ] **Step 3: Extend the shared fake-tofu script to echo the token on init**

Locate `internal/planapply/planapply_test.go`'s (or a shared test-helper file it imports) fake tofu script — if `internal/cli/plan_test.go:38`'s script (`echo "initialized $unit with the token $PROXMOX_VE_API_TOKEN"`) is not already reachable from `internal/planapply`'s own tests, copy that one line's pattern into `internal/planapply`'s own script constant so `init` prints the token it received.

- [ ] **Step 4: Implement `redact.go`**

File: `internal/planapply/redact.go`
```go
package planapply

import (
	"bytes"
	"io"
)

// redactor wraps an io.Writer, replacing every occurrence of any secret in
// secrets with "<redacted>" before forwarding to w. A nil w makes Write a
// no-op, matching the "output discarded" behavior callers already rely on
// when they pass a nil out to PlanUnits/Apply.
type redactor struct {
	w       io.Writer
	secrets [][]byte
}

func newRedactor(w io.Writer, secrets [][]byte) io.Writer {
	if w == nil || len(secrets) == 0 {
		return w
	}
	return &redactor{w: w, secrets: secrets}
}

func (r *redactor) Write(p []byte) (int, error) {
	out := p
	for _, secret := range r.secrets {
		if len(secret) == 0 {
			continue
		}
		out = bytes.ReplaceAll(out, secret, []byte("<redacted>"))
	}
	if _, err := r.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// scrubCommandError returns err with any *opentofu.CommandError's Stderr
// field scrubbed of every secret in secrets, in place. Other error types
// pass through unchanged.
func scrubCommandError(err error, secrets [][]byte) error {
	var cmdErr *opentofu.CommandError
	if !errors.As(err, &cmdErr) {
		return err
	}
	scrubbed := []byte(cmdErr.Stderr)
	for _, secret := range secrets {
		if len(secret) == 0 {
			continue
		}
		scrubbed = bytes.ReplaceAll(scrubbed, secret, []byte("<redacted>"))
	}
	cmdErr.Stderr = string(scrubbed)
	return err
}
```
Add `"errors"` and `"github.com/centopw/nodr/internal/engine/opentofu"` to the import block.

- [ ] **Step 5: Add `Resolver` type and wire both functions**

File: `internal/planapply/planapply.go`. Add near the top, after the existing type declarations:
```go
// Resolver resolves a credentialsRef to its secret value. secrets.Store's
// Resolve method satisfies this directly. A nil Resolver injects no
// credentials, matching the behavior before this type existed.
type Resolver func(ctx context.Context, ref string) ([]byte, error)
```
Change `PlanUnits`'s signature (currently lines ~134-183) to accept `resolve Resolver` as a new final parameter, and inside the loop over `units` (lines ~166-177), after `compile.Compile` is called (Task 2 already changed this call site to also return `unitClusters`), resolve and inject before `u.Runner.Init`:
```go
func PlanUnits(ctx context.Context, ws *workspace.Workspace, only []string, planDir string, out io.Writer, interrupts <-chan struct{}, resolve Resolver) (written map[string][]byte, plans []UnitPlan, err error) {
	bin, err := opentofu.LookPath()
	if err != nil {
		return nil, nil, err
	}
	files, unitClusters, diags := compile.Compile(ws)
	// ... existing diag print / CompileError handling unchanged ...
	units, err := StateUnits(ws.FS, files)
	// ... existing SelectUnits / Write / len==0 handling unchanged ...
	plans = make([]UnitPlan, 0, len(units))
	var secrets [][]byte
	for _, dir := range units {
		var env []string
		if resolve != nil {
			if cluster := unitClusters[dir]; cluster != "" {
				if ref, ok := credentialsRefFor(ws, cluster); ok {
					secret, err := resolve(ctx, ref)
					if err != nil {
						return files, nil, UnitError(dir, fmt.Errorf("resolve credentials for cluster %s: %w", cluster, err))
					}
					env = []string{"PROXMOX_VE_API_TOKEN=" + string(secret)}
					secrets = append(secrets, secret)
				}
			}
		}
		u := UnitPlan{
			Dir: dir,
			Runner: &opentofu.Runner{
				Binary:     bin,
				Dir:        filepath.Join(ws.Root, filepath.FromSlash(dir)),
				Stdout:     newRedactor(out, secrets),
				Stderr:     newRedactor(out, secrets),
				Env:        env,
				Interrupts: interrupts,
			},
			File: filepath.Join(planDir, path.Base(dir)+".tfplan"),
		}
		if err := u.Runner.Init(ctx); err != nil {
			return files, nil, UnitError(dir, scrubCommandError(err, secrets))
		}
		if u.Plan, err = u.Runner.Plan(ctx, u.File); err != nil {
			return files, nil, UnitError(dir, scrubCommandError(err, secrets))
		}
		plans = append(plans, u)
	}
	return files, plans, nil
}

// credentialsRefFor looks up the credentialsRef of the named ProxmoxCluster.
func credentialsRefFor(ws *workspace.Workspace, cluster string) (string, bool) {
	d := ws.Find(nrm.Ref{Kind: v1alpha1.KindProxmoxCluster, Name: cluster})
	if d == nil {
		return "", false
	}
	spec, ok := v1alpha1.Decode[v1alpha1.ProxmoxClusterSpec](d)
	if !ok || spec.Spec.CredentialsRef == "" {
		return "", false
	}
	return spec.Spec.CredentialsRef, true
}
```
Add `"github.com/centopw/nodr/internal/nrm"` and `"github.com/centopw/nodr/internal/nrm/v1alpha1"` to the import block. Adjust `v1alpha1.Decode`'s exact call shape to match its real signature (confirm via the existing usage in `internal/compile/unit.go:115-116` — `cluster, _ := v1alpha1.Decode[v1alpha1.ProxmoxClusterSpec](d)` returns `(*v1alpha1.ProxmoxCluster, bool)` or similar; read that call site before writing this helper to match its exact return shape rather than guessing).

Change `Apply`'s signature (currently lines ~194-214) to accept `resolve Resolver`, remove the dead `runner == nil` fallback (lines ~196-202), and re-resolve per unit exactly as `PlanUnits` does, scrubbing any error the same way:
```go
func Apply(ctx context.Context, plans []UnitPlan, out io.Writer, interrupts <-chan struct{}, resolve Resolver) (applied []string, err error) {
	applied = make([]string, 0, len(plans))
	for _, u := range plans {
		runner := u.Runner
		var secrets [][]byte
		if resolve != nil && len(runner.Env) > 0 {
			for _, kv := range runner.Env {
				if strings.HasPrefix(kv, "PROXMOX_VE_API_TOKEN=") {
					secrets = append(secrets, []byte(strings.TrimPrefix(kv, "PROXMOX_VE_API_TOKEN=")))
				}
			}
		}
		runner.Stdout = newRedactor(out, secrets)
		runner.Stderr = newRedactor(out, secrets)
		runner.Interrupts = interrupts
		if err := runner.Apply(ctx, u.File); err != nil {
			return applied, UnitError(u.Dir, scrubCommandError(err, secrets))
		}
		applied = append(applied, u.Dir)
	}
	return applied, nil
}
```
`Apply` reuses the `Env` `PlanUnits` already set on each `UnitPlan.Runner` (plans are saved and re-applied, potentially by a separate process invocation, but `UnitPlan.Runner` is an in-memory struct carried alongside the saved `.tfplan` file within one call to `PlanUnits`+`Apply` in the same process — confirm this is true for both the CLI's and API's call patterns before relying on it; if a real cross-process gap exists — read `internal/api/api.go`'s `storedPlan` struct and `getStoredPlan` to confirm `stored.plans` are the same in-memory `[]UnitPlan` from the original `PlanUnits` call, not reloaded from disk — this reuse is safe and no separate resolution in `Apply` is needed; if it is not, adapt `Apply` to accept `resolve` and re-run the same per-unit-cluster lookup `PlanUnits` does, requiring `ws` as an added parameter). Add `"strings"` to imports if not already present.

- [ ] **Step 6: Update the two callers to compile**

File: `internal/cli/plan.go`, line 110: `planapply.PlanUnits(ctx, l.ws, only, planDir, a.stderr, a.interrupts)` becomes `planapply.PlanUnits(ctx, l.ws, only, planDir, a.stderr, a.interrupts, nil)` — pass `nil` as a placeholder in this task; Task 9 replaces it with `a.secretsStore().Resolve`. Line 163: `planapply.Apply(ctx, plans, a.stderr, a.interrupts)` becomes `planapply.Apply(ctx, plans, a.stderr, a.interrupts, nil)`, same placeholder.

File: `internal/api/api.go`, line 921: `planapply.PlanUnits(r.Context(), ws, nil, planDir, nil, nil)` becomes `planapply.PlanUnits(r.Context(), ws, nil, planDir, nil, nil, nil)` — placeholder, Task 7 replaces the final `nil` with `a.secrets.Resolve`. Line 1041: `planapply.Apply(ctx, stored.plans, nil, nil)` becomes `planapply.Apply(ctx, stored.plans, nil, nil, nil)`, same placeholder.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/planapply/... ./internal/cli/... ./internal/api/...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
git add internal/planapply/ internal/cli/plan.go internal/api/api.go
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(planapply): resolve credentialsRef into Runner.Env with output redaction"
```

---

### Task 4: `internal/proxmox` TLS fingerprint pinning

**Files:**
- Create: `internal/proxmox/tls.go`
- Create: `internal/proxmox/tls_test.go`

- [ ] **Step 1: Write the failing tests**

File: `internal/proxmox/tls_test.go`
```go
package proxmox_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/proxmox"
)

func TestNewPinnedHTTPClient_MatchingFingerprintSucceeds(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fp := fingerprintOf(t, srv.Certificate().Raw)
	client := proxmox.NewPinnedHTTPClient(fp)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
}

func TestNewPinnedHTTPClient_MismatchedFingerprintFails(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := proxmox.NewPinnedHTTPClient("00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF")
	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, proxmox.ErrFingerprintMismatch) {
		t.Errorf("err = %v, want ErrFingerprintMismatch", err)
	}
}

func fingerprintOf(t *testing.T, der []byte) string {
	t.Helper()
	sum := sha256.Sum256(der)
	hexParts := make([]string, len(sum))
	for i, b := range sum {
		hexParts[i] = strings.ToUpper(hex.EncodeToString([]byte{b}))
	}
	return strings.Join(hexParts, ":")
	_ = fmt.Sprintf // placeholder to keep import if unused after edits; remove if fmt ends up used elsewhere
}
```
(Remove the placeholder `fmt` import/usage line if it turns out unnecessary once the helper compiles cleanly — it exists only to avoid an unused-import error if you trim the helper differently; prefer deleting `"fmt"` from imports and that line entirely, which is cleaner.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/proxmox/... -run TestNewPinnedHTTPClient`
Expected: FAIL (`NewPinnedHTTPClient` not defined).

- [ ] **Step 3: Implement `tls.go`**

File: `internal/proxmox/tls.go`
```go
package proxmox

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"strings"
)

// NewPinnedHTTPClient returns an *http.Client whose TLS handshakes are
// verified against fingerprint (Proxmox VE's own colon-separated uppercase
// hex SHA-256 format, as returned by GetCertFingerprint) instead of the
// system certificate pool. A handshake with a server whose leaf certificate
// does not match fails with ErrFingerprintMismatch.
//
// InsecureSkipVerify is set deliberately: VerifyPeerCertificate below
// replaces Go's chain validation with the fingerprint pin, which is
// Proxmox's own trust model for self-signed cluster certificates.
func NewPinnedHTTPClient(fingerprint string) *http.Client {
	want := normalizeFingerprint(fingerprint)
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // fingerprint pinning below replaces chain validation
				VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
					if len(rawCerts) == 0 {
						return ErrFingerprintMismatch
					}
					sum := sha256.Sum256(rawCerts[0])
					if normalizeFingerprint(hex.EncodeToString(sum[:])) != want {
						return ErrFingerprintMismatch
					}
					return nil
				},
			},
		},
	}
}

// normalizeFingerprint strips colons and lowercases, so pinned values in
// either Proxmox's colon-separated uppercase format or a plain hex string
// compare equal.
func normalizeFingerprint(fp string) string {
	return strings.ToLower(strings.ReplaceAll(fp, ":", ""))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v ./internal/proxmox/... -run TestNewPinnedHTTPClient`
Expected: PASS.

- [ ] **Step 5: Run full proxmox package suite**

Run: `go test ./internal/proxmox/...`
Expected: PASS (Slice A's existing tests unaffected).

- [ ] **Step 6: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
git add internal/proxmox/tls.go internal/proxmox/tls_test.go
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(proxmox): add TLS fingerprint pinning"
```

---

### Task 5: `internal/proxmoxbootstrap` — bootstrap flow

**Files:**
- Create: `internal/proxmoxbootstrap/bootstrap.go`
- Create: `internal/proxmoxbootstrap/bootstrap_test.go`

- [ ] **Step 1: Write the failing test**

File: `internal/proxmoxbootstrap/bootstrap_test.go`
```go
package proxmoxbootstrap_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/proxmox/proxmoxtest"
	"github.com/centopw/nodr/internal/proxmoxbootstrap"
)

func TestBootstrap_Sequence(t *testing.T) {
	var calls []string
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"POST /api2/json/access/users": func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, "CreateUser")
			proxmoxtest.JSONResponse(w, http.StatusOK, "")
		},
		"POST /api2/json/access/roles": func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, "CreateRole")
			proxmoxtest.JSONResponse(w, http.StatusOK, "")
		},
		"PUT /api2/json/access/acl": func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, "UpdateACL")
			proxmoxtest.JSONResponse(w, http.StatusOK, "")
		},
		"POST /api2/json/access/users/nodr@pve/token/nodr": func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, "CreateAPIToken")
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"value":"bootstrap-secret-abc"}`)
		},
	})
	defer srv.Close()

	client := proxmox.NewClient(srv.URL, nil)
	client.SetAPIToken("root@pam", "admin-token", "admin-secret")

	result, err := proxmoxbootstrap.Bootstrap(context.Background(), client, []string{"VM.Audit"})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if result.TokenSecret != "bootstrap-secret-abc" {
		t.Errorf("TokenSecret = %q", result.TokenSecret)
	}
	if result.User != "nodr@pve" || result.Role != "NodrOperator" || result.TokenID != "nodr" {
		t.Errorf("result = %+v", result)
	}
	want := []string{"CreateUser", "CreateRole", "UpdateACL", "CreateAPIToken"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("calls[%d] = %q, want %q", i, calls[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/proxmoxbootstrap/...`
Expected: FAIL (package does not exist).

- [ ] **Step 3: Implement `bootstrap.go`**

File: `internal/proxmoxbootstrap/bootstrap.go`
```go
// Package proxmoxbootstrap sequences the Proxmox VE API calls that create a
// dedicated, least-privilege nodr user, role and API token (design
// docs/design/06-proxmox.md §6.2), reusing internal/proxmox's client.
package proxmoxbootstrap

import (
	"context"
	"fmt"

	"github.com/centopw/nodr/internal/proxmox"
)

// Result holds everything the caller needs to store and record. TokenSecret
// is the plaintext API token secret; the caller must store it via
// secrets.Store.Put and must not log it.
type Result struct {
	User        string
	Role        string
	TokenID     string
	TokenSecret string
}

const (
	bootstrapUser    = "nodr@pve"
	bootstrapRole    = "NodrOperator"
	bootstrapTokenID = "nodr"
)

// Bootstrap creates the dedicated user, role and API token on the cluster
// client authenticates against, and grants the role on "/" for the new
// user. client must already be authenticated as an administrator (via
// Login or SetAPIToken); Bootstrap does not manage that credential's
// lifetime — the caller discards it after this call returns.
func Bootstrap(ctx context.Context, client *proxmox.Client, privileges []string) (Result, error) {
	if err := client.CreateUser(ctx, bootstrapUser, proxmox.UserOptions{Comment: "created by nodr cluster connect"}); err != nil {
		return Result{}, fmt.Errorf("proxmoxbootstrap: create user: %w", err)
	}
	if err := client.CreateRole(ctx, bootstrapRole, privileges); err != nil {
		return Result{}, fmt.Errorf("proxmoxbootstrap: create role: %w", err)
	}
	if err := client.UpdateACL(ctx, "/", map[string][]string{bootstrapUser: {bootstrapRole}}); err != nil {
		return Result{}, fmt.Errorf("proxmoxbootstrap: update ACL: %w", err)
	}
	secret, err := client.CreateAPIToken(ctx, bootstrapUser, bootstrapTokenID)
	if err != nil {
		return Result{}, fmt.Errorf("proxmoxbootstrap: create API token: %w", err)
	}
	return Result{
		User:        bootstrapUser,
		Role:        bootstrapRole,
		TokenID:     bootstrapTokenID,
		TokenSecret: secret,
	}, nil
}

// Privileges is the fixed privilege list for NodrOperator (design
// docs/design/06-proxmox.md §6.2's table), covering Proxmox VE 8.x and 9.x.
var Privileges = []string{
	"VM.Allocate", "VM.Audit", "VM.Clone", "VM.Config.CDROM", "VM.Config.CPU",
	"VM.Config.Cloudinit", "VM.Config.Disk", "VM.Config.HWType", "VM.Config.Memory",
	"VM.Config.Network", "VM.Config.Options", "VM.PowerMgmt", "VM.Migrate",
	"VM.Snapshot", "VM.Snapshot.Rollback", "VM.Backup", "VM.Console",
	"Datastore.Allocate", "Datastore.AllocateSpace", "Datastore.AllocateTemplate", "Datastore.Audit",
	"Pool.Allocate", "Pool.Audit", "Mapping.Use", "Mapping.Audit",
	"SDN.Use", "SDN.Audit",
	"Sys.Audit", "Sys.Modify",
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v ./internal/proxmoxbootstrap/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
git add internal/proxmoxbootstrap/
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(proxmoxbootstrap): sequence least-privilege user/role/token creation"
```

---

### Task 6: `internal/authn` — Argon2id accounts and sessions

**Files:**
- Create: `internal/authn/store.go`
- Create: `internal/authn/store_test.go`
- Create: `internal/authn/middleware.go`
- Create: `internal/authn/middleware_test.go`

- [ ] **Step 1: Write the failing store tests**

File: `internal/authn/store_test.go`
```go
package authn_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/centopw/nodr/internal/authn"
)

func openStore(t *testing.T) *authn.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := authn.Open(filepath.Join(dir, "authn.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStore_CreateAndAuthenticate(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateAccount(ctx, "admin", "correct horse battery staple"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token, expiresAt, err := s.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("expiresAt = %v, want future", expiresAt)
	}
	if !s.ValidateSession(ctx, token) {
		t.Error("ValidateSession = false, want true")
	}
}

func TestStore_WrongPasswordRejected(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateAccount(ctx, "admin", "correct horse battery staple"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, _, err := s.Authenticate(ctx, "admin", "wrong password")
	if !errors.Is(err, authn.ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestStore_LogoutInvalidatesSession(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateAccount(ctx, "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token, _, err := s.Authenticate(ctx, "admin", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if err := s.Logout(ctx, token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if s.ValidateSession(ctx, token) {
		t.Error("ValidateSession = true after Logout, want false")
	}
}

func TestStore_CreateAccountReplacesExisting(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateAccount(ctx, "admin", "first-password"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := s.CreateAccount(ctx, "admin2", "second-password"); err != nil {
		t.Fatalf("CreateAccount (replace): %v", err)
	}
	if _, _, err := s.Authenticate(ctx, "admin", "first-password"); !errors.Is(err, authn.ErrInvalidCredentials) {
		t.Errorf("old account still authenticates: %v", err)
	}
	if _, _, err := s.Authenticate(ctx, "admin2", "second-password"); err != nil {
		t.Errorf("new account: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/authn/...`
Expected: FAIL (package does not exist).

- [ ] **Step 3: Implement `store.go`**

File: `internal/authn/store.go`
```go
// Package authn implements the single local administrator account and
// session-cookie authentication for nodr's dashboard and API
// (docs/design/10-security.md §10.3).
package authn

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	_ "modernc.org/sqlite"
)

// ErrInvalidCredentials reports a wrong username or password.
var ErrInvalidCredentials = errors.New("authn: invalid credentials")

// sessionTTL is the fixed absolute session lifetime for this slice; idle
// timeouts are deferred (see the design doc's open risks).
const sessionTTL = 24 * time.Hour

// Store persists the one local administrator account and active sessions
// in a SQLite database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS account (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    username      TEXT NOT NULL,
    password_hash TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS session (
    token      TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL
);
`

// Open opens or creates the authn database at dbPath, mode 0600.
func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("authn: create dir: %w", err)
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("authn: create db file: %w", err)
		}
		_ = f.Close()
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("authn: open sqlite db: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("authn: init schema: %w", err)
	}
	return &Store{db: db}, nil
}

// argon2Params follows the OWASP-recommended baseline for Argon2id.
const (
	argon2Time    = 1
	argon2Memory  = 64 * 1024
	argon2Threads = 4
	argon2KeyLen  = 32
	saltLen       = 16
)

func hashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("authn: generate salt: %w", err)
	}
	sum := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argon2Memory, argon2Time, argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum),
	), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var memory, time_, threads uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time_, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, time_, memory, uint8(threads), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// CreateAccount sets the one local admin account, replacing any existing
// one and invalidating all existing sessions.
func (s *Store) CreateAccount(ctx context.Context, username, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("authn: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op
	if _, err := tx.ExecContext(ctx, `DELETE FROM session`); err != nil {
		return fmt.Errorf("authn: clear sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO account (id, username, password_hash) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET username = excluded.username, password_hash = excluded.password_hash
	`, username, hash); err != nil {
		return fmt.Errorf("authn: upsert account: %w", err)
	}
	return tx.Commit()
}

// Authenticate checks username/password and, on success, creates a new
// session and returns its opaque token and absolute expiry.
func (s *Store) Authenticate(ctx context.Context, username, password string) (string, time.Time, error) {
	var storedUsername, hash string
	err := s.db.QueryRowContext(ctx, `SELECT username, password_hash FROM account WHERE id = 1`).Scan(&storedUsername, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrInvalidCredentials
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("authn: query account: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(storedUsername), []byte(username)) != 1 || !verifyPassword(password, hash) {
		return "", time.Time{}, ErrInvalidCredentials
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", time.Time{}, fmt.Errorf("authn: generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expiresAt := time.Now().Add(sessionTTL)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO session (token, expires_at) VALUES (?, ?)`, token, expiresAt.Unix()); err != nil {
		return "", time.Time{}, fmt.Errorf("authn: create session: %w", err)
	}
	return token, expiresAt, nil
}

// ValidateSession reports whether token is a live, unexpired session.
func (s *Store) ValidateSession(ctx context.Context, token string) bool {
	if token == "" {
		return false
	}
	var expiresAt int64
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM session WHERE token = ?`, token).Scan(&expiresAt)
	if err != nil {
		return false
	}
	return time.Now().Before(time.Unix(expiresAt, 0))
}

// Logout deletes the session for token.
func (s *Store) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM session WHERE token = ?`, token)
	if err != nil {
		return fmt.Errorf("authn: delete session: %w", err)
	}
	return nil
}

// Close closes the underlying SQLite database.
func (s *Store) Close() error {
	return s.db.Close()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v ./internal/authn/... -run TestStore`
Expected: PASS.

- [ ] **Step 5: Write the failing middleware tests**

File: `internal/authn/middleware_test.go`
```go
package authn_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/authn"
)

func TestMiddleware_RejectsWithoutSession(t *testing.T) {
	s := openStore(t)
	protected := authn.Middleware(s, "/login")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestMiddleware_AllowsLoginPath(t *testing.T) {
	s := openStore(t)
	called := false
	protected := authn.Middleware(s, "/login")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if !called {
		t.Error("login path was blocked")
	}
}

func TestLoginHandler_SetsSessionCookieAndMiddlewareAccepts(t *testing.T) {
	s := openStore(t)
	if err := s.CreateAccount(t.Context(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	login := authn.LoginHandler(s, "/login")
	loginReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"password12345"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	login.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", loginRec.Code, loginRec.Body.String())
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "nodr_session" {
		t.Fatalf("cookies = %v, want one nodr_session cookie", cookies)
	}

	protected := authn.Middleware(s, "/login")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	req.AddCookie(cookies[0])
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestLoginHandler_WrongPasswordRejected(t *testing.T) {
	s := openStore(t)
	if err := s.CreateAccount(t.Context(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	login := authn.LoginHandler(s, "/login")
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	login.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestLogoutHandler_ClearsSession(t *testing.T) {
	s := openStore(t)
	if err := s.CreateAccount(t.Context(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token, _, err := s.Authenticate(t.Context(), "admin", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	logout := authn.LogoutHandler(s, "/logout")
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: "nodr_session", Value: token})
	rec := httptest.NewRecorder()
	logout.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if s.ValidateSession(t.Context(), token) {
		t.Error("session still valid after logout")
	}
}
```

- [ ] **Step 6: Run tests to verify they fail**

Run: `go test ./internal/authn/... -run 'TestMiddleware|TestLoginHandler|TestLogoutHandler'`
Expected: FAIL (`Middleware`/`LoginHandler`/`LogoutHandler` not defined).

- [ ] **Step 7: Implement `middleware.go`**

File: `internal/authn/middleware.go`
```go
package authn

import (
	"encoding/json"
	"net/http"
)

const sessionCookieName = "nodr_session"

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: "about:blank", Title: title, Status: status, Detail: detail})
}

// Middleware wraps h, requiring a valid session cookie for every request
// whose path is not exactly one of allowPaths.
func Middleware(store *Store, allowPaths ...string) func(http.Handler) http.Handler {
	allow := make(map[string]bool, len(allowPaths))
	for _, p := range allowPaths {
		allow[p] = true
	}
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if allow[r.URL.Path] {
				h.ServeHTTP(w, r)
				return
			}
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil || !store.ValidateSession(r.Context(), cookie.Value) {
				writeProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid session is required")
				return
			}
			h.ServeHTTP(w, r)
		})
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginHandler handles a login POST: decodes {"username","password"},
// authenticates, and on success sets the session cookie.
func LoginHandler(store *Store, _ string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid request", "request body must be JSON with username and password")
			return
		}
		token, expiresAt, err := store.Authenticate(r.Context(), req.Username, req.Password)
		if err != nil {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "invalid username or password")
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    token,
			Path:     "/",
			Expires:  expiresAt,
			HttpOnly: true,
			Secure:   r.TLS != nil,
			SameSite: http.SameSiteStrictMode,
		})
		w.WriteHeader(http.StatusOK)
	})
}

// LogoutHandler handles a logout POST: deletes the session and clears the cookie.
func LogoutHandler(store *Store, _ string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			_ = store.Logout(r.Context(), cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		w.WriteHeader(http.StatusOK)
	})
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test -v ./internal/authn/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
git add internal/authn/
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(authn): add Argon2id accounts and session-cookie middleware"
```

---

### Task 7: `internal/api` auth gate and secrets wiring

**Files:**
- Modify: `internal/api/api.go`
- Modify: `internal/api/api_test.go`

- [ ] **Step 1: Update `Handler`'s signature and route registration**

File: `internal/api/api.go`. `Handler` (lines 36-56) gains `auth *authn.Store, secretsStore *secrets.Store` parameters, registers the two new unauthenticated routes, and wraps the existing mux with `authn.Middleware`:
```go
func Handler(ctx context.Context, root string, auth *authn.Store, secretsStore *secrets.Store) http.Handler {
	a := &server{
		ctx:     ctx,
		root:    root,
		plans:   make(map[string]*storedPlan),
		secrets: secretsStore,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+apiPrefix+"/workspaces", a.listWorkspaces)
	mux.HandleFunc("GET "+apiPrefix+"/workspaces/{workspace}", a.getWorkspace)
	mux.HandleFunc("GET "+apiPrefix+"/workspaces/{workspace}/resources", a.listResources)
	mux.HandleFunc("POST "+apiPrefix+"/workspaces/{workspace}/commands", a.runCommand)
	mux.HandleFunc("POST "+apiPrefix+"/workspaces/{workspace}/resources/{kind}/{name}", a.resourceAction)
	mux.HandleFunc("DELETE "+apiPrefix+"/workspaces/{workspace}/resources/{kind}/{name}", a.deleteResource)
	mux.HandleFunc(apiPrefix+"/workspaces", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}/resources", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}/commands", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}/resources/{kind}/{name}", methodNotAllowed)

	root2 := http.NewServeMux()
	root2.HandleFunc("POST "+apiPrefix+"/auth/login", func(w http.ResponseWriter, r *http.Request) {
		authn.LoginHandler(auth, apiPrefix+"/auth/login").ServeHTTP(w, r)
	})
	root2.HandleFunc("POST "+apiPrefix+"/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		authn.LogoutHandler(auth, apiPrefix+"/auth/logout").ServeHTTP(w, r)
	})
	root2.Handle("/", authn.Middleware(auth, apiPrefix+"/auth/login", apiPrefix+"/auth/logout")(mux))
	root2.HandleFunc("/api/", notFound)
	return root2
}
```
Add `"github.com/centopw/nodr/internal/authn"` and `"github.com/centopw/nodr/internal/secrets"` to the import block. Add a `secrets *secrets.Store` field to the `server` struct (lines 65-72).

- [ ] **Step 2: Wire the resolver into `planWorkspace`/`applyWorkspace`**

Line 921: `planapply.PlanUnits(r.Context(), ws, nil, planDir, nil, nil, nil)` becomes `planapply.PlanUnits(r.Context(), ws, nil, planDir, nil, nil, a.secrets.Resolve)`. Line 1041: `planapply.Apply(ctx, stored.plans, nil, nil, nil)` becomes `planapply.Apply(ctx, stored.plans, nil, nil, a.secrets.Resolve)`.

- [ ] **Step 3: Update `testHandler` and add an authenticated-request test helper**

File: `internal/api/api_test.go`. Replace `testHandler` (lines 126-128):
```go
func testHandler(t *testing.T, root string) http.Handler {
	t.Helper()
	return testHandlerWithAuth(t, root)
}

// authedRequest wraps testHandler's handler so tests that don't care about
// the auth gate itself can keep calling the API without a session cookie;
// it logs in once per test and attaches the resulting cookie to req.
func authedRequest(t *testing.T, h http.Handler, req *http.Request) *http.Request {
	t.Helper()
	loginReq := httptest.NewRequest(http.MethodPost, apiPrefix+"/auth/login", strings.NewReader(`{"username":"admin","password":"test-password-123"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login failed: status = %d, body = %s", loginRec.Code, loginRec.Body.String())
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	req.AddCookie(cookies[0])
	return req
}

func testHandlerWithAuth(t *testing.T, root string) http.Handler {
	t.Helper()
	dir := t.TempDir()
	authStore, err := authn.Open(filepath.Join(dir, "authn.db"))
	if err != nil {
		t.Fatalf("authn.Open: %v", err)
	}
	t.Cleanup(func() { authStore.Close() })
	if err := authStore.CreateAccount(t.Context(), "admin", "test-password-123"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	kek := make([]byte, 32)
	secretsStore, err := secrets.Open(filepath.Join(dir, "secrets.db"), kek)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	t.Cleanup(func() { secretsStore.Close() })
	return Handler(t.Context(), root, authStore, secretsStore)
}
```
Add `"github.com/centopw/nodr/internal/authn"` and `"github.com/centopw/nodr/internal/secrets"` to the import block.

Update **every** existing test in this file that issues a request against `testHandler`'s result to route the request through `authedRequest` first — this is a large mechanical edit across all tests currently listed in the handoff's test inventory (`TestListWorkspaces`, `TestGetWorkspace`, `TestListResources`, `TestCreateVM`, `TestWorkspacePlan`, `TestWorkspaceApplySuccess`, `TestVMLifecycleCommands`, etc.). The pattern for each call site:
```go
// before
req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil)
rec := httptest.NewRecorder()
handler.ServeHTTP(rec, req)
// after
req := authedRequest(t, handler, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil))
rec := httptest.NewRecorder()
handler.ServeHTTP(rec, req)
```
Read each test's existing request-construction lines before editing (do not guess at line numbers you have not just re-read) and apply this transform consistently.

- [ ] **Step 4: Add auth-gate tests**

Add near the top of the test file (after `testHandlerWithAuth`):
```go
func TestAuthGate_RejectsUnauthenticatedListWorkspaces(t *testing.T) {
	root := testWorkspace(t)
	handler := testHandler(t, root)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAuthGate_RejectsUnauthenticatedCreateVM(t *testing.T) {
	root := testWorkspace(t)
	handler := testHandler(t, root)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/homelab/commands", strings.NewReader(`{"command":"vm.create","params":{}}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAuthGate_AllowsAuthenticatedRequest(t *testing.T) {
	root := testWorkspace(t)
	handler := testHandler(t, root)
	req := authedRequest(t, handler, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 5: Extend the fake tofu script to echo the token, and add leak-proof plan/apply tests**

Extend `fakeTofuScript`'s `init)` case (currently `echo "initialized $unit"`, around line 547) to `echo "initialized $unit with the token $PROXMOX_VE_API_TOKEN"`, matching `internal/cli/plan_test.go`'s existing pattern. Update `testPlatform`'s fixture cluster (already has `credentialsRef: proxmox/pve-main-token` per the existing constant at line 40) — add a step in whichever test exercises `planWorkspace` to first `Put` a known secret at that ref via the test's `secretsStore`, then assert the log contains it and the response body/detail do not:
```go
func TestWorkspacePlan_ResolvesAndDoesNotLeakToken(t *testing.T) {
	root := testWorkspace(t)
	dir := t.TempDir()
	authStore, _ := authn.Open(filepath.Join(dir, "authn.db"))
	defer authStore.Close()
	authStore.CreateAccount(t.Context(), "admin", "test-password-123")
	kek := make([]byte, 32)
	secretsStore, _ := secrets.Open(filepath.Join(dir, "secrets.db"), kek)
	defer secretsStore.Close()
	secretsStore.Put(t.Context(), "proxmox/pve-main-token", []byte("plan-leak-check-token"))
	handler := Handler(t.Context(), root, authStore, secretsStore)

	fake := installFakeTofu(t)
	req := authedRequest(t, handler, httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/homelab/commands", strings.NewReader(`{"command":"workspace.plan"}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "plan-leak-check-token") {
		t.Errorf("response body leaked the token: %s", rec.Body.String())
	}
	log, _ := os.ReadFile(fake.log)
	if !strings.Contains(string(log), "plan-leak-check-token") {
		t.Errorf("fake tofu log = %q, want it to contain the resolved token (proves it reached the child process)", log)
	}
}

func TestWorkspacePlan_ErrorResponseDoesNotLeakToken(t *testing.T) {
	root := testWorkspace(t)
	dir := t.TempDir()
	authStore, _ := authn.Open(filepath.Join(dir, "authn.db"))
	defer authStore.Close()
	authStore.CreateAccount(t.Context(), "admin", "test-password-123")
	kek := make([]byte, 32)
	secretsStore, _ := secrets.Open(filepath.Join(dir, "secrets.db"), kek)
	defer secretsStore.Close()
	secretsStore.Put(t.Context(), "proxmox/pve-main-token", []byte("error-leak-check-token"))
	handler := Handler(t.Context(), root, authStore, secretsStore)

	installFakeTofu(t)
	t.Setenv("FAKE_TOFU_FAIL", "pve-main-compute init") // adjust unit name to fixture; the fake script must echo the failing unit's stderr including any interpolated content it has access to
	req := authedRequest(t, handler, httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/homelab/commands", strings.NewReader(`{"command":"workspace.plan"}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "error-leak-check-token") {
		t.Errorf("error response leaked the token: %s", rec.Body.String())
	}
}
```
(`TestWorkspacePlan_ErrorResponseDoesNotLeakToken` primarily proves the scrubbing path holds even when the failure is unrelated to the token; if the fake script needs to be told to echo the token on failure too to make this a meaningful assertion, extend the `FAKE_TOFU_FAIL` branch to also `echo "...$PROXMOX_VE_API_TOKEN..." >&2` before `exit 1`, matching the same env var it already reads on `init`.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -v ./internal/api/...`
Expected: PASS. This step will surface every remaining unauthenticated call site from Step 3 that was missed — fix each one and re-run until the full package passes.

- [ ] **Step 7: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
git add internal/api/
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(api): gate every route behind session auth and resolve credentials for plan/apply"
```

---

### Task 8: `internal/webui` login page

**Files:**
- Create: `internal/webui/login.html`
- Modify: `internal/webui/server.go`
- Create/Modify: `internal/webui/server_test.go`

- [ ] **Step 1: Write the failing test**

Find or create `internal/webui/server_test.go`; add:
```go
func TestHandler_ServesLoginPageUnauthenticated(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<form") {
		t.Errorf("body does not contain a login form: %s", rec.Body.String())
	}
}

func TestHandler_LoginPathNeverFallsThroughToSPA(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "<div id=\"root\">") {
		t.Error("served the SPA shell instead of the login form")
	}
}
```
(Adjust the SPA-shell marker string to whatever `web/dist/index.html` actually contains once built — read it if uncertain rather than guessing a marker that may not exist.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/webui/...`
Expected: FAIL (`/login` currently falls through to the SPA's `index.html` via `handlerFS`'s fallback).

- [ ] **Step 3: Add the static login page and route**

File: `internal/webui/login.html`
```html
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>nodr — Sign in</title>
<style>
  body { font-family: system-ui, sans-serif; display: flex; align-items: center; justify-content: center; height: 100vh; margin: 0; background: #0f172a; color: #e2e8f0; }
  form { background: #1e293b; padding: 2rem; border-radius: 8px; width: 320px; }
  h1 { font-size: 1.25rem; margin: 0 0 1rem; }
  label { display: block; margin-bottom: 0.75rem; }
  input { width: 100%; padding: 0.5rem; margin-top: 0.25rem; border-radius: 4px; border: 1px solid #334155; background: #0f172a; color: #e2e8f0; }
  button { width: 100%; padding: 0.6rem; border-radius: 4px; border: none; background: #2563eb; color: white; cursor: pointer; }
  .error { color: #f87171; margin-bottom: 0.75rem; display: none; }
</style>
</head>
<body>
<form id="login-form">
  <h1>Sign in to nodr</h1>
  <div class="error" id="error">Invalid username or password</div>
  <label>Username<input type="text" name="username" autocomplete="username" required></label>
  <label>Password<input type="password" name="password" autocomplete="current-password" required></label>
  <button type="submit">Sign in</button>
</form>
<script>
document.getElementById("login-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const form = e.target;
  const body = JSON.stringify({ username: form.username.value, password: form.password.value });
  const resp = await fetch("/api/v1/auth/login", { method: "POST", headers: { "Content-Type": "application/json" }, body });
  if (resp.ok) {
    window.location.href = "/";
  } else {
    document.getElementById("error").style.display = "block";
  }
});
</script>
</body>
</html>
```

File: `internal/webui/server.go`, add the embed and route inside `handlerFS` before the SPA-fallback check (after the `/api` guard at lines 31-34):
```go
//go:embed login.html
var loginPage []byte

// inside handlerFS's returned handler, after the /api guard:
if r.URL.Path == "/login" {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "login.html", time.Time{}, bytes.NewReader(loginPage))
	return
}
```
Place the `//go:embed login.html` directive at package level near the existing `//go:embed dist` (line 14), and the route check inside the handler function body, right after the existing `if r.URL.Path == "/api" ...` block (lines 31-34).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v ./internal/webui/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
git add internal/webui/
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(webui): serve a static login page outside the auth gate"
```

---

### Task 9: `internal/cli` server hardening, `auth create-admin`, `cluster connect`

**Files:**
- Modify: `internal/cli/server.go`
- Modify: `internal/cli/cli.go`
- Create: `internal/cli/auth.go`
- Create: `internal/cli/auth_test.go`
- Create: `internal/cli/cluster.go`
- Create: `internal/cli/cluster_test.go`
- Modify: `internal/cli/plan.go`

- [ ] **Step 1: Harden the server bind default and open the auth/secrets stores**

File: `internal/cli/server.go`. Line 32: `cmd.Flags().StringVar(&addr, "addr", ":8080", ...)` becomes `cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "HTTP listen `address`")`. Add `--kek-file`/`NODR_KEK` flags (mirroring `KEKConfig`) and open both stores before calling `nodrapi.Handler`:
```go
func (a *app) serverCommand() *cobra.Command {
	var addr, kekFile string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the nodr API and web UI",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			loaded, err := a.mustLoad()
			if err != nil {
				return err
			}
			kek, err := secrets.LoadKEK(secrets.KEKConfig{FilePath: kekFile, EnvVar: "NODR_KEK"})
			if err != nil {
				return err
			}
			authStore, err := authn.Open(filepath.Join(loaded.ws.Root, ".nodr", "authn.db"))
			if err != nil {
				return err
			}
			defer authStore.Close()
			secretsStore, err := secrets.Open(filepath.Join(loaded.ws.Root, ".nodr", "secrets.db"), kek)
			if err != nil {
				return err
			}
			defer secretsStore.Close()
			return runServer(cmd.Context(), loaded.ws.Root, addr, a.stdout, authStore, secretsStore)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "HTTP listen `address`")
	cmd.Flags().StringVar(&kekFile, "kek-file", "", "path to the 32-byte key encryption key file (or set NODR_KEK to a base64-encoded key)")
	return cmd
}

func runServer(ctx context.Context, root, addr string, stdout io.Writer, auth *authn.Store, secretsStore *secrets.Store) error {
	// ... unchanged listener setup ...
	mux := http.NewServeMux()
	mux.Handle("/api/", nodrapi.Handler(ctx, root, auth, secretsStore))
	mux.Handle("/api", nodrapi.Handler(ctx, root, auth, secretsStore))
	mux.Handle("/", webui.Handler())
	// ... unchanged server.Serve / shutdown handling ...
}
```
Add `"path/filepath"`, `"github.com/centopw/nodr/internal/authn"`, `"github.com/centopw/nodr/internal/secrets"` to imports.

- [ ] **Step 2: Wire `plan`/`apply`'s resolver**

File: `internal/cli/plan.go`. Add a lazily-opened secrets store to `app` (`internal/cli/cli.go`'s `app` struct gains a `secretsOnce sync.Once` / `secretsStore *secrets.Store` pair, or simpler: a method that opens on first use and caches):
```go
// in cli.go's app struct:
type app struct {
	stdin io.Reader
	stdout, stderr io.Writer
	interactive bool
	interrupts <-chan struct{}
	workspaceDir string

	secretsMu    sync.Mutex
	secretsStore *secrets.Store
}

// secretsResolve returns a.secretsStore's Resolve method, opening the store
// from the loaded workspace's .nodr/secrets.db on first use. kekFile/NODR_KEK
// follow the same KEKConfig convention as the server command.
func (a *app) secretsResolve(root string) (planapply.Resolver, error) {
	a.secretsMu.Lock()
	defer a.secretsMu.Unlock()
	if a.secretsStore == nil {
		kek, err := secrets.LoadKEK(secrets.KEKConfig{EnvVar: "NODR_KEK"})
		if err != nil {
			return nil, err
		}
		store, err := secrets.Open(filepath.Join(root, ".nodr", "secrets.db"), kek)
		if err != nil {
			return nil, err
		}
		a.secretsStore = store
	}
	return a.secretsStore.Resolve, nil
}
```
In `planUnits` (line 105-130), before the `planapply.PlanUnits` call: `resolve, err := a.secretsResolve(l.ws.Root); if err != nil { return nil, err }`, then line 110 becomes `planapply.PlanUnits(ctx, l.ws, only, planDir, a.stderr, a.interrupts, resolve)`. In `apply` (line 135-180), the `planapply.Apply` call at line 163 becomes `planapply.Apply(ctx, plans, a.stderr, a.interrupts, resolve)` reusing the same `resolve` from the `planUnits` call inside `apply` (or resolving again the same way — either is correct since `secretsResolve` is idempotent after the first open).

Also update `--addr`'s default in any place cobra help text or examples reference `:8080` if present (check `cmd.Example` fields in `server.go` and `cli.go`'s root help; only edit if such a reference literally exists — do not invent one).

- [ ] **Step 3: Write the failing `auth create-admin` tests**

File: `internal/cli/auth_test.go`
```go
package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/centopw/nodr/internal/authn"
)

func TestAuthCreateAdmin_PasswordStdin(t *testing.T) {
	root := t.TempDir()
	writeTestWorkspace(t, root) // reuse whatever fixture helper this package's other tests already use for a minimal valid workspace
	var stdout, stderr bytes.Buffer
	stdin := bytes.NewBufferString("admin\nsecretpass123\n")
	code := Run(context.Background(), nil, []string{"--workspace", root, "auth", "create-admin", "--password-stdin"}, stdin, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	store, err := authn.Open(filepath.Join(root, ".nodr", "authn.db"))
	if err != nil {
		t.Fatalf("authn.Open: %v", err)
	}
	defer store.Close()
	if _, _, err := store.Authenticate(context.Background(), "admin", "secretpass123"); err != nil {
		t.Errorf("Authenticate: %v", err)
	}
}
```
(Read `internal/cli`'s existing test helpers, e.g. how `plan_test.go` or `cli_test.go` build a minimal workspace and invoke `Run`, before writing `writeTestWorkspace` — reuse an existing helper by its real name rather than inventing a new one with a name that collides or duplicates.)

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./internal/cli/... -run TestAuthCreateAdmin`
Expected: FAIL (`auth create-admin` subcommand does not exist).

- [ ] **Step 5: Implement `internal/cli/auth.go`**

File: `internal/cli/auth.go`
```go
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/centopw/nodr/internal/authn"
)

func (a *app) authCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage the local administrator account",
	}
	cmd.AddCommand(a.authCreateAdminCommand())
	return cmd
}

func (a *app) authCreateAdminCommand() *cobra.Command {
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "create-admin",
		Short: "Create or replace the single local administrator account",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			loaded, err := a.mustLoad()
			if err != nil {
				return err
			}
			var username, password string
			if passwordStdin {
				reader := bufio.NewReader(a.stdin)
				username, err = readLine(reader, "Username: ", a.stdout)
				if err != nil {
					return err
				}
				password, err = readLine(reader, "", nil)
				if err != nil {
					return err
				}
			} else {
				if !a.interactive {
					return usageError{errors.New("stdin is not a terminal; pass --password-stdin to create an admin account non-interactively")}
				}
				username, err = readLine(bufio.NewReader(a.stdin), "Username: ", a.stdout)
				if err != nil {
					return err
				}
				password, err = readPasswordTwice(a.stdin, a.stdout)
				if err != nil {
					return err
				}
			}
			store, err := authn.Open(filepath.Join(loaded.ws.Root, ".nodr", "authn.db"))
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.CreateAccount(cmd.Context(), username, password); err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "created administrator account %q\n", username)
			return nil
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read username then password as two lines from stdin, for scripted setup")
	return cmd
}

func readLine(reader *bufio.Reader, prompt string, out interface{ Write([]byte) (int, error) }) (string, error) {
	if prompt != "" && out != nil {
		fmt.Fprint(out, prompt)
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func readPasswordTwice(stdin interface{ Fd() uintptr }, stdout interface{ Write([]byte) (int, error) }) (string, error) {
	// implementation reads via golang.org/x/term.ReadPassword against the
	// file descriptor of stdin, prompting twice and requiring the two
	// entries to match; see internal/cli/cli.go's isTerminal for the
	// existing *os.File type-assertion pattern this must follow, since
	// term.ReadPassword needs a real fd, not an io.Reader.
	panic("implement using term.ReadPassword, matching the *os.File assertion pattern in cli.go's isTerminal")
}
```
This sketch is intentionally incomplete on `readPasswordTwice`: implement it by type-asserting `a.stdin` to `*os.File` (exactly as `isTerminal` does in `internal/cli/cli.go`), calling `term.ReadPassword(int(f.Fd()))` twice with a re-entry prompt, comparing the two results, and returning an error if they differ or if the assertion fails (report a clear "stdin is not a terminal" usage error in that case, consistent with the `--password-stdin` escape hatch already provided). Read `internal/cli/cli.go`'s `isTerminal` function (already cited: `func isTerminal(r io.Reader) bool { f, ok := r.(*os.File); return ok && term.IsTerminal(int(f.Fd())) }`) before writing this, and follow its exact type-assertion style rather than inventing a different one. Remove the placeholder `panic` and the unused `readLine` parameter types once written against real `io.Reader`/`io.Writer` interfaces (the sketch above uses inline anonymous interfaces only to keep this plan doc's snippet self-contained; use `io.Reader`/`io.Writer` directly in the real implementation).

- [ ] **Step 6: Register the command**

File: `internal/cli/cli.go`, inside `rootCommand()` (lines 98-107), add `root.AddCommand(a.authCommand())` alongside the existing `AddCommand` calls for `versionCommand`, `validateCommand`, etc.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test -v ./internal/cli/... -run TestAuthCreateAdmin`
Expected: PASS.

- [ ] **Step 8: Write the failing `cluster connect` test**

File: `internal/cli/cluster_test.go`
```go
package cli

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/centopw/nodr/internal/proxmox/proxmoxtest"
	"github.com/centopw/nodr/internal/secrets"
)

func TestClusterConnect_BootstrapsAndWritesIntent(t *testing.T) {
	root := t.TempDir()
	writeTestWorkspace(t, root)

	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"POST /api2/json/access/ticket": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"ticket":"T","CSRFPreventionToken":"C"}`)
		},
		"GET /api2/json/nodes/pve1/certificates/info": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `[{"fingerprint":"AA:BB:CC"}]`)
		},
		"POST /api2/json/access/users":                            func(w http.ResponseWriter, r *http.Request) { proxmoxtest.JSONResponse(w, http.StatusOK, "") },
		"POST /api2/json/access/roles":                            func(w http.ResponseWriter, r *http.Request) { proxmoxtest.JSONResponse(w, http.StatusOK, "") },
		"PUT /api2/json/access/acl":                                func(w http.ResponseWriter, r *http.Request) { proxmoxtest.JSONResponse(w, http.StatusOK, "") },
		"POST /api2/json/access/users/nodr@pve/token/nodr":        func(w http.ResponseWriter, r *http.Request) { proxmoxtest.JSONResponse(w, http.StatusOK, `{"value":"wizard-secret-xyz"}`) },
	})
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	stdin := bytes.NewBufferString("pve-main\n" + srv.URL + "\npve1\nroot\nadminpass\nyes\n")
	code := Run(context.Background(), nil, []string{"--workspace", root, "cluster", "connect"}, stdin, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}

	kek := make([]byte, 32)
	store, err := secrets.Open(filepath.Join(root, ".nodr", "secrets.db"), kek)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	defer store.Close()
	got, err := store.Resolve(context.Background(), "proxmox/pve-main-token")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(got) != "wizard-secret-xyz" {
		t.Errorf("token = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "intent", "platform", "pve-main.yaml")); err != nil {
		t.Errorf("intent file not written: %v", err)
	}
}
```
(This test sketch assumes a specific prompt order for `cluster connect`'s interactive flow — cluster name, endpoint URL, node name for fingerprint lookup, admin username, admin password, confirmation. Adjust the sketch's `stdin` content to match whatever exact prompt sequence you implement in Step 9, and keep the two consistent.)

- [ ] **Step 9: Run test to verify it fails**

Run: `go test ./internal/cli/... -run TestClusterConnect`
Expected: FAIL (`cluster connect` subcommand does not exist).

- [ ] **Step 10: Implement `internal/cli/cluster.go`**

File: `internal/cli/cluster.go`
```go
package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"go.yaml.in/yaml/v3"

	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/proxmoxbootstrap"
	"github.com/centopw/nodr/internal/secrets"
)

func (a *app) clusterCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage Proxmox cluster connections",
	}
	cmd.AddCommand(a.clusterConnectCommand())
	return cmd
}

func (a *app) clusterConnectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Connect a Proxmox VE cluster: bootstrap a least-privilege user and pin its TLS certificate",
		Long: `Connect walks through onboarding a Proxmox VE cluster: it asks for the
cluster's name, an endpoint URL, a node to fetch the TLS certificate
fingerprint from, and an administrator credential. After the operator
confirms the printed fingerprint, it creates a dedicated nodr@pve user,
NodrOperator role and API token on the cluster (docs/design/06-proxmox.md
§6.2), discards the administrator credential, stores the new token via the
workspace's secrets store, and writes a ProxmoxCluster intent document.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !a.interactive {
				return usageError{fmt.Errorf("stdin is not a terminal; cluster connect requires an interactive session")}
			}
			return a.clusterConnect(cmd.Context())
		},
	}
	return cmd
}

func (a *app) clusterConnect(ctx context.Context) error {
	loaded, err := a.mustLoad()
	if err != nil {
		return err
	}
	reader := bufio.NewReader(a.stdin)
	clusterName, err := readLine(reader, "Cluster name: ", a.stdout)
	if err != nil {
		return err
	}
	endpoint, err := readLine(reader, "Endpoint URL (e.g. https://10.0.0.1:8006): ", a.stdout)
	if err != nil {
		return err
	}
	node, err := readLine(reader, "Node to fetch the certificate from: ", a.stdout)
	if err != nil {
		return err
	}
	adminUser, err := readLine(reader, "Administrator username: ", a.stdout)
	if err != nil {
		return err
	}
	f, ok := a.stdin.(*os.File)
	if !ok {
		return usageError{fmt.Errorf("stdin is not a terminal; cannot read the administrator password securely")}
	}
	fmt.Fprint(a.stdout, "Administrator password: ")
	passwordBytes, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(a.stdout)
	if err != nil {
		return err
	}
	adminPassword := string(passwordBytes)

	insecureClient := proxmox.NewClient(endpoint, nil)
	if err := insecureClient.Login(ctx, "pam", adminUser, adminPassword); err != nil {
		return fmt.Errorf("nodr: cannot log in to %s: %w", endpoint, err)
	}
	fingerprint, err := insecureClient.GetCertFingerprint(ctx, node)
	if err != nil {
		return fmt.Errorf("nodr: cannot fetch certificate fingerprint: %w", err)
	}
	fmt.Fprintf(a.stdout, "Certificate fingerprint: %s\n", fingerprint)
	approved, err := a.confirm(ctx)
	if err != nil {
		return err
	}
	if !approved {
		fmt.Fprintln(a.stderr, "nodr: cluster connect canceled")
		return errReported
	}

	pinnedClient := proxmox.NewClient(endpoint, proxmox.NewPinnedHTTPClient(fingerprint))
	if err := pinnedClient.Login(ctx, "pam", adminUser, adminPassword); err != nil {
		return fmt.Errorf("nodr: cannot re-authenticate over the pinned connection: %w", err)
	}
	adminPassword = "" // discard the administrator credential from memory as soon as it is no longer needed

	result, err := proxmoxbootstrap.Bootstrap(ctx, pinnedClient, proxmoxbootstrap.Privileges)
	if err != nil {
		return err
	}

	ref := "proxmox/" + clusterName + "-token"
	kek, err := secrets.LoadKEK(secrets.KEKConfig{EnvVar: "NODR_KEK"})
	if err != nil {
		return err
	}
	store, err := secrets.Open(filepath.Join(loaded.ws.Root, ".nodr", "secrets.db"), kek)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Put(ctx, ref, []byte(result.TokenSecret)); err != nil {
		return err
	}

	doc := struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec v1alpha1.ProxmoxClusterSpec `yaml:"spec"`
	}{
		APIVersion: "nodr/v1alpha1",
		Kind:       v1alpha1.KindProxmoxCluster,
	}
	doc.Metadata.Name = clusterName
	doc.Spec = v1alpha1.ProxmoxClusterSpec{
		Endpoints:      []string{endpoint},
		CredentialsRef: ref,
		Nodes:          []string{node},
		TLS:            &v1alpha1.ProxmoxTLS{Fingerprint: fingerprint},
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	intentPath := filepath.Join(loaded.ws.Root, "intent", "platform", clusterName+".yaml")
	if err := os.MkdirAll(filepath.Dir(intentPath), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(intentPath, out, 0644); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "connected cluster %q; wrote %s\n", clusterName, intentPath)
	return nil
}
```
Add `"bufio"`, `"os"` to imports. Confirm `v1alpha1.ProxmoxClusterSpec`'s and `v1alpha1.ProxmoxTLS`'s exact field names/tags against `internal/nrm/v1alpha1/types.go:135-148` before writing the literal struct above — the plan doc's citation (`Endpoints []string, CredentialsRef string, TLS *ProxmoxTLS{Fingerprint string}, Nodes []string, Network *ProxmoxNetwork`) should match, but re-read that file's current state rather than trusting this plan doc's transcription if anything looks off during implementation. Confirm the doc-writing struct's YAML shape actually matches what `workspace.Load`/`ws.Validate` expects for a `ProxmoxCluster` document (compare against `examples/homelab/intent/platform/pve-main.yaml`'s existing format) before finalizing.

- [ ] **Step 11: Register the command**

File: `internal/cli/cli.go`, add `root.AddCommand(a.clusterCommand())` in `rootCommand()`.

- [ ] **Step 12: Run tests to verify they pass**

Run: `go test -v ./internal/cli/... -run TestClusterConnect`
Expected: PASS.

- [ ] **Step 13: Run the full `internal/cli` suite**

Run: `go test ./internal/cli/...`
Expected: PASS (including every test touched by Task 3 and 9's `--addr` default change — any test asserting the literal default `:8080` string must be updated to `127.0.0.1:8080`).

- [ ] **Step 14: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
git add internal/cli/
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(cli): harden server bind default, add auth create-admin and cluster connect"
```

---

### Task 10: `web/src/api.ts` — redirect to login on 401

**Files:**
- Modify: `web/src/api.ts`

- [ ] **Step 1: Update `fetchJSON`**

File: `web/src/api.ts`, lines 84-93:
```ts
async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  if (response.status === 401) {
    window.location.href = "/login";
    throw new Error("session expired, redirecting to /login");
  }
  const body: unknown = await response.json();
  if (!response.ok) {
    throw new ProblemError(body as ProblemDetails);
  }
  return body as T;
}
```

- [ ] **Step 2: Run the existing frontend test suite**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard/web
npm test
```
Expected: PASS (no existing test asserts on 401 handling in `fetchJSON`; if one does and now fails because it expected `ProblemError` for a 401 specifically, update that test to expect the redirect behavior instead — do not leave a stale assertion pinning the old behavior).

- [ ] **Step 3: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
git add web/src/api.ts
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(web): redirect to /login on an expired or missing session"
```

---

### Task 11: CHANGELOG and full verification

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Update `CHANGELOG.md`**

Add to the `## [Unreleased]` section, in the same prose-bullet style as existing entries, under `### Added`:
```markdown
- `credentialsRef` now resolves through the secrets store into
  `PROXMOX_VE_API_TOKEN` for every OpenTofu plan and apply, for both the CLI
  and the API.
- `nodr cluster connect`: an interactive wizard that bootstraps a
  least-privilege `nodr@pve` user, role and API token on a Proxmox VE
  cluster, pins its TLS certificate fingerprint, and writes the resulting
  intent.
- `nodr auth create-admin`: creates the local administrator account used to
  sign in to the dashboard and API.
- The dashboard and API now require an authenticated session
  (`HttpOnly`/`Secure`/`SameSite=Strict` cookie) for every route.
```
Under `### Changed`:
```markdown
- `nodr server`'s default `--addr` changed from `:8080` to `127.0.0.1:8080`
  (loopback-only); pass an explicit `--addr` to bind elsewhere.
```
Under `### Fixed` (or `### Security` if the file's existing convention has that heading — check the file before adding a new heading category):
```markdown
- OpenTofu output and error messages are now scrubbed of any resolved
  Proxmox API token before reaching logs or API responses.
```

- [ ] **Step 2: Run the full test suite with race detection**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
go test -v -race ./...
```
Expected: PASS across all packages.

- [ ] **Step 3: Run the linter**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
golangci-lint run ./...
```
Expected: PASS with 0 lint issues. Fix any `unparam`, `revive`, `errorlint`, `gocritic` findings before proceeding — do not add `//nolint` without a `require-specific`-satisfying explanation, per `.golangci.yml`'s `nolintlint` settings.

- [ ] **Step 4: Manual smoke test proving the auth gate and no leak**

Run in the worktree, using a real built binary against a temporary workspace copied from `examples/homelab`:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
go build -o /tmp/nodr ./cmd/nodr
cp -r examples/homelab /tmp/smoke-workspace
cd /tmp/smoke-workspace
NODR_KEK=$(head -c32 /dev/urandom | base64) /tmp/nodr auth create-admin --password-stdin <<< $'admin\nsmoketestpass123'
NODR_KEK=$(head -c32 /dev/urandom | base64) /tmp/nodr server --addr 127.0.0.1:18080 &
sleep 1
curl -s -o /dev/null -w "%{http_code}\n" -X POST http://127.0.0.1:18080/api/v1/workspaces/homelab/commands -d '{"command":"workspace.plan"}'
# expect 401
curl -s -c /tmp/cookies.txt -X POST http://127.0.0.1:18080/api/v1/auth/login -H 'Content-Type: application/json' -d '{"username":"admin","password":"smoketestpass123"}'
curl -s -b /tmp/cookies.txt -o /dev/null -w "%{http_code}\n" http://127.0.0.1:18080/api/v1/workspaces
# expect 200
kill %1
```
Expected: the first `curl` prints `401`; login succeeds (empty body, HTTP 200 — check via `-w` if the body-only output is ambiguous); the authenticated `curl` prints `200`. Note the exact `NODR_KEK` used in both invocations must match (the smoke script above uses two different random values, which is a bug in the sketch — use one `export NODR_KEK=...` for both commands when actually running this) before treating a failure as a real problem rather than a script mistake.

- [ ] **Step 5: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/cluster-connect-wizard
git add CHANGELOG.md
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "docs(changelog): document cluster-connect wizard and server hardening"
```
