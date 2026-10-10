# M0 GitHub ledger: connect, drafts, plan, apply

Status: approved by standing instruction ("continue development to meet requirements")
Date: 2026-10-09
Implements: `docs/design/02-architecture.md` §2.2–2.3, `docs/design/11-api-and-cli.md` §11.2–11.4,
`docs/design/13-operations-and-delivery.md` §13.8 (M0 exit criteria).

## Problem

`main` ships a local-workspace server. The M0 contract is a browser control
plane whose desired state lives in a GitHub repository. Four foundation
packages exist but nothing consumes them: `internal/repository` (bare mirror,
draft refs, CAS), `internal/githubapp` (app auth, webhook signature),
`internal/operations` (changesets, operations, idempotency, events),
`internal/sync` (render-lock seam). This slice wires them into an API and UI.

## Decisions

1. **Ledger mode is opt-in.** Enabled when `NODR_GITHUB_APP_ID`,
   `NODR_GITHUB_PRIVATE_KEY_FILE`, `NODR_GITHUB_WEBHOOK_SECRET`, `NODR_PUBLIC_URL`
   are all set (all-or-none, like the bootstrap group). Without them the
   server behaves exactly as today (local workspace). In ledger mode the
   bootstrap-created local workspace only hosts `authn.db`/`secrets.db`; the
   workspace routes below replace the legacy `/workspaces*` routes and the
   local workspace is not listed.
2. **Slug = manifest name.** A connected workspace's slug is
   `metadata.name` of the repository's `nodr.yaml`. This lets existing
   per-workspace handlers be reused against a checkout.
3. **Renderer is the existing `compile` path.** Commands mutate intent in a
   checkout, then `compile.Compile`+`Write` regenerates HCL, all in one
   commit. `internal/sync.Synchronize` (render.lock seam) is NOT wired in this
   slice; it needs the lens to emit lock entries first. Stated here so it is
   not mistaken for done.
4. **Runner seam, local implementation.** `ledger.Runner` is the interface to
   the runner. This slice ships `LocalRunner` (spawns `tofu` on the same host
   via `planapply`). The separate runner OCI service and its protocol stay an
   open implementation gate and are not claimed.
5. **Saved plans are process-local.** `LocalRunner` retains the plan
   directory and `planapply.UnitPlan`s in memory keyed by plan operation ID. A
   restart loses them; apply then fails with `409 PLAN_CHANGED`
   (`ErrPlanExpired`) and the user re-plans. Artifact metadata stays in SQLite.
6. **Legacy local API stays.** The command logic is extracted from
   `internal/api/api.go` into `internal/commands` and the legacy handlers call
   it (clean cutover for that logic; no duplicate copy).

## Components and ownership

| Package | Owner task | Notes |
| --- | --- | --- |
| `internal/operations` | T0 | additions below |
| `internal/repository` | T1 | auth, push, promote, diff, ref lookup |
| `internal/commands` + `internal/api/api.go` (legacy handlers only) | T2 | extract VM command logic |
| `internal/ledger` (service, connect, webhook, revision, commands, changesets) | T3 | |
| `internal/ledger` (runner.go, plan.go) | T4 | Runner, LocalRunner, Plan/Apply |
| `internal/api/ledger*.go` | T5 | HTTP surface over `ledger.Service` |
| `web/src` | T6 | connect-GitHub, changesets, operations UI |
| `internal/cli/server.go`, deploy/docs/CHANGELOG | orchestrator, after T1–T6 | wiring, readyz |

## Contract: operations additions (T0)

Existing methods and tests are unchanged. Added to `*operations.Store`:

```go
var ErrStaleDraft = errors.New("operations: stale draft")
// AdvanceDraft sets DraftSHA=newSHA and Revision++ iff the changeset is open or planned and
// DraftSHA==expectedSHA (else ErrStaleDraft; closed changeset -> ErrInvalidTransition).
// A planned changeset returns to open (its plan no longer matches). Appends event "changeset.updated".
func (s *Store) AdvanceDraft(ctx context.Context, id, expectedSHA, newSHA string) (Changeset, error)
// Changesets lists a workspace's changesets, newest first.
func (s *Store) Changesets(ctx context.Context, workspace string) ([]Changeset, error)
// FinishOperation moves a running operation to succeeded|failed and stores result (summary text or JSON) atomically.
func (s *Store) FinishOperation(ctx context.Context, id string, to OperationStatus, result string) (Operation, error)
// OperationByKey returns the operation claimed under (scope,key) or ErrNotFound.
func (s *Store) OperationByKey(ctx context.Context, scope, key string) (Operation, error)
// LatestPlan returns the newest succeeded plan operation of a changeset and its artifact, or ErrNotFound.
func (s *Store) LatestPlan(ctx context.Context, changesetID string) (Operation, PlanArtifact, error)
```
Command operations use kind `"command"` (PlanSHA empty) and store the JSON `CommandResult`
in the result summary via `FinishOperation`. `ApplyChangeset`-style marking uses existing
`TransitionChangeset(..., ChangesetApplied)`.

