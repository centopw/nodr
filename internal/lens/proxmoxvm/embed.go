package proxmoxvm

import (
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/quantity"
	"github.com/centopw/nodr/internal/resolve"
)

// embedder writes the values of a lifted view back into intent. It handles
// only the code paths that differ from the VM's projection, so fields the
// code did not change keep exactly what the intent document says. Values
// that intent cannot express are recorded as failures; they make the field
// code-owned and the intent keeps its previous value.
type embedder struct {
	out      *v1alpha1.VirtualMachine
	base     view
	lifted   view
	changed  map[string]bool
	failures map[string]string
	r        resolve.Resolver
}

// embed returns a copy of vm with the changed values of lifted applied, and
// the code paths that could not be applied with the reason for each.
func embed(vm *v1alpha1.VirtualMachine, base, lifted view, changed []string, r resolve.Resolver) (*v1alpha1.VirtualMachine, map[string]string) {
	out := &v1alpha1.VirtualMachine{
		Metadata: copyMetadata(vm.Metadata),
		Spec:     vm.Spec.DeepCopy(),
		Document: vm.Document,
	}
	e := &embedder{out: out, base: base, lifted: lifted, changed: map[string]bool{}, failures: map[string]string{}, r: r}
	for _, p := range changed {
		e.changed[p] = true
	}
	e.scalars()
	e.clone()
	e.resources()
	e.disks()
	e.nics()
	e.initialization()
	return out, e.failures
}

// has reports whether path itself changed.
func (e *embedder) has(path string) bool { return e.changed[path] }

// under reports whether path or anything below it changed.
func (e *embedder) under(path string) bool {
	for p := range e.changed {
		if p == path || strings.HasPrefix(p, path+".") || strings.HasPrefix(p, path+"[") {
			return true
		}
	}
	return false
}

func (e *embedder) fail(path, format string, args ...any) {
	if _, dup := e.failures[path]; !dup {
		e.failures[path] = fmt.Sprintf(format, args...)
	}
}

func (e *embedder) scalars() {
	s, lv := &e.out.Spec, e.lifted
	if e.has("name") {
		if err := nrm.ValidateName(lv.Name); err != nil {
			e.fail("name", "%q is not a valid resource name: it %v", lv.Name, err)
		} else {
			e.out.Metadata.Name = lv.Name
		}
	}
	if e.has("node_name") {
		s.Placement.AssignedNode = lv.NodeName
		if node := s.Placement.Node; node != "" && node != v1alpha1.DefaultNode {
			s.Placement.Node = lv.NodeName
		}
	}
	if e.has("vm_id") {
		s.Identity.VMID = lv.VMID
	}
	if e.has("tags") {
		derived := derivedTags(e.out.Metadata.Labels)
		var missing, extra []string
		for _, t := range derived {
			if !slices.Contains(lv.Tags, t) {
				missing = append(missing, t)
			}
		}
		for _, t := range lv.Tags {
			if !slices.Contains(derived, t) && !slices.Contains(extra, t) {
				extra = append(extra, t)
			}
		}
		if len(missing) > 0 {
			e.fail("tags", "nodr sets the tags %s from labels; change the labels instead", strings.Join(missing, ", "))
		} else {
			sort.Strings(extra)
			s.Proxmox.Tags = extra
		}
	}
	if e.has("on_boot") {
		s.Lifecycle.StartOnBoot = ptr(lv.OnBoot)
	}
	if e.has("started") {
		switch {
		case lv.Started == nil:
			s.Lifecycle.PowerState = v1alpha1.PowerStateUnmanaged
		case *lv.Started:
			s.Lifecycle.PowerState = v1alpha1.PowerStateRunning
		default:
			s.Lifecycle.PowerState = v1alpha1.PowerStateStopped
		}
	}
	if e.has("protection") {
		s.Lifecycle.Protection = ptr(lv.Protection)
	}
	if e.has("machine") {
		s.Proxmox.Machine = lv.Machine
	}
	if e.has("bios") {
		if lv.BIOS != "" && lv.BIOS != "seabios" && lv.BIOS != "ovmf" {
			e.fail("bios", "nodr supports the firmware seabios and ovmf, not %q", lv.BIOS)
		} else {
			s.Proxmox.BIOS = lv.BIOS
		}
	}
	if e.has("scsi_hardware") {
		allowed := []string{"lsi", "lsi53c810", "virtio-scsi-pci", "virtio-scsi-single", "megasas", "pvscsi"}
		if lv.SCSIHardware != "" && !slices.Contains(allowed, lv.SCSIHardware) {
			e.fail("scsi_hardware", "unknown SCSI controller %q", lv.SCSIHardware)
		} else {
			s.Proxmox.SCSIController = lv.SCSIHardware
		}
	}
	if e.has("agent.enabled") && lv.Agent != nil {
		s.Guest.Agent = ptr(lv.Agent.Enabled)
	}
}

