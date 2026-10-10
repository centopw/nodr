package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/centopw/nodr/internal/authn"
	"github.com/centopw/nodr/internal/commands"
	"github.com/centopw/nodr/internal/ledger"
	"github.com/centopw/nodr/internal/operations"
)

// ledgerFake implements LedgerService with function fields. Unset methods
// fail the test so unexpected calls are visible.
type ledgerFake struct {
	t *testing.T

	mu            sync.Mutex
	handlePushN   int
	installations func(ctx context.Context) ([]ledger.Installation, error)
	connect       func(ctx context.Context, in ledger.ConnectInput) (ledger.Workspace, error)
	workspaces    func(ctx context.Context) ([]ledger.Workspace, error)
	workspace     func(ctx context.Context, slug string) (ledger.Workspace, error)
	withRevision  func(ctx context.Context, slug, revision string, fn func(root string) error) error
	revisionOf    func(ctx context.Context, slug, changesetID string) (string, error)
	runCommand    func(ctx context.Context, in ledger.CommandInput) (ledger.CommandResult, error)
	changesets    func(ctx context.Context, slug string) ([]operations.Changeset, error)
	changeset     func(ctx context.Context, slug, id string) (ledger.ChangesetDetail, error)
	plan          func(ctx context.Context, in ledger.PlanInput) (operations.Operation, error)
	apply         func(ctx context.Context, in ledger.ApplyInput) (operations.Operation, error)
	operation     func(ctx context.Context, id string) (operations.Operation, error)
	planSummary   func(ctx context.Context, operationID string) (ledger.PlanSummary, error)
	subscribe     func() (<-chan operations.Event, func())
	eventsAfter   func(ctx context.Context, afterID int64) ([]operations.Event, error)
	handlePush    func(ctx context.Context, event ledger.PushEvent) error
	verifyWebhook func(payload []byte, signature string) error
}

func (f *ledgerFake) unset(name string) error {
	f.t.Helper()
	f.t.Errorf("unexpected call to %s", name)
	return errors.New("unexpected call")
}

func (f *ledgerFake) Installations(ctx context.Context) ([]ledger.Installation, error) {
	if f.installations == nil {
		return nil, f.unset("Installations")
	}
	return f.installations(ctx)
}

func (f *ledgerFake) Connect(ctx context.Context, in ledger.ConnectInput) (ledger.Workspace, error) {
	if f.connect == nil {
		return ledger.Workspace{}, f.unset("Connect")
	}
	return f.connect(ctx, in)
}

func (f *ledgerFake) Workspaces(ctx context.Context) ([]ledger.Workspace, error) {
	if f.workspaces == nil {
		return nil, f.unset("Workspaces")
	}
	return f.workspaces(ctx)
}

func (f *ledgerFake) Workspace(ctx context.Context, slug string) (ledger.Workspace, error) {
	if f.workspace == nil {
		return ledger.Workspace{}, f.unset("Workspace")
	}
	return f.workspace(ctx, slug)
}

func (f *ledgerFake) WithRevision(ctx context.Context, slug, revision string, fn func(root string) error) error {
	if f.withRevision == nil {
		return f.unset("WithRevision")
	}
	return f.withRevision(ctx, slug, revision, fn)
}

func (f *ledgerFake) RevisionOf(ctx context.Context, slug, changesetID string) (string, error) {
	if f.revisionOf == nil {
		return "", f.unset("RevisionOf")
	}
	return f.revisionOf(ctx, slug, changesetID)
}

func (f *ledgerFake) RunCommand(ctx context.Context, in ledger.CommandInput) (ledger.CommandResult, error) {
	if f.runCommand == nil {
		return ledger.CommandResult{}, f.unset("RunCommand")
	}
	return f.runCommand(ctx, in)
}

func (f *ledgerFake) Changesets(ctx context.Context, slug string) ([]operations.Changeset, error) {
	if f.changesets == nil {
		return nil, f.unset("Changesets")
	}
	return f.changesets(ctx, slug)
}

func (f *ledgerFake) Changeset(ctx context.Context, slug, id string) (ledger.ChangesetDetail, error) {
	if f.changeset == nil {
		return ledger.ChangesetDetail{}, f.unset("Changeset")
	}
	return f.changeset(ctx, slug, id)
}

