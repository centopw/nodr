package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/centopw/nodr/internal/authn"
	"github.com/centopw/nodr/internal/ledger"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/operations"
)

// ledgerSessionCookie is the session cookie name set by authn.LoginHandler.
const ledgerSessionCookie = "nodr_session"

// maxWebhookBytes caps the GitHub webhook body that is read into memory.
const maxWebhookBytes = 5 << 20

// LedgerService is the subset of *ledger.Service used by the ledger HTTP
// surface. It exists so HTTP tests can inject a fake.
type LedgerService interface {
	Installations(ctx context.Context) ([]ledger.Installation, error)
	Connect(ctx context.Context, in ledger.ConnectInput) (ledger.Workspace, error)
	Workspaces(ctx context.Context) ([]ledger.Workspace, error)
	Workspace(ctx context.Context, slug string) (ledger.Workspace, error)
	WithRevision(ctx context.Context, slug, revision string, fn func(root string) error) error
	RevisionOf(ctx context.Context, slug, changesetID string) (string, error)
	RunCommand(ctx context.Context, in ledger.CommandInput) (ledger.CommandResult, error)
	Changesets(ctx context.Context, slug string) ([]operations.Changeset, error)
	Changeset(ctx context.Context, slug, id string) (ledger.ChangesetDetail, error)
	Plan(ctx context.Context, in ledger.PlanInput) (operations.Operation, error)
	Apply(ctx context.Context, in ledger.ApplyInput) (operations.Operation, error)
	Operation(ctx context.Context, id string) (operations.Operation, error)
	PlanSummary(ctx context.Context, operationID string) (ledger.PlanSummary, error)
	Subscribe() (<-chan operations.Event, func())
	EventsAfter(ctx context.Context, afterID int64) ([]operations.Event, error)
	HandlePush(ctx context.Context, event ledger.PushEvent) error
	VerifyWebhookSignature(payload []byte, signature string) error
}

// LedgerConfig configures LedgerHandler.
type LedgerConfig struct {
	// Service executes ledger operations.
	Service LedgerService
	// Auth authenticates session cookies and CSRF tokens for protected routes.
	Auth *authn.Store
	// Ready reports failed readiness checks by name; nil or empty means ready.
	Ready func(ctx context.Context) []string
}

// ledgerAPI holds the dependencies of the ledger HTTP handlers.
type ledgerAPI struct {
	svc   LedgerService
	auth  *authn.Store
	ready func(ctx context.Context) []string
}

// LedgerHandler serves the GitHub-ledger HTTP contract under /api/v1 plus the
// public health probes. Authenticated routes require a session cookie and,
// for mutations, an X-CSRF-Token header. Paths the ledger does not own are
// delegated to next.
func LedgerHandler(cfg LedgerConfig, next http.Handler) http.Handler {
	l := &ledgerAPI{svc: cfg.Service, auth: cfg.Auth, ready: cfg.Ready}
	guard := authn.Middleware(cfg.Auth)
	secured := func(h http.HandlerFunc) http.Handler { return guard(h) }

	mux := http.NewServeMux()
	mux.Handle("POST "+apiPrefix+"/setup/github/installations", secured(l.installations))
	mux.Handle("GET "+apiPrefix+"/workspaces", secured(l.listWorkspaces))
	mux.Handle("POST "+apiPrefix+"/workspaces", secured(l.connectWorkspace))
	mux.Handle("GET "+apiPrefix+"/workspaces/{workspace}", secured(l.getWorkspace))
	mux.Handle("GET "+apiPrefix+"/workspaces/{workspace}/resources", secured(l.listResources))
	mux.Handle("POST "+apiPrefix+"/workspaces/{workspace}/commands", secured(l.runCommand))
	mux.Handle("GET "+apiPrefix+"/workspaces/{workspace}/changesets", secured(l.listChangesets))
	mux.Handle("GET "+apiPrefix+"/workspaces/{workspace}/changesets/{id}", secured(l.getChangeset))
	mux.Handle("POST "+apiPrefix+"/workspaces/{workspace}/changesets/{idaction}", secured(l.changesetAction))
	mux.Handle("GET "+apiPrefix+"/operations/{id}", secured(l.getOperation))
	mux.Handle("GET "+apiPrefix+"/events", secured(l.events))
	mux.HandleFunc("POST "+apiPrefix+"/integrations/github/webhook", l.webhook)
	mux.HandleFunc("GET /healthz", l.healthz)
	mux.HandleFunc("GET /readyz", l.readyz)
	mux.Handle("/", next)
	return mux
}