func (e *embedder) clone() {
	if !e.under("clone") {
		return
	}
	c := e.lifted.Clone
	if c == nil {
		e.out.Spec.Source.Template = ""
		return
	}
	if !c.Full {
		e.fail("clone.full", "nodr always creates full clones")
	}
	if !e.has("clone") && !e.has("clone.vm_id") && !e.has("clone.node_name") {
		return
	}
	cluster := e.out.Spec.Placement.Cluster
	var (
		t   resolve.Template
		err error
	)
	if e.has("clone") || e.has("clone.vm_id") {
		t, err = e.r.TemplateFor(cluster, c.VMID)
	} else {
		t, err = e.r.Template(cluster, e.out.Spec.Source.Template)
	}
	if err != nil {
		// Without a template, intent cannot describe the clone at all, so
		// the whole block stays in code.
		e.fail("clone", "%v", err)
		return
	}
	e.out.Spec.Source.Template = t.Name
	switch {
	case c.NodeName == t.Node:
	case t.Node == "":
		e.fail("clone.node_name", "the template %s does not name its node; set spec.node of the template instead", t.Name)
	default:
		e.fail("clone.node_name", "the template %s is on node %s", t.Name, t.Node)
	}
}

func (e *embedder) resources() {
	s, lv := &e.out.Spec, e.lifted
	if lv.CPU != nil {
		if e.has("cpu.cores") {
			s.Resources.CPU.Cores = lv.CPU.Cores
		}
		if e.has("cpu.sockets") {
			s.Resources.CPU.Sockets = lv.CPU.Sockets
		}
		if e.has("cpu.type") {
			s.Resources.CPU.Type = lv.CPU.Type
		}
	}
	if lv.Memory == nil {
		return
	}
	if e.has("memory.dedicated") {
		if q, err := quantity.FromUnits(int64(lv.Memory.Dedicated), quantity.MiB); err != nil || q.IsZero() {
			e.fail("memory.dedicated", "the memory size must be a positive number of MiB")
		} else {
			s.Resources.Memory.Size = q
		}
	}
	if e.has("memory.floating") {
		switch q, err := quantity.FromUnits(int64(lv.Memory.Floating), quantity.MiB); {
		case err != nil:
			e.fail("memory.floating", "the ballooning minimum must be a number of MiB")
		case q.IsZero():
			s.Resources.Memory.Minimum = nil
		default:
			s.Resources.Memory.Minimum = &q
		}
	}
}

func (e *embedder) disks() {
	if !e.under("disk") {
		return
	}
	s := &e.out.Spec
	lifted := e.lifted.Disks
	kept := len(e.base.Disks)
	if len(lifted) < kept {
		kept = len(lifted)
	}
	s.Disks = s.Disks[:min(len(s.Disks), kept)]
	for i := range kept {
		e.disk(i, lifted[i], false)
	}
	// Disks added in code are embedded only while every field maps; the
	// first one that does not, and all after it, stay code-owned.
	for i := kept; i < len(lifted); i++ {
		s.Disks = append(s.Disks, v1alpha1.Disk{Name: newDiskName(s.Disks)})
		if !e.disk(i, lifted[i], true) {
			s.Disks = s.Disks[:i]
			for j := i; j < len(lifted); j++ {
				e.fail(fmt.Sprintf("disk[%d]", j), "the new disk cannot be described in intent; fix the attributes reported for it")
			}
			return
		}
	}
}

