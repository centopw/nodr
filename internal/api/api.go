// Package api serves nodr's versioned HTTP API.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/centopw/nodr/internal/admission"
	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/engine/opentofu"
	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/planapply"
	"github.com/centopw/nodr/internal/workspace"
)

const apiPrefix = "/api/v1"

// Handler returns the API handler for the workspace rooted at root. The
// workspace is loaded from disk for every request, so responses and commands
// always operate on current intent rather than a process-local cache.
func Handler(ctx context.Context, root string) http.Handler {
	a := &server{
		ctx:   ctx,
		root:  root,
		plans: make(map[string]*storedPlan),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+apiPrefix+"/workspaces", a.listWorkspaces)
	mux.HandleFunc("GET "+apiPrefix+"/workspaces/{workspace}", a.getWorkspace)
	mux.HandleFunc("GET "+apiPrefix+"/workspaces/{workspace}/resources", a.listResources)
	mux.HandleFunc("POST "+apiPrefix+"/workspaces/{workspace}/commands", a.runCommand)
	mux.HandleFunc(apiPrefix+"/workspaces", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}/resources", methodNotAllowed)
	mux.HandleFunc(apiPrefix+"/workspaces/{workspace}/commands", methodNotAllowed)
	mux.HandleFunc("/api/", notFound)
	return mux
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
		addresses, _ := networkValues(vm.Spec.NICs)
		items = append(items, virtualMachineItem{
			Kind: kind, Name: vm.Metadata.Name, Cluster: vm.Spec.Placement.Cluster,
			Node: vm.Spec.Placement.AssignedNode, VMID: vm.Spec.Identity.VMID,
			CPU: vm.Spec.Resources.CPU.Cores, Memory: vm.Spec.Resources.Memory.Size.String(),
			Environment: vm.Metadata.Labels[nrm.LabelEnvironment], Addresses: addresses,
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

type vmCreateParams struct {
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Cluster     string `json:"cluster"`
	Template    string `json:"template"`
	Network     string `json:"network"`
	Storage     string `json:"storage"`
	Size        string `json:"size"`
}

type size struct {
	CPU    int
	Memory string
	Disk   string
}

var sizes = map[string]size{
	"S": {CPU: 1, Memory: "2Gi", Disk: "20Gi"},
	"M": {CPU: 2, Memory: "4Gi", Disk: "40Gi"},
	"L": {CPU: 4, Memory: "8Gi", Disk: "80Gi"},
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
		var params vmCreateParams
		if len(request.Params) > 0 {
			dec := json.NewDecoder(bytes.NewReader(request.Params))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&params); err != nil {
				writeProblem(w, http.StatusBadRequest, "Invalid request", fmt.Sprintf("decode params: %v", err), nil)
				return
			}
		}
		a.createVM(w, params)
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
	default:
		message := fmt.Sprintf("command %q is not supported", request.Command)
		writeProblem(w, http.StatusBadRequest, "Unknown command", message, []fieldError{{Path: "command", Message: message}})
	}
}

// createVM validates and admits a new VirtualMachine. It reloads the
// workspace itself, under createMu, so the ws the caller already loaded
// (which may be stale by the time the lock is acquired) is not used.
func (a *server) createVM(w http.ResponseWriter, params vmCreateParams) {
	a.createMu.Lock()
	defer a.createMu.Unlock()
	ws, ok := a.load(w)
	if !ok {
		return
	}
	validation := validateCreate(ws, params)
	if len(validation.errs) > 0 {
		writeProblem(w, validation.status, validation.title, validation.detail(), validation.errs)
		return
	}
	selectedSize := sizes[params.Size]
	file := filepath.Join(ws.Root, "intent", "compute", params.Name+".yaml")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Create VM failed", err.Error(), nil)
		return
	}
	data, err := marshalVMIntent(params, selectedSize)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Create VM failed", err.Error(), nil)
		return
	}
	created, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			message := fmt.Sprintf("VirtualMachine %q already exists", params.Name)
			writeProblem(w, http.StatusConflict, "Resource conflict", message, []fieldError{{Path: "params.name", Message: message}})
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Create VM failed", err.Error(), nil)
		return
	}
	if _, err = created.Write(data); err == nil {
		err = created.Close()
	} else {
		_ = created.Close()
	}
	if err != nil {
		_ = os.Remove(file)
		writeProblem(w, http.StatusInternalServerError, "Create VM failed", err.Error(), nil)
		return
	}
	rollback := func() { _ = os.Remove(file) }

	admissionWorkspace, ok := a.loadForCommand(w, "Admission failed")
	if !ok {
		rollback()
		return
	}
	document := admissionWorkspace.Find(nrm.Ref{Kind: v1alpha1.KindVirtualMachine, Name: params.Name})
	if document == nil {
		rollback()
		writeProblem(w, http.StatusInternalServerError, "Admission failed", "the newly written VirtualMachine could not be loaded", nil)
		return
	}
	vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](document)
	if err != nil {
		rollback()
		writeProblem(w, http.StatusInternalServerError, "Admission failed", err.Error(), nil)
		return
	}
	assignments, diags := admission.PlanAll(admissionWorkspace, []*v1alpha1.VirtualMachine{vm}, nil, admission.Options{})
	if diags.HasErrors() {
		rollback()
		writeDiagnosticProblem(w, http.StatusBadRequest, "Admission failed", diags)
		return
	}
	if err := admission.Apply(admissionWorkspace, assignments, false); err != nil {
		rollback()
		writeProblem(w, http.StatusInternalServerError, "Admission failed", err.Error(), nil)
		return
	}

	finalWorkspace, ok := a.loadForCommand(w, "Create VM failed")
	if !ok {
		rollback()
		return
	}
	finalDocument := finalWorkspace.Find(nrm.Ref{Kind: v1alpha1.KindVirtualMachine, Name: params.Name})
	if finalDocument == nil {
		rollback()
		writeProblem(w, http.StatusInternalServerError, "Create VM failed", "the created VirtualMachine could not be reloaded", nil)
		return
	}
	finalVM, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](finalDocument)
	if err != nil {
		rollback()
		writeProblem(w, http.StatusInternalServerError, "Create VM failed", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusCreated, createResponse(finalVM))
}

