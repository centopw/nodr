package v1alpha1

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/quantity"
)

// checker reports semantic problems in one document.
type checker struct {
	d     *nrm.Document
	diags *diag.List
}

func (c checker) errorf(path []string, format string, args ...any) {
	c.diags.Errorf(c.d.File, c.d.LineOf(path...), c.d.FieldPath(path...), format, args...)
}

func (c checker) warnf(path []string, format string, args ...any) {
	c.diags.Warnf(c.d.File, c.d.LineOf(path...), c.d.FieldPath(path...), format, args...)
}

func fieldPath(segs ...any) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		switch v := s.(type) {
		case int:
			out[i] = strconv.Itoa(v)
		default:
			out[i] = fmt.Sprint(v)
		}
	}
	return out
}

func checkVirtualMachine(d *nrm.Document, diags *diag.List) {
	vm, err := Decode[VirtualMachineSpec](d)
	if err != nil {
		return
	}
	c := checker{d: d, diags: diags}
	s := vm.Spec

	sizePath := fieldPath("spec", "resources", "memory", "size")
	size := s.Resources.Memory.Size
	if size.IsZero() {
		c.errorf(sizePath, "must be greater than zero")
	} else if _, err := size.In(quantity.MiB); err != nil {
		c.errorf(sizePath, "must be a whole number of MiB: %v", err)
	}
	if minimum := s.Resources.Memory.Minimum; minimum != nil {
		minPath := fieldPath("spec", "resources", "memory", "minimum")
		if _, err := minimum.In(quantity.MiB); err != nil {
			c.errorf(minPath, "must be a whole number of MiB: %v", err)
		}
		if minimum.Bytes() > size.Bytes() {
			c.errorf(minPath, "cannot be larger than the memory size %s", size)
		}
	}

	names := map[string]int{}
	for i, disk := range s.Disks {
		if j, dup := names[disk.Name]; dup {
			c.errorf(fieldPath("spec", "disks", i, "name"), "disk name %q is already used by spec.disks[%d]", disk.Name, j)
		} else {
			names[disk.Name] = i
		}
		p := fieldPath("spec", "disks", i, "size")
		if disk.Size.IsZero() {
			c.errorf(p, "must be greater than zero")
		} else if _, err := disk.Size.In(quantity.GiB); err != nil {
			c.errorf(p, "must be a whole number of GiB: %v", err)
		}
	}

	for i, nic := range s.NICs {
		if nic.IPv4 == nil {
			continue
		}
		if nic.IPv4.Mode == IPv4ModeDHCP && nic.IPv4.Address != "" {
			c.errorf(fieldPath("spec", "nics", i, "ipv4", "address"), "an address cannot be combined with mode dhcp")
		}
		if nic.IPv4.Address != "" {
			if p, err := netip.ParsePrefix(nic.IPv4.Address); err != nil {
				c.errorf(fieldPath("spec", "nics", i, "ipv4", "address"), "invalid address: %v", err)
			} else if p.Addr() == p.Masked().Addr() && p.Bits() < 31 {
				c.errorf(fieldPath("spec", "nics", i, "ipv4", "address"), "%s is the network address of its subnet, not a host address", p.Addr())
			}
		}
	}

	self := d.Metadata.Name
	keep := map[string]bool{}
	for _, n := range s.Placement.Affinity.KeepWith {
		keep[n] = true
	}
	for i, n := range s.Placement.Affinity.SeparateFrom {
		p := fieldPath("spec", "placement", "affinity", "separateFrom", i)
		switch {
		case n == self:
			c.errorf(p, "a guest cannot be separated from itself")
		case keep[n]:
			c.errorf(p, "%s is also listed in keepWith", n)
		}
	}
	if node := s.Placement.Node; node != "" && node != DefaultNode && s.Placement.AssignedNode != "" && s.Placement.AssignedNode != node {
		c.errorf(fieldPath("spec", "placement", "assignedNode"), "must match the pinned node %s", node)
	}
}

