# First-Run Browser Bootstrap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `nodr server` create and serve one Git-backed workspace through the existing browser setup flow on a blank host, with the same transaction available through all-or-none deployment environment variables.

**Architecture:** Preserve the existing `authn.Store.Setup`, `POST /api/v1/setup`, `SetupForm`, session, and CSRF flow. Add a bootstrap coordinator that owns data-root state, validated local workspace creation, Git initialization, KEK resolution, and completion persistence; inject that coordinator into the existing setup handler. The server begins with the bootstrap UI/API and atomically swaps to the ordinary workspace handler after successful setup without closing its listener.

**Tech Stack:** Go 1.24, Cobra, `net/http`, SQLite/authn, `os/exec` for fixed Git commands, React 19, TypeScript 5.9, Vite.

**Spec:** `docs/proposals/2026-10-02-first-run-bootstrap-design.md`

## Global Constraints

- Installer remains non-interactive; no `nodr init` or terminal setup wizard.
- Interactive setup accepts no filesystem path; workspace is always `<data-dir>/workspaces/<validated-name>`.
- `--workspace` retains existing workspace and `NODR_BOOTSTRAP_TOKEN` behavior; it never enters blank-host bootstrap.
- Browser and unattended setup call the same coordinator transaction.
- `NODR_BOOTSTRAP_WORKSPACE_NAME`, `NODR_BOOTSTRAP_ADMIN_USERNAME`, `NODR_BOOTSTRAP_ADMIN_PASSWORD`, and `NODR_BOOTSTRAP_TOKEN` are all-or-none; partial configuration fails before binding. Generated tokens exist only for browser bootstrap.
- Secret values are never logged, returned, included in diagnostics, or asserted in tests.
- KEK precedence: `--kek-file`, then `NODR_KEK`, then durable generated `<data-dir>/kek` mode `0600` in bootstrap mode only.
- Git has no fallback: missing/failed Git means bootstrap fails and cannot leave a completed workspace.
- Keep documentation in `docs/proposals/`; force-add it when committing because `docs/` is globally ignored.

---

### Task 1: Bootstrap state, data root, KEK, and repository primitives

**Files:**
- Create: `internal/bootstrap/bootstrap.go`
- Create: `internal/bootstrap/bootstrap_test.go`
- Create: `internal/bootstrap/git.go`
- Create: `internal/bootstrap/git_test.go`

**Interfaces:**
- Produces `Config{DataDir, KEKFile, KEKEnv}`, `State{Version, Workspace}`, `LoadState`, `WriteState`, `ResolveKEK`, `ValidateName`, `CreateWorkspace`, and `GitRunner`.
- `CreateWorkspace(ctx, dataDir, name)` creates only a newly derived target, validates its manifest through `workspace.Load`/NRM, initializes Git, and returns its root plus a rollback closure.

- [ ] **Step 1: Write failing state/data-root tests**

Cover default data root with and without `XDG_STATE_HOME`; `bootstrap.json` parsing; rejecting absolute/traversal workspace state; atomic state file mode `0600`; existing/non-empty target rejection; and symlink target rejection.

- [ ] **Step 2: Run state tests to verify RED**

Run: `GOTOOLCHAIN=go1.24.13 go test ./internal/bootstrap -run 'Test(DataDir|State|WorkspaceTarget)'`

Expected: FAIL because package does not exist.

- [ ] **Step 3: Implement state and path functions**

Use `filepath.Join(dataDir, "workspaces", name)` only after name validation. Create data root and workspace parent with `0700`. Write completion JSON to a same-directory temporary file, chmod `0600`, fsync/close, then rename. Reject a state workspace string that is not a clean relative child of `workspaces`.

- [ ] **Step 4: Write failing KEK tests**

Assert `--kek-file` is chosen over `NODR_KEK`; valid environment key is accepted; no configured key creates a stable `0600` `<data-dir>/kek`; malformed configured keys fail; generated key is reused after a simulated failed bootstrap.

- [ ] **Step 5: Implement KEK resolution**

