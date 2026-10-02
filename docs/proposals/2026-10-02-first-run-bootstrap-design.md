# First-Run Browser Bootstrap Design

## Goal

Let an operator install `nodr`, start `nodr server` on a blank host, and complete all first-run configuration in the browser. Support fully unattended setup through environment variables for Docker, Compose, Kubernetes, and systemd.

The installer remains non-interactive. No interactive CLI or TUI setup path is added.

## Scope

This slice creates exactly one local Git-backed workspace and its first local administrator. It then sends the authenticated operator to the existing browser cluster-connection flow.

It does not clone a remote repository, configure a Git remote, discover infrastructure, or provision TLS. Those are later workflows.

## Existing foundation

This design extends the already-shipped first-admin foundation rather than
replacing it. `authn.Store.InitializeBootstrap` stores an Argon2id hash of a
bootstrap token, `authn.Store.Setup` consumes it while creating the first
account and session/CSRF token, `POST /api/v1/setup` exposes that operation,
and `web/src/SetupForm.tsx` renders its browser form. The current gap is that
`nodr server` calls workspace loading before listening, so that flow cannot
run on a blank host and it cannot create a workspace.

The bootstrap transaction adds workspace creation to the existing setup
operation. It extends the existing request with `workspaceName`; it retains
the same endpoint, token verification, password hashing, session issuance,
RFC 9457 errors, and setup-status probe. It does not introduce a parallel
bootstrap API, second account store, or second browser flow.

## Server lifecycle and mode selection

`nodr server` gains `--data-dir DIR`. It owns bootstrap state and newly created workspaces. Its default is user-writable: `$XDG_STATE_HOME/nodr` when `XDG_STATE_HOME` is set, otherwise `$HOME/.local/state/nodr`.

`--workspace` keeps its current meaning: it selects an existing workspace. When supplied, it must load that workspace normally; it never falls back to bootstrap and ignores `--data-dir` for workspace selection. This preserves deterministic existing-server behavior.

Without `--workspace`, server mode is selected in this order:

1. Validate the `NODR_BOOTSTRAP_*` environment group.
2. If the complete environment group is present, run unattended bootstrap and then serve the new workspace normally.
3. If none of the environment group is present and setup is incomplete, serve browser bootstrap mode.
4. If setup is complete, load the recorded workspace and serve normally.
5. If only part of the environment group is present, fail before binding the listener with the names of the missing variables. Browser values are never mixed with deployment values.

Bootstrap completion is an atomically renamed, `0600` JSON file at
`<data-dir>/bootstrap.json`:

```json
{"version":1,"workspace":"workspaces/<workspace-name>"}
```

It records only the relative workspace identity and completion marker; it
never stores bootstrap tokens or administrator passwords. Its parsed path
must remain below `<data-dir>/workspaces`.

## Authorization and exposure

The default listener remains `127.0.0.1:8080`.

Browser bootstrap is authorized by a one-time secret submitted in a POST body. It is never accepted in a URL, query parameter, cookie, persisted state file, or response payload.

Token selection is:

1. If `NODR_BOOTSTRAP_TOKEN` is set, use it for this process lifetime. It takes precedence over generated tokens.
2. Otherwise generate a cryptographically random token at startup and print it once to server stdout with a warning that log readers can use it.
3. A successful bootstrap invalidates the in-memory token as the completion marker is committed.
4. On restart before completion, a generated token is replaced; an environment token remains usable because its source is operator configuration.

An explicit non-loopback `--addr` is supported. Before binding in browser-bootstrap mode, the server emits a warning that the endpoint is remotely reachable, requires the bootstrap token, and should be protected by TLS at a reverse proxy. The server does not claim to provide TLS itself.

Unattended bootstrap requires no browser token because environment access is the authorization boundary.

## Key encryption key

Bootstrap resolves the KEK before binding the listener so browser and
unattended setup use exactly the same secrets contract:

1. `--kek-file` retains its existing precedence and supplies the KEK.
2. Otherwise `NODR_KEK` supplies the base64-encoded KEK.
3. Otherwise bootstrap generates a random 32-byte KEK once at
   `<data-dir>/kek`, with mode `0600`, using an atomic create operation.

The server prints the generated key-file location and an explicit warning:
back up `<data-dir>/kek` before moving or restoring the data directory. Loss
of that key makes previously encrypted Proxmox credentials unrecoverable.
The key is never printed, returned by the API, placed in bootstrap state, or
logged. Later restarts load that same file; a different generated key is never
silently substituted.

If a configured KEK is malformed or the generated key file cannot be created,
server startup fails before binding. Existing `--workspace` server mode keeps
the current KEK requirement and does not generate a data-root key.

## Data root and workspace creation

The browser submits only a workspace name, administrator username, password, and password confirmation. It never supplies a filesystem path.

The server validates the workspace name with the existing NRM name rules and derives exactly:

```text
<data-dir>/workspaces/<workspace-name>
```

The target must not exist. The server rejects traversal, symlink traversal, collisions, and non-empty existing targets. Data-root directories use `0700` and bootstrap-created sensitive files retain the existing auth store's `0600` mode.

The created workspace contains:

```yaml
apiVersion: nodr/v1alpha1
kind: Workspace
metadata:
  name: <workspace-name>
spec: {}
```

and an `intent/` directory. The manifest is validated through the normal workspace/NRM loader before completion.

## Git ledger

Workspace creation initializes Git and creates one initial commit containing `nodr.yaml` and `intent/`.

There is no current repository initializer. Add one focused internal service that runs the host `git` executable with fixed arguments and a controlled working directory:

```text
git init
git add nodr.yaml intent
git commit -m "chore: initialize nodr workspace"
```

The service reports a clear startup/setup error when `git` is unavailable. There is no non-Git fallback: Git is nodr's ledger, so a workspace without the initial commit is not a successful bootstrap.

## Admin authentication and normal-mode cutover

The bootstrap service must not call `authn.Store.CreateAccount`, because that
API replaces an existing account and invalidates sessions. After the workspace
has a valid manifest and initial Git commit, the transaction calls the existing
create-once `authn.Store.Setup` path with the already verified bootstrap token.
That path retains its existing Argon2id hashing, token consumption, session
issuance, CSRF generation, and secure session cookie behavior.

Bootstrap and unattended setup call the exact same internal transaction/service.
The UI only transports browser inputs; it never writes YAML, Git state, or auth
state itself.

The HTTP handler owns a mode switch guarded by synchronization. It starts with
bootstrap routes only, then atomically swaps to the ordinary workspace API/web
handler on successful setup without closing or restarting the listener.
Bootstrap routes are absent after completion.

For existing `--workspace` deployments, `NODR_BOOTSTRAP_TOKEN` semantics remain
unchanged: its configured token initializes the current first-admin setup flow;
an absent token continues to fail before serving an account-less workspace.

## Atomicity and recovery

Before any mutation, validate every browser/environment field and verify the
target is absent. The bootstrap auth database is always located at
`<attempt-workspace>/.nodr/authn.db`; it is part of the attempt-created
workspace, not a data-root database.

For a new target, bootstrap tracks whether it created the workspace directory,
auth database, and completion-state temporary file. If manifest validation,
Git initialization, Git add/commit, admin creation, session creation, or
completion-state persistence fails, it:

1. closes the attempt auth and secrets stores before cleanup;
2. removes only the newly created workspace directory, which removes that
   attempt's auth database, account, sessions, bootstrap-token hash, and token
   consumption state together;
3. removes temporary completion state;
4. leaves any generated `<data-dir>/kek` durable and reuses it on every retry
   and restart; bootstrap never deletes or replaces it during rollback;
5. leaves any pre-existing target and configured key material untouched; and
6. keeps browser bootstrap available for retry, or exits with a non-zero error
   in unattended mode.
Completion state is atomically renamed only after all preceding operations
succeed. A crash before that rename leaves an incomplete target. On the next
start, the absence of `bootstrap.json` plus any directory under
`<data-dir>/workspaces` is a startup error naming that incomplete directory;
nodr never silently adopts, overwrites, or removes it. The operator must
inspect/remove it before retrying.

## Unattended environment contract

The complete group is:

```text
NODR_BOOTSTRAP_WORKSPACE_NAME
NODR_BOOTSTRAP_ADMIN_USERNAME
NODR_BOOTSTRAP_ADMIN_PASSWORD
NODR_BOOTSTRAP_TOKEN
```

All four are required together. Unattended bootstrap passes its configured
token to the existing `authn.Store.Setup` transaction, so it has the same
one-time authorization and account/session semantics as browser bootstrap.
`NODR_KEK` or the existing `--kek-file` mechanism remains required when the
normal secrets store is opened; existing `--kek-file` takes precedence over
`NODR_KEK` under its current `secrets.LoadKEK` contract.

CLI flags retain precedence for server process configuration:

- `--workspace` disables bootstrap workspace selection.
- `--data-dir` overrides the data-root default.
- `--addr` overrides its default listener address.
- `--kek-file` retains its existing precedence over `NODR_KEK`.

Passwords and tokens must be redacted from all errors, logs, diagnostics, responses, and tests. Browser bootstrap receives its secret in a request body; unattended operators should use platform secret injection rather than literal command-line values.

## Browser flow

1. The setup page asks for the bootstrap token, workspace name, admin username, password, and confirmation.
2. It submits one POST to the bootstrap endpoint and renders RFC 9457 field errors.
3. Success sets the normal session cookie and reloads the normal application.
4. The normal application detects no connected cluster and opens the existing cluster-connect wizard.

## Tests

- CLI/server tests cover mode selection, `--workspace` bypass, data-root derivation, all-or-none environment validation, generated versus environment token lifecycle, and non-loopback warning.
- HTTP tests cover token failure, one-time use, no token in URL/response, field validation, session creation, route removal after completion, and atomic handler cutover.
- Bootstrap service tests cover validated starter manifest, Git command sequence, missing Git, existing/colliding targets, traversal/symlink rejection, create-once admin behavior, rollback at every mutation boundary, and unattended/browser use of the same transaction.
- UI build/manual smoke coverage verifies setup form errors, successful cutover, and handoff to cluster connection.