func checkNetwork(d *nrm.Document, diags *diag.List) {
	n, err := Decode[NetworkSpec](d)
	if err != nil || n.Spec.IPv4 == nil {
		return
	}
	c := checker{d: d, diags: diags}
	ip := n.Spec.IPv4
	subnet, err := netip.ParsePrefix(ip.Subnet)
	if err != nil {
		c.errorf(fieldPath("spec", "ipv4", "subnet"), "invalid subnet: %v", err)
		return
	}
	if subnet != subnet.Masked() {
		c.errorf(fieldPath("spec", "ipv4", "subnet"), "has host bits set; did you mean %s?", subnet.Masked())
		return
	}
	var gateway netip.Addr
	if ip.Gateway != "" {
		gateway, err = netip.ParseAddr(ip.Gateway)
		if err != nil || !subnet.Contains(gateway) {
			c.errorf(fieldPath("spec", "ipv4", "gateway"), "must be an address inside %s", subnet)
			gateway = netip.Addr{}
		}
	}
	var ranges []addrRange
	check := func(value string, path []string) {
		if value == "" {
			return
		}
		r, err := parseRange(value)
		switch {
		case err != nil:
			c.errorf(path, "%v", err)
		case !subnet.Contains(r.first) || !subnet.Contains(r.last):
			c.errorf(path, "must be inside %s", subnet)
		default:
			for _, other := range ranges {
				if r.overlaps(other) {
					c.errorf(path, "overlaps the range %s", other)
				}
			}
			if gateway.IsValid() && r.contains(gateway) {
				c.warnf(path, "contains the gateway %s", gateway)
			}
			ranges = append(ranges, r)
		}
	}
	if ip.DHCP != nil {
		check(ip.DHCP.Range, fieldPath("spec", "ipv4", "dhcp", "range"))
	}
	if ip.Static != nil {
		check(ip.Static.Range, fieldPath("spec", "ipv4", "static", "range"))
	}
}

func checkTemplate(d *nrm.Document, diags *diag.List) {
	t, err := Decode[TemplateSpec](d)
	if err != nil {
		return
	}
	algo, sum, _ := strings.Cut(t.Spec.Image.Checksum, ":")
	want := map[string]int{"sha256": 64, "sha512": 128}[algo]
	if len(sum) != want {
		checker{d: d, diags: diags}.errorf(fieldPath("spec", "image", "checksum"), "a %s checksum has %d hexadecimal digits, not %d", algo, want, len(sum))
	}
}

// occurrence is a field of a document that holds a value that must be
// unique.
type occurrence struct {
	d    *nrm.Document
	path []string
}

// String formats the occurrence for messages, for example
// "VirtualMachine/web-01 at intent/compute/web-01.yaml:17".
func (o occurrence) String() string {
	return fmt.Sprintf("%s at %s:%d", o.d.Ref(), o.d.File, o.d.LineOf(o.path...))
}