Call existing `secrets.LoadKEK` for configured sources where its API permits; otherwise add a small bootstrap file loader/generator compatible with its 32-byte base64 contract. Generate with `crypto/rand`, atomically create once, print only its path and recovery warning via injected writer. Never print key bytes.

- [ ] **Step 6: Write failing Git/workspace tests**

Install a temporary fake Git executable and assert fixed calls: `init`, `add nodr.yaml intent`, `commit -m chore: initialize nodr workspace`; confirm a minimal manifest validates; assert missing Git and failing commit return errors and leave no workspace after rollback.

- [ ] **Step 7: Implement controlled Git runner and workspace creation**

Use `exec.CommandContext` with executable injected for tests, fixed args, `Cmd.Dir` set to derived root, and no shell. Create `intent/`, write the exact minimal manifest, validate via normal workspace/NRM, then Git init/add/commit. Return rollback that removes only the directory created by this call.

- [ ] **Step 8: Run focused package tests**

Run: `GOTOOLCHAIN=go1.24.13 go test ./internal/bootstrap`

Expected: PASS.

- [ ] **Step 9: Commit**

```sh
git add internal/bootstrap
git commit -m "feat: add bootstrap workspace primitives"
```

### Task 2: Extend auth setup atomically through a coordinator

**Files:**
- Modify: `internal/authn/setup.go`
- Modify: `internal/authn/store.go`
- Modify: `internal/authn/setup_test.go`
- Create: `internal/bootstrap/service.go`
- Create: `internal/bootstrap/service_test.go`

**Interfaces:**
- Produces `bootstrap.Service.Setup(ctx, Request) (sessionToken, csrfToken string, expiresAt time.Time, err error)` where `Request` has `Token`, `WorkspaceName`, `Username`, and `Password`; this mirrors the existing `authn.Store.Setup` return contract.
- Browser and unattended callers both invoke existing `authn.Store.Setup`; the unattended environment token is required and is never bypassed.

- [ ] **Step 1: Write failing coordinator transaction tests**

Use fake Git and temporary data root. Assert success creates manifest, initial
commit, account/session, and completion state. For invalid workspace name, Git
failure, and auth setup failure, assert no completion state and only the
attempt-created workspace is removed. For a forced completion-state write
failure after `authn.Store.Setup` returns success, assert the service closes
the attempt's stores, removes `<attempt-workspace>/.nodr/authn.db` with the
attempt workspace, and a second call with the same bootstrap token succeeds
from a clean auth database. Assert an existing directory is never deleted.

- [ ] **Step 2: Run coordinator tests to verify RED**

Run: `GOTOOLCHAIN=go1.24.13 go test ./internal/bootstrap -run TestService`

Expected: FAIL because service does not exist.

- [ ] **Step 3: Implement coordinator ordering and compensation**

Validate every input first. Create workspace and Git commit first; open its
attempt-local auth database at `<workspace>/.nodr/authn.db`, never at the data
root. Call existing `authn.Store.Setup` second, receiving `(sessionToken,
csrfToken, expiresAt, error)` so it retains Argon2id, one-time token, session,
and CSRF behavior. Write completion state last. If state persistence fails
after Setup succeeds, close auth/secrets stores before removing only the
attempt-created workspace; this removes the account, sessions, bootstrap hash,
and consumed-token state atomically with that failed attempt. The retry opens a
fresh attempt-local store and reinitializes the same token. Preserve durable
KEK and return a sanitized error.

- [ ] **Step 4: Extend existing setup request/handler through an interface**

Add `workspaceName` to the JSON request. Make `SetupHandler` depend on a narrow setup service interface rather than directly on Store, while adapt existing workspace-mode behavior to retain its current token/account semantics. Map coordinator validation failures to existing RFC 9457 field errors; do not expose secrets or filesystem internals.

- [ ] **Step 5: Add handler regression tests**

Assert the endpoint still returns 403 with identical generic detail for wrong/consumed token, sets session cookie and CSRF on success, accepts `workspaceName` in bootstrap mode, and keeps legacy `--workspace` setup tests valid.

