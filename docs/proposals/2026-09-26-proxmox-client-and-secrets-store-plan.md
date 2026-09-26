# Slice A: Proxmox API Client and Secrets Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Working Directory:** All work must take place in the isolated git worktree:
> `/Users/cento/git/personal/nodr/.worktrees/slice-a-proxmox-client`
> on branch `feat/proxmox-client-and-secrets-store`.
> Never modify files in the root repository checkout.

**Goal:** Build two standalone, well-tested Go packages: `internal/proxmox` (a thin native REST client for Proxmox VE) and `internal/secrets` (an envelope-encrypted, SQLite-backed secret store).

**Architecture:** `internal/proxmox` wraps `net/http` and `encoding/json` directly with ticket and API-token auth, credential redaction, and typed endpoints. `internal/secrets` implements envelope encryption using pure-Go SQLite (`modernc.org/sqlite`) and AES-256-GCM with independent nonces for key wrapping and ciphertext.

**Tech Stack:** Go 1.24, `modernc.org/sqlite`, `crypto/aes`, `crypto/cipher`, `crypto/rand`, `net/http`, `net/http/httptest`.

---

## File Structure

```
.worktrees/slice-a-proxmox-client/
├── go.mod                                          # add modernc.org/sqlite
├── go.sum
├── internal/
│   ├── proxmox/
│   │   ├── client.go                              # Client, auth (Login, SetAPIToken), do/doJSON/doForm, redaction
│   │   ├── client_test.go                         # Unit tests for client auth, redaction, request building
│   │   ├── errors.go                              # APIError, ErrFingerprintMismatch
│   │   ├── endpoints.go                           # Typed endpoints (GetVersion, GetClusterStatus, etc.)
│   │   ├── endpoints_test.go                      # Unit tests for each endpoint against fake server
│   │   ├── types.go                               # Minimal response types (Version, ClusterNode, QEMUConfig, etc.)
│   │   └── proxmoxtest/
│   │       ├── server.go                          # Fake httptest.Server helper for tests
│   │       └── server_test.go                     # Tests for fake server itself
│   └── secrets/
│       ├── kek.go                                 # KEKConfig, LoadKEK (file + env var, permissions check)
│       ├── kek_test.go                            # Tests for KEK loading, permissions, length validation
│       ├── store.go                               # Store, Open, Put, Resolve, Delete, List, Close
│       ├── store_test.go                          # Tests for round-trip, wrong KEK, concurrent access
│       └── errors.go                              # ErrNotFound, ErrDecrypt, ErrInvalidKEK
```

---

### Task 1: Add SQLite dependency to go.mod

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add modernc.org/sqlite dependency**

Run in worktree:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/slice-a-proxmox-client
go get modernc.org/sqlite@latest
go mod tidy
```

- [ ] **Step 2: Verify dependencies compile**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/slice-a-proxmox-client
go test ./...
```
Expected: PASS (all existing tests pass)

- [ ] **Step 3: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/slice-a-proxmox-client
git add go.mod go.sum
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "build(deps): add modernc.org/sqlite dependency"
```

---

### Task 2: Create `internal/proxmox/proxmoxtest` fake server

**Files:**
- Create: `internal/proxmox/proxmoxtest/server.go`
- Create: `internal/proxmox/proxmoxtest/server_test.go`

- [ ] **Step 1: Write the failing test**

File: `internal/proxmox/proxmoxtest/server_test.go`
```go
package proxmoxtest_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/centopw/nodr/internal/proxmox/proxmoxtest"
)

func TestNewServer_MatchedRoute(t *testing.T) {
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"GET /api2/json/version": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"version":"8.2.4"}`)
		},
	})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api2/json/version")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, _ := io.ReadAll(resp.Body)
	want := `{"data":{"version":"8.2.4"}}`
	if string(body) != want {
		t.Errorf("body = %q, want %q", string(body), want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/proxmox/proxmoxtest/...`
Expected: FAIL (package does not exist)

- [ ] **Step 3: Implement `server.go`**

File: `internal/proxmox/proxmoxtest/server.go`
```go
// Package proxmoxtest provides a fake Proxmox VE HTTP server for tests in
// internal/proxmox and in later discovery/adoption slices.
package proxmoxtest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// NewServer returns an httptest.Server configured with the provided handlers.
// The handlers map keys requests by "METHOD /path", for example:
//
//	"GET /api2/json/version"
//	"POST /api2/json/access/ticket"
//
// Any request that does not match an exact key fails the test via t.Errorf
// and returns HTTP 501.
func NewServer(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := fmt.Sprintf("%s %s", r.Method, r.URL.Path)
		handler, ok := handlers[key]
		if !ok {
			t.Errorf("proxmoxtest: unexpected request %s (registered: %s)", key, strings.Join(keys(handlers), ", "))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"message":"unexpected request: %s"}`, key)))
			return
		}
		handler(w, r)
	}))
}

