# Slice B: Cluster-connect wizard, credential wiring and server hardening

> Second of four sequenced slices toward real-Proxmox-node adoption (§6.3 of
> `docs/design/06-proxmox.md`). Order: A (Proxmox client + secrets store,
> merged) → **B (this doc)** → C (discovery) → D (adoption). Each slice ships
> independently and gets its own design doc, plan and PR.
>
> Slice A shipped `internal/proxmox` and `internal/secrets` fully tested but
> completely unwired: `credentialsRef` is declared in intent
> (`ProxmoxClusterSpec.CredentialsRef`) but nothing resolves it, and
> `nodr server` accepts every mutating request with no authentication. This
> slice closes both gaps — it is the first slice where nodr can run a real
> `tofu plan`/`apply` against a live cluster without an operator manually
> exporting `PROXMOX_VE_API_TOKEN`, and the first slice where the dashboard
> is safe to expose to more than one trusted person on one loopback socket.

## Goal

1. **Credential wiring.** `ProxmoxCluster.spec.credentialsRef` resolves
   through `secrets.Store` into `PROXMOX_VE_API_TOKEN` in the environment of
   the `tofu` child process that plans/applies that cluster's units — never
   into Git, OpenTofu state on disk (already true — state holds no env vars),
   application logs, or an HTTP response body.
2. **Bootstrap flow.** A CLI wizard walks the §6.2 flow end to end: operator
   supplies an administrator credential once, nodr creates a dedicated
   `nodr@pve` user, `NodrOperator` role and API token using Slice A's
   `internal/proxmox` client, stores the token via `secrets.Store.Put`,
   writes the resulting `ProxmoxCluster` intent document, and discards the
   administrator credential from memory. TLS fingerprint pinning is
   confirmed and recorded during this flow.
3. **Server hardening.** `nodr server` requires an authenticated session for
   every workspace/resource/command route, binds to loopback by default, and
   never lets a Proxmox token or Proxmox admin password reach a log line, a
   `Problem` JSON body, or OpenTofu's stdout/stderr writer.

## Non-goals (deferred to later slices or out of scope entirely)

- Discovery listing, adoption, import-block generation (slices C, D) —
  `GetClusterResources`, `GetNodeQEMU`, `GetQEMUConfig` stay unused outside
  Slice A's own tests until slice C.
- Cluster formation, node join, QDevice, Ceph wizards (§6.4–§6.5) — those are
  post-D follow-on slices per `docs/design/06-proxmox.md`.
