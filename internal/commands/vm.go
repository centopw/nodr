package commands

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/centopw/nodr/internal/admission"
	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/workspace"
	"github.com/centopw/nodr/internal/yamledit"
)

// VMCreateParams are the vm.create parameters.
type VMCreateParams struct {
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Cluster     string `json:"cluster"`
	Template    string `json:"template"`
	Network     string `json:"network"`
	Storage     string `json:"storage"`
	Size        string `json:"size"`
}

// VMCreateResult describes a newly admitted VirtualMachine.
type VMCreateResult struct {
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

// VMPowerStateResult reports the power state persisted for a VirtualMachine.
type VMPowerStateResult struct {
	Name       string `json:"name"`
	PowerState string `json:"powerState"`
}

// VMDeleteResult reports the removal of a VirtualMachine intent file.
type VMDeleteResult struct {
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}

// vmActionParams are the params of vm.start, vm.stop and vm.delete.
type vmActionParams struct {
	Name string `json:"name"`
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

// CreateVM validates params against the workspace at root, writes a new
// intent file under intent/compute, and runs admission so the VirtualMachine
// gets its VMID, UID, IPv4 and MAC. Any failure after the file is written
// removes it again. Callers must serialize calls against the same root.
func CreateVM(root string, params VMCreateParams) (VMCreateResult, error) {
	ws, lerr := load(root)
	if lerr != nil {
		return VMCreateResult{}, lerr
	}
	validation := validateCreate(ws, params)
	if len(validation.fields) > 0 {
		return VMCreateResult{}, &Error{
			Status: validation.status,
			Title:  validation.title,
			Detail: validation.detail(),
			Fields: validation.fields,
		}
	}
	selectedSize := sizes[params.Size]
	file := filepath.Join(ws.Root, "intent", "compute", params.Name+".yaml")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return VMCreateResult{}, failure("Create VM failed", err)
	}
	data, err := marshalVMIntent(params, selectedSize)
	if err != nil {
		return VMCreateResult{}, failure("Create VM failed", err)
	}
	created, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			message := fmt.Sprintf("VirtualMachine %q already exists", params.Name)
			return VMCreateResult{}, &Error{
				Status: http.StatusConflict,
				Title:  "Resource conflict",
				Detail: message,
				Fields: []FieldError{{Path: "params.name", Message: message}},
			}
		}
		return VMCreateResult{}, failure("Create VM failed", err)
	}
	if _, err = created.Write(data); err == nil {
		err = created.Close()
	} else {
		_ = created.Close()
	}
	if err != nil {
		_ = os.Remove(file)
		return VMCreateResult{}, failure("Create VM failed", err)
	}
	rollback := func() { _ = os.Remove(file) }

	admissionWorkspace, lerr := loadFor(root, "Admission failed")
	if lerr != nil {
		rollback()
		return VMCreateResult{}, lerr
	}
	document := admissionWorkspace.Find(nrm.Ref{Kind: v1alpha1.KindVirtualMachine, Name: params.Name})
	if document == nil {
		rollback()
		return VMCreateResult{}, &Error{
			Status: http.StatusInternalServerError,
			Title:  "Admission failed",
			Detail: "the newly written VirtualMachine could not be loaded",
		}
	}
	vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](document)
	if err != nil {
		rollback()
		return VMCreateResult{}, failure("Admission failed", err)
	}
	assignments, diags := admission.PlanAll(admissionWorkspace, []*v1alpha1.VirtualMachine{vm}, nil, admission.Options{})
	if diags.HasErrors() {
		rollback()
		return VMCreateResult{}, diagnosticError(http.StatusBadRequest, "Admission failed", diags)
	}
	if err := admission.Apply(admissionWorkspace, assignments, false); err != nil {
		rollback()
		return VMCreateResult{}, failure("Admission failed", err)
	}

	finalWorkspace, lerr := loadFor(root, "Create VM failed")
	if lerr != nil {
		rollback()
		return VMCreateResult{}, lerr
	}
	finalDocument := finalWorkspace.Find(nrm.Ref{Kind: v1alpha1.KindVirtualMachine, Name: params.Name})
	if finalDocument == nil {
		rollback()
		return VMCreateResult{}, &Error{
			Status: http.StatusInternalServerError,
			Title:  "Create VM failed",
			Detail: "the created VirtualMachine could not be reloaded",
		}
	}
	finalVM, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](finalDocument)
	if err != nil {
		rollback()
		return VMCreateResult{}, failure("Create VM failed", err)
	}
	return createResult(finalVM), nil
}