// JSONResponse writes a standard Proxmox VE {"data": ...} envelope response.
func JSONResponse(w http.ResponseWriter, statusCode int, dataJSON string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if dataJSON == "" {
		_, _ = w.Write([]byte(`{"data":null}`))
		return
	}
	_, _ = w.Write([]byte(fmt.Sprintf(`{"data":%s}`, dataJSON)))
}

// ErrorResponse writes a Proxmox VE error response.
func ErrorResponse(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"message":"%s"}`, message)))
}

func keys(m map[string]http.HandlerFunc) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/proxmox/proxmoxtest/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/slice-a-proxmox-client
git add internal/proxmox/proxmoxtest/
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(proxmox): add proxmoxtest fake HTTP server"
```

---

### Task 3: Create `internal/proxmox` core types and errors

**Files:**
- Create: `internal/proxmox/errors.go`
- Create: `internal/proxmox/types.go`

- [ ] **Step 1: Implement `errors.go`**

File: `internal/proxmox/errors.go`
```go
package proxmox

import (
	"errors"
	"fmt"
)

// ErrFingerprintMismatch reports that a server's TLS certificate does not
// match the pinned fingerprint configured for the connection.
var ErrFingerprintMismatch = errors.New("proxmox: TLS certificate fingerprint does not match pinned value")

// APIError reports a non-2xx response from the Proxmox VE API. It never
// includes credentials in its error message.
type APIError struct {
	Status  int    // HTTP status code (e.g. 401, 403, 500)
	Message string // Redacted error message from the response body
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("proxmox: API error (status %d)", e.Status)
	}
	return fmt.Sprintf("proxmox: API error %d: %s", e.Status, e.Message)
}
```

- [ ] **Step 2: Implement `types.go`**

File: `internal/proxmox/types.go`
```go
package proxmox

// Version holds output from GET /version.
type Version struct {
	Release string `json:"release"`
	RepoID  string `json:"repoid"`
	Version string `json:"version"`
}

// ClusterNode holds one node's status from GET /cluster/status.
type ClusterNode struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"` // "cluster" or "node"
	IP      string `json:"ip"`
	Online  int    `json:"online"`
	Level   string `json:"level"`
	Local   int    `json:"local"`
	NodeID  int    `json:"nodeid"`
}

// ClusterResource holds one resource from GET /cluster/resources?type=vm.
type ClusterResource struct {
	ID        string  `json:"id"`        // "qemu/100"
	VMID      int     `json:"vmid"`      // 100
	Name      string  `json:"name"`      // VM name
	Node      string  `json:"node"`      // node name
	Type      string  `json:"type"`      // "qemu"
	Status    string  `json:"status"`    // "running", "stopped"
	MaxMem    int64   `json:"maxmem"`    // bytes
	MaxDisk   int64   `json:"maxdisk"`   // bytes
	MaxCPU    int     `json:"maxcpu"`    // cores
	Template  int     `json:"template"`  // 1 if template
	Pool      string  `json:"pool"`
}

