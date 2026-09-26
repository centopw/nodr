# Slice A: Proxmox API client and secrets store

> First of four sequenced slices toward real-Proxmox-node adoption (§6.3 of
> `docs/design/06-proxmox.md`). Order: **A (this doc)** → B (cluster-connect
> wizard) → C (discovery) → D (adoption). Each slice ships independently and
> gets its own design doc, plan and PR.

## Goal

Two standalone, well-tested Go packages with no user-visible surface yet:

- `internal/proxmox`: a thin native REST client for the Proxmox VE API,
  covering exactly the endpoints slices A–D need.
- `internal/secrets`: an envelope-encrypted, file-backed secret store that
  resolves `credentialsRef`/`secretRef` names (§10.5, §3.3) to values.

Neither package is wired into `cmd/nodr`, `internal/cli` or `internal/api` in
this slice. Verification is unit and integration tests only.

## Non-goals (deferred to later slices or out of scope entirely)

- Cluster-connect UI, bootstrap automation, TLS fingerprint confirmation UX
  (slice B).
- Discovery listing, adoption, import-block generation (slices C, D).
- KEK sourcing from TPM, passphrase-stretch or external KMS (§10.5 lists these;
  only file and env var are built here).
- Secret rotation workflows.
- Multi-workspace/global secret stores — one store per workspace, matching the
  existing `nodr.yaml`-scoped model.
- **Wiring `secrets.Store.Resolve` output into `opentofu.Runner.Env`.**
  `internal/planapply` constructs `opentofu.Runner{Binary, Dir, Stdout}` today
  with no `Env` at all (confirmed: `planapply.go:168-172`), so
  `ProxmoxCluster.spec.credentialsRef` is declared in intent but never
  resolved or turned into `PROXMOX_VE_API_TOKEN` for any workspace, adopted or
  not. This slice builds `Resolve()` but does not call it from
  `planapply` — that wiring lands in **slice B**, the first slice where a real
  API token exists to inject. Until then, real `tofu plan`/`apply` against a
  live cluster still requires the operator to export
  `PROXMOX_VE_API_TOKEN` manually, exactly as today.

## `internal/proxmox`

### Client and auth

```go
type Client struct {
    Endpoint   string        // "https://10.0.10.11:8006"
    HTTPClient *http.Client  // TLS config, including pinned fingerprint verification
    // auth state: either a ticket+CSRF pair or an API token
}

func NewClient(endpoint string, httpClient *http.Client) *Client

// Login performs ticket authentication (POST /api2/json/access/ticket).
// Bootstrap-only: the caller discards the ticket after creating a
// dedicated user and API token (slice B). Not used for steady-state calls.
func (c *Client) Login(ctx context.Context, realm, username, password string) error

// SetAPIToken configures steady-state auth: all requests carry
// "Authorization: PVEAPIToken=<user>!<tokenID>=<secret>". No CSRF token
// needed for token auth.
func (c *Client) SetAPIToken(user, tokenID, secret string)
```

Internal request helper decodes Proxmox's `{"data": ...}` envelope and maps
non-2xx responses to:

```go
type APIError struct {
    Status  int
    Message string
}
func (e *APIError) Error() string
```

### Transport hardening

- Every request takes a `context.Context`; the client never starts a request
  without one and never applies its own hidden default timeout — the caller
  (slice B/C/D code, or a test) controls cancellation/timeout via `ctx`.
- `APIError.Error()` and any wrapped/logged error never includes the request's
  `Authorization` header value, the ticket, the CSRF token, or an API token
  secret — these are redacted (e.g. `PVEAPIToken=<redacted>`) before an error
  is formatted or logged. Applies to both auth modes.
- TLS fingerprint mismatch (checked once pinning is wired up, starting slice
  B) is a distinct typed error, `ErrFingerprintMismatch`, never surfaced as a
  generic `*APIError` or bare `x509` error — callers must be able to
  distinguish "wrong/rotated certificate" from "ordinary HTTP failure" to
  drive the correct UX (re-confirm vs. retry).

### Endpoints (typed methods, one per REST call)