// SetVMPowerState persists spec.lifecycle.powerState for the named
// VirtualMachine. If the workspace fails to load afterwards, the original file
// content is restored and the load error is returned. Callers must serialize
// calls against the same root.
func SetVMPowerState(root, name, powerState string) (VMPowerStateResult, error) {
	ws, lerr := load(root)
	if lerr != nil {
		return VMPowerStateResult{}, lerr
	}
	document := ws.Find(nrm.Ref{Kind: v1alpha1.KindVirtualMachine, Name: name})
	if document == nil {
		return VMPowerStateResult{}, notFound(name)
	}
	filePath := filepath.Join(ws.Root, filepath.FromSlash(document.File))
	src, err := os.ReadFile(filePath)
	if err != nil {
		return VMPowerStateResult{}, failure("VM update failed", err)
	}
	line := document.Line
	if line <= 0 {
		line = 1
	}
	edits := []yamledit.Edit{
		{
			Line:  line,
			Path:  []string{"spec", "lifecycle", "powerState"},
			Value: powerState,
			Order: vmKeyOrder,
		},
	}
	updated, err := yamledit.Set(src, edits, vmKeyOrder)
	if err != nil {
		return VMPowerStateResult{}, failure("VM update failed", err)
	}
	if err := os.WriteFile(filePath, updated, 0o644); err != nil {
		return VMPowerStateResult{}, failure("VM update failed", err)
	}
	if _, lerr := load(root); lerr != nil {
		_ = os.WriteFile(filePath, src, 0o644)
		return VMPowerStateResult{}, lerr
	}
	return VMPowerStateResult{Name: name, PowerState: powerState}, nil
}

// DeleteVM removes the intent file of the named VirtualMachine. If the
// workspace fails to load afterwards, the file is restored and the load error
// is returned. Callers must serialize calls against the same root.
func DeleteVM(root, name string) (VMDeleteResult, error) {
	ws, lerr := load(root)
	if lerr != nil {
		return VMDeleteResult{}, lerr
	}
	document := ws.Find(nrm.Ref{Kind: v1alpha1.KindVirtualMachine, Name: name})
	if document == nil {
		return VMDeleteResult{}, notFound(name)
	}
	filePath := filepath.Join(ws.Root, filepath.FromSlash(document.File))
	src, err := os.ReadFile(filePath)
	if err != nil {
		return VMDeleteResult{}, failure("VM delete failed", err)
	}
	if err := os.Remove(filePath); err != nil {
		return VMDeleteResult{}, failure("VM delete failed", err)
	}
	if _, lerr := load(root); lerr != nil {
		_ = os.WriteFile(filePath, src, 0o644)
		return VMDeleteResult{}, lerr
	}
	return VMDeleteResult{Name: name, Deleted: true}, nil
}

func notFound(name string) *Error {
	return &Error{
		Status: http.StatusNotFound,
		Title:  "Resource not found",
		Detail: fmt.Sprintf("VirtualMachine %q not found", name),
	}
}

func vmKeyOrder(path []string) []string {
	switch strings.Join(path, ".") {
	case "":
		return []string{"apiVersion", "kind", "metadata", "spec"}
	case "metadata":
		return []string{"name", "uid", "labels", "annotations"}
	case "spec":
		return []string{"placement", "identity", "source", "resources", "disks", "nics", "guest", "ha", "policies", "lifecycle", "proxmox"}
	case "spec.lifecycle":
		return []string{"powerState", "protection", "startOnBoot", "ignoreDrift"}
	}
	return nil
}

type createValidation struct {
	status int
	title  string
	fields []FieldError
}