// QEMUSummary holds guest overview from GET /nodes/{node}/qemu.
type QEMUSummary struct {
	VMID      int    `json:"vmid"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CPUs      int    `json:"cpus"`
	MaxMem    int64  `json:"maxmem"`
	MaxDisk   int64  `json:"maxdisk"`
	Uptime    int64  `json:"uptime"`
	NetIn     int64  `json:"netin"`
	NetOut    int64  `json:"netout"`
	DiskRead  int64  `json:"diskread"`
	DiskWrite int64  `json:"diskwrite"`
}

// QEMUConfig holds raw VM configuration from GET /nodes/{node}/qemu/{vmid}/config.
type QEMUConfig struct {
	Digest      string            `json:"digest"` // SHA1 digest for optimistic locking
	RawSettings map[string]string `json:"-"`      // dynamically decoded key-values
}

// CertificateInfo holds output from GET /nodes/{node}/certificates/info.
type CertificateInfo struct {
	Fingerprint string `json:"fingerprint"`
	Subject     string `json:"subject"`
	Issuer      string `json:"issuer"`
	NotBefore   int64  `json:"notbefore"`
	NotAfter    int64  `json:"notafter"`
}

// UserOptions specifies options for creating a user via POST /access/users.
type UserOptions struct {
	Comment  string `json:"comment,omitempty"`
	Email    string `json:"email,omitempty"`
	Enable   int    `json:"enable,omitempty"`
	Expire   int64  `json:"expire,omitempty"`
	Password string `json:"password,omitempty"`
}

// APITokenResult holds the secret returned upon creating an API token.
type APITokenResult struct {
	Value string `json:"value"`
}
```

- [ ] **Step 3: Verify package compiles**

Run: `go build ./internal/proxmox/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/slice-a-proxmox-client
git add internal/proxmox/errors.go internal/proxmox/types.go
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(proxmox): add types and error definitions"
```

---

### Task 4: Create `internal/proxmox` Client with auth and transport hardening

**Files:**
- Create: `internal/proxmox/client.go`
- Create: `internal/proxmox/client_test.go`

- [ ] **Step 1: Write the failing tests**

File: `internal/proxmox/client_test.go`
```go
package proxmox_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/proxmox/proxmoxtest"
)

func TestClient_Login_Success(t *testing.T) {
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"POST /api2/json/access/ticket": func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			if r.FormValue("username") != "root@pam" || r.FormValue("password") != "secret" {
				proxmoxtest.ErrorResponse(w, http.StatusUnauthorized, "login failed")
				return
			}
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"ticket":"TICKET:123","CSRFPreventionToken":"CSRF:456"}`)
		},
		"GET /api2/json/version": func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("PVEAuthCookie")
			if err != nil || cookie.Value != "TICKET:123" {
				t.Errorf("cookie = %v, want TICKET:123", cookie)
			}
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"version":"8.2"}`)
		},
	})
	defer srv.Close()

	c := proxmox.NewClient(srv.URL, nil)
	ctx := context.Background()
	if err := c.Login(ctx, "pam", "root", "secret"); err != nil {
		t.Fatalf("Login: %v", err)
	}

	var v proxmox.Version
	if err := c.Get(ctx, "/version", &v); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestClient_APIToken_Header(t *testing.T) {
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"GET /api2/json/version": func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			want := "PVEAPIToken=root@pam!token1=abc-def"
			if auth != want {
				t.Errorf("Authorization = %q, want %q", auth, want)
			}
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"version":"8.2"}`)
		},
	})
	defer srv.Close()

	c := proxmox.NewClient(srv.URL, nil)
	c.SetAPIToken("root@pam", "token1", "abc-def")

	var v proxmox.Version
	if err := c.Get(context.Background(), "/version", &v); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestClient_RedactsCredentialsInErrors(t *testing.T) {
	tokenSecret := "super-secret-token-uuid"
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"GET /api2/json/fail": func(w http.ResponseWriter, r *http.Request) {
			// echo back authorization header in error message to simulate leaking
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"invalid request containing super-secret-token-uuid"}`))
		},
	})
	defer srv.Close()

	c := proxmox.NewClient(srv.URL, nil)
	c.SetAPIToken("root@pam", "token1", tokenSecret)

	err := c.Get(context.Background(), "/fail", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if strings.Contains(err.Error(), tokenSecret) {
		t.Fatalf("error leaked secret: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -v ./internal/proxmox/...`
Expected: FAIL (Client.Login, SetAPIToken, Get not yet implemented)

- [ ] **Step 3: Implement `client.go`**

File: `internal/proxmox/client.go`
```go
package proxmox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Client talks to one Proxmox VE node or cluster endpoint over the REST API.
type Client struct {
	Endpoint   string
	HTTPClient *http.Client

	ticket    string
	csrf      string
	authToken string
}

func NewClient(endpoint string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		Endpoint:   strings.TrimSuffix(endpoint, "/"),
		HTTPClient: httpClient,
	}
}

type ticketResponse struct {
	Ticket              string `json:"ticket"`
	CSRFPreventionToken string `json:"CSRFPreventionToken"`
}

func (c *Client) Login(ctx context.Context, realm, username, password string) error {
	form := url.Values{
		"username": {fmt.Sprintf("%s@%s", username, realm)},
		"password": {password},
	}
	var resp ticketResponse
	if err := c.doForm(ctx, http.MethodPost, "/api2/json/access/ticket", form, &resp); err != nil {
		return err
	}
	c.ticket = resp.Ticket
	c.csrf = resp.CSRFPreventionToken
	c.authToken = ""
	return nil
}

func (c *Client) SetAPIToken(user, tokenID, secret string) {
	c.authToken = fmt.Sprintf("PVEAPIToken=%s!%s=%s", user, tokenID, secret)
	c.ticket = ""
	c.csrf = ""
}

func (c *Client) redact(s string) string {
	if c.ticket != "" {
		s = strings.ReplaceAll(s, c.ticket, "<redacted>")
	}
	if c.csrf != "" {
		s = strings.ReplaceAll(s, c.csrf, "<redacted>")
	}
	if c.authToken != "" {
		s = strings.ReplaceAll(s, c.authToken, "PVEAPIToken=<redacted>")
	}
	return s
}

type envelope struct {
	Data json.RawMessage `json:"data"`
}

type errorEnvelope struct {
	Message string            `json:"message"`
	Errors  map[string]string `json:"errors"`
}

func (c *Client) authenticate(req *http.Request) {
	switch {
	case c.authToken != "":
		req.Header.Set("Authorization", c.authToken)
	case c.ticket != "":
		req.AddCookie(&http.Cookie{Name: "PVEAuthCookie", Value: c.ticket})
		if req.Method != http.MethodGet {
			req.Header.Set("CSRFPreventionToken", c.csrf)
		}
	}
}

func (c *Client) doForm(ctx context.Context, method, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("proxmox: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, out)
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("proxmox: encode body: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, reader)
	if err != nil {
		return fmt.Errorf("proxmox: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	c.authenticate(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("proxmox: %s %s: %w", req.Method, c.redact(req.URL.Path), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("proxmox: %s %s: read response: %w", req.Method, c.redact(req.URL.Path), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.apiError(resp.StatusCode, body)
	}
	if out == nil {
		return nil
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("proxmox: %s %s: decode envelope: %w", req.Method, c.redact(req.URL.Path), err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("proxmox: %s %s: decode data: %w", req.Method, c.redact(req.URL.Path), err)
	}
	return nil
}

func (c *Client) apiError(status int, body []byte) error {
	var ee errorEnvelope
	message := c.redact(strings.TrimSpace(string(body)))
	if json.Unmarshal(body, &ee) == nil {
		if ee.Message != "" {
			message = c.redact(ee.Message)
		} else if len(ee.Errors) > 0 {
			parts := make([]string, 0, len(ee.Errors))
			for k, v := range ee.Errors {
				parts = append(parts, fmt.Sprintf("%s: %s", k, v))
			}
			message = c.redact(strings.Join(parts, "; "))
		}
	}
	return &APIError{Status: status, Message: message}
}

// Get performs an authenticated GET request to path and decodes into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	p := path
	if !strings.HasPrefix(p, "/api2/json") {
		p = "/api2/json" + path
	}
	return c.doJSON(ctx, http.MethodGet, p, nil, out)
}

// Post performs an authenticated POST request with body to path and decodes into out.
func (c *Client) Post(ctx context.Context, path string, body any, out any) error {
	p := path
	if !strings.HasPrefix(p, "/api2/json") {
		p = "/api2/json" + path
	}
	return c.doJSON(ctx, http.MethodPost, p, body, out)
}

// Put performs an authenticated PUT request with body to path and decodes into out.
func (c *Client) Put(ctx context.Context, path string, body any, out any) error {
	p := path
	if !strings.HasPrefix(p, "/api2/json") {
		p = "/api2/json" + path
	}
	return c.doJSON(ctx, http.MethodPut, p, body, out)
}

// Delete performs an authenticated DELETE request to path.
func (c *Client) Delete(ctx context.Context, path string) error {
	p := path
	if !strings.HasPrefix(p, "/api2/json") {
		p = "/api2/json" + path
	}
	return c.doJSON(ctx, http.MethodDelete, p, nil, nil)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v ./internal/proxmox/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/slice-a-proxmox-client
git add internal/proxmox/client.go internal/proxmox/client_test.go
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(proxmox): implement Client with auth and transport redaction"
```

---

### Task 5: Implement all required Proxmox typed endpoints

**Files:**
- Create: `internal/proxmox/endpoints.go`
- Create: `internal/proxmox/endpoints_test.go`

- [ ] **Step 1: Write the failing tests covering all 10 endpoints**

File: `internal/proxmox/endpoints_test.go`
```go
package proxmox_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/proxmox/proxmoxtest"
)

func TestEndpoints(t *testing.T) {
	srv := proxmoxtest.NewServer(t, map[string]http.HandlerFunc{
		"GET /api2/json/version": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"release":"8.2","repoid":"1","version":"8.2.4"}`)
		},
		"GET /api2/json/cluster/status": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `[{"id":"node/pve1","name":"pve1","type":"node","online":1}]`)
		},
		"GET /api2/json/cluster/resources": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("type") != "vm" {
				t.Errorf("query type = %s, want vm", r.URL.Query().Get("type"))
			}
			proxmoxtest.JSONResponse(w, http.StatusOK, `[{"id":"qemu/100","vmid":100,"name":"test-vm","node":"pve1","type":"qemu","status":"running"}]`)
		},
		"GET /api2/json/nodes/pve1/qemu": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `[{"vmid":100,"name":"test-vm","status":"running","cpus":2}]`)
		},
		"GET /api2/json/nodes/pve1/qemu/100/config": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"digest":"abc123sha1","cores":2,"memory":2048,"name":"test-vm"}`)
		},
		"GET /api2/json/nodes/pve1/certificates/info": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `[{"fingerprint":"AA:BB:CC:DD"}]`)
		},
		"POST /api2/json/access/users": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, "")
		},
		"POST /api2/json/access/roles": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, "")
		},
		"PUT /api2/json/access/acl": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, "")
		},
		"POST /api2/json/access/users/nodr@pve/token/nodr-token": func(w http.ResponseWriter, r *http.Request) {
			proxmoxtest.JSONResponse(w, http.StatusOK, `{"value":"token-secret-uuid-1234"}`)
		},
	})
	defer srv.Close()

	c := proxmox.NewClient(srv.URL, nil)
	ctx := context.Background()

	// 1. GetVersion
	ver, err := c.GetVersion(ctx)
	if err != nil || ver.Version != "8.2.4" {
		t.Fatalf("GetVersion: %v, %v", ver, err)
	}

	// 2. GetClusterStatus
	status, err := c.GetClusterStatus(ctx)
	if err != nil || len(status) != 1 || status[0].Name != "pve1" {
		t.Fatalf("GetClusterStatus: %v, %v", status, err)
	}

	// 3. GetClusterResources
	res, err := c.GetClusterResources(ctx, "vm")
	if err != nil || len(res) != 1 || res[0].VMID != 100 {
		t.Fatalf("GetClusterResources: %v, %v", res, err)
	}

	// 4. GetNodeQEMU
	qemu, err := c.GetNodeQEMU(ctx, "pve1")
	if err != nil || len(qemu) != 1 || qemu[0].VMID != 100 {
		t.Fatalf("GetNodeQEMU: %v, %v", qemu, err)
	}

	// 5. GetQEMUConfig
	cfg, digest, err := c.GetQEMUConfig(ctx, "pve1", 100)
	if err != nil || digest != "abc123sha1" || cfg.Digest != "abc123sha1" {
		t.Fatalf("GetQEMUConfig: %v, %s, %v", cfg, digest, err)
	}

	// 6. GetCertFingerprint
	fp, err := c.GetCertFingerprint(ctx, "pve1")
	if err != nil || fp != "AA:BB:CC:DD" {
		t.Fatalf("GetCertFingerprint: %v, %v", fp, err)
	}

	// 7. CreateUser
	if err := c.CreateUser(ctx, "nodr@pve", proxmox.UserOptions{Comment: "nodr"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// 8. CreateRole
	if err := c.CreateRole(ctx, "NodrOperator", []string{"VM.Audit", "VM.Config.Disk"}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	// 9. UpdateACL
	if err := c.UpdateACL(ctx, "/", map[string][]string{"nodr@pve": {"NodrOperator"}}); err != nil {
		t.Fatalf("UpdateACL: %v", err)
	}

	// 10. CreateAPIToken
	secret, err := c.CreateAPIToken(ctx, "nodr@pve", "nodr-token")
	if err != nil || secret != "token-secret-uuid-1234" {
		t.Fatalf("CreateAPIToken: %s, %v", secret, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/proxmox/... -run TestEndpoints`
