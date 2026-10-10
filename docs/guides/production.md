# Production guide

How to run nodr as a self-hosted web control plane on a Linux server or in
containers. All paths share the same runtime contract:

- nodr runs as a single binary. A blank host starts with
  `nodr server --data-dir <dir> --addr <host:port>`; an existing workspace
  starts with `nodr server -w <workspace> --addr <host:port>`.
- On a blank host, first start creates the workspace under
  `<data-dir>/workspaces/<name>`, initializes a Git repository there, and makes
  one initial commit containing `nodr.yaml` and `intent/`. The host `git`
  executable must be installed.
- `-w`/`--workspace` selects an **existing, valid Git workspace** and never
  creates one. Without `-w`, nodr serves the workspace recorded in
  `<data-dir>/bootstrap.json` once setup has completed.
- Runtime state lives inside the workspace: `<workspace>/.nodr/authn.db`
  (accounts and sessions) and `<workspace>/.nodr/secrets.db` (encrypted
  credentials).
- The key encryption key (KEK) unlocks `secrets.db`. A configured KEK is taken
  from `--kek-file`, else `NODR_KEK` (base64 32-byte key). When neither is set,
  a blank-host server generates one at `<data-dir>/kek`. The `-w` workspace
  mode still requires a configured KEK.
- The server speaks plain HTTP. Bind loopback (systemd, Compose) or front the
  pod with an Ingress/reverse proxy that terminates TLS (Helm).

Release archives cover Linux on amd64 and arm64 only. Releases are produced
by CI on `v*.*.*` tags, which rebuilds the embedded web UI before archiving.

## 1. Install the binary

Install the latest stable release as root (the installer verifies the archive
SHA-256 against `checksums.txt` and installs only the binary):

```sh
curl -fsSL https://raw.githubusercontent.com/centopw/nodr/main/scripts/install.sh | sudo sh
```

`sudo` must wrap `sh` — `sudo curl ... | sh` downloads as root but still runs
the installer as your user, which cannot write `/usr/local/bin`.

Without root, install into your own `PATH`:

```sh
curl -fsSL https://raw.githubusercontent.com/centopw/nodr/main/scripts/install.sh | sh -s -- --prefix "$HOME/.local"
```

To pin an existing release tag instead, add `--version vX.Y.Z`:

```sh
curl -fsSL https://raw.githubusercontent.com/centopw/nodr/main/scripts/install.sh | sudo sh -s -- --version vX.Y.Z
```

Options: `--arch amd64|arm64` (default: machine), `--prefix DIR`
(default `/usr/local`), `--force` to overwrite, `--token TOKEN` for private
repository downloads. The installer never creates workspaces, secrets, or
services.

## 2. Start on a blank host (browser setup)

Start the server with a persistent data directory and no workspace:

```sh
nodr server --data-dir /srv/nodr/data --addr 127.0.0.1:8080
```

1. The server prints a generated bootstrap token once to stdout, with a
   warning. Copy it from that output. Anyone holding it can create the
   administrator, so treat it as a secret.
2. Open `http://127.0.0.1:8080` in a browser.
3. Enter the bootstrap token, a workspace name (lowercase letters, digits and
   hyphens), an administrator username, and a password. nodr creates the
   workspace, makes its initial Git commit, creates the administrator, and
   signs you in. The browser then opens the cluster-connection wizard.

`--data-dir` defaults to `$XDG_STATE_HOME/nodr`, or `$HOME/.local/state/nodr`
when `XDG_STATE_HOME` is unset. It is the server's own state, and it holds:

- `bootstrap.json`: records the completed workspace (no secrets).
- `workspaces/<name>/`: the workspace created by setup.
- `kek`: the generated key-encryption key, only when no KEK is configured.
  Created with mode `0600` and reused on every restart.

Browser bootstrap is a one-time operation. Once it succeeds, the bootstrap
routes are removed and later starts serve the recorded workspace.
The token is regenerated on each start until setup completes.

To serve an existing workspace instead, pass `-w`:

```sh
nodr server -w /srv/nodr/workspace --addr 127.0.0.1:8080
```

`--workspace` keeps its legacy behavior: it loads that workspace, ignores
`--data-dir` for workspace selection, never enters blank-host bootstrap, and
requires a configured KEK. The first administrator is created with
`NODR_BOOTSTRAP_TOKEN` as before; if it is unset, the server refuses to serve
an account-less workspace.

## 3. Unattended bootstrap

For Docker, Compose, Kubernetes and systemd, a blank host can be set up
without a browser by supplying the four bootstrap variables together:

| Variable | Meaning |
| --- | --- |
| `NODR_BOOTSTRAP_WORKSPACE_NAME` | Name of the workspace to create. |
| `NODR_BOOTSTRAP_ADMIN_USERNAME` | Username of the first administrator. |
| `NODR_BOOTSTRAP_ADMIN_PASSWORD` | Password of the first administrator. |
| `NODR_BOOTSTRAP_TOKEN` | Authorizes the setup call. |

The group is all-or-none. If all four are set, the server creates the
workspace, creates the administrator, and then serves the new workspace
without any browser step. If only some are set, the server exits with the
missing names before it listens. Browser values are never mixed with these
deployment values.

`NODR_BOOTSTRAP_TOKEN` in this group authorizes the same one-time
`authn.Store.Setup` call that browser bootstrap uses. The generated token from
section 2 exists only for interactive browser mode. Inject all four values from
your platform's secret store (systemd `EnvironmentFile` with root-only
permissions, Compose secrets, Kubernetes Secrets). Do not put literal passwords
on a command line or in a committed file.

Unattended setup is complete once the server starts successfully. Restarts
serve the recorded workspace, so the group can stay set, but removing it after
the first successful start is recommended.

## 4. Key encryption key and data directory

Back up the data directory, including `kek` when nodr generated it, before you
move or restore it. Losing `<data-dir>/kek` makes previously encrypted Proxmox
credentials unrecoverable. The generated key is never printed, returned by the
API, or stored in bootstrap state. Restarts reuse the same file; nodr never
silently replaces it.

If you configure a KEK with `--kek-file` or `NODR_KEK`, nodr uses that key and
does not generate one. Keep the configured key in your secret manager.

## 5. Network exposure and TLS

The default listener is `127.0.0.1:8080`. An explicit non-loopback `--addr`
is accepted, but nodr prints a warning that the bootstrap endpoint is
remotely reachable. Browser bootstrap then requires the bootstrap token, which
is sent in the request body. The server does not provide TLS itself, so put a
TLS-terminating reverse proxy in front of any non-loopback listener.

## 6. Prepare an existing workspace (legacy `-w` mode)

Provision a Git workspace on the host before starting the server — for
example clone your infrastructure repository into `/srv/nodr/workspace` and
validate it:

```sh
git clone git@github.com:you/infra.git /srv/nodr/workspace
nodr validate -w /srv/nodr/workspace
```

Backups must cover the Git history **and** the `.nodr/` databases. Losing
Git loses desired state; losing `secrets.db` without the KEK loses stored
credentials permanently.

## 7. systemd

`deploy/systemd/nodr.service` runs nodr as the unprivileged `nodr` user with
`ProtectSystem=strict`. It starts `nodr server --data-dir /var/lib/nodr`, and
systemd creates `/var/lib/nodr` (its `StateDirectory`) owned by that user. That
directory is the only writable path.

Copy `deploy/systemd/nodr.env.example` to `/etc/nodr/nodr.env` and make it
root-owned with mode `0600`. Leave every value empty to set up in the browser,
or set all four `NODR_BOOTSTRAP_*` values together for unattended setup:

```sh
install -d -m 755 /etc/nodr
install -o root -g root -m 0600 deploy/systemd/nodr.env.example /etc/nodr/nodr.env
$EDITOR /etc/nodr/nodr.env
systemctl daemon-reload
systemctl enable --now nodr
```

For browser setup, read the one-time token from the journal:

```sh
journalctl -u nodr | grep "generated bootstrap token"
```

The unit expects the binary at `/usr/local/bin/nodr` (installer default).
TLS terminates at your reverse proxy (`NODR_ADDR` stays `127.0.0.1:8080`).

Ledger mode (optional, section 13) also needs the App private key as a file the `nodr` user can read, for example `/etc/nodr/github-app.pem`, owned by `nodr` with mode `0400`.

## 8. Docker

The image contains the nodr binary with the embedded dashboard and the `git`
executable needed for the bootstrap ledger. It runs
`nodr server --data-dir /var/lib/nodr --addr 0.0.0.0:8080`. Mount a persistent
volume at `/var/lib/nodr`:

```sh
docker run --rm -p 127.0.0.1:8080:8080 \
    -v nodr-data:/var/lib/nodr \
    ghcr.io/centopw/nodr:latest
```

The generated bootstrap token appears in the container log. Read it with
`docker logs <container>`, then complete setup in the browser at
`http://127.0.0.1:8080`. To set up unattended, pass the four
`NODR_BOOTSTRAP_*` variables with `--env-file` or your orchestrator's secret
mechanism instead of `-e` with literal values.

