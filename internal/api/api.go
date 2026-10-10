// Package api serves nodr's versioned HTTP API.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/centopw/nodr/internal/authn"
	"github.com/centopw/nodr/internal/commands"
	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/engine/opentofu"
	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/planapply"
	"github.com/centopw/nodr/internal/proxmox"
	"github.com/centopw/nodr/internal/proxmoxbootstrap"
	"github.com/centopw/nodr/internal/proxmoxdiscovery"
	"github.com/centopw/nodr/internal/secrets"
	"github.com/centopw/nodr/internal/workspace"
)

const apiPrefix = "/api/v1"

// Handler returns the API handler for the workspace rooted at root. The
// workspace is loaded from disk for every request, so responses and commands
// always operate on current intent rather than a process-local cache.
func Handler(ctx context.Context, root string, auth *authn.Store, secretsStore *secrets.Store) http.Handler {
	return handler(ctx, root, auth, secretsStore, true)
}

// HandlerWithoutSetup returns the ordinary workspace API after first-run setup
// has completed. It omits POST /setup so setup cannot repeat, but keeps the
// public GET /setup/status probe the SPA uses to choose login over setup.
func HandlerWithoutSetup(ctx context.Context, root string, auth *authn.Store, secretsStore *secrets.Store) http.Handler {
	return handler(ctx, root, auth, secretsStore, false)
}

func handler(ctx context.Context, root string, auth *authn.Store, secretsStore *secrets.Store, setup bool) http.Handler {
	a := &server{
		ctx:     ctx,
		root:    root,
		plans:   make(map[string]*storedPlan),
		secrets: secretsStore,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+apiPrefix+"/workspaces", a.listWorkspaces)
	mux.HandleFunc("GET "+apiPrefix+"/workspaces/{workspace}", a.getWorkspace)
	mux.HandleFunc("GET "+apiPrefix+"/workspaces/{workspace}/resources", a.listResources)
	mux.HandleFunc("POST "+apiPrefix+"/workspaces/{workspace}/commands", a.runCommand)
	mux.HandleFunc("POST "+apiPrefix+"/workspaces/{workspace}/resources/{kind}/{name}", a.resourceAction)
	mux.HandleFunc("DELETE "+apiPrefix+"/workspaces/{workspace}/resources/{kind}/{name}", a.deleteResource)
	mux.HandleFunc(apiPrefix+"/workspaces", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}/resources", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}/commands", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}/resources/{kind}/{name}", methodNotAllowed)
	mux.HandleFunc("/api/", notFound)

	public := http.NewServeMux()
	public.HandleFunc("POST "+apiPrefix+"/auth/login", func(w http.ResponseWriter, r *http.Request) {
		authn.LoginHandler(auth, apiPrefix+"/auth/login").ServeHTTP(w, r)
	})
	public.Handle("GET "+apiPrefix+"/setup/status", authn.SetupStatusHandler(auth))
	if setup {
		public.Handle("POST "+apiPrefix+"/setup", authn.SetupHandler(auth))
	}

	protected := http.NewServeMux()
	protected.Handle("POST "+apiPrefix+"/auth/logout", authn.LogoutHandler(auth, apiPrefix+"/auth/logout"))
	protected.Handle("GET "+apiPrefix+"/auth/session", authn.SessionHandler(auth))
	protected.Handle("/", mux)

	root2 := http.NewServeMux()
	root2.Handle("POST "+apiPrefix+"/auth/login", public)
	root2.Handle("GET "+apiPrefix+"/setup/status", public)
	if setup {
		root2.Handle("POST "+apiPrefix+"/setup", public)
	}
	root2.Handle("/", authn.Middleware(auth)(protected))
	return root2
}

type storedPlan struct {
	id        string
	dir       string
	plans     []planapply.UnitPlan
	createdAt time.Time
}