Expected: FAIL (endpoints methods not defined)

- [ ] **Step 3: Implement `endpoints.go`**

File: `internal/proxmox/endpoints.go`
```go
package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

func (c *Client) GetVersion(ctx context.Context) (Version, error) {
	var v Version
	err := c.Get(ctx, "/version", &v)
	return v, err
}

func (c *Client) GetClusterStatus(ctx context.Context) ([]ClusterNode, error) {
	var nodes []ClusterNode
	err := c.Get(ctx, "/cluster/status", &nodes)
	return nodes, err
}

func (c *Client) GetClusterResources(ctx context.Context, kind string) ([]ClusterResource, error) {
	path := "/cluster/resources"
	if kind != "" {
		path = fmt.Sprintf("%s?type=%s", path, url.QueryEscape(kind))
	}
	var res []ClusterResource
	err := c.Get(ctx, path, &res)
	return res, err
}

func (c *Client) GetNodeQEMU(ctx context.Context, node string) ([]QEMUSummary, error) {
	var vms []QEMUSummary
	err := c.Get(ctx, fmt.Sprintf("/nodes/%s/qemu", url.PathEscape(node)), &vms)
	return vms, err
}

func (c *Client) GetQEMUConfig(ctx context.Context, node string, vmid int) (QEMUConfig, string, error) {
	var raw map[string]any
	path := fmt.Sprintf("/nodes/%s/qemu/%d/config", url.PathEscape(node), vmid)
	if err := c.Get(ctx, path, &raw); err != nil {
		return QEMUConfig{}, "", err
	}
	digest, _ := raw["digest"].(string)

	settings := make(map[string]string, len(raw))
	for k, v := range raw {
		if k == "digest" {
			continue
		}
		settings[k] = fmt.Sprintf("%v", v)
	}

	return QEMUConfig{
		Digest:      digest,
		RawSettings: settings,
	}, digest, nil
}

func (c *Client) GetCertFingerprint(ctx context.Context, node string) (string, error) {
	var certs []CertificateInfo
	path := fmt.Sprintf("/nodes/%s/certificates/info", url.PathEscape(node))
	if err := c.Get(ctx, path, &certs); err != nil {
		return "", err
	}
	for _, cert := range certs {
		if cert.Fingerprint != "" {
			return cert.Fingerprint, nil
		}
	}
	return "", fmt.Errorf("proxmox: no certificate fingerprint found for node %s", node)
}

func (c *Client) CreateUser(ctx context.Context, userid string, opts UserOptions) error {
	payload := map[string]any{
		"userid": userid,
	}
	if opts.Comment != "" {
		payload["comment"] = opts.Comment
	}
	if opts.Email != "" {
		payload["email"] = opts.Email
	}
	if opts.Enable != 0 {
		payload["enable"] = opts.Enable
	}
	if opts.Expire != 0 {
		payload["expire"] = opts.Expire
	}
	if opts.Password != "" {
		payload["password"] = opts.Password
	}
	return c.Post(ctx, "/access/users", payload, nil)
}

func (c *Client) CreateRole(ctx context.Context, roleid string, privileges []string) error {
	payload := map[string]any{
		"roleid": roleid,
		"privs":  strings.Join(privileges, ","),
	}
	return c.Post(ctx, "/access/roles", payload, nil)
}

func (c *Client) UpdateACL(ctx context.Context, path string, roles map[string][]string) error {
	for subject, roleList := range roles {
		payload := map[string]any{
			"path":  path,
			"roles": strings.Join(roleList, ","),
		}
		if strings.Contains(subject, "@") {
			payload["users"] = subject
		} else {
			payload["groups"] = subject
		}
		if err := c.Put(ctx, "/access/acl", payload, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) CreateAPIToken(ctx context.Context, userid, tokenID string) (string, error) {
	path := fmt.Sprintf("/access/users/%s/token/%s", url.PathEscape(userid), url.PathEscape(tokenID))
	var res APITokenResult
	if err := c.Post(ctx, path, nil, &res); err != nil {
		return "", err
	}
	return res.Value, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v ./internal/proxmox/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/slice-a-proxmox-client
git add internal/proxmox/endpoints.go internal/proxmox/endpoints_test.go
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(proxmox): implement typed endpoints for cluster, node, qemu and access"
```

