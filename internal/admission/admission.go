// Package admission fills in the values that nodr allocates for virtual
// machines and templates (design §3.7 and §3.9): UIDs, nodes, Proxmox guest
// IDs, IPv4 addresses and MAC addresses. It chooses them from the loaded
// workspace alone, so the same workspace always gets the same values, apart
// from new UIDs, and writes them into the intent files with minimal edits.
package admission

import (
	"cmp"
	"crypto/sha256"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/workspace"
	"github.com/centopw/nodr/internal/yamledit"
)

// Assignment is a value that admission allocated for an empty field.
type Assignment struct {
	// Document is the document that gets the value.
	Document *nrm.Document
	// Path holds the segments of the field, for example
	// ["spec", "identity", "vmid"].
	Path []string
	// Value is the allocated value: a string or an int.
	Value any
}

// String formats the assignment for display, for example
// "vm/web-02: spec.identity.vmid = 1013 (intent/compute/web-02.yaml)".
// It names the resource because a file can hold several.
func (a Assignment) String() string {
	d := a.Document
	prefix := strings.ToLower(d.Kind)
	if d.Kind == v1alpha1.KindVirtualMachine {
		prefix = "vm"
	}
	return fmt.Sprintf("%s/%s: %s = %v (%s)", prefix, d.Metadata.Name, d.FieldPath(a.Path...), a.Value, d.File)
}

// Options adjust admission.
type Options struct {
	// NewUID returns a new resource UID. If it is nil, nrm.NewUID is used.
	NewUID func() string
}

// Plan allocates the values that the virtual machines in vms lack: a UID,
// the node in spec.placement.assignedNode, the guest ID in
// spec.identity.vmid, an address for each network interface with IPv4 mode
// auto and a MAC address for each interface. Values that are set never
// change, so planning an admitted workspace returns nothing. The workspace
// must be valid.
//
// The virtual machines are admitted in the order of their files and names,
// and each one sees the values allocated before it, so no two get the same
// guest ID, address or MAC address. Problems are returned as diagnostics; if
// there are errors, the assignments are incomplete.
func Plan(ws *workspace.Workspace, vms []*v1alpha1.VirtualMachine, opts Options) ([]Assignment, diag.List) {
	return PlanAll(ws, vms, nil, opts)
}

// PlanTemplates allocates a Proxmox guest ID from the templates environment
// for each template without one. The workspace must be valid.
func PlanTemplates(ws *workspace.Workspace, templates []*v1alpha1.Template, opts Options) ([]Assignment, diag.List) {
	return PlanAll(ws, nil, templates, opts)
}

// PlanAll allocates missing values for virtual machines and templates with a
// single allocator. It admits virtual machines first, then templates, so a
// guest ID allocated to a VM is reserved before templates are considered.
// Within each kind, resources are admitted in file and name order. Each
// document is admitted at most once per kind.
func PlanAll(ws *workspace.Workspace, vms []*v1alpha1.VirtualMachine, templates []*v1alpha1.Template, opts Options) ([]Assignment, diag.List) {
	a := newAllocator(ws, opts)

	vmTargets := slices.Clone(vms)
	sort.SliceStable(vmTargets, func(i, j int) bool {
		return documentLess(vmTargets[i].Document, vmTargets[j].Document)
	})
	seenVMs := map[*nrm.Document]bool{}
	for _, vm := range vmTargets {
		if !seenVMs[vm.Document] {
			seenVMs[vm.Document] = true
			a.admit(vm)
		}
	}

	templateTargets := slices.Clone(templates)
	sort.SliceStable(templateTargets, func(i, j int) bool {
		return documentLess(templateTargets[i].Document, templateTargets[j].Document)
	})
	seenTemplates := map[*nrm.Document]bool{}
	for _, template := range templateTargets {
		if !seenTemplates[template.Document] {
			seenTemplates[template.Document] = true
			a.admitTemplate(template)
		}
	}

	a.diags.Sort()
	return a.out, a.diags
}