- Multi-user accounts, groups, OIDC/RBAC (`docs/design/11-api-and-cli.md`
  §11's `/users`, `/groups`, `/rolebindings` resources) — this slice ships
  exactly one local admin account, created once via the CLI, no user
  management surface.
- CSRF token plumbing in `web/src/api.ts`. §10.3 lists CSRF tokens as part of
  the eventual session model, but this slice's threat model is a single
  trusted operator on a same-origin, loopback-bound server with
  `SameSite=Strict` cookies — there is no cross-origin actor to defend
  against yet. Revisited when remote/multi-user access ships.
- OAuth device authorization flow for the CLI (§10.3) — the CLI already runs
  on the same machine as the server for this slice; it authenticates with a
  session cookie obtained via `nodr auth create-admin`'s own local login, not
  a device flow.
- SSH-based host-level credentials (§10.6) — only the Proxmox API token path
  is wired this slice.
- KEK sourcing beyond file/env var — unchanged from Slice A.

## `internal/compile`

`Compile` currently discards the unit→cluster association it builds
(`compiler.units map[string]map[string]bool`, `compile.go:85-87`) — built for
`addProviders`'s multi-cluster check (`unit.go:110-112`) and then thrown away.
`internal/planapply` needs that association to know which cluster's
`credentialsRef` to resolve for each unit's `Runner.Env`. `addProviders`
already guarantees at most one cluster per unit (it errors otherwise), so the
new return value is a plain `map[string]string`.

```go
// Compile returns the files to write, the cluster each unit directory's
// provider block reaches (unit dir -> ProxmoxCluster name), and diagnostics.
func Compile(ws *workspace.Workspace) (files map[string][]byte, unitClusters map[string]string, diags diag.List)
```

`compiler.addProviders` records the cluster name into a new
`compiler.unitClusters map[string]string` field alongside the existing
`c.files[p] = providers(cluster)` assignment; `changes()` stays as-is (it
only filters `c.files`, not the new map). All 17 existing call sites (1
production in `internal/planapply/planapply.go:142`, 16 in
`internal/compile/compile_test.go`) are updated mechanically to accept the
second return value; tests that do not care about it assign to `_`.

## `internal/planapply`

`PlanUnits` and `Apply` gain a credential resolver, threaded through the one
shared layer both `internal/cli/plan.go` and `internal/api/api.go` call —
not duplicated per caller:

```go
// Resolver resolves a credentialsRef to its secret value. A nil Resolver
// means no cluster's credentials are injected (units still plan/apply using
// whatever the operator exported into nodr's own environment, unchanged
// from Slice A's behavior).
type Resolver func(ctx context.Context, ref string) ([]byte, error)

func PlanUnits(ctx context.Context, ws *workspace.Workspace, only []string, planDir string, out io.Writer, interrupts <-chan struct{}, resolve Resolver) (written map[string][]byte, plans []UnitPlan, err error)

func Apply(ctx context.Context, plans []UnitPlan, out io.Writer, interrupts <-chan struct{}, resolve Resolver) (applied []string, err error)
```

`secrets.Store.Resolve` already has this exact shape
(`func (s *Store) Resolve(ctx context.Context, ref string) ([]byte, error)`),
so callers pass `store.Resolve` directly; no adapter type is needed.

Inside `PlanUnits`, after `compile.Compile` returns `unitClusters`, for each
unit directory: look up its cluster name, `ws.Find` the `ProxmoxCluster`
document, decode its `CredentialsRef`, call `resolve(ctx, ref)`, and set
`u.Runner.Env = []string{"PROXMOX_VE_API_TOKEN=" + string(secret)}` before
`Init`. A unit whose provider reaches no cluster (there is none today, since
`addProviders` always resolves exactly one — but an empty `unitClusters[dir]`
guards defensively) or whose `resolve` is nil skips injection, matching
today's behavior exactly. `Apply` re-resolves per unit for the same reason
`PlanUnits` does — plans are saved to disk and may be applied by a separate
process invocation (the CLI's `withPlanDir` pattern, and the API's stored-plan
flow both re-run `Apply` well after `PlanUnits` returned) — the secret must
not be cached across that boundary.

`Apply`'s dead nil-`Runner` fallback (`planapply.go:196-202`, zero test or
production coverage — `UnitPlan.Runner` is always set by `PlanUnits`) is
removed rather than extended with credential logic: extending untested code
adds risk with no benefit; every real caller already has a `Runner`.

### Masking: two leak surfaces, one fix point

`internal/planapply` is the only layer that knows both the resolved secret
values and the `io.Writer`/error values that could carry them, so both fixes
live here:

1. **Writer wrapping.** A new unexported `redactor` (`io.Writer` wrapping
   another `io.Writer`, replacing every occurrence of a set of secret byte
   strings with `<redacted>` before forwarding — same pattern as
   `proxmox.Client.redact`, generalized to arbitrary byte strings instead of
   the client's own known fields). `PlanUnits`/`Apply` wrap `out` with a
   `redactor` seeded with every secret resolved during that call, before
   assigning it to `Runner.Stdout`/`Runner.Stderr`. `out == nil` stays `nil`
   (nothing to wrap). This closes the leak into the CLI's `a.stderr` and any
   future writer callers pass.
2. **`CommandError` scrubbing.** `opentofu.Runner.run` already builds
   `CommandError{Stderr: stderr.tail.String(), ...}` from the last lines of
   real stderr, independent of whether `Runner.Stderr` was wrapped (the tail
   buffer captures raw bytes before the wrapped writer forwards them — see
   `opentofu.go:187-189`, `stderr := &output{... w: r.Stderr, tail: new(tail)}`,
   which writes to `tail` unconditionally, then to `w` only if `w != nil`).
   `PlanUnits`/`Apply` therefore also scrub `*opentofu.CommandError.Stderr`
   in place (same secret-byte-string replacement) before wrapping it in
   `UnitError` and returning it. `unitError.Error()` (`planapply.go:46-51`)
   already avoids `cmdErr.Err`'s full stderr in its own formatting — the field
   that must be scrubbed is the exported `CommandError.Stderr` itself, since
   `internal/api/api.go`'s `planWorkspace`/`applyWorkspace` type-assert
   `errors.As(err, &cmdErr)` and read `cmdErr.Stderr` directly
   (`api.go:941,1063`) rather than going through `unitError`/`.Error()`.

This makes the redaction a property of `internal/planapply`'s public
contract: every `*opentofu.CommandError` and every byte written to `out` that
`PlanUnits`/`Apply` return or produce is already scrubbed by the time it
leaves the package — `internal/api` and `internal/cli` need no additional
scrubbing of their own.

## `internal/engine/opentofu`

No signature changes. `Runner.Env` (already exists, unused today) becomes
live via `planapply`'s wiring. The doc comment on `providers()` in
`internal/compile/unit.go:144-153` ("the API token comes from the environment
variable `PROXMOX_VE_API_TOKEN`... when nodr sets it from the cluster's
credentialsRef") becomes true for the first time.

## `internal/proxmox`

### TLS fingerprint pinning (wiring `ErrFingerprintMismatch`)

`ErrFingerprintMismatch` exists (`errors.go:10`) with no producer. This slice
adds the producer:

```go
// PinnedFingerprint, if set, is compared against the SHA-256 fingerprint of
// the server's leaf certificate on every TLS handshake; a mismatch fails
// the handshake with ErrFingerprintMismatch instead of the usual
// certificate-verification error, so callers can distinguish "this looks
// like the wrong server" from "ordinary connection failure".
func NewPinnedHTTPClient(fingerprint string) *http.Client
```

Implemented via `tls.Config{InsecureSkipVerify: true, VerifyPeerCertificate:
func(rawCerts [][]byte, _ [][]*x509.Certificate) error { ... }}`: computes
the SHA-256 of `rawCerts[0]` (the leaf), formats it colon-separated
upper-hex to match Proxmox's own `GetCertFingerprint` format, compares
case-insensitively, returns `ErrFingerprintMismatch` on mismatch.
`InsecureSkipVerify` is safe here specifically because
`VerifyPeerCertificate` replaces Go's own chain validation with the
fingerprint pin, which is exactly Proxmox's own trust model for self-signed
certificates (§6.2: "nodr pins the fingerprint on first contact after the
user confirms it, or trusts ACME certificates when the cluster uses them").
When the cluster uses an ACME certificate, the wizard skips pinning and uses
`http.DefaultClient`'s ordinary chain validation instead (§6.2's "or trusts
ACME certificates").

### Bootstrap flow (`internal/proxmoxbootstrap`, new package)

A new small package sequences Slice A's existing client methods —
`internal/proxmox` itself gains no new endpoint methods, since Slice A
already built every call this flow needs:

```go
package proxmoxbootstrap

// Result holds everything the wizard needs to store and record.
type Result struct {
    User          string // "nodr@pve"
    Role          string // "NodrOperator"
    TokenID       string // "nodr"
    TokenSecret   string // never logged; caller stores it and discards this struct
    Fingerprint   string // SHA-256 pin recorded for the endpoint, "" if ACME
}

// Bootstrap logs in with the administrator credential, creates the
// dedicated user, role and API token, and grants the role on "/" for the
// new user. It returns after the administrator credential's ticket/CSRF
// pair is discarded from the client (Client.Login followed by
// Client.SetAPIToken clears them, see client.go:52-55,63-64) — the caller
// never sees the plaintext administrator password beyond passing it into
// this call.
func Bootstrap(ctx context.Context, client *proxmox.Client, privileges []string) (Result, error)
```

`Bootstrap`'s implementation: `client.CreateUser(ctx, "nodr@pve", opts)` →
`client.CreateRole(ctx, "NodrOperator", privileges)` →
`client.UpdateACL(ctx, "/", map[string][]string{"nodr@pve": {"NodrOperator"}})`
→ `client.CreateAPIToken(ctx, "nodr@pve", "nodr")` → returns the secret in
`Result.TokenSecret`. The privilege list (§6.2's table) is a package-level
`[]string` constant in `proxmoxbootstrap`, not yet version-conditional
(`nodr pve role-spec --version 9` from §6.2 is a later refinement — this
slice ships one fixed list covering Proxmox VE 8.x and 9.x, the two versions
in scope per §6.1).

## `internal/authn` (new package)

Minimal local-admin session auth, matching the shape of §10.3 that is in
scope for a single-admin, loopback-bound server:

```go
package authn

// Account is the one local administrator account. Argon2id per §10.3;
// golang.org/x/crypto/argon2, parameters follow the OWASP-recommended
// baseline (time=1, memory=64*1024 KiB, threads=4, keyLen=32), stored
// alongside the salt in the hash string (standard $argon2id$... encoding).
type Account struct {
    Username     string
    PasswordHash string // $argon2id$... encoded
}

// Store persists the one Account and active sessions. Backed by the same
// per-workspace SQLite file secrets.Store already uses
// (<workspace>/.nodr/authn.db, mode 0600, never in Git — same .gitignore
// rule already covers .nodr/) rather than a new file, keeping the
// per-workspace state surface small.
type Store struct { /* db *sql.DB */ }

func Open(dbPath string) (*Store, error)

// CreateAccount sets the one local admin account, replacing any existing
// one. Used only by the "nodr auth create-admin" CLI command.
func (s *Store) CreateAccount(ctx context.Context, username, password string) error

// Authenticate checks username/password against the stored Account and,
// on success, creates a new session and returns its opaque token (32
// crypto/rand bytes, base64url-encoded) and expiry.
func (s *Store) Authenticate(ctx context.Context, username, password string) (token string, expiresAt time.Time, err error)

// ValidateSession reports whether token is a live, unexpired session.
func (s *Store) ValidateSession(ctx context.Context, token string) (ok bool)

// Logout deletes the session for token.
func (s *Store) Logout(ctx context.Context, token string) error

func (s *Store) Close() error
```

Sessions: absolute timeout matching §10.3 ("idle and absolute timeouts") —
fixed 24h absolute expiry for this slice; idle timeout is deferred (needs
last-seen tracking not yet justified for a single-admin server). No lockout
with exponential backoff yet (§10.3 lists it for the eventual multi-account
model) — deferred, tracked as an open risk below since a single admin
account with no rate limiting is brute-forceable if exposed beyond loopback.

### HTTP middleware and routes

```go
// Middleware wraps h, requiring a valid session cookie (name "nodr_session",
// HttpOnly, Secure when the request came in over TLS, SameSite=Strict) for
// every request except those loginPaths names exactly. On failure it
// writes a 401 application/problem+json body (matching internal/api's
// existing Problem shape) rather than redirecting, since this guards an
// API, not a browsable page.
func Middleware(store *Store, loginPaths ...string) func(http.Handler) http.Handler

// LoginHandler handles POST {loginPath}: decodes {"username","password"},
// calls store.Authenticate, sets the session cookie on success.
func LoginHandler(store *Store, loginPath string) http.Handler

// LogoutHandler handles POST {logoutPath}: reads the session cookie, calls
// store.Logout, clears the cookie.
func LogoutHandler(store *Store, logoutPath string) http.Handler
```

## `internal/api`

`Handler(ctx, root)` gains an `authn.Store` parameter (opened by the caller —
`internal/cli/server.go` — from `<workspace root>/.nodr/authn.db`, mirroring
how the caller will eventually open `secrets.Store` too):

```go
func Handler(ctx context.Context, root string, auth *authn.Store, secrets *secrets.Store) http.Handler
```

Inside `Handler`, two new unauthenticated routes are registered first,
`POST /api/v1/auth/login` and `POST /api/v1/auth/logout`, then
`authn.Middleware(auth, "/api/v1/auth/login")` wraps the rest of the mux
(the existing `workspaces`/`resources`/`commands` routes) — inserted here,
inside `internal/api`'s own `Handler`, not in `internal/cli/server.go`'s
outer mux, so `internal/webui`'s static assets (including the new login
page, see below) stay reachable unauthenticated while every
`/api/v1/workspaces/...` route requires a session, GET included: listing
workspaces/resources discloses cluster topology and VM inventory, which is
exactly the kind of intent §10.1's objective 1 protects.

`server.secrets *secrets.Store` becomes a new field, threaded into
`planWorkspace`/`applyWorkspace`'s calls to
`planapply.PlanUnits(..., a.secrets.Resolve)` /
`planapply.Apply(..., a.secrets.Resolve)` (replacing today's `nil, nil` for
`out, interrupts` is unchanged — those stay `nil` on the API path, matching
current behavior; only the new `Resolver` parameter is added).

### Login page

A minimal static HTML form, served by `internal/webui` (not a new React
route — `web/src/App.tsx` has no auth-aware view today, and a session-cookie
gate needs no client-side state beyond "redirect to /login on 401", which a
plain `fetch` interceptor in `web/src/api.ts`'s `fetchJSON` can do without a
new view):

```go
// internal/webui: LoginPage returns the embedded static login form, served
// at GET /login (outside the API's own auth gate, alongside the SPA's other
// static assets).
```

`web/src/api.ts`'s `fetchJSON` (the sole fetch chokepoint, `api.ts:84-93`)
gains one line: on a `401` response, `window.location.href = "/login"`
instead of throwing `ProblemError`, so any stale session redirects to the
login form without every call site handling it. No other client change —
cookies are sent same-origin automatically; no CSRF header per the Non-goals
above.

## `internal/cli`

- `internal/cli/server.go:32`: `--addr` default changes from `":8080"` to
  `"127.0.0.1:8080"`. Binding to a non-loopback address stays possible via
  the existing flag — this is a default change, not a removed capability.
- The `app` struct (`internal/cli/cli.go`) gains a lazily-opened
  `*secrets.Store` field, opened from `<workspace root>/.nodr/secrets.db` the
  first time `planUnits` or `apply` runs (mirroring `mustLoad`'s own
  lazy-load pattern), using the same `--kek-file`/`NODR_KEK` flag/env-var
  pair the server command uses. `a.planUnits`/`a.apply`
  (`internal/cli/plan.go:96,140`) pass `a.secretsStore().Resolve` into
  `planapply.PlanUnits`/`Apply` as the new `Resolver` parameter — the CLI
  path needs credential wiring exactly as much as the API path, since both
  ultimately call the same `internal/planapply` functions. A workspace with
  no `.nodr/secrets.db` yet (no cluster connected) opens an empty store
  (`secrets.Open` creates the file), so `Resolve` simply returns
  `ErrNotFound` for any `credentialsRef` looked up, unless the compiled
  units reference no cluster at all, in which case `Resolve` is never
  called at all.
- `runServer` (`internal/cli/server.go`) opens `<root>/.nodr/authn.db` via
  `authn.Open` and `<root>/.nodr/secrets.db` via `secrets.Open` (same
  `--kek-file`/`NODR_KEK` pair), passing both into `nodrapi.Handler`.
- New `nodr auth create-admin` subcommand (registered in
  `internal/cli/cli.go:98-107`'s `rootCommand()`): prompts for a username
  and, using `term.ReadPassword` (already available via the existing
  `golang.org/x/term` dependency — no new import needed beyond the one
  already at `cli.go:12`) twice for confirmation, then calls
  `authn.Store.CreateAccount`. Refuses to run non-interactively (same
  `a.interactive` check pattern already used by `applyCommand`,
  `cli.go`/`plan.go`'s existing convention) unless a `--password-stdin` flag
  is given, for scripted setup.
- New `nodr cluster connect` subcommand: the interactive wizard. Prompts for
  the cluster's endpoint URL and an administrator username/password (via
  `term.ReadPassword`), fetches the certificate fingerprint via
  `client.GetCertFingerprint`, prints it for the operator to confirm (typed
  "yes", matching apply's existing confirmation pattern in
  `internal/cli/plan.go`'s `confirm`), builds a pinned `http.Client` via
  `proxmox.NewPinnedHTTPClient`, calls `proxmoxbootstrap.Bootstrap`, writes
  the resulting secret via `secrets.Store.Put(ctx, ref, token)` where `ref`
  is a generated name such as `proxmox/<cluster-name>-token`, and writes a
  new `ProxmoxCluster` intent document under `intent/platform/` with
  `credentialsRef: <ref>` and `tls.fingerprint: <pinned value>`, using the
  same YAML-writing approach `internal/api/api.go`'s `createVM` already uses
  for `VirtualMachine` documents (`marshalVMIntent`-style struct + `yaml.Marshal`).

## Testing

- `internal/compile`: existing 16 tests updated for the new second return
  value; a new test asserts `unitClusters` maps each unit directory to the
  correct cluster name, and stays absent/empty for a unit with no managed
  Proxmox VMs.
- `internal/planapply`: new tests using the existing fake-tofu-script
  pattern (mirroring `internal/cli/plan_test.go:27-65`'s script that already
  echoes `$PROXMOX_VE_API_TOKEN` on `init`) proving: (a) a `Resolver`
  returning a known token makes that token appear in the child process's
  environment (assert via the fake script's log file); (b) a nil `Resolver`
  behaves exactly as today (no `PROXMOX_VE_API_TOKEN` set beyond whatever
  the test process's own environment already has); (c) forcing the fake
  script to fail and echo the token to stderr, the returned
  `*opentofu.CommandError.Stderr` does **not** contain the token substring,
  while the raw stderr (captured by the test harness independent of nodr)
  does — proving the scrubbing is real, not merely a case where the token
  never appeared.
- `internal/authn`: `CreateAccount`→`Authenticate` round-trip; wrong password
  rejected; `ValidateSession` true immediately after `Authenticate`, false
  after `Logout`, false after the fixed 24h expiry (test with an injectable
  clock or a session directly inserted with a past expiry); concurrent
  session creation (SQLite's own locking, no additional
  application-level locking — same reasoning Slice A used for
  `secrets.Store`).
- `internal/proxmox`: `NewPinnedHTTPClient` tested against
  `httptest.NewTLSServer` — matching fingerprint succeeds, mismatched
  fingerprint returns `ErrFingerprintMismatch` via `errors.Is`.
- `internal/proxmoxbootstrap`: `Bootstrap` against `proxmoxtest.NewServer`
  (Slice A's fake server), asserting the exact sequence of requests
  (`CreateUser` → `CreateRole` → `UpdateACL` → `CreateAPIToken`) and that the
  returned `Result.TokenSecret` matches the fake server's configured value.
- `internal/api`: extend `api_test.go`'s fake-tofu script (currently around
  lines 538-567) to also echo `$PROXMOX_VE_API_TOKEN` on `init`, mirroring
  `plan_test.go`; new tests prove the token reaches the child process
  (log-file assertion) but is absent from both the success-path
  `workspacePlanResponse`/`applyWorkspace` JSON bodies and the error-path
  `Problem.Detail` string (extending `TestWorkspacePlanUnitCommandFails` and
  a new equivalent for apply, forcing `FAKE_TOFU_FAIL` and asserting
  `!strings.Contains(response.Body.String(), token)`). New tests for the
  auth gate: every existing mutating-route test (`TestCreateVM`,
  `TestVMLifecycleCommands`, `TestWorkspacePlan`, `TestWorkspaceApplySuccess`,
  etc.) is updated to authenticate first (a shared test helper logs in via
  `LoginHandler` and attaches the resulting cookie to subsequent requests);
  new tests assert every one of those routes returns 401 with no session
  cookie, and that `GET /api/v1/workspaces` (previously unauthenticated) now
  also requires one.
- `internal/webui`: `GET /login` serves the static form; `/api`-prefixed
  paths still 404 through the login page's own handler (unchanged from
  today's exclusion at `server.go:31-34`).
- Manual smoke test (not automated, run once before merge): start
  `nodr server` on the loopback default, confirm an unauthenticated
  `curl -X POST .../commands` gets 401, log in via `nodr auth create-admin`
  then the browser login form, confirm the same request succeeds with the
  session cookie, and grep the server's stdout/stderr and
  `git log -p`/`git grep` across the test workspace for the literal token
  value to confirm absence.

## Dependencies added

- `golang.org/x/crypto/argon2` (Argon2id password hashing for the local
  admin account).

## Open risks

- No account lockout/backoff on failed logins this slice (§10.3 lists it,
  deferred). Mitigated by the loopback-only default bind — brute-forcing
  requires either local access already, or the operator opting into a
  non-loopback `--addr`, which is now a deliberate, documented choice rather
  than the default.
- Proxmox privilege list in `proxmoxbootstrap` is a single fixed list, not
  yet version-conditional per §6.2's `nodr pve role-spec --version 9`
  aspiration; a version mismatch in an untested Proxmox VE release could
  grant an incomplete role. Follow-up if slice C/D discovery reveals a
  missing privilege.
- `NewPinnedHTTPClient`'s `InsecureSkipVerify: true` is a deliberate,
  narrowly-scoped bypass of Go's chain validation, replaced by an equivalent
  fingerprint check — reviewers should confirm `VerifyPeerCertificate`
  actually runs on every connection reuse, not just the first handshake on a
  connection (Go's `tls` package guarantee: `VerifyPeerCertificate` runs once
  per handshake, and connection pooling reuses an already-verified TLS
  connection, which is the correct, intended behavior — pinning is a
  per-handshake check, not a per-request one).