// ledgerStatusOK is the JSON body of the health probes.
type ledgerStatusOK struct {
	Status string `json:"status"`
}

// decodeStrict decodes exactly one JSON value from r into dst, rejecting
// unknown fields and trailing data. An empty body reports io.EOF.
func decodeStrict(r io.Reader, dst any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after JSON value")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after JSON value")
	}
	return nil
}

// ifMatchSHA returns the revision named by the If-Match header, accepting
// quoted, unquoted, and weak (W/"…") forms. It reports false when absent.
func ifMatchSHA(r *http.Request) (string, bool) {
	value := strings.TrimSpace(r.Header.Get("If-Match"))
	value = strings.TrimPrefix(value, "W/")
	value = strings.Trim(value, `"`)
	return value, value != ""
}

// quoteETag formats sha as a strong entity tag.
func quoteETag(sha string) string {
	return `"` + sha + `"`
}

// requireIfMatch reads If-Match or writes 428 and reports false.
func requireIfMatch(w http.ResponseWriter, r *http.Request) (string, bool) {
	sha, ok := ifMatchSHA(r)
	if !ok {
		writeProblem(w, http.StatusPreconditionRequired, "Precondition required",
			"an If-Match header carrying the current revision is required", nil)
		return "", false
	}
	return sha, true
}

// requireIdempotencyKey reads Idempotency-Key or writes 400 and reports false.
func requireIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "the Idempotency-Key header is required",
			[]fieldError{{Path: "Idempotency-Key", Message: "is required"}})
		return "", false
	}
	return key, true
}

// ledgerActor names the session account that issued r, or "unknown".
func (l *ledgerAPI) ledgerActor(r *http.Request) string {
	cookie, err := r.Cookie(ledgerSessionCookie)
	if err != nil || l.auth == nil {
		return "unknown"
	}
	if name, ok := l.auth.SessionUsername(r.Context(), cookie.Value); ok {
		return name
	}
	return "unknown"
}

// ledgerFileDiff is one file change in a changeset diff.
type ledgerFileDiff struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Patch  string `json:"patch"`
}

func ledgerDiffs(diffs []ledger.FileDiff) []ledgerFileDiff {
	out := make([]ledgerFileDiff, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, ledgerFileDiff{Path: d.Path, Status: d.Status, Patch: d.Patch})
	}
	return out
}

// ledgerWorkspace is the JSON shape of a connected workspace.
type ledgerWorkspace struct {
	Name          string `json:"name"`
	Owner         string `json:"owner"`
	Repository    string `json:"repository"`
	DefaultBranch string `json:"defaultBranch"`
	HeadSHA       string `json:"headSha"`
}

func ledgerWorkspaceOf(ws ledger.Workspace) ledgerWorkspace {
	return ledgerWorkspace{
		Name: ws.Slug, Owner: ws.Owner, Repository: ws.Repository,
		DefaultBranch: ws.DefaultBranch, HeadSHA: ws.DefaultSHA,
	}
}

// ledgerChangeset is the JSON shape of a changeset.
type ledgerChangeset struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	BaseSHA  string `json:"baseSha"`
	DraftSHA string `json:"draftSha"`
	Revision int64  `json:"revision"`
}

func ledgerChangesetOf(cs operations.Changeset) ledgerChangeset {
	return ledgerChangeset{
		ID: cs.ID, Status: string(cs.Status), BaseSHA: cs.BaseSHA,
		DraftSHA: cs.DraftSHA, Revision: cs.Revision,
	}
}