---

### Task 6: Implement KEK loading and permissions validation

**Files:**
- Create: `internal/secrets/errors.go`
- Create: `internal/secrets/kek.go`
- Create: `internal/secrets/kek_test.go`

- [ ] **Step 1: Write the failing tests**

File: `internal/secrets/kek_test.go`
```go
package secrets_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/centopw/nodr/internal/secrets"
)

func TestLoadKEK_FromFile(t *testing.T) {
	dir := t.TempDir()
	kekFile := filepath.Join(dir, "kek.bin")
	rawKEK := make([]byte, 32)
	for i := range rawKEK {
		rawKEK[i] = byte(i)
	}
	if err := os.WriteFile(kekFile, rawKEK, 0600); err != nil {
		t.Fatalf("write kek file: %v", err)
	}

	got, err := secrets.LoadKEK(secrets.KEKConfig{FilePath: kekFile})
	if err != nil {
		t.Fatalf("LoadKEK: %v", err)
	}
	if string(got) != string(rawKEK) {
		t.Errorf("got %v, want %v", got, rawKEK)
	}
}

func TestLoadKEK_FromEnvVar(t *testing.T) {
	rawKEK := make([]byte, 32)
	for i := range rawKEK {
		rawKEK[i] = byte(i + 10)
	}
	encoded := base64.StdEncoding.EncodeToString(rawKEK)
	t.Setenv("TEST_NODR_KEK", encoded)

	got, err := secrets.LoadKEK(secrets.KEKConfig{EnvVar: "TEST_NODR_KEK"})
	if err != nil {
		t.Fatalf("LoadKEK: %v", err)
	}
	if string(got) != string(rawKEK) {
		t.Errorf("got %v, want %v", got, rawKEK)
	}
}

func TestLoadKEK_FilePermissionsRejection(t *testing.T) {
	dir := t.TempDir()
	kekFile := filepath.Join(dir, "kek_insecure.bin")
	rawKEK := make([]byte, 32)
	// Mode 0644 gives group/other read permissions
	if err := os.WriteFile(kekFile, rawKEK, 0644); err != nil {
		t.Fatalf("write kek file: %v", err)
	}

	_, err := secrets.LoadKEK(secrets.KEKConfig{FilePath: kekFile})
	if err == nil {
		t.Fatal("expected error for file with permissions 0644, got nil")
	}
}

func TestLoadKEK_InvalidLength(t *testing.T) {
	dir := t.TempDir()
	kekFile := filepath.Join(dir, "short_kek.bin")
	if err := os.WriteFile(kekFile, []byte("too-short"), 0600); err != nil {
		t.Fatalf("write kek file: %v", err)
	}

	_, err := secrets.LoadKEK(secrets.KEKConfig{FilePath: kekFile})
	if err == nil {
		t.Fatal("expected error for short KEK, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/secrets/...`