func documentLess(a, b *nrm.Document) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	return a.Metadata.Name < b.Metadata.Name
}

// allocator holds the values in use while admission allocates new ones.
type allocator struct {
	newUID       func() string
	environments map[string]v1alpha1.Environment
	clusters     map[string]*v1alpha1.ProxmoxCluster
	networks     map[string]*v1alpha1.Network
	vms          []*v1alpha1.VirtualMachine
	// vmids holds the guest IDs in use, by cluster.
	vmids map[string]map[int]bool
	// macs holds the uppercase MAC addresses in use, by cluster.
	macs map[string]map[string]bool
	// memory holds the memory assigned to guests, by cluster and node.
	memory map[string]map[string]int64
	// nodes holds the node each guest is assigned to, by guest name.
	nodes map[string]string
	// addresses holds the IPv4 addresses of network interfaces and cluster
	// endpoints.
	addresses map[netip.Addr]bool

	out   []Assignment
	diags diag.List
}

func newAllocator(ws *workspace.Workspace, opts Options) *allocator {
	a := &allocator{
		newUID:    opts.NewUID,
		clusters:  map[string]*v1alpha1.ProxmoxCluster{},
		networks:  map[string]*v1alpha1.Network{},
		vmids:     map[string]map[int]bool{},
		macs:      map[string]map[string]bool{},
		memory:    map[string]map[string]int64{},
		nodes:     map[string]string{},
		addresses: map[netip.Addr]bool{},
	}
	if a.newUID == nil {
		a.newUID = nrm.NewUID
	}
	if ws.Manifest != nil {
		if m, err := v1alpha1.Decode[v1alpha1.WorkspaceSpec](ws.Manifest); err == nil {
			a.environments = m.Spec.Environments
		}
	}
	// Documents that do not decode are skipped; validation reports them.
	for _, d := range ws.Documents {
		if d.APIVersion != v1alpha1.APIVersion {
			continue
		}
		switch d.Kind {
		case v1alpha1.KindProxmoxCluster:
			if c, err := v1alpha1.Decode[v1alpha1.ProxmoxClusterSpec](d); err == nil {
				a.clusters[c.Metadata.Name] = c
				// The nodes of the cluster use the addresses of its endpoints.
				for _, e := range c.Spec.Endpoints {
					if u, err := url.Parse(e); err == nil {
						if addr, err := netip.ParseAddr(u.Hostname()); err == nil {
							a.addresses[addr] = true
						}
					}
				}
			}
		case v1alpha1.KindNetwork:
			if n, err := v1alpha1.Decode[v1alpha1.NetworkSpec](d); err == nil {
				a.networks[n.Metadata.Name] = n
			}
		case v1alpha1.KindTemplate:
			if t, err := v1alpha1.Decode[v1alpha1.TemplateSpec](d); err == nil && t.Spec.Identity.VMID != 0 {
				a.useVMID(t.Spec.Cluster, t.Spec.Identity.VMID)
			}
		case v1alpha1.KindVirtualMachine:
			vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](d)
			if err != nil {
				continue
			}
			a.vms = append(a.vms, vm)
			s := vm.Spec
			if s.Identity.VMID != 0 {
				a.useVMID(s.Placement.Cluster, s.Identity.VMID)
			}
			// A pinned guest runs on its node even before it is admitted,
			// so automatic placement counts it there from the start.
			if node := cmp.Or(s.Placement.AssignedNode, pinnedNode(s.Placement)); node != "" {
				a.assignNode(vm, node)
			}
			for _, nic := range s.NICs {
				if nic.MAC != "" {
					a.useMAC(s.Placement.Cluster, nic.MAC)
				}
				if nic.IPv4 == nil {
					continue
				}
				if p, err := netip.ParsePrefix(nic.IPv4.Address); err == nil {
					a.addresses[p.Addr()] = true
				}
			}
		}
	}
	return a
}

