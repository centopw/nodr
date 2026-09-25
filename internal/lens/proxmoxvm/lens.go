// Package proxmoxvm is the lens for the provision aspect of VirtualMachine
// resources with OpenTofu and the bpg/proxmox provider (design §4.5 and
// ADR-0005). It renders a proxmox_virtual_environment_vm resource for a VM,
// writes intent changes into existing code with minimal edits, and lifts
// code edits back into intent while reporting who owns each field.
package proxmoxvm

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/centopw/nodr/internal/lens"
	"github.com/centopw/nodr/internal/lens/hclmap"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/resolve"
)

// Identity of the lens.
const (
	// ResourceType is the bpg/proxmox resource type that holds a VM.
	ResourceType = "proxmox_virtual_environment_vm"
	// Aspect is the aspect of VirtualMachine this lens realizes.
	Aspect = "provision"
	// Engine is the engine the lens renders code for.
	Engine = "opentofu"
)

// markerPrefix starts the provenance comment above a managed block
// (design §4.6).
const markerPrefix = "nodr:managed vm/"

// ErrNotManaged reports that no managed block for a VM was found.
var ErrNotManaged = errors.New("no managed block found")

// Lens maps VirtualMachine resources to HCL for the bpg/proxmox provider.
type Lens struct {
	Resolver resolve.Resolver
}

// Address returns the HCL resource name for a VM name: hyphens become
// underscores, and names that start with a digit get a "vm_" prefix, so
// web-01 becomes web_01.
func Address(name string) string {
	a := strings.ReplaceAll(name, "-", "_")
	if a != "" && a[0] >= '0' && a[0] <= '9' {
		a = "vm_" + a
	}
	return a
}

// Marker returns the provenance comment of a VM's managed block, without
// the leading "#", for example "nodr:managed vm/web-01".
func Marker(name string) string { return markerPrefix + name }

// Render returns the source of a new managed block for vm, with its
// provenance comment.
func (l Lens) Render(vm *v1alpha1.VirtualMachine) ([]byte, error) {
	v, err := project(vm, l.Resolver)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", vm.Metadata.Name, err)
	}
	return hclmap.Render(target(Address(vm.Metadata.Name)), Marker(vm.Metadata.Name), v)
}

// Lift reads the managed block of vm in src and returns a copy of vm with
// the values the code changed, together with the ownership of every field.
// Values the code sets but intent cannot express leave the intent value
// alone and are reported as code-owned.
func (l Lens) Lift(vm *v1alpha1.VirtualMachine, src []byte, filename string) (*v1alpha1.VirtualMachine, lens.Report, error) {
	out, report, _, err := l.lift(vm, src, filename)
	return out, report, err
}

// Put writes vm into its managed block in src and returns the new source.
// Only synced values that differ change; code-owned values, extensions,
// comments and formatting are kept.
func (l Lens) Put(vm *v1alpha1.VirtualMachine, src []byte, filename string) ([]byte, error) {
	desired, err := project(vm, l.Resolver)
	if err != nil {
		return nil, fmt.Errorf("put %s: %w", vm.Metadata.Name, err)
	}
	_, _, failures, err := l.lift(vm, src, filename)
	if err != nil {
		return nil, err
	}
	keep := make(map[string]bool, len(failures))
	for p := range failures {
		keep[p] = true
	}
	t, err := Locate(src, filename, vm.Metadata.Name)
	if err != nil {
		return nil, err
	}
	return hclmap.PutWith(src, filename, t, desired, hclmap.Options{Keep: keep})
}

func (l Lens) lift(vm *v1alpha1.VirtualMachine, src []byte, filename string) (*v1alpha1.VirtualMachine, lens.Report, map[string]string, error) {
	base, err := project(vm, l.Resolver)
	if err != nil {
		return nil, lens.Report{}, nil, fmt.Errorf("lift %s: %w", vm.Metadata.Name, err)
	}
	t, err := Locate(src, filename, vm.Metadata.Name)
	if err != nil {
		return nil, lens.Report{}, nil, err
	}
	lifted, states, err := hclmap.Lift(src, filename, t, base)
	if err != nil {
		return nil, lens.Report{}, nil, err
	}
	out, failures := embed(vm, base, lifted, hclmap.Changed(base, lifted), l.Resolver)
	return out, buildReport(filename, states, failures), failures, nil
}