- [ ] **Step 6: Run auth/bootstrap tests**

Run: `GOTOOLCHAIN=go1.24.13 go test ./internal/authn ./internal/bootstrap`

Expected: PASS.

- [ ] **Step 7: Commit**

```sh
git add internal/authn internal/bootstrap
git commit -m "feat: bootstrap workspace through setup transaction"
```

### Task 3: Add server bootstrap mode and atomic handler cutover

**Files:**
- Modify: `internal/cli/server.go`
- Modify: `internal/cli/server_test.go`
- Create: `internal/cli/bootstrap_server_test.go`
- Modify: `internal/api/api.go`
- Modify: `internal/api/api_test.go`

**Interfaces:**
- `nodr server --data-dir DIR` chooses blank-host bootstrap only when `--workspace` is absent.
- A synchronization-safe switching handler serves bootstrap routes before completion and normal `api.Handler` after completion.

- [ ] **Step 1: Write failing mode-selection tests**

Cover: no workspace/no env starts bootstrap listener; existing `--workspace` loads normally; partial unattended group exits before listen and names only missing variable names; complete group bootstraps then runs normal mode; state file reload uses recorded workspace; incomplete directory without state fails clearly; non-loopback bootstrap emits warning.

- [ ] **Step 2: Run CLI tests to verify RED**

Run: `GOTOOLCHAIN=go1.24.13 go test ./internal/cli -run 'TestServer.*Bootstrap'`

Expected: FAIL because `--data-dir` and bootstrap mode do not exist.

- [ ] **Step 3: Implement flags and startup selection**

Add `--data-dir`; compute default data dir only when `--workspace` is absent. Preserve current `--workspace` `NODR_BOOTSTRAP_TOKEN` initialization. Resolve bootstrap KEK before listener, establish auth/secrets stores for the candidate workspace transaction, generate an in-memory token only for browser mode when the environment token is absent, and require all four unattended variables together.

- [ ] **Step 4: Implement switching handler**

Use a mutex or atomic handler reference. Bootstrap handler exposes setup status/setup and static SPA; ordinary handler is constructed only after the coordinator returns a validated workspace plus stores. The successful HTTP setup response must set the session cookie before swapping. Do not restart or rebind the listener.

- [ ] **Step 5: Add HTTP cutover and route tests**

Use `httptest` to prove bootstrap setup routes exist before completion, ordinary workspace routes are unavailable without a session, a successful setup creates a usable session, then normal workspace API works and bootstrap operation cannot repeat. Verify token is not reflected in URL/response.

- [ ] **Step 6: Run CLI/API tests**

Run: `GOTOOLCHAIN=go1.24.13 go test ./internal/cli ./internal/api`

Expected: PASS.

- [ ] **Step 7: Commit**

```sh
git add internal/cli internal/api
git commit -m "feat: serve first-run browser bootstrap"
```

### Task 4: Extend the existing browser setup form and API client

**Files:**
- Modify: `web/src/api.ts`
- Modify: `web/src/SetupForm.tsx`
- Modify: `web/src/App.tsx`
- Modify: `web/src/App.css`
- Modify: `web/src/SetupForm.stories.tsx` if an existing story pattern exists
- Regenerate: `internal/webui/dist/`

**Interfaces:**
- `SetupParams` gains `workspaceName`.
- Existing `setup()` continues to POST `/api/v1/setup`, receives CSRF token, and lets App transition into authenticated mode.

- [ ] **Step 1: Update typed request contract**

Add `workspaceName: string` to `SetupParams`. Keep token/password fields `type="password"`; preserve `ProblemError` field mapping.

- [ ] **Step 2: Extend setup form**

Add a controlled, required workspace-name input ahead of admin fields. Change heading/copy to explain it creates the local Git-backed workspace and administrator. Maintain accessible labels, error associations, disabled submit state, and no secret display.

- [ ] **Step 3: Preserve post-setup handoff**

After successful setup, retain current App authentication refresh and route the newly authenticated empty workspace to Infrastructure with the existing cluster connection wizard expanded/focused. Do not create a new cluster wizard.