func (a *allocator) useVMID(cluster string, vmid int) {
	if a.vmids[cluster] == nil {
		a.vmids[cluster] = map[int]bool{}
	}
	a.vmids[cluster][vmid] = true
}

func (a *allocator) useMAC(cluster, mac string) {
	if a.macs[cluster] == nil {
		a.macs[cluster] = map[string]bool{}
	}
	a.macs[cluster][strings.ToUpper(mac)] = true
}

func (a *allocator) assignNode(vm *v1alpha1.VirtualMachine, node string) {
	cluster := vm.Spec.Placement.Cluster
	if a.memory[cluster] == nil {
		a.memory[cluster] = map[string]int64{}
	}
	a.memory[cluster][node] += vm.Spec.Resources.Memory.Size.Bytes()
	a.nodes[vm.Metadata.Name] = node
}

func (a *allocator) assign(vm *v1alpha1.VirtualMachine, value any, path ...string) {
	a.assignDoc(vm.Document, value, path...)
}

func (a *allocator) assignDoc(doc *nrm.Document, value any, path ...string) {
	a.out = append(a.out, Assignment{Document: doc, Path: path, Value: value})
}

func (a *allocator) errorf(vm *v1alpha1.VirtualMachine, path []string, format string, args ...any) {
	a.errDoc(vm.Document, path, format, args...)
}

func (a *allocator) errDoc(doc *nrm.Document, path []string, format string, args ...any) {
	a.diags.Errorf(doc.File, doc.LineOf(path...), doc.FieldPath(path...), format, args...)
}

func (a *allocator) admit(vm *v1alpha1.VirtualMachine) {
	uid := vm.Metadata.UID
	if uid == "" {
		uid = a.newUID()
		a.assign(vm, uid, "metadata", "uid")
	}
	a.place(vm)
	a.allocateVMID(vm)
	a.allocateAddresses(vm)
	a.allocateMACs(vm, uid)
}

func (a *allocator) admitTemplate(template *v1alpha1.Template) {
	if template.Spec.Identity.VMID == 0 {
		a.allocateGuestID(template.Document, []string{"spec", "identity", "vmid"}, template.Spec.Cluster, "templates")
	}
}

// place assigns the node: the pinned node, or for automatic placement the
// node chosen by chooseNode.
func (a *allocator) place(vm *v1alpha1.VirtualMachine) {
	p := vm.Spec.Placement
	if p.AssignedNode != "" {
		return
	}
	cluster := a.clusters[p.Cluster]
	if cluster == nil {
		a.errorf(vm, []string{"spec", "placement", "cluster"}, "cluster %q does not exist", p.Cluster)
		return
	}
	node := pinnedNode(p)
	if node == "" {
		var ok bool
		if node, ok = a.chooseNode(vm, cluster); !ok {
			return
		}
		a.assignNode(vm, node)
	} else if nodes := cluster.Spec.Nodes; len(nodes) > 0 && !slices.Contains(nodes, node) {
		a.errorf(vm, []string{"spec", "placement", "node"}, "node %q is not in spec.nodes of cluster %q", node, p.Cluster)
		return
	}
	a.assign(vm, node, "spec", "placement", "assignedNode")
}

// pinnedNode returns the node that p pins a guest to, or "" for automatic
// placement.
func pinnedNode(p v1alpha1.Placement) string {
	if p.Node == v1alpha1.DefaultNode {
		return ""
	}
	return p.Node
}