Expected: FAIL (package does not exist)

- [ ] **Step 3: Implement `errors.go` and `kek.go`**

File: `internal/secrets/errors.go`
```go
package secrets

import "errors"

// ErrNotFound reports that a requested secret reference does not exist in the store.
var ErrNotFound = errors.New("secrets: secret not found")

// ErrDecrypt reports that decryption failed (invalid KEK, corrupted data, or tampering).
var ErrDecrypt = errors.New("secrets: decryption failed")

// ErrInvalidKEK reports that a KEK was missing or did not meet length/permission requirements.
var ErrInvalidKEK = errors.New("secrets: invalid KEK")
```

File: `internal/secrets/kek.go`
```go
package secrets

import (
	"encoding/base64"
	"fmt"
	"os"
)

// KEKConfig specifies how to locate and load the 32-byte Key Encryption Key.
type KEKConfig struct {
	FilePath string // Path to file containing 32 raw bytes on disk
	EnvVar   string // Name of environment variable containing base64-encoded 32 bytes
}

// LoadKEK loads a 32-byte KEK from FilePath if set, or EnvVar if set.
// If FilePath is used, the file mode must not grant read/write/execute to group or others
// (mode & 0077 != 0 is rejected).
func LoadKEK(cfg KEKConfig) ([]byte, error) {
	if cfg.FilePath != "" {
		info, err := os.Stat(cfg.FilePath)
		if err != nil {
			return nil, fmt.Errorf("secrets: stat KEK file: %w", err)
		}
		// Enforce strict file permissions: reject group/other permissions
		if info.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("secrets: KEK file %q has insecure permissions %04o (must be 0600 or stricter): %w",
				cfg.FilePath, info.Mode().Perm(), ErrInvalidKEK)
		}
		data, err := os.ReadFile(cfg.FilePath)
		if err != nil {
			return nil, fmt.Errorf("secrets: read KEK file: %w", err)
		}
		if len(data) != 32 {
			return nil, fmt.Errorf("secrets: KEK file %q contains %d bytes (want 32): %w", cfg.FilePath, len(data), ErrInvalidKEK)
		}
		return data, nil
	}

	if cfg.EnvVar != "" {
		val := os.Getenv(cfg.EnvVar)
		if val == "" {
			return nil, fmt.Errorf("secrets: environment variable %s is empty or unset: %w", cfg.EnvVar, ErrInvalidKEK)
		}
		decoded, err := base64.StdEncoding.DecodeString(val)
		if err != nil {
			return nil, fmt.Errorf("secrets: decode KEK from env %s: %w", cfg.EnvVar, err)
		}
		if len(decoded) != 32 {
			return nil, fmt.Errorf("secrets: KEK from env %s is %d bytes (want 32): %w", cfg.EnvVar, len(decoded), ErrInvalidKEK)
		}
		return decoded, nil
	}

	return nil, fmt.Errorf("secrets: no KEK source specified in config: %w", ErrInvalidKEK)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v ./internal/secrets/... -run TestLoadKEK`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/slice-a-proxmox-client