// disk embeds one disk and reports whether all of its fields mapped.
func (e *embedder) disk(i int, d diskView, isNew bool) bool {
	p := fmt.Sprintf("disk[%d]", i)
	target := &e.out.Spec.Disks[i]
	ok := true
	check := func(field string) bool { return isNew || e.has(p+"."+field) }
	if check("interface") && d.Interface != diskInterface(i) {
		e.fail(p+".interface", "nodr attaches disks in order as scsi0, scsi1 and so on; this disk has to be %s", diskInterface(i))
		ok = false
	}
	if check("datastore_id") {
		if d.DatastoreID == "" {
			e.fail(p+".datastore_id", "the storage must not be empty")
			ok = false
		} else {
			target.Storage = d.DatastoreID
		}
	}
	if check("size") {
		if q, err := quantity.FromUnits(int64(d.Size), quantity.GiB); err != nil || q.IsZero() {
			e.fail(p+".size", "the disk size must be a positive number of GiB")
			ok = false
		} else {
			target.Size = q
		}
	}
	if check("discard") {
		switch d.Discard {
		case discardOn:
			target.Options.Discard = true
		case discardIgnore:
			target.Options.Discard = false
		default:
			e.fail(p+".discard", "discard must be %q or %q", discardOn, discardIgnore)
			ok = false
		}
	}
	if check("ssd") {
		target.Options.SSD = d.SSD
	}
	if check("iothread") {
		target.Options.IOThread = d.IOThread
	}
	return ok
}

// newDiskName returns a disk name that the VM does not use yet.
func newDiskName(disks []v1alpha1.Disk) string {
	for n := len(disks); ; n++ {
		name := fmt.Sprintf("disk%d", n)
		if !slices.ContainsFunc(disks, func(d v1alpha1.Disk) bool { return d.Name == name }) {
			return name
		}
	}
}

func (e *embedder) nics() {
	if !e.under("network_device") {
		return
	}
	s := &e.out.Spec
	lifted := e.lifted.NICs
	kept := min(len(e.base.NICs), len(lifted))
	s.NICs = s.NICs[:min(len(s.NICs), kept)]
	for i := range kept {
		e.nic(i, lifted[i], false)
	}
	for i := kept; i < len(lifted); i++ {
		s.NICs = append(s.NICs, v1alpha1.NIC{})
		if !e.nic(i, lifted[i], true) {
			s.NICs = s.NICs[:i]
			for j := i; j < len(lifted); j++ {
				e.fail(fmt.Sprintf("network_device[%d]", j), "the new network device cannot be described in intent; fix the attributes reported for it")
			}
			return
		}
	}
}

// nic embeds one network device and reports whether all of its fields
// mapped.
func (e *embedder) nic(i int, n nicView, isNew bool) bool {
	p := fmt.Sprintf("network_device[%d]", i)
	target := &e.out.Spec.NICs[i]
	ok := true
	if isNew || e.has(p+".bridge") || e.has(p+".vlan_id") {
		net, err := e.r.NetworkFor(e.out.Spec.Placement.Cluster, n.Bridge, n.VLANID)
		if err != nil {
			field := p + ".vlan_id"
			if isNew || e.has(p+".bridge") {
				field = p + ".bridge"
			}
			e.fail(field, "%v", err)
			ok = false
		} else {
			target.Network = net.Name
		}
	}
	if isNew || e.has(p+".mac_address") {
		target.MAC = n.MACAddress
	}
	if (isNew || e.has(p+".model")) && n.Model != nicModel {
		e.fail(p+".model", "nodr uses %s network devices", nicModel)
		ok = false
	}
	return ok
}