// chooseNode picks a node for a guest with automatic placement. Nodes that
// run a guest it must be separated from are out, and if some of the others
// run a guest it should be kept with, only those remain. Of the remaining
// nodes, the one with the least memory assigned to guests wins, and ties
// go to the first name. Affinity counts in both directions: a guest that
// lists this one is treated as if this one listed it.
func (a *allocator) chooseNode(vm *v1alpha1.VirtualMachine, cluster *v1alpha1.ProxmoxCluster) (string, bool) {
	path := []string{"spec", "placement", "assignedNode"}
	if len(cluster.Spec.Nodes) == 0 {
		a.errorf(vm, path, "cannot choose a node: cluster %q lists no nodes in spec.nodes", cluster.Metadata.Name)
		return "", false
	}
	avoid, prefer := map[string]bool{}, map[string]bool{}
	name, aff := vm.Metadata.Name, vm.Spec.Placement.Affinity
	for _, other := range a.vms {
		node := a.nodes[other.Metadata.Name]
		if other.Metadata.Name == name || node == "" || other.Spec.Placement.Cluster != cluster.Metadata.Name {
			continue
		}
		oaff := other.Spec.Placement.Affinity
		if slices.Contains(aff.SeparateFrom, other.Metadata.Name) || slices.Contains(oaff.SeparateFrom, name) {
			avoid[node] = true
		}
		if slices.Contains(aff.KeepWith, other.Metadata.Name) || slices.Contains(oaff.KeepWith, name) {
			prefer[node] = true
		}
	}
	nodes := slices.Clone(cluster.Spec.Nodes)
	sort.Strings(nodes)
	var candidates, preferred []string
	for _, n := range nodes {
		if avoid[n] {
			continue
		}
		candidates = append(candidates, n)
		if prefer[n] {
			preferred = append(preferred, n)
		}
	}
	if len(candidates) == 0 {
		a.errorf(vm, path, "cannot choose a node: every node of cluster %q runs a guest that %s must be separated from", cluster.Metadata.Name, name)
		return "", false
	}
	if len(preferred) > 0 {
		candidates = preferred
	}
	memory := a.memory[cluster.Metadata.Name]
	best := candidates[0]
	for _, n := range candidates[1:] {
		if memory[n] < memory[best] {
			best = n
		}
	}
	return best, true
}

// allocateVMID assigns the lowest guest ID in the range of the VM's
// environment that no VM or template of its cluster uses.
func (a *allocator) allocateVMID(vm *v1alpha1.VirtualMachine) {
	if vm.Spec.Identity.VMID != 0 {
		return
	}
	a.allocateGuestID(vm.Document, []string{"spec", "identity", "vmid"}, vm.Spec.Placement.Cluster, vm.Metadata.Labels[nrm.LabelEnvironment])
}

func (a *allocator) allocateGuestID(doc *nrm.Document, path []string, cluster, env string) {
	e, defined := a.environments[env]
	switch {
	case env == "":
		a.errDoc(doc, path, "cannot allocate a guest ID: the label %s is missing, so the range of IDs is unknown", nrm.LabelEnvironment)
		return
	case !defined:
		a.errDoc(doc, path, "cannot allocate a guest ID: environment %q is not defined in %s", env, workspace.ManifestFile)
		return
	case len(e.VMIDRange) != 2:
		a.errDoc(doc, path, "cannot allocate a guest ID: environment %q has no vmidRange in %s", env, workspace.ManifestFile)
		return
	case e.VMIDRange[0] > e.VMIDRange[1]:
		a.errDoc(doc, path, "cannot allocate a guest ID: the vmidRange of environment %q in %s ends before it starts", env, workspace.ManifestFile)
		return
	}
	first, last := e.VMIDRange[0], e.VMIDRange[1]
	used := a.vmids[cluster]
	for id := first; id <= last; id++ {
		if !used[id] {
			a.assignDoc(doc, id, path...)
			a.useVMID(cluster, id)
			return
		}
	}
	a.errDoc(doc, path, "cannot allocate a guest ID: cluster %q uses every ID in the range %d-%d of environment %q", cluster, first, last, env)
}

