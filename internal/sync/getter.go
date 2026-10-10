package sync

import (
	"strings"

	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/quantity"
)

// valueAt returns the value of vm at the given intent path, and whether
// the path is one this package knows how to read and write. It covers
// exactly the leaf intent paths that internal/lens/proxmoxvm's Report ever
// reports as Synced (lens.go's intentPaths table, minus the entries whose
// pattern is itself a container, per lens.go's containers set): that
// closed, small set is the only domain Synchronize ever needs to write
// back into intent.
//
// A few code attributes (the derived SCSI/network-device index, the
// cloud-init datastore) map onto a container's whole-struct intent path
// with no leaf of their own; valueAt reports those as not ok, since
// nothing in intent represents them and there is nothing to embed.
//
// path segments that index a sequence, such as "spec.disks[1].size", are
// handled by first stripping and parsing the index.
func valueAt(vm *v1alpha1.VirtualMachine, path string) (any, bool) {
	if path == "metadata.name" {
		return vm.Metadata.Name, true
	}
	return valueAtSpec(&vm.Spec, path)
}

func valueAtSpec(spec *v1alpha1.VirtualMachineSpec, path string) (any, bool) {
	switch path {
	case "spec.placement.assignedNode":
		return spec.Placement.AssignedNode, true
	case "spec.identity.vmid":
		return spec.Identity.VMID, true
	case "spec.source.template":
		return spec.Source.Template, true
	case "spec.proxmox.tags":
		return spec.Proxmox.Tags, true
	case "spec.proxmox.machine":
		return spec.Proxmox.Machine, true
	case "spec.proxmox.bios":
		return spec.Proxmox.BIOS, true
	case "spec.proxmox.scsiController":
		return spec.Proxmox.SCSIController, true
	case "spec.lifecycle.startOnBoot":
		if spec.Lifecycle.StartOnBoot == nil {
			return nil, false
		}
		return *spec.Lifecycle.StartOnBoot, true
	case "spec.lifecycle.powerState":
		return spec.Lifecycle.PowerState, true
	case "spec.lifecycle.protection":
		if spec.Lifecycle.Protection == nil {
			return nil, false
		}
		return *spec.Lifecycle.Protection, true
	case "spec.guest.agent":
		if spec.Guest.Agent == nil {
			return nil, false
		}
		return *spec.Guest.Agent, true
	case "spec.guest.cloudInit.user":
		if spec.Guest.CloudInit == nil {
			return nil, false
		}
		return spec.Guest.CloudInit.User, true
	case "spec.guest.cloudInit.authorizedKeys":
		if spec.Guest.CloudInit == nil {
			return nil, false
		}
		return spec.Guest.CloudInit.AuthorizedKeys, true
	case "spec.resources.cpu.cores":
		return spec.Resources.CPU.Cores, true
	case "spec.resources.cpu.sockets":
		return spec.Resources.CPU.Sockets, true
	case "spec.resources.cpu.type":
		return spec.Resources.CPU.Type, true
	case "spec.resources.memory.size":
		return spec.Resources.Memory.Size, true
	case "spec.resources.memory.minimum":
		if spec.Resources.Memory.Minimum == nil {
			return nil, false
		}
		return *spec.Resources.Memory.Minimum, true
	}

	if i, rest, ok := indexed(path, "spec.disks["); ok {
		if i < 0 || i >= len(spec.Disks) {
			return nil, false
		}
		d := &spec.Disks[i]
		switch rest {
		case ".storage":
			return d.Storage, true
		case ".size":
			return d.Size, true
		case ".options.discard":
			return d.Options.Discard, true
		case ".options.ssd":
			return d.Options.SSD, true
		case ".options.iothread":
			return d.Options.IOThread, true
		}
		// A bare "spec.disks[i]" or any other suffix has no intent leaf
		// of its own (for example the derived SCSI interface number).
		return nil, false
	}

	if i, rest, ok := indexed(path, "spec.nics["); ok {
		if i < 0 || i >= len(spec.NICs) {
			return nil, false
		}
		n := &spec.NICs[i]
		switch rest {
		case ".network":
			return n.Network, true
		case ".mac":
			return n.MAC, true
		case ".ipv4.address":
			if n.IPv4 == nil {
				return nil, false
			}
			return n.IPv4.Address, true
		}
		return nil, false
	}

	return nil, false
}

// indexed splits a path of the form prefix+"N]"+rest into N and rest, for
// example indexed("spec.disks[1].size", "spec.disks[") returns (1, ".size",
// true).
func indexed(path, prefix string) (index int, rest string, ok bool) {
	tail, found := strings.CutPrefix(path, prefix)
	if !found {
		return 0, "", false
	}
	end := strings.IndexByte(tail, ']')
	if end < 0 {
		return 0, "", false
	}
	n, ok := parseIndex(tail[:end])
	if !ok {
		return 0, "", false
	}
	return n, tail[end+1:], true
}

