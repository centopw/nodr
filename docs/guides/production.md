# Production guide

How to run nodr as a self-hosted web control plane on a Linux server or in
containers. All paths share the same runtime contract:

- nodr runs as a single binary: `nodr server -w <workspace> --addr <host:port>`.
- `-w` points at an **existing, valid Git workspace** (`nodr.yaml`, `intent/`,
  `terraform/`). nodr never creates, clones, or initializes a workspace.
- Runtime state lives inside the workspace: `<workspace>/.nodr/authn.db`
  (accounts and sessions) and `<workspace>/.nodr/secrets.db` (encrypted
  credentials).
- `NODR_KEK` (base64 32-byte key encryption key) is required whenever the
  secret store is opened.
- The server speaks plain HTTP. Bind loopback (systemd, Compose) or front the
  pod with an Ingress/reverse proxy that terminates TLS (Helm).

Release archives cover Linux on amd64 and arm64 only. Releases are produced
by CI on `v*.*.*` tags, which rebuilds the embedded web UI before archiving.

## 1. Install the binary

Use the installer to install the latest stable release (it verifies the
archive SHA-256 against `checksums.txt` and installs only the binary):

```console
$ curl -fsSL https://raw.githubusercontent.com/centopw/nodr/main/scripts/install.sh | sh
```

To pin an existing release tag instead:

```console
$ curl -fsSL https://raw.githubusercontent.com/centopw/nodr/main/scripts/install.sh | sh -s -- --version vX.Y.Z
```

Options: `--arch amd64|arm64` (default: machine), `--prefix DIR`
(default `/usr/local`), `--force` to overwrite, `--token TOKEN` for private
repository downloads. The installer never creates workspaces, secrets, or
services.

## 2. Prepare a workspace

Provision a Git workspace on the host before starting the server — for
example clone your infrastructure repository into `/srv/nodr/workspace` and
validate it:

```console
$ git clone git@github.com:you/infra.git /srv/nodr/workspace
$ nodr validate -w /srv/nodr/workspace
```

Backups must cover the Git history **and** the `.nodr/` databases. Losing
Git loses desired state; losing `secrets.db` without the KEK loses stored
credentials permanently.

## 3. systemd

`deploy/systemd/nodr.service` runs nodr as the unprivileged `nodr` user with
`ProtectSystem=strict`; the workspace is the only writable path. Copy
`deploy/systemd/nodr.env.example` to `/etc/nodr/nodr.env`, make it
root-owned and mode `0600`, and fill in at least `NODR_WORKSPACE` and
`NODR_KEK`:

```console
$ install -d -m 755 /etc/nodr
$ install -o root -g root -m 0600 deploy/systemd/nodr.env.example /etc/nodr/nodr.env
$ $EDITOR /etc/nodr/nodr.env
$ systemctl daemon-reload
$ systemctl enable --now nodr
```

The unit expects the binary at `/usr/local/bin/nodr` (installer default).
Set `NODR_BOOTSTRAP_TOKEN` in the env file for the first start only, then
remove it and restart once the administrator exists. TLS terminates at your
reverse proxy (`NODR_ADDR` stays `127.0.0.1:8080`).

## 4. Docker

The image contains only the nodr binary with the embedded dashboard; the
workspace is mounted at runtime:

```console
$ docker run --rm -p 127.0.0.1:8080:8080 \
    -v /srv/nodr/workspace:/workspace \
    -e NODR_KEK="$(openssl rand -base64 32)" \
    ghcr.io/centopw/nodr:latest
```

The container entrypoint is `nodr server -w /workspace --addr 0.0.0.0:8080`.
If the mounted directory is not a valid workspace, the server exits with the
validation diagnostic instead of creating one.

## 5. Docker Compose

`deploy/compose/docker-compose.yml` mounts the workspace and reads
`deploy/compose/nodr.env` (copy `nodr.env.example`, keep it out of Git):

```console
$ cp deploy/compose/nodr.env.example deploy/compose/nodr.env
$ $EDITOR deploy/compose/nodr.env       # NODR_WORKSPACE, NODR_KEK
$ docker compose -f deploy/compose/docker-compose.yml up -d
```

The published port is `127.0.0.1:8080`; terminate TLS in a reverse proxy.

## 6. Helm

`deploy/helm/nodr` deploys one `Recreate` replica with the workspace on a
PVC. Provision the PVC contents (a complete workspace) before the pod
starts — for example from a small init job or a hostPath on single-node
clusters:

```console
$ helm install nodr deploy/helm/nodr \
    --set secret.existingSecret=nodr-secrets \
    --set workspace.existingClaim=nodr-workspace
```

- Secrets must provide `nodr-kek` (and optionally `nodr-bootstrap-token`).
- `service.type=ClusterIP` by default; enable `ingress.enabled` with TLS to
  expose the dashboard.
- The workspace PVC is `ReadWriteOnce` and the deployment uses
  `Recreate` — do not scale beyond one replica.

## 7. Environment reference

| Variable | Required | Meaning |
| --- | --- | --- |
| `NODR_KEK` | yes | Base64-encoded 32-byte KEK unlocking `<workspace>/.nodr/secrets.db`. `--kek-file` is the file alternative. |
| `NODR_BOOTSTRAP_TOKEN` | first start | One-time token that arms browser first-run setup. Remove after bootstrap. |
| `NODR_WORKSPACE` | systemd docs | Used only by the systemd unit's `ExecStart` substitution; the binary itself does not read it. Containers use `-w /workspace`. |
| `NODR_TOFU` | plan/apply | Path to the OpenTofu binary (falls back to `PATH`). |

## 8. Upgrades and rollback

- Replace the binary (installer `--force`), pull the newer image tag, or
  `helm upgrade`. Stop the server during binary upgrades so the SQLite
  databases are not open twice.
- Schema migrations for `.nodr` databases run on server start; downgrade is
  not guaranteed. Snapshot the workspace directory (including `.nodr/`)
  before upgrading.
- The KEK must remain available across upgrades; store it in your secret
  manager and in your offline recovery material.

## 9. Not shipped

The following are designed (see `docs/design/13-operations-and-delivery.md`)
but not part of the current single-binary contract: hosted runner service,
GitHub App-managed workspaces, `/readyz`, HA, OIDC, and automated TLS. This
guide covers only the shipped `nodr server` behavior.