package v1alpha1

import (
	"fmt"
	"net/netip"
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