// allocateAddresses assigns each network interface with IPv4 mode auto and
// no address the lowest host address in the static range of its network
// that is neither the gateway nor used by another network interface or a
// cluster endpoint.
func (a *allocator) allocateAddresses(vm *v1alpha1.VirtualMachine) {
	for i, nic := range vm.Spec.NICs {
		if nic.IPv4 == nil || nic.IPv4.Mode != v1alpha1.IPv4ModeAuto || nic.IPv4.Address != "" {
			continue
		}
		path := []string{"spec", "nics", strconv.Itoa(i), "ipv4", "address"}
		n := a.networks[nic.Network]
		if n == nil || n.Spec.IPv4 == nil || n.Spec.IPv4.Static == nil || n.Spec.IPv4.Static.Range == "" {
			a.errorf(vm, path, "cannot allocate an address: network %q has no static range in spec.ipv4.static.range", nic.Network)
			continue
		}
		ip := n.Spec.IPv4
		subnet, err := netip.ParsePrefix(ip.Subnet)
		first, last, rangeErr := parseRange(ip.Static.Range)
		if err != nil || rangeErr != nil || !first.Is4() {
			a.errorf(vm, path, "cannot allocate an address: network %q is invalid", nic.Network)
			continue
		}
		gateway, _ := netip.ParseAddr(ip.Gateway)
		if addr, ok := a.freeAddress(subnet, first, last, gateway); ok {
			a.assign(vm, netip.PrefixFrom(addr, subnet.Bits()).String(), path...)
			a.addresses[addr] = true
		} else {
			a.errorf(vm, path, "cannot allocate an address: every address in the static range %s of network %q is in use", ip.Static.Range, nic.Network)
		}
	}
}

// allocateMACs assigns each network interface without a MAC address a
// deterministic address derived from the resource UID and interface index,
// using the cluster's prefix and avoiding addresses already in use.
func (a *allocator) allocateMACs(vm *v1alpha1.VirtualMachine, uid string) {
	for i, nic := range vm.Spec.NICs {
		if nic.MAC != "" {
			continue
		}
		path := []string{"spec", "nics", strconv.Itoa(i), "mac"}
		cluster := a.clusters[vm.Spec.Placement.Cluster]
		if cluster == nil {
			a.errorf(vm, path, "cannot allocate a MAC address: cluster %q does not exist", vm.Spec.Placement.Cluster)
			continue
		}
		prefix := strings.ToUpper(cluster.Spec.MACPrefix())
		allocated := false
		for attempt := range 1 << 16 {
			h := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%d", uid, i, attempt)))
			mac := fmt.Sprintf("%s:%02X:%02X:%02X", prefix, h[0], h[1], h[2])
			if !a.macs[cluster.Metadata.Name][mac] {
				a.assign(vm, mac, path...)
				a.useMAC(cluster.Metadata.Name, mac)
				allocated = true
				break
			}
		}
		if !allocated {
			a.errorf(vm, path, "cannot allocate a MAC address: every candidate for interface %d is in use in cluster %q", i, cluster.Metadata.Name)
		}
	}
}

func (a *allocator) freeAddress(subnet netip.Prefix, first, last, gateway netip.Addr) (netip.Addr, bool) {
	for addr := first; addr.IsValid() && !last.Less(addr); addr = addr.Next() {
		if isHost(addr, subnet) && addr != gateway && !a.addresses[addr] {
			return addr, true
		}
	}
	return netip.Addr{}, false
}

// isHost reports whether addr is a host address of subnet. The first and
// the last address of a subnet are its network and broadcast addresses,
// except in /31 and /32 subnets.
func isHost(addr netip.Addr, subnet netip.Prefix) bool {
	if !subnet.Contains(addr) {
		return false
	}
	if subnet.Bits() >= 31 {
		return true
	}
	network := subnet.Masked().Addr()
	b := network.As4()
	for i := subnet.Bits(); i < 32; i++ {
		b[i/8] |= 1 << (7 - i%8)
	}
	return addr != network && addr != netip.AddrFrom4(b)
}