Mount an existing workspace instead by overriding the command:

```sh
docker run --rm -p 127.0.0.1:8080:8080 \
    -v /srv/nodr/workspace:/workspace \
    -e NODR_KEK="$(openssl rand -base64 32)" \
    ghcr.io/centopw/nodr:latest server -w /workspace --addr 0.0.0.0:8080
```

If the mounted `-w` directory is not a valid workspace, the server exits with
the validation diagnostic instead of creating one.

## 9. Docker Compose

`deploy/compose/docker-compose.yml` stores data in the named volume
`nodr-data` mounted at `/var/lib/nodr`. It reads `deploy/compose/nodr.env`
(copy `nodr.env.example`, keep it out of Git). Leave the variables empty for
browser setup, or set all four `NODR_BOOTSTRAP_*` values together for
unattended setup:

```sh
cp deploy/compose/nodr.env.example deploy/compose/nodr.env
$EDITOR deploy/compose/nodr.env
docker compose -f deploy/compose/docker-compose.yml up -d
docker compose -f deploy/compose/docker-compose.yml logs nodr
```

The published port is `127.0.0.1:8080`; terminate TLS in a reverse proxy.

To enable GitHub-backed ledger mode, uncomment the `secrets:` lines in
`docker-compose.yml` (this mounts the App key at
`/run/secrets/github-app-private-key`), set
`NODR_GITHUB_PRIVATE_KEY_FILE=/run/secrets/github-app-private-key` and the other
three `NODR_GITHUB_*`/`NODR_PUBLIC_URL` values in `nodr.env`, and see section 13.

## 10. Helm

`deploy/helm/nodr` deploys one `Recreate` replica. Its data PVC is mounted at
`/var/lib/nodr`, where nodr creates the workspace on a blank host. By default
the chart creates the PVC (`data.create=true`). To reuse an existing claim, set
`data.existingClaim`:

```sh
helm install nodr deploy/helm/nodr \
    --set secret.existingSecret=nodr-secrets \
    --set data.existingClaim=nodr-data
```

- The Secret may provide `nodr-kek`, plus the optional keys
  `nodr-bootstrap-workspace-name`, `nodr-bootstrap-admin-username`,
  `nodr-bootstrap-admin-password`, `nodr-bootstrap-token`,
  `nodr-github-private-key`, and `nodr-github-webhook-secret`. Missing keys are
  unset. Supply all four bootstrap keys together or none.
- With no secret values, read the generated token from the pod log
  (`kubectl logs deploy/nodr`) and finish setup in the browser through a port
  forward or Ingress.
- `service.type=ClusterIP` by default; enable `ingress.enabled` with TLS to
  expose the dashboard.
- The data PVC is `ReadWriteOnce` and the deployment uses `Recreate` — do not
  scale beyond one replica.
- GitHub-backed ledger (optional): set `github.appId` and `github.publicUrl`, and provide the App private key (`nodr-github-private-key`) and webhook secret (`nodr-github-webhook-secret`) in `existingSecret`. The chart mounts the key at `/etc/nodr/github/private-key.pem`. See section 13.

## 11. Environment reference

| Variable | Required | Meaning |
| --- | --- | --- |
| `NODR_KEK` | no | Base64-encoded 32-byte KEK. `--kek-file` is the file alternative. When neither is set on a blank host, a KEK is generated at `<data-dir>/kek`. `-w` mode requires one. |
| `NODR_BOOTSTRAP_WORKSPACE_NAME` | all-or-none | Workspace to create on unattended bootstrap. |
| `NODR_BOOTSTRAP_ADMIN_USERNAME` | all-or-none | First administrator username. |
| `NODR_BOOTSTRAP_ADMIN_PASSWORD` | all-or-none | First administrator password. |
| `NODR_BOOTSTRAP_TOKEN` | all-or-none; `-w` first start | Authorizes setup. With `-w`, it initializes the first administrator. Remove after bootstrap. |
| `NODR_GITHUB_APP_ID` | all-or-none (ledger) | GitHub App ID. Enables GitHub-backed ledger mode with the three variables below. See section 13. |
| `NODR_GITHUB_PRIVATE_KEY_FILE` | all-or-none (ledger) | Path to the App's PEM private key file. A mounted file, never the key content. |
| `NODR_GITHUB_WEBHOOK_SECRET` | all-or-none (ledger) | Secret that signs webhook deliveries. |
| `NODR_PUBLIC_URL` | all-or-none (ledger) | Public base URL of this server. The webhook URL is `<NODR_PUBLIC_URL>/api/v1/integrations/github/webhook`. |
| `NODR_TOFU` | plan/apply | Path to the OpenTofu binary (falls back to `PATH`). |