// ledgerOperation is the JSON shape of an operation.
type ledgerOperation struct {
	ID          string              `json:"id"`
	Kind        string              `json:"kind"`
	Status      string              `json:"status"`
	ChangesetID string              `json:"changesetId"`
	PlanSHA     string              `json:"planSha"`
	Result      any                 `json:"result"`
	Plan        *ledger.PlanSummary `json:"plan,omitempty"`
}

func ledgerOperationOf(op operations.Operation) ledgerOperation {
	return ledgerOperation{
		ID: op.ID, Kind: op.Kind, Status: string(op.Status),
		ChangesetID: op.ChangesetID, PlanSHA: op.PlanSHA, Result: ledgerResult(op.Result),
	}
}

// ledgerResult emits stored result text as raw JSON when it is valid JSON,
// as a JSON string otherwise, and null when empty.
func ledgerResult(stored string) any {
	switch {
	case stored == "":
		return nil
	case json.Valid([]byte(stored)):
		return json.RawMessage(stored)
	default:
		return stored
	}
}

// writeLedgerError maps a ledger service error to an RFC 9457 problem.
func writeLedgerError(w http.ResponseWriter, err error) {
	var cmdErr *ledger.CommandError
	switch {
	case errors.Is(err, ledger.ErrStaleRevision):
		writeProblem(w, http.StatusPreconditionFailed, "PRECONDITION_FAILED",
			"the revision changed since it was read; reload and retry", nil)
	case errors.Is(err, ledger.ErrPlanChanged):
		writeProblem(w, http.StatusConflict, "PLAN_CHANGED",
			"the changeset changed after it was planned; plan again", nil)
	case errors.Is(err, ledger.ErrPlanRequired):
		writeProblem(w, http.StatusConflict, "PLAN_REQUIRED", "the changeset has not been planned", nil)
	case errors.Is(err, ledger.ErrChangesetClosed):
		writeProblem(w, http.StatusConflict, "CHANGESET_CLOSED", "the changeset is closed", nil)
	case errors.Is(err, ledger.ErrWorkspaceExists):
		writeProblem(w, http.StatusConflict, "Workspace exists", "the workspace is already connected", nil)
	case errors.Is(err, ledger.ErrCommandInFlight):
		writeProblem(w, http.StatusConflict, "Command in flight", "a command for this changeset is already running", nil)
	case errors.Is(err, ledger.ErrWorkspaceNotFound):
		writeProblem(w, http.StatusNotFound, "Workspace not found", "the workspace does not exist", nil)
	case errors.Is(err, ledger.ErrChangesetNotFound), errors.Is(err, operations.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "Not found", "the requested resource does not exist", nil)
	case errors.Is(err, ledger.ErrRepositoryInaccessible), errors.Is(err, ledger.ErrNotNodrRepository):
		writeProblem(w, http.StatusUnprocessableEntity, "Repository rejected", err.Error(),
			[]fieldError{{Path: "repository", Message: err.Error()}})
	case errors.Is(err, ledger.ErrDestructiveNeedsApproval):
		writeProblem(w, http.StatusUnprocessableEntity, "Destructive changes need approval", err.Error(),
			[]fieldError{{Path: "allowDestroy", Message: err.Error()}})
	case errors.As(err, &cmdErr) && cmdErr.Err != nil:
		status := cmdErr.Err.Status
		if status == 0 {
			status = http.StatusBadRequest
		}
		var errs []fieldError
		for _, f := range cmdErr.Err.Fields {
			errs = append(errs, fieldError{Path: f.Path, Message: f.Message})
		}
		writeProblem(w, status, cmdErr.Err.Title, cmdErr.Err.Detail, errs)
	default:
		writeProblem(w, http.StatusInternalServerError, "Internal server error",
			"the request could not be completed", nil)
	}
}