- [ ] **Step 4: Build and manually smoke the UI**

Run: `make web-ui`

Start a blank-host bootstrap server with a temporary data root, open the browser, submit invalid fields and then valid setup, verify normal dashboard loads and Cluster Connect appears. Close the browser tab/process after observation.

- [ ] **Step 5: Commit generated assets with UI source**

```sh
git add web/src internal/webui/dist
git commit -m "feat(web): create workspace during browser setup"
```

### Task 5: Update deployment contracts, documentation, and regressions

**Files:**
- Modify: `README.md`
- Modify: `docs/guides/production.md`
- Modify: `deploy/compose/nodr.env.example`
- Modify: `deploy/systemd/nodr.env.example`
- Modify: `deploy/helm/nodr/templates/deployment.yaml`
- Modify: applicable Helm values/docs files found alongside the deployment
- Modify: `CHANGELOG.md`
- Modify: `docs/proposals/2026-10-02-first-run-bootstrap-design.md` only if implementation reveals an actual contract correction

- [ ] **Step 1: Document browser-first start**

Replace any shell-admin setup instructions with: start server, obtain generated token from startup output or configure `NODR_BOOTSTRAP_TOKEN`, open the browser, create workspace and admin. State `--workspace` preserves legacy existing-workspace behavior.

- [ ] **Step 2: Document unattended deployment variables**

Document exact all-or-none group:

```text
NODR_BOOTSTRAP_WORKSPACE_NAME
NODR_BOOTSTRAP_ADMIN_USERNAME
NODR_BOOTSTRAP_ADMIN_PASSWORD
NODR_BOOTSTRAP_TOKEN
```

Explain the token authorizes the same `authn.Store.Setup` call used by browser
bootstrap; generated tokens apply only to interactive browser mode. Explain
secrets must come from platform secret injection, and document `--data-dir`,
generated KEK path/back-up warning, and non-loopback/TLS reverse-proxy warning.

- [ ] **Step 3: Update Compose/systemd/Helm examples**

Make examples valid for either interactive token bootstrap or complete unattended bootstrap; never provide a partial environment group. Keep credentials as secret references/empty placeholders, never literal defaults.

- [ ] **Step 4: Add user-visible changelog entry**

Under `[Unreleased]`, state that blank-host `nodr server` now supports secured browser setup and unattended environment bootstrap, creates an initial local Git workspace, and uses a durable generated KEK only when no configured KEK is supplied.

- [ ] **Step 5: Run release/documentation checks**

Run:

```sh
scripts/changelog-notes.sh 0.2.0 >/dev/null
git diff --check
goreleaser check
```

Expected: all exit 0.

- [ ] **Step 6: Commit**

```sh
git add README.md CHANGELOG.md deploy
 git add -f docs/guides/production.md docs/proposals/2026-10-02-first-run-bootstrap-design.md
git commit -m "docs: describe browser bootstrap deployment"
```

### Task 6: Final verification

**Files:** No new source files expected.

- [ ] **Step 1: Run Go suite**

Run: `GOTOOLCHAIN=go1.24.13 go test ./...`

Expected: PASS.

- [ ] **Step 2: Run race suite for server/concurrent cutover changes**

Run: `GOTOOLCHAIN=go1.24.13 go test -race ./internal/authn ./internal/bootstrap ./internal/api ./internal/cli`

Expected: PASS.

- [ ] **Step 3: Smoke browser and unattended modes**

1. Start blank bootstrap server with temporary data root; use the browser setup form and confirm dashboard/session/cluster wizard.
2. Start server with the complete four-variable environment group and temporary data root; confirm it creates a valid workspace and initial Git commit without browser setup.
3. Start with a partial environment group and confirm it exits before binding.
4. Start with `--workspace` and confirm current token setup behavior remains unchanged.

- [ ] **Step 4: Verify build and working tree**

Run:

```sh
make web-ui
GOTOOLCHAIN=go1.24.13 go build ./...
git diff --check
git status --short
```

Expected: intended source, docs, and committed `internal/webui/dist` assets only.