## Contract: repository additions (T1)

```go
// Option configures LocalStore.
type Option func(*LocalStore)
// WithAuth supplies per-installation Git credentials for clone/fetch/push.
func WithAuth(f func(ctx context.Context, installationID int64) (transport.AuthMethod, error)) Option
func NewLocalStore(dataRoot string, opts ...Option) (*LocalStore, error) // opts optional; existing callers unchanged

type FileDiff struct {
    Path   string // forward slashes
    Status string // "added" | "modified" | "deleted"
    Patch  string // unified diff; empty for binary
}

var ErrNotFastForward = errors.New("repository: not a fast-forward")

// Added to Store:
Ref(ctx context.Context, slug, ref string) (sha string, err error)           // resolves a ref in the mirror
Diff(ctx context.Context, slug, baseSHA, headSHA string) ([]FileDiff, error)
Push(ctx context.Context, slug, ref string) error                            // push one ref to origin (installation auth)
// Promote fast-forwards the default branch to draftSHA iff default == expectedDefaultSHA
// and expectedDefaultSHA is an ancestor of draftSHA; then pushes the default branch.
// ErrStaleRevision if default moved, ErrNotFastForward if not an ancestor.
Promote(ctx context.Context, slug, draftSHA, expectedDefaultSHA string) error
```
`CreateWorkspace`/`Fetch` use the auth option when `GitHubInstallationID != 0`.
`CompareAndSwap` on a draft ref also pushes the new draft ref to origin so the
draft is durable in GitHub; a push failure returns an error and leaves the local
ref unchanged. Tests use a local bare repo as `origin` (no network).

## Contract: commands (T2)

```go
package commands

type FieldError struct{ Path, Message string }
// Error is a user-facing command failure; API maps Status/Title/Detail/Fields to RFC 9457.
type Error struct{ Status int; Title, Detail string; Fields []FieldError }
func (e *Error) Error() string

type VMCreateParams struct{ /* exactly the current api.vmCreateParams fields and json tags */ }
type VMCreateResult struct{ /* exactly the current api.vmCreateResponse */ }

func CreateVM(root string, p VMCreateParams) (VMCreateResult, error)
func SetVMPowerState(root, name, powerState string) (VMPowerStateResult, error)
func DeleteVM(root, name string) (VMDeleteResult, error)
// Run dispatches "vm.create" | "vm.start" | "vm.stop" | "vm.delete" with raw JSON params
// (DisallowUnknownFields) and returns the JSON-serialisable result; unknown command -> *Error 400.
func Run(root, command string, params json.RawMessage) (any, error)
```
Behavior (validation messages, status codes, rollback-on-failure, admission) must
be byte-for-byte what `api.go` does today; the existing API tests are the
regression suite. `createMu` stays in the api server (legacy) — `commands`
functions are pure over `root`.

## Contract: ledger (T3 owns service.go; T4 adds methods)