type server struct {
	ctx      context.Context
	root     string
	createMu sync.Mutex
	planMu   sync.Mutex
	plansMu  sync.Mutex
	plans    map[string]*storedPlan
	secrets  *secrets.Store
}

type fieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type problem struct {
	Type   string             `json:"type"`
	Title  string             `json:"title"`
	Status int                `json:"status"`
	Detail string             `json:"detail"`
	Errors []fieldError       `json:"errors,omitempty"`
	Units  []applyUnitOutcome `json:"units,omitempty"`
}

func methodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	writeProblem(w, http.StatusMethodNotAllowed, "Method not allowed", "the requested method is not supported", nil)
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeProblem(w, http.StatusNotFound, "Not found", "the requested API path does not exist", nil)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeProblem(w http.ResponseWriter, status int, title, detail string, errs []fieldError) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type: "about:blank", Title: title, Status: status, Detail: detail, Errors: errs,
	})
}

func writeProblemWithUnits(w http.ResponseWriter, status int, title, detail string, units []applyUnitOutcome) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: detail,
		Units:  units,
	})
}

func (a *server) load(w http.ResponseWriter) (*workspace.Workspace, bool) {
	ws, diags := workspace.Load(a.root)
	if diags.HasErrors() {
		writeDiagnosticProblem(w, http.StatusInternalServerError, "Workspace load failed", diags)
		return nil, false
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal server error", err.Error(), nil)
		return nil, false
	}
	diags.Append(ws.Validate(reg))
	if diags.HasErrors() {
		writeDiagnosticProblem(w, http.StatusInternalServerError, "Workspace validation failed", diags)
		return nil, false
	}
	return ws, true
}

func writeDiagnosticProblem(w http.ResponseWriter, status int, title string, diags diag.List) {
	var (
		errs     []fieldError
		messages []string
	)
	for _, d := range diags {
		if d.Severity != diag.Error {
			continue
		}
		messages = append(messages, d.Message)
		if d.Path != "" {
			errs = append(errs, fieldError{Path: d.Path, Message: d.Message})
		}
	}
	writeProblem(w, status, title, strings.Join(messages, "; "), errs)
}

func (a *server) workspace(w http.ResponseWriter, r *http.Request) (*workspace.Workspace, bool) {
	ws, ok := a.load(w)
	if !ok {
		return nil, false
	}
	if r.PathValue("workspace") != ws.Manifest.Metadata.Name {
		writeProblem(w, http.StatusNotFound, "Workspace not found", fmt.Sprintf("workspace %q does not exist", r.PathValue("workspace")), nil)
		return nil, false
	}
	return ws, true
}

func (a *server) listWorkspaces(w http.ResponseWriter, _ *http.Request) {
	ws, ok := a.load(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []namedItem `json:"items"`
	}{Items: []namedItem{{Name: ws.Manifest.Metadata.Name}}})
}

type namedItem struct {
	Kind string `json:"kind,omitempty"`
	Name string `json:"name"`
}

func (a *server) getWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, ok := a.workspace(w, r)
	if !ok {
		return
	}
	manifest, err := v1alpha1.Decode[v1alpha1.WorkspaceSpec](ws.Manifest)
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
	writeJSON(w, http.StatusOK, struct {
		Name         string                          `json:"name"`
		Environments map[string]v1alpha1.Environment `json:"environments"`
	}{Name: manifest.Metadata.Name, Environments: environments})
}

type virtualMachineItem struct {
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Cluster     string   `json:"cluster"`
	Node        string   `json:"node"`
	VMID        int      `json:"vmid"`
	CPU         int      `json:"cpu"`
	Memory      string   `json:"memory"`
	Environment string   `json:"environment"`
	Addresses   []string `json:"addresses"`
	PowerState  string   `json:"powerState"`
}