func (a *server) loadForCommand(w http.ResponseWriter, title string) (*workspace.Workspace, bool) {
	ws, diags := workspace.Load(a.root)
	if diags.HasErrors() {
		writeDiagnosticProblem(w, http.StatusInternalServerError, title, diags)
		return nil, false
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, title, err.Error(), nil)
		return nil, false
	}
	diags.Append(ws.Validate(reg))
	if diags.HasErrors() {
		writeDiagnosticProblem(w, http.StatusBadRequest, title, diags)
		return nil, false
	}
	return ws, true
}

type createValidation struct {
	status int
	title  string
	errs   []fieldError
}

func (v createValidation) detail() string {
	messages := make([]string, len(v.errs))
	for i, item := range v.errs {
		messages[i] = item.Message
	}
	return strings.Join(messages, "; ")
}

func validateCreate(ws *workspace.Workspace, params vmCreateParams) createValidation {
	result := createValidation{status: http.StatusBadRequest, title: "Invalid vm.create parameters"}
	addRequired := func(path, value string) bool {
		if strings.TrimSpace(value) == "" {
			result.errs = append(result.errs, fieldError{Path: path, Message: strings.TrimPrefix(path, "params.") + " is required"})
			return true
		}
		return false
	}
	nameMissing := addRequired("params.name", params.Name)
	if !nameMissing {
		if err := nrm.ValidateName(params.Name); err != nil {
			result.errs = append(result.errs, fieldError{Path: "params.name", Message: err.Error()})
		} else if ws.Find(nrm.Ref{Kind: v1alpha1.KindVirtualMachine, Name: params.Name}) != nil {
			message := fmt.Sprintf("VirtualMachine %q already exists", params.Name)
			return createValidation{status: http.StatusConflict, title: "Resource conflict", errs: []fieldError{{Path: "params.name", Message: message}}}
		}
	}
	if !addRequired("params.environment", params.Environment) {
		manifest, err := v1alpha1.Decode[v1alpha1.WorkspaceSpec](ws.Manifest)
		switch {
		case err != nil:
			result.errs = append(result.errs, fieldError{Path: "params.environment", Message: err.Error()})
		case params.Environment == "templates":
			result.errs = append(result.errs, fieldError{Path: "params.environment", Message: "environment templates is not available for VirtualMachines"})
		default:
			if _, exists := manifest.Spec.Environments[params.Environment]; !exists {
				result.errs = append(result.errs, fieldError{Path: "params.environment", Message: fmt.Sprintf("environment %q does not exist", params.Environment)})
			}
		}
	}
	validateRef := func(path, kind, value string) {
		if addRequired(path, value) {
			return
		}
		if ws.Find(nrm.Ref{Kind: kind, Name: value}) == nil {
			result.errs = append(result.errs, fieldError{Path: path, Message: fmt.Sprintf("%s %q does not exist", kind, value)})
		}
	}
	validateRef("params.cluster", v1alpha1.KindProxmoxCluster, params.Cluster)
	validateRef("params.template", v1alpha1.KindTemplate, params.Template)
	validateRef("params.network", v1alpha1.KindNetwork, params.Network)
	addRequired("params.storage", params.Storage)
	if !addRequired("params.size", params.Size) {
		if _, exists := sizes[params.Size]; !exists {
			result.errs = append(result.errs, fieldError{Path: "params.size", Message: "size must be S, M or L"})
		}
	}
	return result
}