```go
package ledger

type Deps struct {
    Repo    repository.Store
    Meta    *repository.MetadataStore
    Ops     *operations.Store
    GitHub  GitHub
    Secrets *secrets.Store      // credentials resolver for tofu; may be nil
    Runner  Runner              // T4
    WebhookSecret string        // stored (encrypted) in workspace metadata
}
type GitHub interface { // satisfied by *githubapp.Service
    ListInstallations(ctx context.Context) ([]*github.Installation, error)
    ListRepositories(ctx context.Context, installationID int64) ([]*github.Repository, error)
    VerifyWebhookSignature(payload []byte, signature string) error
}
type Service struct { deps Deps; ctx context.Context /* server lifetime; used for plan/apply goroutines */ ; ... }
func New(ctx context.Context, d Deps) (*Service, error)
func (s *Service) Close() error // waits for in-flight background operations

type Workspace struct{ Slug, Owner, Repository, DefaultBranch, DefaultSHA string; InstallationID int64 }
type RepositoryRef struct{ Owner, Name, DefaultBranch string; Private bool }
type Installation struct{ ID int64; Account string; Repositories []RepositoryRef }
type ConnectInput struct{ InstallationID int64; Owner, Repository string }
type FileDiff = repository.FileDiff

var (
    ErrRepositoryInaccessible, ErrNotNodrRepository, ErrWorkspaceExists,
    ErrWorkspaceNotFound, ErrChangesetNotFound, ErrStaleRevision, // -> 412
    ErrPlanChanged,                                                // -> 409 PLAN_CHANGED
    ErrPlanRequired, ErrChangesetClosed error                      // -> 409
)
// *CommandError wraps *commands.Error for API mapping: type CommandError struct{ Err *commands.Error }.

func (s *Service) Installations(ctx) ([]Installation, error)
func (s *Service) Connect(ctx, ConnectInput) (Workspace, error)   // verifies repo in installation, clones mirror, requires nodr.yaml with valid Workspace manifest; slug=metadata.name
func (s *Service) Workspaces(ctx) ([]Workspace, error)
func (s *Service) Workspace(ctx, slug string) (Workspace, error)
// WithRevision checks out revision (SHA or "" for default HEAD), calls fn(root), always removes the checkout.
func (s *Service) WithRevision(ctx, slug, revision string, fn func(root string) error) error
// RevisionOf resolves a changeset ID to its draft SHA ("" id -> default HEAD).
func (s *Service) RevisionOf(ctx, slug, changesetID string) (string, error)

type CommandInput struct{ Workspace, ChangesetID, ExpectedSHA, Actor, Command, IdempotencyKey string; Params json.RawMessage }
type CommandResult struct{ Changeset operations.Changeset; Diff []FileDiff; Result any }
// RunCommand: ChangesetID=="" creates a new changeset+draft ref from default HEAD, ExpectedSHA must equal
// default HEAD; otherwise ExpectedSHA must equal changeset DraftSHA (else ErrStaleRevision).
// Checkout -> commands.Run -> compile.Compile+Write -> repository.Commit("nodr: <actor> <command>")
// -> CompareAndSwap(draft ref) -> ops store update (revision++, DraftSHA). Replayed IdempotencyKey returns the
// recorded CommandResult and creates no second commit (record result JSON in operations table via ClaimOperation kind "command").
func (s *Service) RunCommand(ctx, CommandInput) (CommandResult, error)
func (s *Service) Changesets(ctx, slug string) ([]operations.Changeset, error)
type ChangesetDetail struct{ operations.Changeset; Diff []FileDiff }
func (s *Service) Changeset(ctx, slug, id string) (ChangesetDetail, error)

// T4:
type PlanInput struct{ Workspace, ChangesetID, ExpectedSHA, Actor, IdempotencyKey string }
func (s *Service) Plan(ctx, PlanInput) (operations.Operation, error)  // 202: queued, runs in background on s.ctx
type ApplyInput struct{ Workspace, ChangesetID, ExpectedSHA, Actor, IdempotencyKey string; AllowDestroy bool }
func (s *Service) Apply(ctx, ApplyInput) (operations.Operation, error)
func (s *Service) Operation(ctx, id string) (operations.Operation, error)
func (s *Service) PlanSummary(ctx, operationID string) (PlanSummary, error) // from artifact JSON
func (s *Service) Subscribe() (<-chan operations.Event, func())
func (s *Service) EventsAfter(ctx, afterID int64) ([]operations.Event, error)

// Webhook (T3):
// HandlePush verifies nothing (API verifies signature); it receives the parsed push.
type PushEvent struct{ DeliveryID, Owner, Repository, Ref, After string }
func (s *Service) HandlePush(ctx, PushEvent) error
```
`HandlePush`: unknown repository -> nil (ignored). Duplicate `DeliveryID`
(`Meta.DeliverySeen`/`RecordDelivery`) -> nil with no work. Ref must be the
default branch or `refs/heads/nodr/drafts/…`, else ignored. Default-branch push:
`Fetch`, then if `After` != `LastProcessedSHA`: checkout `After`, run
`compile.Compile`; if it yields file changes, create a server-owned sync draft
(commit `nodr: sync`) as a new open changeset; `SetLastProcessed(After)` only
after that succeeds. Draft-ref push: `Fetch` only. Record the delivery ID last.

### Runner (T4)

```go
type PlanRequest struct{ OperationID, Workspace, DraftSHA, Root string /* checkout, retained */ }
type PlanResult struct{ JSON []byte /* normalized PlanSummary */ ; HasChanges, HasDestructive bool }
type ApplyRequest struct{ PlanOperationID string; AllowDestroy bool }
type Runner interface {
    Plan(ctx context.Context, req PlanRequest, resolve planapply.Resolver) (PlanResult, error)
    Apply(ctx context.Context, req ApplyRequest) error // ErrPlanExpired if unknown
    Discard(planOperationID string)
}
type PlanSummary struct {
    Units []struct{ Dir string; Create, Update, Replace, Delete int; Changes []struct{ Address, Action string } }
    HasChanges, HasDestructive bool
}
```
`LocalRunner{Dir string}` keeps `Dir/<opID>/` plan files and a retained checkout.
Plan: `Service.Plan` claims op (kind "plan", PlanSHA = draft SHA), returns it, then in a goroutine:
queued->running, checkout draft SHA into retained dir, `Runner.Plan`, `PutPlanArtifact`,
op->succeeded, changeset->planned (or op->failed with Result = redacted error). Apply: changeset must be
`planned`, latest succeeded plan artifact DraftSHA == changeset.DraftSHA == ExpectedSHA, else `ErrPlanChanged`
(`ErrPlanRequired` when never planned); destructive plan without AllowDestroy -> 422 field `allowDestroy`.
Then `Promote(draftSHA, defaultSHA)` once, then `Runner.Apply`; op succeeded/failed; changeset->applied on success.
`Promote` stale/not-ff -> `ErrStaleRevision`/conflict surfaced, nothing applied. Plan/apply never use the request
context for execution (server-lifetime `s.ctx`).