func (f *ledgerFake) Plan(ctx context.Context, in ledger.PlanInput) (operations.Operation, error) {
	if f.plan == nil {
		return operations.Operation{}, f.unset("Plan")
	}
	return f.plan(ctx, in)
}

func (f *ledgerFake) Apply(ctx context.Context, in ledger.ApplyInput) (operations.Operation, error) {
	if f.apply == nil {
		return operations.Operation{}, f.unset("Apply")
	}
	return f.apply(ctx, in)
}

func (f *ledgerFake) Operation(ctx context.Context, id string) (operations.Operation, error) {
	if f.operation == nil {
		return operations.Operation{}, f.unset("Operation")
	}
	return f.operation(ctx, id)
}

func (f *ledgerFake) PlanSummary(ctx context.Context, operationID string) (ledger.PlanSummary, error) {
	if f.planSummary == nil {
		return ledger.PlanSummary{}, f.unset("PlanSummary")
	}
	return f.planSummary(ctx, operationID)
}

func (f *ledgerFake) Subscribe() (<-chan operations.Event, func()) {
	if f.subscribe == nil {
		_ = f.unset("Subscribe")
		ch := make(chan operations.Event)
		close(ch)
		return ch, func() {}
	}
	return f.subscribe()
}

func (f *ledgerFake) EventsAfter(ctx context.Context, afterID int64) ([]operations.Event, error) {
	if f.eventsAfter == nil {
		return nil, f.unset("EventsAfter")
	}
	return f.eventsAfter(ctx, afterID)
}

func (f *ledgerFake) HandlePush(ctx context.Context, event ledger.PushEvent) error {
	f.mu.Lock()
	f.handlePushN++
	f.mu.Unlock()
	if f.handlePush == nil {
		return f.unset("HandlePush")
	}
	return f.handlePush(ctx, event)
}

func (f *ledgerFake) VerifyWebhookSignature(payload []byte, signature string) error {
	if f.verifyWebhook == nil {
		return f.unset("VerifyWebhookSignature")
	}
	return f.verifyWebhook(payload, signature)
}