func (a *server) listResources(w http.ResponseWriter, r *http.Request) {
	ws, ok := a.workspace(w, r)
	if !ok {
		return
	}
	kind := r.URL.Query().Get("kind")
	supported := []string{
		v1alpha1.KindVirtualMachine,
		v1alpha1.KindProxmoxCluster,
		v1alpha1.KindNetwork,
		v1alpha1.KindTemplate,
	}
	if !slices.Contains(supported, kind) {
		message := "kind is required and must be one of VirtualMachine, ProxmoxCluster, Network or Template"
		writeProblem(w, http.StatusBadRequest, "Invalid resource kind", message, []fieldError{{Path: "kind", Message: message}})
		return
	}
	if kind != v1alpha1.KindVirtualMachine {
		items := make([]namedItem, 0, len(ws.OfKind(kind)))
		for _, document := range ws.OfKind(kind) {
			items = append(items, namedItem{Kind: kind, Name: document.Metadata.Name})
		}
		writeJSON(w, http.StatusOK, struct {
			Items []namedItem `json:"items"`
		}{Items: items})
		return
	}

	items := make([]virtualMachineItem, 0, len(ws.OfKind(kind)))
	for _, document := range ws.OfKind(kind) {
		vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](document)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Resource decode failed", err.Error(), nil)
			return
		}
		addresses, _ := commands.NetworkValues(vm.Spec.NICs)
		powerState := vm.Spec.Lifecycle.PowerState
		if powerState == "" {
			powerState = v1alpha1.PowerStateRunning
		}
		items = append(items, virtualMachineItem{
			Kind: kind, Name: vm.Metadata.Name, Cluster: vm.Spec.Placement.Cluster,
			Node: vm.Spec.Placement.AssignedNode, VMID: vm.Spec.Identity.VMID,
			CPU: vm.Spec.Resources.CPU.Cores, Memory: vm.Spec.Resources.Memory.Size.String(),
			Environment: vm.Metadata.Labels[nrm.LabelEnvironment], Addresses: addresses,
			PowerState: powerState,
		})
	}
	writeJSON(w, http.StatusOK, struct {
		Items []virtualMachineItem `json:"items"`
	}{Items: items})
}

type commandRequest struct {
	Command string          `json:"command"`
	Target  string          `json:"target,omitempty"`
	Params  json.RawMessage `json:"params"`
}

type planUnitSummaryResponse struct {
	Create  int `json:"create"`
	Update  int `json:"update"`
	Replace int `json:"replace"`
	Delete  int `json:"delete"`
}

type planChangeResponse struct {
	Address string `json:"address"`
	Action  string `json:"action"`
}

type planUnitResponse struct {
	Dir         string                  `json:"dir"`
	HasChanges  bool                    `json:"hasChanges"`
	Summary     planUnitSummaryResponse `json:"summary"`
	Destructive bool                    `json:"destructive"`
	Changes     []planChangeResponse    `json:"changes"`
}

type workspacePlanResponse struct {
	PlanID                string             `json:"planId"`
	Units                 []planUnitResponse `json:"units"`
	HasChanges            bool               `json:"hasChanges"`
	HasDestructiveChanges bool               `json:"hasDestructiveChanges"`
}

type workspaceApplyParams struct {
	PlanID       string `json:"planId"`
	AllowDestroy bool   `json:"allowDestroy"`
}

type applyUnitOutcome struct {
	Dir     string `json:"dir"`
	Outcome string `json:"outcome"`
}

type workspaceApplyResponse struct {
	Units []applyUnitOutcome `json:"units"`
}