| Method | Proxmox route | Used by |
| ------ | -------------- | ------- |
| `GetVersion` | `GET /version` | B (connectivity check) |
| `GetClusterStatus` | `GET /cluster/status` | B, C (topology, failover) |
| `GetClusterResources(kind string)` | `GET /cluster/resources?type=vm` | C (discovery) |
| `GetNodeQEMU(node string)` | `GET /nodes/{node}/qemu` | C |
| `GetQEMUConfig(node string, vmid int)` | `GET /nodes/{node}/qemu/{vmid}/config` | C, D (config + `digest` for adoption) |
| `GetCertFingerprint(node string)` | `GET /nodes/{node}/certificates/info` | B (pin-and-confirm) |
| `CreateUser(userid string, opts UserOptions)` | `POST /access/users` | B (bootstrap) |
| `CreateRole(roleid string, privileges []string)` | `POST /access/roles` | B |
| `UpdateACL(path string, roles map[string][]string)` | `PUT /access/acl` | B |
| `CreateAPIToken(userid, tokenID string) (secret string)` | `POST /access/users/{userid}/token/{tokenid}` | B |

Response types are minimal structs covering only the fields nodr reads (no
attempt at full Proxmox schema coverage).

### `internal/proxmox/proxmoxtest`

Exported fake server for this package's own tests and for slices C/D's
discovery/adoption tests:

```go
func NewServer(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server
```

Keyed by method+path (e.g. `"GET /api2/json/cluster/status"`); a handler
missing from the map fails the test with a clear "unexpected request" message
rather than a generic 404.

## `internal/secrets`

### KEK sourcing

```go
type KEKConfig struct {
    FilePath string // raw 32 bytes on disk
    EnvVar   string // base64-standard-encoded 32 bytes
}

// LoadKEK tries FilePath first if set, else EnvVar if set, else returns an
// error. The result is always exactly 32 bytes (AES-256); a wrong length is
// a hard error, never truncated or padded. If FilePath is used, its mode
// must not grant group or other read/write/execute (mode & 0077 != 0 is a
// fatal error) — matches "protected by file permissions" (§10.5).
func LoadKEK(cfg KEKConfig) ([]byte, error)
```

### Store

SQLite via `modernc.org/sqlite` (pure Go, no cgo — compiles under the
existing cross-compilation pipeline). File at `<workspace>/.nodr/secrets.db`,
created with mode `0600`. `.nodr/` is added to `.gitignore`: secrets never
enter Git (§3.3).

Schema:

```sql
CREATE TABLE secrets (
    name             TEXT PRIMARY KEY,  -- e.g. "proxmox/pve-main-token"
    wrapped_key      BLOB NOT NULL,     -- 32-byte data key, AES-256-GCM sealed under the KEK
    wrapped_nonce    BLOB NOT NULL,     -- 12-byte nonce for the KEK seal, independent of ciphertext_nonce
    ciphertext       BLOB NOT NULL,     -- value, AES-256-GCM sealed under the data key
    ciphertext_nonce BLOB NOT NULL      -- 12-byte nonce for the value seal
);
```

Each `Put` generates a fresh random 32-byte data key and two independent
random 12-byte nonces (`crypto/rand`) — one nonce is never reused for both
seal operations, and never reused across rows.

```go
type Store struct { /* db *sql.DB, kek []byte */ }

func Open(dbPath string, kek []byte) (*Store, error)
func (s *Store) Put(ctx context.Context, name string, value []byte) error
func (s *Store) Resolve(ctx context.Context, ref string) ([]byte, error)
func (s *Store) Delete(ctx context.Context, name string) error
func (s *Store) List(ctx context.Context) ([]string, error) // names only, never values
func (s *Store) Close() error
```

`Resolve` returns a typed `ErrNotFound` when `ref` has no matching row, and a
distinct `ErrDecrypt` when GCM authentication fails (wrong KEK, or tampered
row) — callers must not conflate "missing" with "corrupted/wrong key".

## Testing

- `internal/proxmox`: unit tests against `proxmoxtest.NewServer` — auth
  header/cookie attachment for both auth modes, `APIError` mapping for 4xx/5xx,
  request shape and response decoding for every endpoint above.
- `internal/secrets`: `Put`→`Resolve` round-trip; wrong-KEK produces
  `ErrDecrypt`; `LoadKEK` precedence (file over env) and length validation;
  KEK file permission rejection; concurrent `Put`/`Resolve` (SQLite's own
  locking is sufficient at this scale — no additional application-level
  locking).
- No test contacts a real Proxmox instance in this slice (nothing to adopt
  yet). Follows the existing `internal/engine/opentofu` convention of
  runtime-skip via env var rather than build tags, if a later slice adds a
  real-server-optional test.

## Dependencies added

- `modernc.org/sqlite` (pure Go SQLite driver).

## Open risks

- Proxmox API response shapes are inferred from public documentation, not
  verified against a real cluster in this slice (no real node access yet).
  Slice B's first real connection is the actual contract test; field
  mismatches found there get fixed as small follow-ups to this package.