func (v createValidation) detail() string {
	messages := make([]string, len(v.fields))
	for i, item := range v.fields {
		messages[i] = item.Message
	}
	return strings.Join(messages, "; ")
}

func validateCreate(ws *workspace.Workspace, params VMCreateParams) createValidation {
	result := createValidation{status: http.StatusBadRequest, title: "Invalid vm.create parameters"}
	addRequired := func(path, value string) bool {
		if strings.TrimSpace(value) == "" {
			result.fields = append(result.fields, FieldError{Path: path, Message: strings.TrimPrefix(path, "params.") + " is required"})
			return true
		}
		return false
	}
	nameMissing := addRequired("params.name", params.Name)
	if !nameMissing {
		if err := nrm.ValidateName(params.Name); err != nil {
			result.fields = append(result.fields, FieldError{Path: "params.name", Message: err.Error()})
		} else if ws.Find(nrm.Ref{Kind: v1alpha1.KindVirtualMachine, Name: params.Name}) != nil {
			message := fmt.Sprintf("VirtualMachine %q already exists", params.Name)
			return createValidation{status: http.StatusConflict, title: "Resource conflict", fields: []FieldError{{Path: "params.name", Message: message}}}
		}
	}
	if !addRequired("params.environment", params.Environment) {
		manifest, err := v1alpha1.Decode[v1alpha1.WorkspaceSpec](ws.Manifest)
		switch {
		case err != nil:
			result.fields = append(result.fields, FieldError{Path: "params.environment", Message: err.Error()})
		case params.Environment == "templates":
			result.fields = append(result.fields, FieldError{Path: "params.environment", Message: "environment templates is not available for VirtualMachines"})
		default:
			if _, exists := manifest.Spec.Environments[params.Environment]; !exists {
				result.fields = append(result.fields, FieldError{Path: "params.environment", Message: fmt.Sprintf("environment %q does not exist", params.Environment)})
			}
		}
	}
	validateRef := func(path, kind, value string) {
		if addRequired(path, value) {
			return
		}
		if ws.Find(nrm.Ref{Kind: kind, Name: value}) == nil {
			result.fields = append(result.fields, FieldError{Path: path, Message: fmt.Sprintf("%s %q does not exist", kind, value)})
		}
	}
	validateRef("params.cluster", v1alpha1.KindProxmoxCluster, params.Cluster)
	validateRef("params.template", v1alpha1.KindTemplate, params.Template)
	validateRef("params.network", v1alpha1.KindNetwork, params.Network)
	addRequired("params.storage", params.Storage)
	if !addRequired("params.size", params.Size) {
		if _, exists := sizes[params.Size]; !exists {
			result.fields = append(result.fields, FieldError{Path: "params.size", Message: "size must be S, M or L"})
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

func marshalVMIntent(params VMCreateParams, selectedSize size) ([]byte, error) {
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

func createResult(vm *v1alpha1.VirtualMachine) VMCreateResult {
	addresses, macs := NetworkValues(vm.Spec.NICs)
	memory := vm.Spec.Resources.Memory.Size.String()
	summary := fmt.Sprintf("Creates %s with %d vCPUs and %s memory on %s", vm.Metadata.Name, vm.Spec.Resources.CPU.Cores, displayGiB(memory), vm.Spec.Placement.AssignedNode)
	if len(addresses) > 0 {
		address, _, _ := net.ParseCIDR(addresses[0])
		if address != nil {
			summary += ", IP " + address.String()
		}
	}
	summary += "."
	return VMCreateResult{
		Name: vm.Metadata.Name, Cluster: vm.Spec.Placement.Cluster,
		Node: vm.Spec.Placement.AssignedNode, VMID: vm.Spec.Identity.VMID,
		CPU: vm.Spec.Resources.CPU.Cores, Memory: memory,
		Addresses: addresses, MACs: macs, Summary: summary,
	}
}

// NetworkValues returns the non-empty IPv4 addresses and MAC addresses of
// nics, in NIC order. Both slices are non-nil so they encode as JSON arrays.
func NetworkValues(nics []v1alpha1.NIC) ([]string, []string) {
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
