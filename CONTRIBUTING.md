# Contributing to nodr

## Setup

- Go 1.24 or later.
- [golangci-lint](https://golangci-lint.run/) v2.8.0 for `make lint` and
  `make fmt`.
- [OpenTofu](https://opentofu.org/) 1.8 or later, optionally, to validate the
  engine code of the examples.

`make help` lists the development tasks: `build`, `test`, `test-race`,
`cover`, `vet`, `lint`, `fmt`, `tidy` and `clean`.

## Layout

| Path | Contents |
| ---- | -------- |
| `cmd/nodr` | Entry point of the command line |
| `internal/cli` | Commands of the command line |
| `internal/nrm` | Intent documents: parsing, metadata, schema and reference validation |
| `internal/nrm/v1alpha1` | The `nodr/v1alpha1` kinds: schemas, Go types and semantic checks |
| `internal/workspace` | Loading a workspace from disk |
| `internal/resolve` | Resolving references between resources, in both directions |
| `internal/lens` | Field ownership reports |
| `internal/lens/hclmap` | The HCL engine: render, lift and put for Go structs |
| `internal/lens/proxmoxvm` | The VM lens for the `bpg/proxmox` provider |
| `internal/quantity`, `internal/diag`, `internal/buildinfo` | Byte quantities, diagnostics, version information |
| `docs/` | Technical design and architecture decisions |
| `examples/` | Example workspaces |

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
- Pull requests need a green CI: tests on Go 1.24 and the latest Go, lint, a
  check of `go.sum` against the checksum database, and OpenTofu validation of
  the example code.