func (a *server) runCommand(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.workspace(w, r); !ok {
		return
	}
	var request commandRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", fmt.Sprintf("decode JSON body: %v", err), nil)
		return
	}
	switch request.Command {
	case "vm.create":
		params, err := commands.ParseVMCreateParams(request.Params)
		if err != nil {
			writeCommandError(w, err)
			return
		}
		a.createVM(w, params)
	case "vm.start":
		if name, ok := decodeActionParams(w, request); ok {
			a.setVMPowerState(w, name, v1alpha1.PowerStateRunning)
		}
	case "vm.stop":
		if name, ok := decodeActionParams(w, request); ok {
			a.setVMPowerState(w, name, v1alpha1.PowerStateStopped)
		}
	case "vm.delete":
		if name, ok := decodeActionParams(w, request); ok {
			a.deleteVM(w, name)
		}
	case "workspace.plan":
		a.planWorkspace(w, r)
	case "workspace.apply":
		var params workspaceApplyParams
		if len(request.Params) > 0 {
			dec := json.NewDecoder(bytes.NewReader(request.Params))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&params); err != nil {
				writeProblem(w, http.StatusBadRequest, "Invalid request", fmt.Sprintf("decode params: %v", err), nil)
				return
			}
		}
		a.applyWorkspace(w, params)
	case "cluster.discover":
		a.clusterDiscover(w, r, request)
	default:
		message := fmt.Sprintf("command %q is not supported", request.Command)
		writeProblem(w, http.StatusBadRequest, "Unknown command", message, []fieldError{{Path: "command", Message: message}})
	}
}