func target(address string) hclmap.Target {
	return hclmap.Target{Type: "resource", Labels: []string{ResourceType, address}}
}

// Locate finds the managed block of the VM called name in src: the
// proxmox_virtual_environment_vm resource whose provenance comment names
// the VM, or else the resource with the VM's address.
func Locate(src []byte, filename, name string) (hclmap.Target, error) {
	file, diags := hclsyntax.ParseConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		return hclmap.Target{}, fmt.Errorf("parse %s: %w", filename, diags)
	}
	lines := strings.Split(string(src), "\n")
	var byAddress *hclsyntax.Block
	for _, b := range file.Body.(*hclsyntax.Body).Blocks {
		if b.Type != "resource" || len(b.Labels) != 2 || b.Labels[0] != ResourceType {
			continue
		}
		if markerAbove(lines, b.TypeRange.Start.Line) == name {
			return target(b.Labels[1]), nil
		}
		if b.Labels[1] == Address(name) {
			byAddress = b
		}
	}
	if byAddress != nil {
		return target(byAddress.Labels[1]), nil
	}
	return hclmap.Target{}, fmt.Errorf("vm/%s in %s: %w", name, filename, ErrNotManaged)
}

// markerAbove returns the VM name in a provenance comment among the comment
// lines directly above line (1-based), or "".
func markerAbove(lines []string, line int) string {
	for i := line - 2; i >= 0; i-- {
		text := strings.TrimSpace(lines[i])
		comment, ok := strings.CutPrefix(text, "#")
		if !ok {
			comment, ok = strings.CutPrefix(text, "//")
		}
		if !ok {
			return ""
		}
		if name, found := strings.CutPrefix(strings.TrimSpace(comment), markerPrefix); found {
			return strings.TrimSpace(name)
		}
	}
	return ""
}

// FindManaged searches the .tf files below dir in fsys for the managed block
// of the VM called name, and returns the file's path and content.
func FindManaged(fsys fs.FS, dir, name string) (string, []byte, error) {
	var found string
	var content []byte
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".tf" || found != "" {
			return nil
		}
		src, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if _, err := Locate(src, p, name); err == nil {
			found, content = p, src
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return "", nil, err
	}
	if found == "" {
		return "", nil, fmt.Errorf("vm/%s below %s/: %w", name, dir, ErrNotManaged)
	}
	return found, content, nil
}

// intentPaths maps code paths of the view to intent paths. Indices are
// written as [*] and carried over in order.
var intentPaths = map[string]string{
	"name":                             "metadata.name",
	"node_name":                        "spec.placement.assignedNode",
	"vm_id":                            "spec.identity.vmid",
	"tags":                             "spec.proxmox.tags",
	"on_boot":                          "spec.lifecycle.startOnBoot",
	"started":                          "spec.lifecycle.powerState",
	"protection":                       "spec.lifecycle.protection",
	"machine":                          "spec.proxmox.machine",
	"bios":                             "spec.proxmox.bios",
	"scsi_hardware":                    "spec.proxmox.scsiController",
	"clone":                            "spec.source.template",
	"clone.vm_id":                      "spec.source.template",
	"clone.node_name":                  "spec.source.template",
	"clone.full":                       "spec.source.template",
	"agent":                            "spec.guest.agent",
	"agent.enabled":                    "spec.guest.agent",
	"cpu":                              "spec.resources.cpu",
	"cpu.cores":                        "spec.resources.cpu.cores",
	"cpu.sockets":                      "spec.resources.cpu.sockets",
	"cpu.type":                         "spec.resources.cpu.type",
	"memory":                           "spec.resources.memory",
	"memory.dedicated":                 "spec.resources.memory.size",
	"memory.floating":                  "spec.resources.memory.minimum",
	"disk":                             "spec.disks",
	"disk[*]":                          "spec.disks[*]",
	"disk[*].datastore_id":             "spec.disks[*].storage",
	"disk[*].interface":                "spec.disks[*]",
	"disk[*].size":                     "spec.disks[*].size",
	"disk[*].discard":                  "spec.disks[*].options.discard",
	"disk[*].ssd":                      "spec.disks[*].options.ssd",
	"disk[*].iothread":                 "spec.disks[*].options.iothread",
	"network_device":                   "spec.nics",
	"network_device[*]":                "spec.nics[*]",
	"network_device[*].bridge":         "spec.nics[*].network",
	"network_device[*].vlan_id":        "spec.nics[*].network",
	"network_device[*].mac_address":    "spec.nics[*].mac",
	"network_device[*].model":          "spec.nics[*]",
	"initialization":                   "spec.guest.cloudInit",
	"initialization.datastore_id":      "spec.guest.cloudInit",
	"initialization.ip_config":         "spec.nics",
	"initialization.ip_config[*]":      "spec.nics[*].ipv4",
	"initialization.ip_config[*].ipv4": "spec.nics[*].ipv4",
	"initialization.ip_config[*].ipv4.address": "spec.nics[*].ipv4.address",
	"initialization.ip_config[*].ipv4.gateway": "spec.nics[*].network",
	"initialization.user_account":              "spec.guest.cloudInit",
	"initialization.user_account.username":     "spec.guest.cloudInit.user",
	"initialization.user_account.keys":         "spec.guest.cloudInit.authorizedKeys",
}