## HTTP contract (T5; all under `/api/v1`, errors RFC 9457)

| Route | Notes |
| --- | --- |
| `POST /setup/github/installations` | auth+CSRF; `{installations:[{id,account,repositories:[{owner,name,defaultBranch,private}]}]}` |
| `GET /workspaces`, `POST /workspaces` | POST body `{installationId,owner,repository}` strict JSON; 201 `{name,owner,repository,defaultBranch,headSha}`; `ErrRepositoryInaccessible`/`ErrNotNodrRepository` -> 422 with field `repository`; `ErrWorkspaceExists` -> 409 |
| `GET /workspaces/{w}` | existing shape `{name, environments}` plus `headSha`, `owner`, `repository`; ETag = headSha |
| `GET /workspaces/{w}/resources?kind=&changeset=` | reuses legacy `server.listResources` against `WithRevision(RevisionOf(changeset))`; ETag = revision SHA |
| `POST /workspaces/{w}/commands` | headers `If-Match` (required, SHA), `Idempotency-Key` (required), optional body field `changeset`; body `{command, params, changeset?}`; 200 `{changeset:{id,status,baseSha,draftSha,revision}, diff:[…], result}` + `ETag: draftSha`. Missing header -> 428; stale -> 412 `PRECONDITION_FAILED`; `*CommandError` -> its status/fields |
| `GET /workspaces/{w}/changesets`, `GET …/changesets/{id}` | detail includes `diff` |
| `POST …/changesets/{id}:plan` | 202 `{operation}`; needs `If-Match`+`Idempotency-Key` |
| `POST …/changesets/{id}:apply` | 202 `{operation}`; body `{allowDestroy?:bool}`; `PLAN_CHANGED` 409 |
| `GET /operations/{id}` | `{id,kind,status,changesetId,planSha,result,plan?:PlanSummary}` |
| `GET /events` | SSE (`text/event-stream`), `Last-Event-ID` replay via `EventsAfter`, CloudEvents JSON `{specversion,type,id,source,data}` with types `nodr.operation.started|succeeded|failed`, `nodr.changeset.changed`; flush each event; closes on client disconnect or subscriber overflow |
| `POST /integrations/github/webhook` | unauthenticated; read body (cap 5 MiB), verify `X-Hub-Signature-256` BEFORE parsing -> 401/403 on failure; `X-GitHub-Event: push` parsed, others 202 ignored; `X-GitHub-Delivery` required; 202 |
| `GET /healthz`, `GET /readyz` | outside auth. readyz checks ops DB ping, secrets store, repository store, GitHub configured; 503 problem listing failed checks |

`LedgerHandler(cfg LedgerConfig, next http.Handler) http.Handler` in
`internal/api/ledger.go`: serves the table above (authenticated routes wrapped by
`authn.Middleware(auth)`), delegates every other path to `next` (auth/setup/login
routes). `commandRequest`/problem helpers in `api.go` are reused.

## Web (T6)

New "Repositories" flow in Infrastructure/Settings area using existing primitives
and `api.ts` DTO conventions: connect GitHub repository (installations → repos →
connect, field error `repository`), changesets list/detail with diff, command
buttons route through `If-Match`/`Idempotency-Key` (client generates UUID per user
action), plan/apply with operation status via SSE invalidation (re-fetch, never trust
payload). Ledger mode detected when `GET /workspaces` items carry `headSha`.

## Verification

- Go: unit tests per package; `go test -race ./...`; golangci-lint v2.8.0.
- Repository tests use a local bare `origin`; LocalRunner tests use the `FAKE_TOFU_*`
  shell-script fake (`NODR_TOFU`); API tests use in-process `httptest` with a fake
  `ledger` seam; HTTP end-to-end test in `internal/ledger` wires real repository +
  operations + fake GitHub + fake tofu.
- Smoke: run the built binary in ledger mode against a local `file://` origin (no
  GitHub reachable offline): connect, command, plan, apply, webhook push, SSE.
- Web: `npm run build`; browser observation of the connect/changeset flow.

## Out of scope (stated, not hidden)

Separate runner service/protocol; `sync.Synchronize` wiring; PR/status checks;
multi-instance; OIDC; GitHub App creation/manifest flow (operator creates the app);
reconnect/disconnect workspace.
