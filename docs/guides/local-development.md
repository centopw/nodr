# Local development guide

How to build, run, and test nodr from a checkout. This guide covers the
shipped single-binary control plane: `nodr server` serves the dashboard and
HTTP API from a Git workspace.

## Prerequisites

- Go 1.24 or later (`go.mod` pins the toolchain).
- Git on `PATH`. A blank-host start runs `git init` and makes the workspace's
  initial commit with it.
- Node.js 20 or later with npm (only needed to rebuild the web UI).
- [OpenTofu](https://opentofu.org/) 1.8 or later on `PATH` (or set
  `NODR_TOFU` to its path) for plan and apply.
- For `-w` mode only: a Git workspace (`nodr.yaml`, YAML intent below
  `intent/`, generated OpenTofu code below `terraform/`). The
  [homelab example](../../examples/homelab/README.md) is a safe reference; its
  placeholder endpoints must be replaced before planning or applying against a
  real cluster.

## Build

```sh
make web-ui        # builds web/ into internal/webui/dist
make build         # builds bin/nodr
bin/nodr version
```

`make build` works without Node.js as long as `internal/webui/dist` exists;
rebuild the UI whenever you change `web/` or the embedded dashboard shows
stale assets.

## Run the server

The simplest local run uses a blank data directory. nodr creates the workspace,
the initial Git commit, and the generated key-encryption key itself:

```sh
bin/nodr server --data-dir path/to/nodr-data
```

- The server prints a generated bootstrap token once, with a warning. Use it in
  the browser form at `http://127.0.0.1:8080` to create the workspace and the
  administrator. Restarting before setup completes prints a new token.
- `--data-dir` defaults to `$XDG_STATE_HOME/nodr`, or `$HOME/.local/state/nodr`
  when `XDG_STATE_HOME` is unset. It holds `bootstrap.json`, the created
  workspace under `workspaces/`, and the generated key at `<data-dir>/kek` when
  no KEK is configured.
- Back up `<data-dir>/kek` before moving or restoring the data directory.
  Losing it makes stored cluster credentials unreadable.
- `NODR_KEK` (base64) or `--kek-file` supplies a configured KEK instead, and
  nodr then does not generate one. `--kek-file` takes precedence over
  `NODR_KEK`.
- `--addr` defaults to `127.0.0.1:8080` (loopback only). A non-loopback address
  prints a warning. The server speaks plain HTTP, so TLS belongs to a reverse
  proxy.
- For unattended setup, set all four `NODR_BOOTSTRAP_WORKSPACE_NAME`,
  `NODR_BOOTSTRAP_ADMIN_USERNAME`, `NODR_BOOTSTRAP_ADMIN_PASSWORD`, and
  `NODR_BOOTSTRAP_TOKEN` together. Setting only some of them makes the server
  exit before it listens.

To serve an existing workspace instead, pass `-w`. The workspace must already
validate and requires a configured KEK:

```sh
install -d -m 700 path/to/workspace/.nodr
umask 077; openssl rand -base64 32 > path/to/workspace/.nodr/kek.b64
NODR_KEK="$(cat path/to/workspace/.nodr/kek.b64)" \
    NODR_BOOTSTRAP_TOKEN=one-time-setup-token \
    bin/nodr server -w path/to/workspace
```

In this legacy mode `NODR_BOOTSTRAP_TOKEN` initializes the first administrator
and is consumed at setup. Remove it afterwards. `-w` never enters blank-host
bootstrap and ignores `--data-dir`.

- The server opens `<workspace>/.nodr/authn.db` (accounts and sessions) and
  `<workspace>/.nodr/secrets.db` (encrypted credentials). Both must stay
  out of Git.

## Browser bootstrap

1. Open `http://127.0.0.1:8080` and paste the generated token printed at startup.
2. Enter a workspace name, the administrator username and password, then submit.
   nodr creates the workspace, its initial Git commit, and the administrator,
   then signs you in.
3. From **Infrastructure**, create VMs or connect clusters; from **Changes**,
   plan and apply explicitly.

## Advanced local tooling

The browser control plane and HTTP API are the primary shipped interfaces.
These CLI commands remain available for local development and operator
workflows against the same workspace; they are not required for browser
administration.

```sh
bin/nodr validate -w path/to/workspace
bin/nodr plan -w path/to/workspace
bin/nodr apply -w path/to/workspace
```

Plan and apply need OpenTofu; `apply` asks for confirmation unless
`--auto-approve` is passed, and destructive changes require
`--allow-destroy`.

## Test

```sh
make test          # unit tests (no network, no Proxmox, no tofu needed)
make test-race     # required for concurrency changes
make vet
make lint          # golangci-lint v2.8.0
cd web && npm run build && npm run build-storybook
```

Engine-facing tests use a fake `tofu` script; the real OpenTofu integration
tests run only when `NODR_TEST_TOFU=tofu` (or `1`) selects the binary.
Unit tests must stay offline: no credentials, providers, or network.

## Release packaging

`make snapshot` rebuilds the web UI first and produces Linux amd64/arm64
release archives in `dist/` with `checksums.txt`. `goreleaser check`
validates the release configuration.