func (e *embedder) initialization() {
	if !e.under("initialization") && !e.under("network_device") && !e.under("disk") {
		return
	}
	s := &e.out.Spec
	in := e.lifted.Init
	if in == nil {
		if e.under("initialization") {
			s.Guest.CloudInit = nil
			for i := range s.NICs {
				s.NICs[i].IPv4 = nil
			}
		}
		return
	}
	isNew := e.base.Init == nil
	if isNew && s.Guest.CloudInit == nil {
		// Keep the cloud-init drive in intent, even without a user account.
		s.Guest.CloudInit = &v1alpha1.CloudInit{}
	}
	if isNew || e.under("initialization.datastore_id") || e.under("disk") {
		expected := ""
		if len(s.Disks) > 0 {
			expected = s.Disks[0].Storage
		}
		if in.DatastoreID != expected {
			e.fail("initialization.datastore_id", "nodr puts the cloud-init drive on the storage of the first disk (%q)", expected)
		}
	}
	for i := len(s.NICs); i < len(in.IPConfigs); i++ {
		e.fail(fmt.Sprintf("initialization.ip_config[%d]", i), "there is no network device for this ip_config block")
	}
	for i := range s.NICs {
		e.ipConfig(i, in, isNew)
	}
	e.userAccount(in.User, isNew)
	// An initialization block means the VM has a cloud-init drive, even when
	// no interface has IPv4 settings and no user account is set.
	if !needsInit(*s) {
		s.Guest.CloudInit = &v1alpha1.CloudInit{}
	}
}

func (e *embedder) ipConfig(i int, in *initView, initIsNew bool) {
	p := fmt.Sprintf("initialization.ip_config[%d]", i)
	nicChanged := e.under(fmt.Sprintf("network_device[%d]", i)) || i >= len(e.base.NICs)
	baseHasItem := e.base.Init != nil && i < len(e.base.Init.IPConfigs)
	if !initIsNew && !nicChanged && baseHasItem && !e.under(p) && !e.has("initialization.ip_config") {
		return
	}
	nic := &e.out.Spec.NICs[i]
	var ip *ipv4View
	if i < len(in.IPConfigs) {
		ip = in.IPConfigs[i].IPv4
	}
	switch {
	case ip == nil:
		nic.IPv4 = nil
	case ip.Address == addressDHCP:
		if ip.Gateway != "" {
			e.fail(p+".ipv4.gateway", "with DHCP the gateway comes from the DHCP server")
		}
		if nic.IPv4 == nil || nic.IPv4.Mode != v1alpha1.IPv4ModeDHCP {
			nic.IPv4 = &v1alpha1.IPv4{Mode: v1alpha1.IPv4ModeDHCP}
		}
	default:
		if _, err := netip.ParsePrefix(ip.Address); err != nil {
			e.fail(p+".ipv4", "%q is not an address with a prefix length, such as 10.0.20.21/24", ip.Address)
			return
		}
		if nic.IPv4 == nil || nic.IPv4.Address != ip.Address {
			nic.IPv4 = &v1alpha1.IPv4{Mode: v1alpha1.IPv4ModeStatic, Address: ip.Address}
		}
		if nic.Network != "" {
			if net, err := e.r.Network(e.out.Spec.Placement.Cluster, nic.Network); err == nil && ip.Gateway != net.Gateway {
				e.fail(p+".ipv4.gateway", "the gateway comes from the network %s (%q)", net.Name, net.Gateway)
			}
		}
	}
}

func (e *embedder) userAccount(ua *userAccountView, initIsNew bool) {
	const p = "initialization.user_account"
	if !initIsNew && !e.under(p) {
		return
	}
	ci := e.out.Spec.Guest.CloudInit
	if ua == nil {
		if ci != nil {
			ci.User, ci.AuthorizedKeys = "", nil
		}
		return
	}
	if ci == nil {
		ci = &v1alpha1.CloudInit{}
		e.out.Spec.Guest.CloudInit = ci
	}
	isNew := initIsNew || e.has(p)
	if isNew || e.has(p+".username") {
		ci.User = ua.Username
	}
	if isNew || e.has(p+".keys") {
		if len(ua.Keys) == 0 {
			ci.AuthorizedKeys = nil
			return
		}
		names, err := e.r.KeyNames(ua.Keys)
		if err != nil {
			// If nothing else would make nodr render the user account, the
			// whole block stays in code.
			path := p + ".keys"
			if ci.User == "" && len(ci.AuthorizedKeys) == 0 {
				path = p
			}
			e.fail(path, "%v", err)
			return
		}
		ci.AuthorizedKeys = names
	}
}

func copyMetadata(m nrm.Metadata) nrm.Metadata {
	out := m
	out.Labels = copyMap(m.Labels)
	out.Annotations = copyMap(m.Annotations)
	return out
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