Flags: `--data-dir` (blank-host state directory), `-w`/`--workspace` (existing
workspace, legacy mode), `--addr` (listen address, default `127.0.0.1:8080`),
and `--kek-file` (KEK file, takes precedence over `NODR_KEK`).

## 12. Upgrades and rollback

- Replace the binary (installer `--force`), pull the newer image tag, or
  `helm upgrade`. Stop the server during binary upgrades so the SQLite
  databases are not open twice.
- Schema migrations for `.nodr` databases run on server start; downgrade is
  not guaranteed. Snapshot the data directory (or the workspace directory
  including `.nodr/`) before upgrading.
- The KEK must remain available across upgrades. Store a configured KEK in
  your secret manager. For a generated `<data-dir>/kek`, back up that file
  with the data directory.

## 13. GitHub-backed ledger (optional)

Ledger mode keeps desired state in GitHub repositories and runs plans and
applies from the browser. It is opt-in and applies to `nodr server --data-dir`
(blank-host and bootstrapped deployments); `nodr server -w` ignores these
variables. Without the group, the server keeps the local-workspace behavior
described in sections 2 to 4.

### Variables

Set all four together. If only some are set, the server exits with the missing
names before it listens.

| Variable | Meaning |
| --- | --- |
| `NODR_GITHUB_APP_ID` | Positive integer ID of the GitHub App. |
| `NODR_GITHUB_PRIVATE_KEY_FILE` | Path to the App's PEM private key, read at startup. Mount it as a file (Compose secret, Helm secret volume, or a root-managed file readable by the `nodr` user). Never put the PEM content in an environment variable or a committed file. |
| `NODR_GITHUB_WEBHOOK_SECRET` | Secret GitHub uses to sign webhook deliveries; the same value as in the App's webhook settings. |
| `NODR_PUBLIC_URL` | Absolute `http(s)` base URL of this server as GitHub reaches it, for example `https://nodr.example.com`. |

### Create the GitHub App

The operator creates the App; nodr does not create it. Configure:

1. Repository permissions: Contents read and write, Metadata read-only, Pull requests read and write, Commit statuses read and write.
2. Subscribe to the `push` event.
3. Webhook: active, URL `<NODR_PUBLIC_URL>/api/v1/integrations/github/webhook`, secret equal to `NODR_GITHUB_WEBHOOK_SECRET`, content type `application/json`.
4. Generate a private key, and install the App on each repository you want to connect.

### Use it in the browser

1. Sign in to the dashboard.
2. Under **Repositories**, choose **Connect GitHub repository**: select an installation, then a repository. The repository must contain a `nodr.yaml` with a valid workspace manifest; its `metadata.name` becomes the workspace name.
3. Edit infrastructure as usual. Each command creates a changeset whose draft commit is pushed to `refs/heads/nodr/drafts/<id>` in the repository.
4. Plan the changeset. The plan runs in the background and its status updates from the event stream.
5. Apply the changeset. Apply fast-forwards the default branch to the planned draft, then runs OpenTofu. Destructive plans need explicit approval (`allowDestroy`).

### Health endpoints

`GET /healthz` reports liveness. `GET /readyz` checks the operations database,
the encrypted secrets store, and the repository store; it returns 503 and names
the failed checks. Both endpoints are served without authentication.

### Ledger state and limits

- Ledger state lives under `<workspace>/.nodr/ledger`. For a blank-host setup that is `<data-dir>/workspaces/<name>/.nodr/ledger`: the operations and metadata databases, the bare mirrors, and the plan files. Back it up with the data directory.
- The runner runs in-process: OpenTofu runs on the same host as the server, found through `NODR_TOFU` or `PATH`. The separate runner service is not implemented.
- Saved plans are held in memory and are lost on restart. Applying a plan made before a restart fails with `409 PLAN_CHANGED`; plan again.
- Render-lock synchronization (`render.lock`) is not wired: nodr does not merge concurrent generated-code changes through it.
- One GitHub App per instance: a deployment has one App ID, one private key, and one webhook secret.
- Pull-request and commit-status checks are not implemented, although the App requests those permissions. Reconnecting or disconnecting a repository is not implemented.

## 14. Not shipped

The following are designed (see `docs/design/13-operations-and-delivery.md`)
but not shipped: a separate runner service, HA and multi-instance operation,
OIDC, automated TLS, pull-request and commit-status checks, and GitHub App
creation through the manifest flow.