// containers are block paths that get a row in the report only when they
// are code-owned; otherwise the rows of their attributes say everything.
var containers = map[string]bool{
	"clone": true, "agent": true, "cpu": true, "memory": true,
	"disk": true, "disk[*]": true, "network_device": true, "network_device[*]": true,
	"initialization": true, "initialization.ip_config": true, "initialization.ip_config[*]": true,
	"initialization.ip_config[*].ipv4": true, "initialization.user_account": true,
}

var indexRE = regexp.MustCompile(`\[(\d+)\]`)

// toIntentPath maps a code path to its intent path.
func toIntentPath(codePath string) (intentPath, pattern string, ok bool) {
	indices := indexRE.FindAllStringSubmatch(codePath, -1)
	pattern = indexRE.ReplaceAllString(codePath, "[*]")
	ip, ok := intentPaths[pattern]
	if !ok {
		return "", pattern, false
	}
	for _, m := range indices {
		ip = strings.Replace(ip, "[*]", "["+m[1]+"]", 1)
	}
	return ip, pattern, true
}

// buildReport turns code-level states into one row per intent field, and
// one row per extension.
func buildReport(file string, states []hclmap.State, failures map[string]string) lens.Report {
	type row struct {
		field lens.Field
		order int
	}
	rows := map[string]*row{}
	for i, s := range states {
		if reason, ok := failureFor(s.Path, failures); ok {
			s.Owner, s.Reason = lens.CodeOwned, reason
		}
		key, pattern, mapped := toIntentPath(s.Path)
		if s.Owner == lens.Extension || !mapped {
			key = s.Path
		} else if containers[pattern] && s.Owner != lens.CodeOwned {
			continue
		}
		r, ok := rows[key]
		if !ok {
			r = &row{field: lens.Field{Path: key, CodePath: s.Path, Owner: s.Owner, Reason: s.Reason, Line: s.Line}, order: i}
			rows[key] = r
			continue
		}
		if s.Owner == lens.CodeOwned && r.field.Owner != lens.CodeOwned {
			r.field.Owner, r.field.Reason, r.field.CodePath = lens.CodeOwned, s.Reason, s.Path
		}
		if s.Line > 0 && (r.field.Line == 0 || s.Line < r.field.Line) {
			r.field.Line = s.Line
		}
	}
	out := make([]*row, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].field, out[j].field
		if (a.Line == 0) != (b.Line == 0) {
			return a.Line != 0
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Path < b.Path
	})
	report := lens.Report{File: file}
	for _, r := range out {
		report.Fields = append(report.Fields, r.field)
	}
	return report
}

// failureFor returns the reason recorded for path or for a block that
// contains it.
func failureFor(p string, failures map[string]string) (string, bool) {
	if reason, ok := failures[p]; ok {
		return reason, true
	}
	for f, reason := range failures {
		if strings.HasPrefix(p, f+".") || strings.HasPrefix(p, f+"[") {
			return reason, true
		}
	}
	return "", false
}