// ledgerAuthStore opens an authn store with one admin account.
func ledgerAuthStore(t *testing.T) *authn.Store {
	t.Helper()
	store, err := authn.Open(filepath.Join(t.TempDir(), "authn.db"))
	if err != nil {
		t.Fatalf("authn.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.CreateAccount(t.Context(), "admin", "test-password-123"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return store
}

// ledgerLogin signs in and returns the session cookie and CSRF token.
func ledgerLogin(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, apiPrefix+"/auth/login", strings.NewReader(`{"username":"admin","password":"test-password-123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	var body struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return cookies[0], body.CSRFToken
}

// ledgerTestHandler builds the ledger handler with a fake service and a
// marker next handler that answers every unowned path with 418.
func ledgerTestHandler(t *testing.T, svc LedgerService, ready func(context.Context) []string) http.Handler {
	t.Helper()
	store := ledgerAuthStore(t)
	// Login is owned by the legacy handler chain that LedgerHandler falls through to.
	fallthroughMux := http.NewServeMux()
	fallthroughMux.Handle("POST "+apiPrefix+"/auth/login", authn.LoginHandler(store, apiPrefix+"/auth/login"))
	fallthroughMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	next := http.Handler(fallthroughMux)
	return LedgerHandler(LedgerConfig{Service: svc, Auth: store, Ready: ready}, next)
}

// ledgerDo sends a request with the given session cookie and optional CSRF
// token and headers.
func ledgerDo(h http.Handler, method, target string, body string, cookie *http.Cookie, csrf string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ledgerProblem decodes an RFC 9457 body.
type ledgerProblem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Errors []struct {
		Path    string `json:"path"`
		Message string `json:"message"`
	} `json:"errors"`
}

func ledgerDecodeProblem(t *testing.T, rec *httptest.ResponseRecorder) ledgerProblem {
	t.Helper()
	var p ledgerProblem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem %q: %v", rec.Body.String(), err)
	}
	return p
}

func TestLedgerAuthGate(t *testing.T) {
	h := ledgerTestHandler(t, &ledgerFake{t: t}, nil)
	for _, target := range []string{apiPrefix + "/workspaces", apiPrefix + "/events"} {
		t.Run(target, func(t *testing.T) {
			rec := ledgerDo(h, http.MethodGet, target, "", nil, "", nil)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestLedgerCSRFRequiredOnMutation(t *testing.T) {
	svc := &ledgerFake{t: t, connect: func(context.Context, ledger.ConnectInput) (ledger.Workspace, error) {
		return ledger.Workspace{Slug: "homelab"}, nil
	}}
	h := ledgerTestHandler(t, svc, nil)
	cookie, csrf := ledgerLogin(t, h)
	body := `{"installationId":1,"owner":"o","repository":"r"}`

	missing := ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces", body, cookie, "", nil)
	if missing.Code != http.StatusForbidden {
		t.Fatalf("no CSRF status = %d, want 403", missing.Code)
	}
	wrong := ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces", body, cookie, csrf+"x", nil)
	if wrong.Code != http.StatusForbidden {
		t.Fatalf("wrong CSRF status = %d, want 403", wrong.Code)
	}
	ok := ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces", body, cookie, csrf, nil)
	if ok.Code != http.StatusCreated {
		t.Fatalf("valid CSRF status = %d, body = %s", ok.Code, ok.Body.String())
	}
}

func TestLedgerCommandPreconditions(t *testing.T) {
	svc := &ledgerFake{t: t, runCommand: func(context.Context, ledger.CommandInput) (ledger.CommandResult, error) {
		return ledger.CommandResult{}, errors.New("must not be reached")
	}}
	h := ledgerTestHandler(t, svc, nil)
	cookie, csrf := ledgerLogin(t, h)
	target := apiPrefix + "/workspaces/homelab/commands"
	body := `{"command":"vm.create","params":{}}`

	t.Run("missing If-Match is 428", func(t *testing.T) {
		rec := ledgerDo(h, http.MethodPost, target, body, cookie, csrf, map[string]string{"Idempotency-Key": "k1"})
		if rec.Code != http.StatusPreconditionRequired {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if p := ledgerDecodeProblem(t, rec); p.Title != "Precondition required" {
			t.Fatalf("title = %q", p.Title)
		}
	})
	t.Run("missing Idempotency-Key is 400 with field", func(t *testing.T) {
		rec := ledgerDo(h, http.MethodPost, target, body, cookie, csrf, map[string]string{"If-Match": `"abc"`})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		p := ledgerDecodeProblem(t, rec)
		if len(p.Errors) != 1 || p.Errors[0].Path != "Idempotency-Key" {
			t.Fatalf("errors = %#v", p.Errors)
		}
	})
}

func TestLedgerCommandSuccessETagAndBody(t *testing.T) {
	var got ledger.CommandInput
	svc := &ledgerFake{t: t, runCommand: func(_ context.Context, in ledger.CommandInput) (ledger.CommandResult, error) {
		got = in
		return ledger.CommandResult{
			Changeset: operations.Changeset{ID: "cs-1", Status: operations.ChangesetOpen, BaseSHA: "base", DraftSHA: "draft2", Revision: 3},
			Diff:      []ledger.FileDiff{{Path: "nodr.yaml", Status: "modified", Patch: "@@"}},
			Result:    map[string]string{"vm": "web-01"},
		}, nil
	}}
	h := ledgerTestHandler(t, svc, nil)
	cookie, csrf := ledgerLogin(t, h)
	rec := ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces/homelab/commands",
		`{"command":"vm.create","params":{"name":"web-01"},"changeset":"cs-1"}`, cookie, csrf,
		map[string]string{"If-Match": `W/"draft1"`, "Idempotency-Key": "idem-1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if etag := rec.Header().Get("ETag"); etag != `"draft2"` {
		t.Fatalf("ETag = %q", etag)
	}
	if got.ExpectedSHA != "draft1" || got.IdempotencyKey != "idem-1" || got.ChangesetID != "cs-1" || got.Workspace != "homelab" || got.Command != "vm.create" || got.Actor != "admin" {
		t.Fatalf("command input = %#v", got)
	}
	var body struct {
		Changeset struct {
			ID       string `json:"id"`
			DraftSHA string `json:"draftSha"`
			Revision int64  `json:"revision"`
		} `json:"changeset"`
		Diff []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
			Patch  string `json:"patch"`
		} `json:"diff"`
		Result map[string]string `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Changeset.ID != "cs-1" || body.Changeset.DraftSHA != "draft2" || body.Changeset.Revision != 3 {
		t.Fatalf("changeset = %#v", body.Changeset)
	}
	if len(body.Diff) != 1 || body.Diff[0].Path != "nodr.yaml" || body.Result["vm"] != "web-01" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestLedgerCommandStrictJSON(t *testing.T) {
	svc := &ledgerFake{t: t, runCommand: func(context.Context, ledger.CommandInput) (ledger.CommandResult, error) {
		return ledger.CommandResult{}, errors.New("must not be reached")
	}}
	h := ledgerTestHandler(t, svc, nil)
	cookie, csrf := ledgerLogin(t, h)
	rec := ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces/homelab/commands",
		`{"command":"vm.create","params":{},"bogus":true}`, cookie, csrf,
		map[string]string{"If-Match": `"draft1"`, "Idempotency-Key": "k"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestLedgerErrorMappings(t *testing.T) {
	cmdErr := &ledger.CommandError{Err: &commands.Error{
		Status: http.StatusUnprocessableEntity, Title: "Invalid VM", Detail: "name is taken",
		Fields: []commands.FieldError{{Path: "name", Message: "already exists"}},
	}}
	tests := []struct {
		name     string
		err      error
		status   int
		title    string
		path     string
		messages string
	}{
		{name: "stale revision", err: ledger.ErrStaleRevision, status: http.StatusPreconditionFailed, title: "PRECONDITION_FAILED"},
		{name: "plan changed", err: ledger.ErrPlanChanged, status: http.StatusConflict, title: "PLAN_CHANGED"},
		{name: "plan required", err: ledger.ErrPlanRequired, status: http.StatusConflict},
		{name: "changeset closed", err: ledger.ErrChangesetClosed, status: http.StatusConflict},
		{name: "workspace exists", err: ledger.ErrWorkspaceExists, status: http.StatusConflict},
		{name: "command in flight", err: ledger.ErrCommandInFlight, status: http.StatusConflict},
		{name: "workspace not found", err: ledger.ErrWorkspaceNotFound, status: http.StatusNotFound},
		{name: "changeset not found", err: ledger.ErrChangesetNotFound, status: http.StatusNotFound},
		{name: "operation not found", err: fmt.Errorf("op: %w", operations.ErrNotFound), status: http.StatusNotFound},
		{name: "repository inaccessible", err: ledger.ErrRepositoryInaccessible, status: http.StatusUnprocessableEntity, path: "repository"},
		{name: "not nodr repository", err: ledger.ErrNotNodrRepository, status: http.StatusUnprocessableEntity, path: "repository"},
		{name: "destructive needs approval", err: ledger.ErrDestructiveNeedsApproval, status: http.StatusUnprocessableEntity, path: "allowDestroy"},
		{name: "command error", err: cmdErr, status: http.StatusUnprocessableEntity, title: "Invalid VM", path: "name", messages: "name is taken"},
		{name: "unknown is generic 500", err: errors.New("secret token abc"), status: http.StatusInternalServerError, title: "Internal server error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &ledgerFake{t: t, workspace: func(context.Context, string) (ledger.Workspace, error) {
				return ledger.Workspace{}, tt.err
			}}
			h := ledgerTestHandler(t, svc, nil)
			cookie, _ := ledgerLogin(t, h)
			rec := ledgerDo(h, http.MethodGet, apiPrefix+"/workspaces/homelab", "", cookie, "", nil)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.status, rec.Body.String())
			}
			p := ledgerDecodeProblem(t, rec)
			if tt.title != "" && p.Title != tt.title {
				t.Errorf("title = %q, want %q", p.Title, tt.title)
			}
			if tt.path != "" && (len(p.Errors) != 1 || p.Errors[0].Path != tt.path) {
				t.Errorf("errors = %#v, want path %q", p.Errors, tt.path)
			}
			if tt.messages != "" && p.Detail != tt.messages {
				t.Errorf("detail = %q, want %q", p.Detail, tt.messages)
			}
			if tt.status == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "secret token") {
				t.Errorf("500 leaked internal error: %s", rec.Body.String())
			}
		})
	}
}