// parseRange parses an address range written as first-last.
func parseRange(s string) (first, last netip.Addr, err error) {
	a, b, _ := strings.Cut(s, "-")
	if first, err = netip.ParseAddr(a); err != nil {
		return first, last, err
	}
	last, err = netip.ParseAddr(b)
	return first, last, err
}

// Apply writes the assignments into the intent files of ws. It only
// inserts the new fields, so everything else in the files stays exactly as
// it is. It prepares every file before it writes any, so an assignment it
// cannot write leaves all files unchanged. With dryRun it only prepares
// the files, so it fails exactly when writing them would.
func Apply(ws *workspace.Workspace, assignments []Assignment, dryRun bool) error {
	var files []string
	edits := map[string][]yamledit.Edit{}
	for _, as := range assignments {
		f := as.Document.File
		if _, ok := edits[f]; !ok {
			files = append(files, f)
		}
		order := keyOrderFor(as.Document.Kind)
		edits[f] = append(edits[f], yamledit.Edit{Line: as.Document.Line, Path: as.Path, Value: as.Value, Order: order})
	}
	contents := make([][]byte, len(files))
	for i, f := range files {
		src, err := os.ReadFile(filepath.Join(ws.Root, filepath.FromSlash(f)))
		if err != nil {
			return err
		}
		if contents[i], err = yamledit.Insert(src, edits[f], nil); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	if dryRun {
		return nil
	}
	for i, f := range files {
		if err := os.WriteFile(filepath.Join(ws.Root, filepath.FromSlash(f)), contents[i], 0o644); err != nil {
			return err
		}
	}
	return nil
}

// vmDocument has the layout of a VirtualMachine document. Its fields give
// the order of the keys in each mapping, which is the order of the schema.
type vmDocument struct {
	APIVersion string                      `json:"apiVersion"`
	Kind       string                      `json:"kind"`
	Metadata   nrm.Metadata                `json:"metadata"`
	Spec       v1alpha1.VirtualMachineSpec `json:"spec"`
}

// templateDocument has the layout of a Template document.
type templateDocument struct {
	APIVersion string                `json:"apiVersion"`
	Kind       string                `json:"kind"`
	Metadata   nrm.Metadata          `json:"metadata"`
	Spec       v1alpha1.TemplateSpec `json:"spec"`
}

// keyOrder returns the keys of the mapping at path in a VirtualMachine
// document in schema order, so that new keys land next to their neighbors.
func keyOrder(path []string) []string {
	return keyOrderOf(reflect.TypeOf(vmDocument{}), path)
}

func keyOrderFor(kind string) yamledit.Order {
	var t reflect.Type
	switch kind {
	case v1alpha1.KindVirtualMachine:
		t = reflect.TypeOf(vmDocument{})
	case v1alpha1.KindTemplate:
		t = reflect.TypeOf(templateDocument{})
	default:
		return nil
	}
	return func(path []string) []string { return keyOrderOf(t, path) }
}

// keyOrderOf returns the keys of the mapping at path in the document type t
// in schema order. Mappings without fixed keys have no order.
func keyOrderOf(t reflect.Type, path []string) []string {
	for _, seg := range path {
		switch t = indirect(t); t.Kind() {
		case reflect.Slice:
			t = t.Elem()
		case reflect.Struct:
			f, ok := fieldByKey(t, seg)
			if !ok {
				return nil
			}
			t = f.Type
		default:
			return nil
		}
	}
	if t = indirect(t); t.Kind() != reflect.Struct {
		return nil
	}
	var keys []string
	for i := range t.NumField() {
		if k := jsonKey(t.Field(i)); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

func indirect(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func fieldByKey(t reflect.Type, key string) (reflect.StructField, bool) {
	for i := range t.NumField() {
		if f := t.Field(i); jsonKey(f) == key {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

func jsonKey(f reflect.StructField) string {
	k, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if k == "-" || !f.IsExported() {
		return ""
	}
	return k
}