// installations handles POST /setup/github/installations.
func (l *ledgerAPI) installations(w http.ResponseWriter, r *http.Request) {
	items, err := l.svc.Installations(r.Context())
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	type repository struct {
		Owner         string `json:"owner"`
		Name          string `json:"name"`
		DefaultBranch string `json:"defaultBranch"`
		Private       bool   `json:"private"`
	}
	type installation struct {
		ID           int64        `json:"id"`
		Account      string       `json:"account"`
		Repositories []repository `json:"repositories"`
	}
	out := make([]installation, 0, len(items))
	for _, in := range items {
		repos := make([]repository, 0, len(in.Repositories))
		for _, rp := range in.Repositories {
			repos = append(repos, repository{Owner: rp.Owner, Name: rp.Name, DefaultBranch: rp.DefaultBranch, Private: rp.Private})
		}
		out = append(out, installation{ID: in.ID, Account: in.Account, Repositories: repos})
	}
	writeJSON(w, http.StatusOK, struct {
		Installations []installation `json:"installations"`
	}{Installations: out})
}

// listWorkspaces handles GET /workspaces.
func (l *ledgerAPI) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	items, err := l.svc.Workspaces(r.Context())
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	out := make([]ledgerWorkspace, 0, len(items))
	for _, ws := range items {
		out = append(out, ledgerWorkspaceOf(ws))
	}
	writeJSON(w, http.StatusOK, struct {
		Items []ledgerWorkspace `json:"items"`
	}{Items: out})
}

// connectWorkspace handles POST /workspaces.
func (l *ledgerAPI) connectWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstallationID int64  `json:"installationId"`
		Owner          string `json:"owner"`
		Repository     string `json:"repository"`
	}
	if err := decodeStrict(r.Body, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "request body must be JSON with installationId, owner and repository", nil)
		return
	}
	ws, err := l.svc.Connect(r.Context(), ledger.ConnectInput{
		InstallationID: req.InstallationID, Owner: req.Owner, Repository: req.Repository,
	})
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ledgerWorkspaceOf(ws))
}

// getWorkspace handles GET /workspaces/{workspace}. The environment list is
// read from the default-branch checkout; ETag is the default-branch SHA.
func (l *ledgerAPI) getWorkspace(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("workspace")
	ws, err := l.svc.Workspace(r.Context(), slug)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	err = l.svc.WithRevision(r.Context(), slug, "", func(root string) error {
		s := &server{root: root, plans: map[string]*storedPlan{}}
		s.getWorkspaceWithHead(w, ws)
		return nil
	})
	if err != nil {
		writeLedgerError(w, err)
	}
}

// getWorkspaceWithHead writes the workspace representation using the legacy
// manifest-reading logic plus the ledger identity fields.
func (a *server) getWorkspaceWithHead(w http.ResponseWriter, ws ledger.Workspace) {
	loaded, ok := a.load(w)
	if !ok {
		return
	}
	if loaded.Manifest.Metadata.Name != ws.Slug {
		writeProblem(w, http.StatusNotFound, "Workspace not found", "the workspace manifest name does not match", nil)
		return
	}
	manifest, err := v1alpha1.Decode[v1alpha1.WorkspaceSpec](loaded.Manifest)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Workspace decode failed", err.Error(), nil)
		return
	}
	environments := make(map[string]v1alpha1.Environment, len(manifest.Spec.Environments))
	for name, environment := range manifest.Spec.Environments {
		if name != "templates" {
			environments[name] = environment
		}
	}
	w.Header().Set("ETag", quoteETag(ws.DefaultSHA))
	writeJSON(w, http.StatusOK, struct {
		Name         string                          `json:"name"`
		Owner        string                          `json:"owner"`
		Repository   string                          `json:"repository"`
		HeadSHA      string                          `json:"headSha"`
		Environments map[string]v1alpha1.Environment `json:"environments"`
	}{
		Name: manifest.Metadata.Name, Owner: ws.Owner, Repository: ws.Repository,
		HeadSHA: ws.DefaultSHA, Environments: environments,
	})
}