func TestLedgerPlanApplyRoutesAndPreconditions(t *testing.T) {
	var planIn ledger.PlanInput
	var applyIn ledger.ApplyInput
	svc := &ledgerFake{t: t,
		plan: func(_ context.Context, in ledger.PlanInput) (operations.Operation, error) {
			planIn = in
			return operations.Operation{ID: "op-p", Kind: "plan", Status: operations.OperationQueued, ChangesetID: in.ChangesetID, PlanSHA: in.ExpectedSHA}, nil
		},
		apply: func(_ context.Context, in ledger.ApplyInput) (operations.Operation, error) {
			applyIn = in
			return operations.Operation{ID: "op-a", Kind: "apply", Status: operations.OperationQueued, ChangesetID: in.ChangesetID}, nil
		},
	}
	h := ledgerTestHandler(t, svc, nil)
	cookie, csrf := ledgerLogin(t, h)
	headers := map[string]string{"If-Match": `"sha1"`, "Idempotency-Key": "p-1"}

	rec := ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces/homelab/changesets/cs-9:plan", "", cookie, csrf, headers)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("plan status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if planIn.ChangesetID != "cs-9" || planIn.ExpectedSHA != "sha1" || planIn.IdempotencyKey != "p-1" || planIn.Workspace != "homelab" {
		t.Fatalf("plan input = %#v", planIn)
	}
	var planBody struct {
		Operation struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"operation"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &planBody)
	if planBody.Operation.ID != "op-p" || planBody.Operation.Kind != "plan" {
		t.Fatalf("plan body = %s", rec.Body.String())
	}

	rec = ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces/homelab/changesets/cs-9:apply", `{"allowDestroy":true}`, cookie, csrf, headers)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("apply status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if applyIn.ChangesetID != "cs-9" || !applyIn.AllowDestroy || applyIn.ExpectedSHA != "sha1" {
		t.Fatalf("apply input = %#v", applyIn)
	}

	rec = ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces/homelab/changesets/cs-9:bogus", "", cookie, csrf, headers)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces/homelab/changesets/cs-9:plan", "", cookie, csrf, map[string]string{"Idempotency-Key": "x"})
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("plan without If-Match status = %d", rec.Code)
	}
}