git add internal/secrets/errors.go internal/secrets/kek.go internal/secrets/kek_test.go
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(secrets): implement KEK loading with permission enforcement"
```

---

### Task 7: Implement SQLite Store with envelope encryption

**Files:**
- Create: `internal/secrets/store.go`
- Create: `internal/secrets/store_test.go`

- [ ] **Step 1: Write the failing tests**

File: `internal/secrets/store_test.go`
```go
package secrets_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/centopw/nodr/internal/secrets"
)

func TestStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "secrets.db")
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 1)
	}

	store, err := secrets.Open(dbPath, kek)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	secretName := "proxmox/pve-main-token"
	secretValue := []byte("pve-token-secret-uuid-12345")

	if err := store.Put(ctx, secretName, secretValue); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Resolve(ctx, secretName)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !bytes.Equal(got, secretValue) {
		t.Errorf("got %q, want %q", got, secretValue)
	}

	// List
	names, err := store.List(ctx)
	if err != nil || len(names) != 1 || names[0] != secretName {
		t.Fatalf("List: %v, %v", names, err)
	}

	// Delete
	if err := store.Delete(ctx, secretName); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = store.Resolve(ctx, secretName)
	if !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestStore_WrongKEK(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "secrets.db")
	kek1 := make([]byte, 32)
	kek2 := make([]byte, 32)
	kek2[0] = 0xFF

	store1, err := secrets.Open(dbPath, kek1)
	if err != nil {
		t.Fatalf("Open store1: %v", err)
	}
	ctx := context.Background()
	if err := store1.Put(ctx, "test/key", []byte("secret-payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	store1.Close()

	store2, err := secrets.Open(dbPath, kek2)
	if err != nil {
		t.Fatalf("Open store2: %v", err)
	}
	defer store2.Close()

	_, err = store2.Resolve(ctx, "test/key")
	if !errors.Is(err, secrets.ErrDecrypt) {
		t.Fatalf("expected ErrDecrypt with wrong KEK, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/secrets/... -run TestStore`
Expected: FAIL (Store not implemented)

- [ ] **Step 3: Implement `store.go`**

File: `internal/secrets/store.go`
```go
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Store is an envelope-encrypted, SQLite-backed store for secrets.
type Store struct {
	db  *sql.DB
	kek []byte
}

const schema = `
CREATE TABLE IF NOT EXISTS secrets (
    name             TEXT PRIMARY KEY,
    wrapped_key      BLOB NOT NULL,
    wrapped_nonce    BLOB NOT NULL,
    ciphertext       BLOB NOT NULL,
    ciphertext_nonce BLOB NOT NULL
);
`

// Open opens or creates an encrypted secrets store at dbPath.
// If the file does not exist, it is created with file mode 0600.
func Open(dbPath string, kek []byte) (*Store, error) {
	if len(kek) != 32 {
		return nil, fmt.Errorf("secrets: KEK must be exactly 32 bytes (got %d): %w", len(kek), ErrInvalidKEK)
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("secrets: create dir: %w", err)
	}

	// If file doesn't exist, create it with 0600
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("secrets: create db file: %w", err)
		}
		_ = f.Close()
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("secrets: open sqlite db: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secrets: init schema: %w", err)
	}

	return &Store{db: db, kek: kek}, nil
}

// Put encrypts value using a freshly generated 32-byte data key, wraps the
// data key with the store's KEK, and inserts or replaces the secret row.
func (s *Store) Put(ctx context.Context, name string, value []byte) error {
	// Generate random 32-byte data key
	dataKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dataKey); err != nil {
		return fmt.Errorf("secrets: generate data key: %w", err)
	}

	// 1. Wrap dataKey with KEK (AES-256-GCM)
	kekBlock, err := aes.NewCipher(s.kek)
	if err != nil {
		return fmt.Errorf("secrets: cipher KEK: %w", err)
	}
	kekGCM, err := cipher.NewGCM(kekBlock)
	if err != nil {
		return fmt.Errorf("secrets: gcm KEK: %w", err)
	}
	wrappedNonce := make([]byte, kekGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, wrappedNonce); err != nil {
		return fmt.Errorf("secrets: generate wrapped nonce: %w", err)
	}
	wrappedKey := kekGCM.Seal(nil, wrappedNonce, dataKey, nil)

	// 2. Encrypt value with dataKey (AES-256-GCM)
	dataBlock, err := aes.NewCipher(dataKey)
	if err != nil {
		return fmt.Errorf("secrets: cipher data key: %w", err)
	}
	dataGCM, err := cipher.NewGCM(dataBlock)
	if err != nil {
		return fmt.Errorf("secrets: gcm data key: %w", err)
	}
	ciphertextNonce := make([]byte, dataGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, ciphertextNonce); err != nil {
		return fmt.Errorf("secrets: generate ciphertext nonce: %w", err)
	}
	ciphertext := dataGCM.Seal(nil, ciphertextNonce, value, nil)

	// Store in DB
	query := `
INSERT INTO secrets (name, wrapped_key, wrapped_nonce, ciphertext, ciphertext_nonce)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    wrapped_key=excluded.wrapped_key,
    wrapped_nonce=excluded.wrapped_nonce,
    ciphertext=excluded.ciphertext,
    ciphertext_nonce=excluded.ciphertext_nonce;
`
	_, err = s.db.ExecContext(ctx, query, name, wrappedKey, wrappedNonce, ciphertext, ciphertextNonce)
	if err != nil {
		return fmt.Errorf("secrets: insert secret %q: %w", name, err)
	}
	return nil
}

// Resolve unwraps the data key for ref using KEK, decrypts ciphertext, and returns the plaintext secret.
func (s *Store) Resolve(ctx context.Context, ref string) ([]byte, error) {
	query := `SELECT wrapped_key, wrapped_nonce, ciphertext, ciphertext_nonce FROM secrets WHERE name = ?`
	row := s.db.QueryRowContext(ctx, query, ref)

	var wrappedKey, wrappedNonce, ciphertext, ciphertextNonce []byte
	err := row.Scan(&wrappedKey, &wrappedNonce, &ciphertext, &ciphertextNonce)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("secrets: %s: %w", ref, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: query %s: %w", ref, err)
	}

	// 1. Unwrap data key using KEK
	kekBlock, err := aes.NewCipher(s.kek)
	if err != nil {
		return nil, fmt.Errorf("secrets: cipher KEK: %w", err)
	}
	kekGCM, err := cipher.NewGCM(kekBlock)
	if err != nil {
		return nil, fmt.Errorf("secrets: gcm KEK: %w", err)
	}
	dataKey, err := kekGCM.Open(nil, wrappedNonce, wrappedKey, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: unwrap key for %s: %w", ref, ErrDecrypt)
	}

	// 2. Decrypt ciphertext using data key
	dataBlock, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, fmt.Errorf("secrets: cipher data key: %w", err)
	}
	dataGCM, err := cipher.NewGCM(dataBlock)
	if err != nil {
		return nil, fmt.Errorf("secrets: gcm data key: %w", err)
	}
	value, err := dataGCM.Open(nil, ciphertextNonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: decrypt ciphertext for %s: %w", ref, ErrDecrypt)
	}

	return value, nil
}

// Delete removes a secret by name.
func (s *Store) Delete(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("secrets: delete %s: %w", name, err)
	}
	return nil
}

// List returns the names of all stored secrets.
func (s *Store) List(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM secrets ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("secrets: list: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("secrets: scan name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// Close closes the underlying SQLite database.
func (s *Store) Close() error {
	return s.db.Close()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v ./internal/secrets/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/cento/git/personal/nodr/.worktrees/slice-a-proxmox-client
git add internal/secrets/store.go internal/secrets/store_test.go
git -c user.name="centopw" -c user.email="hiep@hce.vn" commit -m "feat(secrets): implement envelope-encrypted SQLite Store"
```

---

### Task 8: Full verification and test suite run

**Files:**
- None (verification step)

- [ ] **Step 1: Run all unit and integration tests across the entire repository**

Run in worktree:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/slice-a-proxmox-client
go test -v ./internal/proxmox/... ./internal/secrets/...
go test ./...
```
Expected: PASS across all packages.

- [ ] **Step 2: Run linter and formatting check**

Run:
```bash
cd /Users/cento/git/personal/nodr/.worktrees/slice-a-proxmox-client
golangci-lint run
```
Expected: PASS with 0 lint issues.