// listResources handles GET /workspaces/{workspace}/resources. It reads the
// checkout at the changeset draft (or default HEAD) and reuses the legacy
// resource listing; ETag is the revision SHA.
func (l *ledgerAPI) listResources(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("workspace")
	revision, err := l.svc.RevisionOf(r.Context(), slug, r.URL.Query().Get("changeset"))
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	err = l.svc.WithRevision(r.Context(), slug, revision, func(root string) error {
		s := &server{root: root, plans: map[string]*storedPlan{}}
		w.Header().Set("ETag", quoteETag(revision))
		s.listResources(w, r)
		return nil
	})
	if err != nil {
		writeLedgerError(w, err)
	}
}

// ledgerCommandRequest is the body of POST /workspaces/{w}/commands.
type ledgerCommandRequest struct {
	Command   string          `json:"command"`
	Params    json.RawMessage `json:"params"`
	Changeset string          `json:"changeset,omitempty"`
}

// runCommand handles POST /workspaces/{workspace}/commands.
func (l *ledgerAPI) runCommand(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("workspace")
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	key, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	var req ledgerCommandRequest
	if err := decodeStrict(r.Body, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "request body must be JSON with command, params and optional changeset",
			[]fieldError{{Path: "body", Message: err.Error()}})
		return
	}
	if req.Command == "" {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "command is required",
			[]fieldError{{Path: "command", Message: "is required"}})
		return
	}
	res, err := l.svc.RunCommand(r.Context(), ledger.CommandInput{
		Workspace: slug, ChangesetID: req.Changeset, ExpectedSHA: expected,
		Actor: l.ledgerActor(r), Command: req.Command, IdempotencyKey: key, Params: req.Params,
	})
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	w.Header().Set("ETag", quoteETag(res.Changeset.DraftSHA))
	writeJSON(w, http.StatusOK, struct {
		Changeset ledgerChangeset  `json:"changeset"`
		Diff      []ledgerFileDiff `json:"diff"`
		Result    any              `json:"result"`
	}{Changeset: ledgerChangesetOf(res.Changeset), Diff: ledgerDiffs(res.Diff), Result: res.Result})
}

// listChangesets handles GET /workspaces/{workspace}/changesets.
func (l *ledgerAPI) listChangesets(w http.ResponseWriter, r *http.Request) {
	items, err := l.svc.Changesets(r.Context(), r.PathValue("workspace"))
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	out := make([]ledgerChangeset, 0, len(items))
	for _, cs := range items {
		out = append(out, ledgerChangesetOf(cs))
	}
	writeJSON(w, http.StatusOK, struct {
		Items []ledgerChangeset `json:"items"`
	}{Items: out})
}

// getChangeset handles GET /workspaces/{workspace}/changesets/{id}.
func (l *ledgerAPI) getChangeset(w http.ResponseWriter, r *http.Request) {
	detail, err := l.svc.Changeset(r.Context(), r.PathValue("workspace"), r.PathValue("id"))
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Changeset ledgerChangeset  `json:"changeset"`
		Diff      []ledgerFileDiff `json:"diff"`
	}{Changeset: ledgerChangesetOf(detail.Changeset), Diff: ledgerDiffs(detail.Diff)})
}

// changesetAction dispatches POST …/changesets/{id}:plan and :apply. The Go
// mux cannot match a literal suffix after a wildcard, so the segment is split
// on its last colon here.
func (l *ledgerAPI) changesetAction(w http.ResponseWriter, r *http.Request) {
	segment := r.PathValue("idaction")
	colon := strings.LastIndexByte(segment, ':')
	if colon <= 0 || colon == len(segment)-1 {
		writeProblem(w, http.StatusNotFound, "Not found", "the requested API path does not exist", nil)
		return
	}
	id, action := segment[:colon], segment[colon+1:]
	switch action {
	case "plan":
		l.planChangeset(w, r, id)
	case "apply":
		l.applyChangeset(w, r, id)
	default:
		writeProblem(w, http.StatusNotFound, "Not found", "the requested API path does not exist", nil)
	}
}