// checkUnique reports values that must be unique but are used more than
// once: the guest IDs of virtual machines and templates within a cluster,
// the IPv4 addresses of network interfaces within a network, ignoring the
// prefix length, and their MAC addresses within a cluster, ignoring case.
// It also reports interface addresses that an endpoint of the guest's
// cluster uses. A value is reported at every occurrence after the first,
// and the message points to the first.
func checkUnique(docs []*nrm.Document, diags *diag.List) {
	type (
		clusterID struct {
			cluster string
			id      int
		}
		networkAddr struct {
			network string
			addr    netip.Addr
		}
		clusterMAC struct {
			cluster, mac string
		}
	)
	ids := map[clusterID]occurrence{}
	addrs := map[networkAddr]occurrence{}
	macs := map[clusterMAC]occurrence{}
	endpoints := endpointAddresses(docs)
	for _, d := range docs {
		if d.APIVersion != APIVersion {
			continue
		}
		var (
			cluster string
			vmid    int
			nics    []NIC
		)
		switch d.Kind {
		case KindTemplate:
			t, err := Decode[TemplateSpec](d)
			if err != nil {
				continue
			}
			cluster, vmid = t.Spec.Cluster, t.Spec.Identity.VMID
		case KindVirtualMachine:
			vm, err := Decode[VirtualMachineSpec](d)
			if err != nil {
				continue
			}
			cluster, vmid, nics = vm.Spec.Placement.Cluster, vm.Spec.Identity.VMID, vm.Spec.NICs
		default:
			continue
		}
		c := checker{d: d, diags: diags}
		if vmid != 0 {
			p := fieldPath("spec", "identity", "vmid")
			key := clusterID{cluster, vmid}
			if first, dup := ids[key]; dup {
				c.errorf(p, "guest ID %d is used twice in cluster %q; it is also used by %s", vmid, cluster, first)
			} else {
				ids[key] = occurrence{d, p}
			}
		}
		for i, nic := range nics {
			if nic.MAC != "" {
				p := fieldPath("spec", "nics", i, "mac")
				key := clusterMAC{cluster, strings.ToUpper(nic.MAC)}
				if first, dup := macs[key]; dup {
					c.errorf(p, "MAC address %s is used twice in cluster %q; it is also used by %s", nic.MAC, cluster, first)
				} else {
					macs[key] = occurrence{d, p}
				}
			}
			if nic.IPv4 == nil {
				continue
			}
			prefix, err := netip.ParsePrefix(nic.IPv4.Address)
			if err != nil {
				// No address, or an invalid one, which checkVirtualMachine
				// reports.
				continue
			}
			addr := prefix.Addr()
			p := fieldPath("spec", "nics", i, "ipv4", "address")
			key := networkAddr{nic.Network, addr}
			if first, dup := addrs[key]; dup {
				c.errorf(p, "address %s is used twice in network %q; it is also used by %s", addr, nic.Network, first)
			} else {
				addrs[key] = occurrence{d, p}
			}
			if endpoint, used := endpoints[cluster][addr]; used {
				c.errorf(p, "address %s is used twice; it is also used by an endpoint of %s", addr, endpoint)
			}
		}
	}
}

// endpointAddresses returns the IP addresses of the endpoints of each
// cluster, by cluster name, with the first endpoint that uses each.
// Endpoints given by host name are left out.
func endpointAddresses(docs []*nrm.Document) map[string]map[netip.Addr]occurrence {
	out := map[string]map[netip.Addr]occurrence{}
	for _, d := range docs {
		if d.APIVersion != APIVersion || d.Kind != KindProxmoxCluster {
			continue
		}
		c, err := Decode[ProxmoxClusterSpec](d)
		if err != nil {
			continue
		}
		if _, dup := out[c.Metadata.Name]; dup {
			// The cluster is defined twice, which Validate reports.
			continue
		}
		addrs := map[netip.Addr]occurrence{}
		for i, e := range c.Spec.Endpoints {
			u, err := url.Parse(e)
			if err != nil {
				continue
			}
			addr, err := netip.ParseAddr(u.Hostname())
			if err != nil {
				continue
			}
			if _, dup := addrs[addr]; !dup {
				addrs[addr] = occurrence{d, fieldPath("spec", "endpoints", i)}
			}
		}
		out[c.Metadata.Name] = addrs
	}
	return out
}

// addrRange is an inclusive range of IPv4 addresses.
type addrRange struct {
	first, last netip.Addr
}

func parseRange(s string) (addrRange, error) {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return addrRange{}, fmt.Errorf("invalid range %q: want first-last", s)
	}
	first, err1 := netip.ParseAddr(a)
	last, err2 := netip.ParseAddr(b)
	if err1 != nil || err2 != nil {
		return addrRange{}, fmt.Errorf("invalid range %q", s)
	}
	if last.Less(first) {
		return addrRange{}, fmt.Errorf("invalid range %q: the first address is after the last", s)
	}
	return addrRange{first: first, last: last}, nil
}

func (r addrRange) contains(a netip.Addr) bool {
	return !a.Less(r.first) && !r.last.Less(a)
}

func (r addrRange) overlaps(o addrRange) bool {
	return !r.last.Less(o.first) && !o.last.Less(r.first)
}

func (r addrRange) String() string { return r.first.String() + "-" + r.last.String() }
