package proxmoxvm

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/quantity"
	"github.com/centopw/nodr/internal/resolve"
)

// Values that nodr always renders because it offers no other choice.
const (
	nicModel      = "virtio"
	discardOn     = "on"
	discardIgnore = "ignore"
	addressDHCP   = "dhcp"
	tagNodr       = "nodr"
	labelApp      = "app"
)

// ErrNotAdmitted reports a VM that lacks values admission fills in, such as
// its guest ID or the node it was assigned to (design §3.7).
var ErrNotAdmitted = errors.New("not admitted yet")

func diskInterface(i int) string { return fmt.Sprintf("scsi%d", i) }

// project computes the view of a VM: the values its managed block has to
// hold.
func project(vm *v1alpha1.VirtualMachine, r resolve.Resolver) (view, error) {
	s := vm.Spec.WithDefaults()
	if s.Placement.AssignedNode == "" {
		return view{}, fmt.Errorf("spec.placement.assignedNode is empty: %w", ErrNotAdmitted)
	}
	if s.Identity.VMID == 0 {
		return view{}, fmt.Errorf("spec.identity.vmid is not set: %w", ErrNotAdmitted)
	}
	v := view{
		Name:         vm.Metadata.Name,
		NodeName:     s.Placement.AssignedNode,
		VMID:         s.Identity.VMID,
		Tags:         tags(vm.Metadata.Labels, s.Proxmox.Tags),
		OnBoot:       *s.Lifecycle.StartOnBoot,
		Protection:   *s.Lifecycle.Protection,
		Machine:      s.Proxmox.Machine,
		BIOS:         s.Proxmox.BIOS,
		SCSIHardware: s.Proxmox.SCSIController,
		Agent:        &agentView{Enabled: *s.Guest.Agent},
		CPU:          &cpuView{Cores: s.Resources.CPU.Cores, Sockets: s.Resources.CPU.Sockets, Type: s.Resources.CPU.Type},
	}
	switch s.Lifecycle.PowerState {
	case v1alpha1.PowerStateRunning:
		v.Started = ptr(true)
	case v1alpha1.PowerStateStopped:
		v.Started = ptr(false)
	}
	if name := s.Source.Template; name != "" {
		t, err := r.Template(s.Placement.Cluster, name)
		if err != nil {
			return view{}, fmt.Errorf("spec.source.template: %w", err)
		}
		v.Clone = &cloneView{VMID: t.VMID, NodeName: t.Node, Full: true}
	}

	dedicated, err := s.Resources.Memory.Size.In(quantity.MiB)
	if err != nil {
		return view{}, fmt.Errorf("spec.resources.memory.size: %w", err)
	}
	v.Memory = &memView{Dedicated: int(dedicated)}
	if m := s.Resources.Memory.Minimum; m != nil {
		floating, err := m.In(quantity.MiB)
		if err != nil {
			return view{}, fmt.Errorf("spec.resources.memory.minimum: %w", err)
		}
		v.Memory.Floating = int(floating)
	}

	for i, d := range s.Disks {
		size, err := d.Size.In(quantity.GiB)
		if err != nil {
			return view{}, fmt.Errorf("spec.disks[%d].size: %w", i, err)
		}
		discard := discardIgnore
		if d.Options.Discard {
			discard = discardOn
		}
		v.Disks = append(v.Disks, diskView{
			DatastoreID: d.Storage,
			Interface:   diskInterface(i),
			Size:        int(size),
			Discard:     discard,
			SSD:         d.Options.SSD,
			IOThread:    d.Options.IOThread,
		})
	}

	networks := make([]resolve.Network, len(s.NICs))
	for i, nic := range s.NICs {
		n, err := r.Network(s.Placement.Cluster, nic.Network)
		if err != nil {
			return view{}, fmt.Errorf("spec.nics[%d].network: %w", i, err)
		}
		networks[i] = n
		v.NICs = append(v.NICs, nicView{Bridge: n.Bridge, VLANID: n.VLAN, MACAddress: nic.MAC, Model: nicModel})
	}

	if needsInit(vm.Spec) {
		init := &initView{}
		if len(s.Disks) > 0 {
			init.DatastoreID = s.Disks[0].Storage
		}
		for i, nic := range s.NICs {
			ip, err := ipv4(nic.IPv4, networks[i])
			if err != nil {
				return view{}, fmt.Errorf("spec.nics[%d].ipv4: %w", i, err)
			}
			init.IPConfigs = append(init.IPConfigs, ipConfigView{IPv4: ip})
		}
		if ci := s.Guest.CloudInit; ci != nil && (ci.User != "" || len(ci.AuthorizedKeys) > 0) {
			ua := &userAccountView{Username: ci.User}
			if len(ci.AuthorizedKeys) > 0 {
				keys, err := r.PublicKeys(ci.AuthorizedKeys)
				if err != nil {
					return view{}, fmt.Errorf("spec.guest.cloudInit.authorizedKeys: %w", err)
				}
				ua.Keys = keys
			}
			init.User = ua
		}
		v.Init = init
	}
	return v, nil
}

// needsInit reports whether a VM gets a cloud-init drive: it has cloud-init
// settings or IPv4 settings on a network interface.
func needsInit(s v1alpha1.VirtualMachineSpec) bool {
	if s.Guest.CloudInit != nil {
		return true
	}
	for _, nic := range s.NICs {
		if nic.IPv4 != nil {
			return true
		}
	}
	return false
}

func ipv4(ip *v1alpha1.IPv4, n resolve.Network) (*ipv4View, error) {
	switch ip.Mode {
	case v1alpha1.IPv4ModeDHCP:
		return &ipv4View{Address: addressDHCP}, nil
	case v1alpha1.IPv4ModeAuto:
		if ip.Address == "" {
			return nil, fmt.Errorf("the address is not allocated: %w", ErrNotAdmitted)
		}
	case v1alpha1.IPv4ModeStatic:
		if ip.Address == "" {
			return nil, errors.New("static addressing needs an address")
		}
	default:
		return nil, fmt.Errorf("unknown mode %q", ip.Mode)
	}
	return &ipv4View{Address: ip.Address, Gateway: n.Gateway}, nil
}

// tags returns the Proxmox tags of a VM, sorted: the tags nodr derives from
// labels plus the extra tags of spec.proxmox.tags.
func tags(labels map[string]string, extra []string) []string {
	set := map[string]bool{}
	for _, t := range derivedTags(labels) {
		set[t] = true
	}
	for _, t := range extra {
		set[t] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// derivedTags returns the tags nodr sets from labels: nodr, env-<environment>
// and app-<app> (design §6.7).
func derivedTags(labels map[string]string) []string {
	out := []string{tagNodr}
	if env := labels[nrm.LabelEnvironment]; env != "" {
		out = append(out, "env-"+tagSafe(env))
	}
	if app := labels[labelApp]; app != "" {
		out = append(out, "app-"+tagSafe(app))
	}
	return out
}

// tagSafe lowercases a label value and replaces characters that Proxmox
// tags do not allow.
func tagSafe(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', strings.ContainsRune("-_.+", r):
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func ptr[T any](v T) *T { return &v }
