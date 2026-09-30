# Local development guide

How to build, run, and test nodr from a checkout. This guide covers the
shipped single-binary control plane: `nodr server` serves the dashboard and
HTTP API from an existing Git workspace.

## Prerequisites

- Go 1.24 or later (`go.mod` pins the toolchain).
- Node.js 20 or later with npm (only needed to rebuild the web UI).
- [OpenTofu](https://opentofu.org/) 1.8 or later on `PATH` (or set
  `NODR_TOFU` to its path) for plan and apply.
- A Git workspace: `nodr.yaml`, YAML intent below `intent/`, generated
  OpenTofu code below `terraform/`. The [homelab example](../../examples/homelab/README.md)
  is a safe reference; its placeholder endpoints must be replaced before
  planning or applying against a real cluster.

## Build

```console
$ make web-ui        # builds web/ into internal/webui/dist
$ make build         # builds bin/nodr
$ bin/nodr version
```

`make build` works without Node.js as long as `internal/webui/dist` exists;
rebuild the UI whenever you change `web/` or the embedded dashboard shows
stale assets.

## Run the server

The server requires a KEK for the encrypted secret store and a workspace
that already validates:

```console
$ install -d -m 700 path/to/workspace/.nodr
$ umask 077; openssl rand -base64 32 > path/to/workspace/.nodr/kek.b64
$ NODR_KEK="$(cat path/to/workspace/.nodr/kek.b64)" \
    NODR_BOOTSTRAP_TOKEN=one-time-setup-token \
    bin/nodr server -w path/to/workspace
```

- `--addr` defaults to `127.0.0.1:8080` (loopback only). Pass a different
  listen address only behind network controls; the server speaks plain HTTP,
  so TLS termination belongs to a reverse proxy.
- `NODR_KEK` (base64) or `--kek-file` supplies the 32-byte key encryption
  key. Keep it durable: losing it makes the stored cluster credentials
  unreadable.
- `NODR_BOOTSTRAP_TOKEN` is consumed once at startup to arm first-run
  browser setup at `/login` (or `/setup` depending on state). Remove it
  from the environment after creating the administrator.
- The server opens `<workspace>/.nodr/authn.db` (accounts and sessions) and
  `<workspace>/.nodr/secrets.db` (encrypted credentials). Both must stay
  out of Git.

## Browser bootstrap

1. Open `http://127.0.0.1:8080`.
2. Complete first-run setup with the bootstrap token, then sign in as the
   administrator.
3. From **Infrastructure**, create VMs or connect clusters; from
   **Changes**, plan and apply explicitly.

The CLI works against the same workspace:

```console
$ bin/nodr validate -w path/to/workspace
$ bin/nodr plan -w path/to/workspace
$ bin/nodr apply -w path/to/workspace
```

Plan and apply need OpenTofu; `apply` asks for confirmation unless
`--auto-approve` is passed, and destructive changes require
`--allow-destroy`.

## Test

```console
$ make test          # unit tests (no network, no Proxmox, no tofu needed)
$ make test-race     # required for concurrency changes
$ make vet
$ make lint          # golangci-lint v2.8.0
$ cd web && npm run build && npm run build-storybook
```

Engine-facing tests use a fake `tofu` script; the real OpenTofu integration
tests run only when `NODR_TEST_TOFU=tofu` (or `1`) selects the binary.
Unit tests must stay offline: no credentials, providers, or network.

## Release packaging

`make snapshot` rebuilds the web UI first and produces Linux amd64/arm64
release archives in `dist/` with `checksums.txt`. `goreleaser check`
validates the release configuration.