// planChangeset starts a plan operation for a changeset draft.
func (l *ledgerAPI) planChangeset(w http.ResponseWriter, r *http.Request, id string) {
	slug := r.PathValue("workspace")
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	key, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	op, err := l.svc.Plan(r.Context(), ledger.PlanInput{
		Workspace: slug, ChangesetID: id, ExpectedSHA: expected,
		Actor: l.ledgerActor(r), IdempotencyKey: key,
	})
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Operation ledgerOperation `json:"operation"`
	}{Operation: ledgerOperationOf(op)})
}

// applyChangeset starts an apply operation for a planned changeset.
func (l *ledgerAPI) applyChangeset(w http.ResponseWriter, r *http.Request, id string) {
	slug := r.PathValue("workspace")
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	key, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	var req struct {
		AllowDestroy bool `json:"allowDestroy"`
	}
	if err := decodeStrict(r.Body, &req); err != nil && !errors.Is(err, io.EOF) {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "request body must be JSON with optional allowDestroy",
			[]fieldError{{Path: "body", Message: err.Error()}})
		return
	}
	op, err := l.svc.Apply(r.Context(), ledger.ApplyInput{
		Workspace: slug, ChangesetID: id, ExpectedSHA: expected,
		Actor: l.ledgerActor(r), IdempotencyKey: key, AllowDestroy: req.AllowDestroy,
	})
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Operation ledgerOperation `json:"operation"`
	}{Operation: ledgerOperationOf(op)})
}

// getOperation handles GET /operations/{id}. Plan operations with a saved
// artifact include the normalized plan summary.
func (l *ledgerAPI) getOperation(w http.ResponseWriter, r *http.Request) {
	op, err := l.svc.Operation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	out := ledgerOperationOf(op)
	if op.Kind == "plan" && op.Status == operations.OperationSucceeded {
		summary, err := l.svc.PlanSummary(r.Context(), op.ID)
		switch {
		case err == nil:
			out.Plan = &summary
		case !errors.Is(err, operations.ErrNotFound):
			writeLedgerError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// webhook handles POST /integrations/github/webhook. The signature is verified
// over the raw body before anything is parsed.
func (l *ledgerAPI) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	if err != nil {
		writeProblem(w, http.StatusRequestEntityTooLarge, "Payload too large", "the webhook body exceeds 5 MiB", nil)
		return
	}
	if err := l.svc.VerifyWebhookSignature(body, r.Header.Get("X-Hub-Signature-256")); err != nil {
		writeProblem(w, http.StatusUnauthorized, "Unauthorized", "the webhook signature is invalid", nil)
		return
	}
	delivery := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if delivery == "" {
		writeProblem(w, http.StatusBadRequest, "Invalid request", "the X-GitHub-Delivery header is required",
			[]fieldError{{Path: "X-GitHub-Delivery", Message: "is required"}})
		return
	}
	switch r.Header.Get("X-GitHub-Event") {
	case "ping":
		writeJSON(w, http.StatusOK, struct {
			OK bool `json:"ok"`
		}{OK: true})
	case "push":
		event, err := ledger.ParsePushEvent(delivery, body)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid request", "the push payload could not be parsed", nil)
			return
		}
		if err := l.svc.HandlePush(r.Context(), event); err != nil {
			writeLedgerError(w, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// healthz is the liveness probe.
func (l *ledgerAPI) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, ledgerStatusOK{Status: "ok"})
}

// readyz reports 503 with the failed check names, or 200 when all pass.
func (l *ledgerAPI) readyz(w http.ResponseWriter, r *http.Request) {
	var failed []string
	if l.ready != nil {
		failed = l.ready(r.Context())
	}
	if len(failed) == 0 {
		writeJSON(w, http.StatusOK, ledgerStatusOK{Status: "ok"})
		return
	}
	detail := "failed checks: " + strings.Join(failed, ", ")
	errs := make([]fieldError, 0, len(failed))
	for _, name := range failed {
		errs = append(errs, fieldError{Path: name, Message: "check failed"})
	}
	writeProblem(w, http.StatusServiceUnavailable, "Not ready", detail, errs)
}