func TestLedgerApplyPlanChangedIs409(t *testing.T) {
	svc := &ledgerFake{t: t, apply: func(context.Context, ledger.ApplyInput) (operations.Operation, error) {
		return operations.Operation{}, ledger.ErrPlanChanged
	}}
	h := ledgerTestHandler(t, svc, nil)
	cookie, csrf := ledgerLogin(t, h)
	rec := ledgerDo(h, http.MethodPost, apiPrefix+"/workspaces/homelab/changesets/cs-1:apply", "", cookie, csrf,
		map[string]string{"If-Match": `"s"`, "Idempotency-Key": "k"})
	if rec.Code != http.StatusConflict || ledgerDecodeProblem(t, rec).Title != "PLAN_CHANGED" {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestLedgerWebhookSignatureBeforeParse(t *testing.T) {
	svc := &ledgerFake{t: t,
		verifyWebhook: func([]byte, string) error { return errors.New("bad signature") },
		handlePush: func(context.Context, ledger.PushEvent) error {
			return errors.New("HandlePush must not be called")
		},
	}
	h := ledgerTestHandler(t, svc, nil)
	rec := ledgerDo(h, http.MethodPost, apiPrefix+"/integrations/github/webhook", `{not json`, nil, "",
		map[string]string{"X-GitHub-Event": "push", "X-GitHub-Delivery": "d1", "X-Hub-Signature-256": "sha256=bad"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if svc.handlePushN != 0 {
		t.Fatalf("HandlePush called %d times", svc.handlePushN)
	}
}

func TestLedgerWebhookRoutes(t *testing.T) {
	const payload = `{"ref":"refs/heads/main","after":"abc123","repository":{"name":"homelab","owner":{"login":"acme"}}}`
	var pushes []ledger.PushEvent
	seen := map[string]bool{}
	svc := &ledgerFake{t: t,
		verifyWebhook: func(_ []byte, sig string) error {
			if sig != "sha256=ok" {
				return errors.New("bad")
			}
			return nil
		},
		handlePush: func(_ context.Context, e ledger.PushEvent) error {
			if seen[e.DeliveryID] {
				return nil
			}
			seen[e.DeliveryID] = true
			pushes = append(pushes, e)
			return nil
		},
	}
	h := ledgerTestHandler(t, svc, nil)
	post := func(event, delivery string, headers map[string]string) *httptest.ResponseRecorder {
		hdr := map[string]string{"X-Hub-Signature-256": "sha256=ok", "X-GitHub-Event": event}
		if delivery != "" {
			hdr["X-GitHub-Delivery"] = delivery
		}
		for k, v := range headers {
			hdr[k] = v
		}
		return ledgerDo(h, http.MethodPost, apiPrefix+"/integrations/github/webhook", payload, nil, "", hdr)
	}

	if rec := post("ping", "p1", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("ping status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := post("issues", "i1", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("non-push status = %d", rec.Code)
	}
	if rec := post("push", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing delivery status = %d", rec.Code)
	}
	if rec := post("push", "d1", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("push status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := post("push", "d1", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("duplicate push status = %d", rec.Code)
	}
	if len(pushes) != 1 {
		t.Fatalf("effective pushes = %d, want 1", len(pushes))
	}
}

func TestLedgerWebhookBodyLimit(t *testing.T) {
	svc := &ledgerFake{t: t, verifyWebhook: func([]byte, string) error { return errors.New("must not verify") }}
	h := ledgerTestHandler(t, svc, nil)
	big := strings.Repeat("a", maxWebhookBytes+1)
	rec := ledgerDo(h, http.MethodPost, apiPrefix+"/integrations/github/webhook", big, nil, "",
		map[string]string{"X-GitHub-Event": "push", "X-GitHub-Delivery": "d", "X-Hub-Signature-256": "sha256=x"})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestLedgerHealthProbes(t *testing.T) {
	failing := func(context.Context) []string { return []string{"secrets", "github"} }
	tests := []struct {
		name   string
		ready  func(context.Context) []string
		target string
		status int
		detail string
	}{
		{name: "healthz", target: "/healthz", status: http.StatusOK},
		{name: "readyz ok", target: "/readyz", status: http.StatusOK},
		{name: "readyz failing", ready: failing, target: "/readyz", status: http.StatusServiceUnavailable, detail: "failed checks: secrets, github"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := ledgerTestHandler(t, &ledgerFake{t: t}, tt.ready)
			rec := ledgerDo(h, http.MethodGet, tt.target, "", nil, "", nil)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if tt.detail != "" {
				if p := ledgerDecodeProblem(t, rec); p.Detail != tt.detail {
					t.Fatalf("detail = %q", p.Detail)
				}
			} else if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}

func TestLedgerFallThroughToNext(t *testing.T) {
	h := ledgerTestHandler(t, &ledgerFake{t: t}, nil)
	for _, target := range []string{"/", "/api/v1/auth/session", "/api/v1/setup/status", "/somewhere"} {
		rec := ledgerDo(h, http.MethodGet, target, "", nil, "", nil)
		if rec.Code != http.StatusTeapot {
			t.Errorf("%s status = %d, want next handler", target, rec.Code)
		}
	}
}

func TestLedgerResourcesRequireRevisionETag(t *testing.T) {
	svc := &ledgerFake{t: t,
		revisionOf: func(_ context.Context, _, changesetID string) (string, error) {
			if changesetID != "cs-2" {
				return "", fmt.Errorf("changeset %q", changesetID)
			}
			return "rev-sha", nil
		},
		withRevision: func(_ context.Context, _, revision string, fn func(root string) error) error {
			if revision != "rev-sha" {
				return fmt.Errorf("revision %q", revision)
			}
			return fn(testWorkspace(t))
		},
	}
	h := ledgerTestHandler(t, svc, nil)
	cookie, _ := ledgerLogin(t, h)
	rec := ledgerDo(h, http.MethodGet, apiPrefix+"/workspaces/homelab/resources?kind=Network&changeset=cs-2", "", cookie, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if etag := rec.Header().Get("ETag"); etag != `"rev-sha"` {
		t.Fatalf("ETag = %q", etag)
	}
}

func TestLedgerEventsReplayAndLiveFraming(t *testing.T) {
	replayed := []operations.Event{
		{ID: 5, ChangesetID: "cs-1", Type: "changeset.created", CreatedAt: time.Unix(1, 0)},
		{ID: 6, OperationID: "op-1", Type: "operation.running", CreatedAt: time.Unix(2, 0)},
	}
	live := make(chan operations.Event, 4)
	var afterSeen int64 = -1
	svc := &ledgerFake{t: t,
		subscribe: func() (<-chan operations.Event, func()) {
			return live, func() {}
		},
		eventsAfter: func(_ context.Context, after int64) ([]operations.Event, error) {
			afterSeen = after
			return replayed, nil
		},
	}
	h := ledgerTestHandler(t, svc, nil)
	cookie, _ := ledgerLogin(t, h)

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+apiPrefix+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	req.Header.Set("Last-Event-ID", "4")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q", cc)
	}

	live <- operations.Event{ID: 6, OperationID: "op-1", Type: "operation.running"} // duplicate of replay: skipped
	live <- operations.Event{ID: 7, OperationID: "op-1", Type: "operation.succeeded", ChangesetID: "cs-1"}
	live <- operations.Event{ID: 8, Type: "internal.unexposed"}
	live <- operations.Event{ID: 9, Type: "operation.failed", OperationID: "op-2"}

	frames := ledgerReadFrames(t, resp.Body, 4)
	if afterSeen != 4 {
		t.Fatalf("EventsAfter(%d), want 4", afterSeen)
	}
	want := []struct {
		id, eventType, changeset, operation string
	}{
		{"5", "nodr.changeset.changed", "cs-1", ""},
		{"6", "nodr.operation.started", "", "op-1"},
		{"7", "nodr.operation.succeeded", "cs-1", "op-1"},
		{"9", "nodr.operation.failed", "", "op-2"},
	}
	for i, frame := range frames {
		if !strings.HasPrefix(frame, "id: "+want[i].id+"\nevent: message\ndata: ") {
			t.Fatalf("frame %d = %q", i, frame)
		}
		data := strings.TrimPrefix(frame, "id: "+want[i].id+"\nevent: message\ndata: ")
		var env struct {
			SpecVersion string `json:"specversion"`
			Type        string `json:"type"`
			Source      string `json:"source"`
			ID          string `json:"id"`
			Data        struct {
				ChangesetID string `json:"changesetId"`
				OperationID string `json:"operationId"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSuffix(data, "\n\n")), &env); err != nil {
			t.Fatalf("frame %d json: %v in %q", i, err, frame)
		}
		if env.SpecVersion != "1.0" || env.Source != apiPrefix || env.ID != want[i].id || env.Type != want[i].eventType {
			t.Errorf("frame %d envelope = %#v", i, env)
		}
		if env.Data.ChangesetID != want[i].changeset || env.Data.OperationID != want[i].operation {
			t.Errorf("frame %d data = %#v", i, env.Data)
		}
	}
}

// ledgerReadFrames reads n SSE frames (each terminated by a blank line) from r.
func ledgerReadFrames(t *testing.T, r interface{ Read([]byte) (int, error) }, n int) []string {
	t.Helper()
	scanner := bufio.NewScanner(r)
	var frames []string
	var current strings.Builder
	deadline := time.After(5 * time.Second)
	lines := make(chan string)
	go func() {
		defer close(lines)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	for len(frames) < n {
		select {
		case <-deadline:
			t.Fatalf("timed out after %d frames", len(frames))
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("stream closed after %d frames", len(frames))
			}
			if line == "" {
				if current.Len() > 0 {
					frames = append(frames, current.String()+"\n")
					current.Reset()
				}
				continue
			}
			current.WriteString(line + "\n")
		}
	}
	return frames
}

func TestLedgerEventsClosesOnClientDisconnect(t *testing.T) {
	live := make(chan operations.Event)
	svc := &ledgerFake{t: t,
		subscribe:   func() (<-chan operations.Event, func()) { return live, func() {} },
		eventsAfter: func(context.Context, int64) ([]operations.Event, error) { return nil, nil },
	}
	h := ledgerTestHandler(t, svc, nil)
	cookie, _ := ledgerLogin(t, h)
	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequest(http.MethodGet, apiPrefix+"/events", nil).WithContext(ctx)
	req.AddCookie(cookie)
	done := make(chan struct{})
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after client disconnect")
	}
}