type vmIntent struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   intentMetadata `yaml:"metadata"`
	Spec       intentSpec     `yaml:"spec"`
}

type intentMetadata struct {
	Name   string            `yaml:"name"`
	Labels map[string]string `yaml:"labels"`
}

type intentSpec struct {
	Placement intentPlacement `yaml:"placement"`
	Source    intentSource    `yaml:"source"`
	Resources intentResources `yaml:"resources"`
	Disks     []intentDisk    `yaml:"disks"`
	NICs      []intentNIC     `yaml:"nics"`
}

type intentPlacement struct {
	Cluster string `yaml:"cluster"`
}

type intentSource struct {
	Template string `yaml:"template"`
}

type intentResources struct {
	CPU    intentCPU    `yaml:"cpu"`
	Memory intentMemory `yaml:"memory"`
}

type intentCPU struct {
	Cores int `yaml:"cores"`
}

type intentMemory struct {
	Size string `yaml:"size"`
}

type intentDisk struct {
	Name    string `yaml:"name"`
	Storage string `yaml:"storage"`
	Size    string `yaml:"size"`
}

type intentNIC struct {
	Network string     `yaml:"network"`
	IPv4    intentIPv4 `yaml:"ipv4"`
}

type intentIPv4 struct {
	Mode string `yaml:"mode"`
}

func marshalVMIntent(params vmCreateParams, selectedSize size) ([]byte, error) {
	return yaml.Marshal(vmIntent{
		APIVersion: v1alpha1.APIVersion,
		Kind:       v1alpha1.KindVirtualMachine,
		Metadata: intentMetadata{
			Name: params.Name, Labels: map[string]string{nrm.LabelEnvironment: params.Environment},
		},
		Spec: intentSpec{
			Placement: intentPlacement{Cluster: params.Cluster},
			Source:    intentSource{Template: params.Template},
			Resources: intentResources{CPU: intentCPU{Cores: selectedSize.CPU}, Memory: intentMemory{Size: selectedSize.Memory}},
			Disks:     []intentDisk{{Name: "root", Storage: params.Storage, Size: selectedSize.Disk}},
			NICs:      []intentNIC{{Network: params.Network, IPv4: intentIPv4{Mode: v1alpha1.IPv4ModeAuto}}},
		},
	})
}

type vmCreateResponse struct {
	Name      string   `json:"name"`
	Cluster   string   `json:"cluster"`
	Node      string   `json:"node"`
	VMID      int      `json:"vmid"`
	CPU       int      `json:"cpu"`
	Memory    string   `json:"memory"`
	Addresses []string `json:"addresses"`
	MACs      []string `json:"macs"`
	Summary   string   `json:"summary"`
}

func createResponse(vm *v1alpha1.VirtualMachine) vmCreateResponse {
	addresses, macs := networkValues(vm.Spec.NICs)
	memory := vm.Spec.Resources.Memory.Size.String()
	summary := fmt.Sprintf("Creates %s with %d vCPUs and %s memory on %s", vm.Metadata.Name, vm.Spec.Resources.CPU.Cores, displayGiB(memory), vm.Spec.Placement.AssignedNode)
	if len(addresses) > 0 {
		address, _, _ := net.ParseCIDR(addresses[0])
		if address != nil {
			summary += ", IP " + address.String()
		}
	}
	summary += "."
	return vmCreateResponse{
		Name: vm.Metadata.Name, Cluster: vm.Spec.Placement.Cluster,
		Node: vm.Spec.Placement.AssignedNode, VMID: vm.Spec.Identity.VMID,
		CPU: vm.Spec.Resources.CPU.Cores, Memory: memory,
		Addresses: addresses, MACs: macs, Summary: summary,
	}
}

func networkValues(nics []v1alpha1.NIC) ([]string, []string) {
	addresses := make([]string, 0, len(nics))
	macs := make([]string, 0, len(nics))
	for _, nic := range nics {
		if nic.IPv4 != nil && nic.IPv4.Address != "" {
			addresses = append(addresses, nic.IPv4.Address)
		}
		if nic.MAC != "" {
			macs = append(macs, nic.MAC)
		}
	}
	return addresses, macs
}

func displayGiB(value string) string {
	return strings.TrimSuffix(value, "Gi") + " GiB"
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

	_, plans, err := planapply.PlanUnits(r.Context(), ws, nil, planDir, nil, nil)
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
	applied, err := planapply.Apply(ctx, stored.plans, nil, nil)
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
