# Contributing to nodr

## Setup

- Go 1.24 or later.
- [golangci-lint](https://golangci-lint.run/) v2.8.0 for `make lint` and
  `make fmt`.
- [OpenTofu](https://opentofu.org/) 1.8 or later, optionally, to run
  `nodr plan` and `nodr apply`, the OpenTofu integration tests, and to
  validate the engine code of the examples.
- Node.js 20 or later, optionally, to rebuild the web UI with `make web-ui`.
  `internal/webui/dist` already holds a built copy, so `nodr` builds
  without Node.js; rebuild it only after changing `web/`.

`make help` lists the development tasks: `build`, `web-ui`, `snapshot`,
`test`, `test-race`, `cover`, `vet`, `lint`, `fmt`, `tidy` and `clean`.

## Layout

| Path | Contents |
| ---- | -------- |
| `cmd/nodr` | Entry point of the command line |
| `internal/cli` | Commands of the command line |
| `internal/nrm` | Intent documents: parsing, metadata, schema and reference validation |
| `internal/nrm/v1alpha1` | The `nodr/v1alpha1` kinds: schemas, Go types and semantic checks |
| `internal/workspace` | Loading a workspace from disk |
| `internal/api` | The versioned HTTP API that the web UI and `nodr server` expose |
| `internal/webui` | Embeds and serves the web UI's built output from `internal/webui/dist` |
| `internal/admission` | Allocating UIDs, nodes, VM IDs and addresses, and writing them into intent |
| `internal/yamledit` | Minimal edits of YAML files that keep comments and formatting |
| `internal/resolve` | Resolving references between resources, in both directions |
| `internal/lens` | Field ownership reports |
| `internal/lens/hclmap` | The HCL engine: render, lift and put for Go structs |
| `internal/lens/proxmoxvm` | The VM lens for the `bpg/proxmox` provider |
| `internal/compile` | Compiling intent into the OpenTofu state units below `terraform/` |
| `internal/engine/opentofu` | Running OpenTofu on a state unit: init, saved plans and their changes, apply |
| `internal/quantity`, `internal/diag`, `internal/buildinfo` | Byte quantities, diagnostics, version information |
| `docs/` | Technical design and architecture decisions |
| `examples/` | Example workspaces |
| `web/` | The React and Vite web UI; `make web-ui` builds it into `internal/webui/dist` |
| `scripts/` | Release scripts and their tests |

## Tests

- `make test` runs all tests, and CI also runs them with the race detector.
- Property-based tests with [rapid](https://pkg.go.dev/pgregory.net/rapid)
  check the lens laws. Run more cases before changing a lens, for example
  `go test ./internal/lens/... -run TestLaw -rapid.checks=10000`. A failing
  case is saved below `testdata/rapid/`, and the test prints the flag that
  replays it.
- `examples/examples_test.go` checks that each example workspace is valid and
  that its code and intent agree. If a change alters rendered code, update
  the example's `terraform/` directory to match.
- The integration tests of `internal/engine/opentofu` run a real OpenTofu
  when `tofu` is on `PATH` or `NODR_TEST_TOFU` names the binary, and are
  skipped otherwise. They need no network, and CI runs them with OpenTofu
  1.11.4. The tests of `nodr plan` and `nodr apply` need no OpenTofu: they
  use a fake `tofu`, a shell script that records its calls.

## Git conventions

- **Branches** are named `<type>/<description>`, with a type from the list
  below and a short kebab-case description, for example
  `feat/intent-model-and-vm-lens` or `docs/technical-design`.
- **Commit messages** follow [Conventional Commits](https://www.conventionalcommits.org/):
  `<type>(<scope>): <summary>`. The type is one of `feat`, `fix`, `docs`,
  `refactor`, `test`, `build`, `ci` or `chore`. The scope names the package
  or area, such as `nrm`, `lens`, `cli`, `design` or `adr`, and is optional.
  The summary is imperative, lowercase and without a final period, and the
  whole line has at most 72 characters. The body explains what changed and
  why, wrapped at 72 columns.
- Each commit is one logical change that builds and passes the tests. Commit
  messages describe the change and nothing else.
- Pull requests need a green CI: tests on Go 1.24 and the latest Go,
  including the OpenTofu integration tests, lint, a check of `go.sum`
  against the checksum database, OpenTofu validation of the example code,
  and a check of the release configuration.

## Changelog

- **Every user-facing change** adds a line under `## [Unreleased]` in
  [CHANGELOG.md](CHANGELOG.md), in the same pull request.
- **A release** moves those lines to a `## [x.y.z] - YYYY-MM-DD` section
  and is tagged `vx.y.z`, as described in [Releasing](#releasing).
- **Versions** follow [Semantic Versioning](https://semver.org/), starting
  at `0.x` until the API is stable.

## Releasing

1. In a pull request, move the entries under `## [Unreleased]` in
   [CHANGELOG.md](CHANGELOG.md) to a new `## [x.y.z] - YYYY-MM-DD` section
   below it. `scripts/changelog-notes.sh x.y.z` prints the notes that the
   release will have.
2. After the merge, tag the merge commit on `main` with an annotated tag,
   and push the tag:

   ```console
   $ git switch main
   $ git pull
   $ git tag -a vx.y.z -m "nodr x.y.z"
   $ git push origin vx.y.z
   ```

3. The [release workflow](.github/workflows/release.yml) builds `nodr` for
   Linux, macOS and Windows on amd64 and arm64, adds `checksums.txt`, and
   publishes the GitHub Release with the notes of the version from the
   changelog. It also adds build provenance attestations when the
   repository is public; GitHub does not store them for private
   repositories. If the changelog has no
   section for the version, it fails and publishes nothing. A tag with a
   pre-release suffix, such as `v0.2.0-rc.1`, needs a section of its own
   and publishes a pre-release.

The build is configured in [.goreleaser.yaml](.goreleaser.yaml). On every
pull request, CI checks the configuration, builds a snapshot and runs
`scripts/changelog-notes_test.sh`, which tests the extraction of the
release notes. `make snapshot` builds the same archives locally in
`dist/`, without publishing anything; it needs
[GoReleaser](https://goreleaser.com/) v2.