// createVM admits a new VirtualMachine under createMu. commands.CreateVM
// reloads the workspace itself, so no caller-loaded state is used.
func (a *server) createVM(w http.ResponseWriter, params commands.VMCreateParams) {
	a.createMu.Lock()
	defer a.createMu.Unlock()
	result, err := commands.CreateVM(a.root, params)
	if err != nil {
		writeCommandError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// writeCommandError writes the RFC 9457 problem for a *commands.Error. Any
// other error is reported as an internal server error.
func writeCommandError(w http.ResponseWriter, err error) {
	var cerr *commands.Error
	if !errors.As(err, &cerr) {
		writeProblem(w, http.StatusInternalServerError, "Internal server error", err.Error(), nil)
		return
	}
	fields := make([]fieldError, 0, len(cerr.Fields))
	for _, f := range cerr.Fields {
		fields = append(fields, fieldError{Path: f.Path, Message: f.Message})
	}
	writeProblem(w, cerr.Status, cerr.Title, cerr.Detail, fields)
}

// decodeActionParams resolves the VirtualMachine name of a vm.start, vm.stop
// or vm.delete command, writing the problem response when it is missing.
func decodeActionParams(w http.ResponseWriter, req commandRequest) (string, bool) {
	name, err := commands.ActionName(req.Params, req.Target)
	if err != nil {
		writeCommandError(w, err)
		return "", false
	}
	return name, true
}

func (a *server) resourceAction(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.workspace(w, r); !ok {
		return
	}
	kind := r.PathValue("kind")
	rawName := r.PathValue("name")

	name, op, hasOp := strings.Cut(rawName, ":")
	if !hasOp || op == "" {
		writeProblem(w, http.StatusBadRequest, "Invalid operation", "missing operation suffix (e.g. :start, :stop, :delete)", nil)
		return
	}
	if kind != v1alpha1.KindVirtualMachine {
		message := fmt.Sprintf("resource kind %q does not support lifecycle actions", kind)
		writeProblem(w, http.StatusBadRequest, "Invalid resource kind", message, nil)
		return
	}
	switch op {
	case "start":
		a.setVMPowerState(w, name, v1alpha1.PowerStateRunning)
	case "stop":
		a.setVMPowerState(w, name, v1alpha1.PowerStateStopped)
	case "delete":
		a.deleteVM(w, name)
	default:
		message := fmt.Sprintf("operation %q is not supported; must be :start, :stop, or :delete", op)
		writeProblem(w, http.StatusBadRequest, "Unknown operation", message, nil)
	}
}

func (a *server) deleteResource(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.workspace(w, r); !ok {
		return
	}
	kind := r.PathValue("kind")
	rawName := r.PathValue("name")
	name, _, _ := strings.Cut(rawName, ":")

	if kind != v1alpha1.KindVirtualMachine {
		message := fmt.Sprintf("resource kind %q does not support deletion via this endpoint", kind)
		writeProblem(w, http.StatusBadRequest, "Invalid resource kind", message, nil)
		return
	}
	a.deleteVM(w, name)
}

func (a *server) setVMPowerState(w http.ResponseWriter, name string, powerState string) {
	a.createMu.Lock()
	defer a.createMu.Unlock()
	result, err := commands.SetVMPowerState(a.root, name, powerState)
	if err != nil {
		writeCommandError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *server) deleteVM(w http.ResponseWriter, name string) {
	a.createMu.Lock()
	defer a.createMu.Unlock()
	result, err := commands.DeleteVM(a.root, name)
	if err != nil {
		writeCommandError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *server) getStoredPlan(id string) (*storedPlan, bool) {
	a.plansMu.Lock()
	defer a.plansMu.Unlock()
	stored, ok := a.plans[id]
	if !ok {
		return nil, false
	}
	if time.Since(stored.createdAt) > 15*time.Minute {
		delete(a.plans, id)
		_ = os.RemoveAll(stored.dir)
		return nil, false
	}
	return stored, true
}

func (a *server) removeStoredPlan(id string) {
	a.plansMu.Lock()
	defer a.plansMu.Unlock()
	if stored, ok := a.plans[id]; ok {
		delete(a.plans, id)
		_ = os.RemoveAll(stored.dir)
	}
}

func (a *server) planWorkspace(w http.ResponseWriter, r *http.Request) {
	if !a.planMu.TryLock() {
		writeProblem(w, http.StatusConflict, "Conflict", "another plan or apply is already running", nil)
		return
	}
	defer a.planMu.Unlock()

	ws, ok := a.load(w)
	if !ok {
		return
	}

	planDir, err := os.MkdirTemp("", "nodr-api-plan-")
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal server error", fmt.Sprintf("create plan temp dir: %v", err), nil)
		return
	}

	var resolve planapply.Resolver
	if a.secrets != nil {
		resolve = a.secrets.Resolve
	}
	_, plans, err := planapply.PlanUnits(r.Context(), ws, nil, planDir, nil, nil, resolve)
	if err != nil {
		_ = os.RemoveAll(planDir)
		var compErr *planapply.CompileError
		if errors.As(err, &compErr) {
			writeDiagnosticProblem(w, http.StatusBadRequest, "Compilation failed", compErr.Diagnostics)
			return
		}
		if errors.Is(err, opentofu.ErrNotFound) {
			writeProblem(w, http.StatusServiceUnavailable, "OpenTofu not found", "OpenTofu is not installed or not on PATH; install it or set NODR_TOFU", nil)
			return
		}
		var cmdErr *opentofu.CommandError
		if errors.As(err, &cmdErr) {
			unitDir := planapply.UnitDir(err)
			if unitDir == "" {
				unitDir = cmdErr.Dir
			}
			detail := fmt.Sprintf("%s: %s", unitDir, cmdErr.Command)
			if cmdErr.Stderr != "" {
				detail = fmt.Sprintf("%s: %s: %s", unitDir, cmdErr.Command, strings.TrimSpace(cmdErr.Stderr))
			}
			writeProblem(w, http.StatusBadGateway, "OpenTofu error", detail, nil)
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Internal server error", err.Error(), nil)
		return
	}

	planID := nrm.NewUID()
	a.plansMu.Lock()
	a.plans[planID] = &storedPlan{
		id:        planID,
		dir:       planDir,
		plans:     plans,
		createdAt: time.Now(),
	}
	a.plansMu.Unlock()

	unitsResp := make([]planUnitResponse, 0, len(plans))
	hasAnyChanges := false
	hasAnyDestructive := false

	for _, u := range plans {
		s := u.Plan.Summary()
		unitSummary := planUnitSummaryResponse{
			Create:  s.Create,
			Update:  s.Update,
			Replace: s.Replace,
			Delete:  s.Delete,
		}
		changes := make([]planChangeResponse, 0)
		unitDestructive := false
		for _, c := range u.Plan.Changes {
			if c.Action == opentofu.Replace || c.Action == opentofu.Delete {
				unitDestructive = true
			}
			if c.Action != opentofu.NoOp {
				changes = append(changes, planChangeResponse{
					Address: c.Address,
					Action:  string(c.Action),
				})
			}
		}
		unitHasChanges := u.Plan.HasChanges()
		if unitHasChanges {
			hasAnyChanges = true
		}
		if unitDestructive {
			hasAnyDestructive = true
		}
		unitsResp = append(unitsResp, planUnitResponse{
			Dir:         u.Dir,
			HasChanges:  unitHasChanges,
			Summary:     unitSummary,
			Destructive: unitDestructive,
			Changes:     changes,
		})
	}

	writeJSON(w, http.StatusCreated, workspacePlanResponse{
		PlanID:                planID,
		Units:                 unitsResp,
		HasChanges:            hasAnyChanges,
		HasDestructiveChanges: hasAnyDestructive,
	})
}

func (a *server) applyWorkspace(w http.ResponseWriter, params workspaceApplyParams) {
	if strings.TrimSpace(params.PlanID) == "" {
		message := "params.planId is required"
		writeProblem(w, http.StatusBadRequest, "Invalid request", message, []fieldError{
			{Path: "params.planId", Message: message},
		})
		return
	}

	if !a.planMu.TryLock() {
		writeProblem(w, http.StatusConflict, "Conflict", "another plan or apply is already running", nil)
		return
	}
	defer a.planMu.Unlock()

	stored, ok := a.getStoredPlan(params.PlanID)
	if !ok {
		writeProblem(w, http.StatusNotFound, "Plan not found", fmt.Sprintf("plan %q was not found or has expired", params.PlanID), nil)
		return
	}

	destructive := planapply.DestructiveChanges(stored.plans)
	if len(destructive) > 0 && !params.AllowDestroy {
		detail := fmt.Sprintf("the plan replaces or destroys resources:\n  %s\nrun apply with allowDestroy: true to make these changes", strings.Join(destructive, "\n  "))
		writeProblem(w, http.StatusBadRequest, "Destructive changes require approval", detail, nil)
		return
	}

	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var resolve planapply.Resolver
	if a.secrets != nil {
		resolve = a.secrets.Resolve
	}
	applied, err := planapply.Apply(ctx, stored.plans, nil, nil, resolve)
	unitsOutcome := make([]applyUnitOutcome, 0, len(stored.plans))
	for i, u := range stored.plans {
		switch {
		case i < len(applied):
			unitsOutcome = append(unitsOutcome, applyUnitOutcome{Dir: u.Dir, Outcome: "applied"})
		case i == len(applied) && err != nil:
			unitsOutcome = append(unitsOutcome, applyUnitOutcome{Dir: u.Dir, Outcome: "failed"})
		default:
			unitsOutcome = append(unitsOutcome, applyUnitOutcome{Dir: u.Dir, Outcome: "not applied"})
		}
	}

	if err != nil {
		var cmdErr *opentofu.CommandError
		detail := err.Error()
		if errors.As(err, &cmdErr) {
			unitDir := planapply.UnitDir(err)
			if unitDir == "" {
				unitDir = cmdErr.Dir
			}
			if cmdErr.Stderr != "" {
				detail = fmt.Sprintf("%s: %s: %s", unitDir, cmdErr.Command, strings.TrimSpace(cmdErr.Stderr))
			} else {
				detail = fmt.Sprintf("%s: %s: %v", unitDir, cmdErr.Command, cmdErr.Err)
			}
		}
		writeProblemWithUnits(w, http.StatusBadGateway, "Apply failed", detail, unitsOutcome)
		return
	}

	a.removeStoredPlan(params.PlanID)
	writeJSON(w, http.StatusOK, workspaceApplyResponse{Units: unitsOutcome})
}

type clusterDiscoverParams struct {
	Cluster string `json:"cluster"`
}

type discoveredGuest struct {
	VMID        int      `json:"vmid"`
	Name        string   `json:"name"`
	Node        string   `json:"node"`
	Status      string   `json:"status"`
	CPU         int      `json:"cpu"`
	Memory      string   `json:"memory"`
	Disk        string   `json:"disk"`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description,omitempty"`
	Classified  string   `json:"classified"`
}

func (a *server) clusterDiscover(w http.ResponseWriter, r *http.Request, request commandRequest) {
	ws, ok := a.workspace(w, r)
	if !ok {
		return
	}

	var params clusterDiscoverParams
	if len(request.Params) > 0 {
		dec := json.NewDecoder(bytes.NewReader(request.Params))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&params); err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid request", fmt.Sprintf("decode params: %v", err), nil)
			return
		}
	}
	if params.Cluster == "" && request.Target != "" {
		params.Cluster = request.Target
	}
	if params.Cluster == "" {
		message := "cluster is required"
		writeProblem(w, http.StatusBadRequest, "Invalid request", message, []fieldError{{Path: "cluster", Message: message}})
		return
	}

	credentialsRef, ok := planapply.CredentialsRefFor(ws, params.Cluster)
	if !ok {
		writeProblem(w, http.StatusNotFound, "Cluster not found", fmt.Sprintf("cluster %q not found, or has no credentialsRef", params.Cluster), nil)
		return
	}
	var resolve planapply.Resolver
	if a.secrets != nil {
		resolve = a.secrets.Resolve
	}
	if resolve == nil {
		writeProblem(w, http.StatusServiceUnavailable, "Secrets store unavailable", "no secrets store is configured", nil)
		return
	}
	secret, err := resolve(r.Context(), credentialsRef)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "Cannot resolve credentials", err.Error(), nil)
		return
	}

	d := ws.Find(nrm.Ref{Kind: v1alpha1.KindProxmoxCluster, Name: params.Cluster})
	if d == nil {
		writeProblem(w, http.StatusNotFound, "Cluster not found", fmt.Sprintf("cluster %q not found", params.Cluster), nil)
		return
	}
	spec, err := v1alpha1.Decode[v1alpha1.ProxmoxClusterSpec](d)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Invalid cluster", fmt.Sprintf("decode cluster %q: %v", params.Cluster, err), nil)
		return
	}
	if len(spec.Spec.Endpoints) == 0 {
		writeProblem(w, http.StatusInternalServerError, "Invalid cluster", fmt.Sprintf("cluster %q has no endpoints", params.Cluster), nil)
		return
	}
	var httpClient *http.Client
	if spec.Spec.TLS != nil && spec.Spec.TLS.Fingerprint != "" {
		httpClient = proxmox.NewPinnedHTTPClient(spec.Spec.TLS.Fingerprint)
	}
	client := proxmox.NewClient(spec.Spec.Endpoints[0], httpClient)
	client.SetAPIToken(proxmoxbootstrap.BootstrapUser, proxmoxbootstrap.BootstrapTokenID, string(secret))

	guests, err := proxmoxdiscovery.Discover(r.Context(), client, ws, params.Cluster)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "Discovery failed", err.Error(), nil)
		return
	}

	out := make([]discoveredGuest, len(guests))
	for i, g := range guests {
		out[i] = discoveredGuest{
			VMID: g.VMID, Name: g.Name, Node: g.Node, Status: g.Status,
			CPU: g.CPUs, Memory: g.Memory, Disk: g.Disk,
			Tags: g.Tags, Description: g.Description, Classified: string(g.Classified),
		}
	}
	writeJSON(w, http.StatusOK, out)
}