// setAt sets the value of vm at the given intent path, and reports whether
// the path and the type of value are ones this package knows how to write.
// It is the write-side mirror of valueAt, used to apply the caller's
// desired changes onto the VM the lens lifted from code before Put embeds
// it back.
func setAt(vm *v1alpha1.VirtualMachine, path string, value any) bool {
	if path == "metadata.name" {
		s, ok := value.(string)
		if !ok {
			return false
		}
		vm.Metadata.Name = s
		return true
	}
	return setAtSpec(&vm.Spec, path, value)
}

func setAtSpec(spec *v1alpha1.VirtualMachineSpec, path string, value any) bool {
	switch path {
	case "spec.placement.assignedNode":
		return setString(&spec.Placement.AssignedNode, value)
	case "spec.identity.vmid":
		return setInt(&spec.Identity.VMID, value)
	case "spec.source.template":
		return setString(&spec.Source.Template, value)
	case "spec.proxmox.tags":
		return setStringSlice(&spec.Proxmox.Tags, value)
	case "spec.proxmox.machine":
		return setString(&spec.Proxmox.Machine, value)
	case "spec.proxmox.bios":
		return setString(&spec.Proxmox.BIOS, value)
	case "spec.proxmox.scsiController":
		return setString(&spec.Proxmox.SCSIController, value)
	case "spec.lifecycle.startOnBoot":
		return setBoolPtr(&spec.Lifecycle.StartOnBoot, value)
	case "spec.lifecycle.powerState":
		return setString(&spec.Lifecycle.PowerState, value)
	case "spec.lifecycle.protection":
		return setBoolPtr(&spec.Lifecycle.Protection, value)
	case "spec.guest.agent":
		return setBoolPtr(&spec.Guest.Agent, value)
	case "spec.guest.cloudInit.user":
		if spec.Guest.CloudInit == nil {
			spec.Guest.CloudInit = &v1alpha1.CloudInit{}
		}
		return setString(&spec.Guest.CloudInit.User, value)
	case "spec.guest.cloudInit.authorizedKeys":
		if spec.Guest.CloudInit == nil {
			spec.Guest.CloudInit = &v1alpha1.CloudInit{}
		}
		return setStringSlice(&spec.Guest.CloudInit.AuthorizedKeys, value)
	case "spec.resources.cpu.cores":
		return setInt(&spec.Resources.CPU.Cores, value)
	case "spec.resources.cpu.sockets":
		return setInt(&spec.Resources.CPU.Sockets, value)
	case "spec.resources.cpu.type":
		return setString(&spec.Resources.CPU.Type, value)
	case "spec.resources.memory.size":
		return setQuantity(&spec.Resources.Memory.Size, value)
	case "spec.resources.memory.minimum":
		q, ok := value.(quantity.Quantity)
		if !ok {
			return false
		}
		spec.Resources.Memory.Minimum = &q
		return true
	}

	if i, rest, ok := indexed(path, "spec.disks["); ok {
		if i < 0 || i >= len(spec.Disks) {
			return false
		}
		d := &spec.Disks[i]
		switch rest {
		case ".storage":
			return setString(&d.Storage, value)
		case ".size":
			return setQuantity(&d.Size, value)
		case ".options.discard":
			return setBool(&d.Options.Discard, value)
		case ".options.ssd":
			return setBool(&d.Options.SSD, value)
		case ".options.iothread":
			return setBool(&d.Options.IOThread, value)
		}
		return false
	}

	if i, rest, ok := indexed(path, "spec.nics["); ok {
		if i < 0 || i >= len(spec.NICs) {
			return false
		}
		n := &spec.NICs[i]
		switch rest {
		case ".network":
			return setString(&n.Network, value)
		case ".mac":
			return setString(&n.MAC, value)
		case ".ipv4.address":
			if n.IPv4 == nil {
				n.IPv4 = &v1alpha1.IPv4{}
			}
			return setString(&n.IPv4.Address, value)
		}
		return false
	}

	return false
}

func setString(dst *string, value any) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	*dst = s
	return true
}

func setInt(dst *int, value any) bool {
	n, ok := value.(int)
	if !ok {
		return false
	}
	*dst = n
	return true
}

func setBool(dst *bool, value any) bool {
	b, ok := value.(bool)
	if !ok {
		return false
	}
	*dst = b
	return true
}

func setBoolPtr(dst **bool, value any) bool {
	b, ok := value.(bool)
	if !ok {
		return false
	}
	*dst = &b
	return true
}

func setQuantity(dst *quantity.Quantity, value any) bool {
	q, ok := value.(quantity.Quantity)
	if !ok {
		return false
	}
	*dst = q
	return true
}

func setStringSlice(dst *[]string, value any) bool {
	s, ok := value.([]string)
	if !ok {
		return false
	}
	*dst = s
	return true
}
