package proxmoxvm

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"pgregory.net/rapid"

	"github.com/centopw/nodr/internal/lens"
	"github.com/centopw/nodr/internal/lens/hclmap"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/quantity"
)

// These property tests check the lens laws of design §4.5 for the VM lens,
// end to end through projection, the HCL engine and embedding:
//
//   - creation consistency: lifting rendered code reproduces the VM
//   - stability (GetPut):   put(lift(c), c) = c
//   - fidelity (PutGet):    lift(put(vm, c)) = vm on synced fields

var octet = map[string]int{"lan": 10, "dmz": 20, "iot": 30}

func genVM(t *rapid.T, fixtureVM *v1alpha1.VirtualMachine, label string) *v1alpha1.VirtualMachine {
	draw := func(name string) string { return label + "." + name }
	node := rapid.SampledFrom([]string{"pve1", "pve2", "pve3"}).Draw(t, draw("node"))
	s := v1alpha1.VirtualMachineSpec{
		Placement: v1alpha1.Placement{
			Cluster:      "pve-main",
			Node:         rapid.SampledFrom([]string{"", "auto", node}).Draw(t, draw("pin")),
			AssignedNode: node,
		},
		Identity: v1alpha1.Identity{VMID: rapid.IntRange(100, 99999).Draw(t, draw("vmid"))},
		Source:   v1alpha1.Source{Template: rapid.SampledFrom([]string{"", "debian-12-cloud", "ubuntu-24-cloud"}).Draw(t, draw("template"))},
		Resources: v1alpha1.Resources{
			CPU: v1alpha1.CPU{
				Cores:   rapid.IntRange(1, 16).Draw(t, draw("cores")),
				Sockets: rapid.IntRange(0, 2).Draw(t, draw("sockets")),
				Type:    rapid.SampledFrom([]string{"", "host", "x86-64-v2-AES"}).Draw(t, draw("cpuType")),
			},
		},
	}
	size := rapid.IntRange(256, 65536).Draw(t, draw("memory"))
	s.Resources.Memory.Size = mustUnits(size, quantity.MiB)
	if rapid.Bool().Draw(t, draw("ballooning")) {
		m := mustUnits(rapid.IntRange(128, size).Draw(t, draw("minimum")), quantity.MiB)
		s.Resources.Memory.Minimum = &m
	}
	for i := range rapid.IntRange(0, 3).Draw(t, draw("disks")) {
		s.Disks = append(s.Disks, v1alpha1.Disk{
			Name:    fmt.Sprintf("disk%d", i),
			Storage: rapid.SampledFrom([]string{"local-lvm", "ceph-vm"}).Draw(t, draw("storage")),
			Size:    mustUnits(rapid.IntRange(1, 500).Draw(t, draw("diskSize")), quantity.GiB),
			Options: v1alpha1.DiskOptions{
				Discard:  rapid.Bool().Draw(t, draw("discard")),
				SSD:      rapid.Bool().Draw(t, draw("ssd")),
				IOThread: rapid.Bool().Draw(t, draw("iothread")),
			},
		})
	}
	for range rapid.IntRange(0, 2).Draw(t, draw("nics")) {
		network := rapid.SampledFrom([]string{"lan", "dmz", "iot"}).Draw(t, draw("network"))
		nic := v1alpha1.NIC{Network: network}
		if rapid.Bool().Draw(t, draw("hasMAC")) {
			nic.MAC = fmt.Sprintf("BC:24:11:00:00:%02X", rapid.IntRange(0, 255).Draw(t, draw("mac")))
		}
		switch rapid.IntRange(0, 2).Draw(t, draw("ipv4")) {
		case 1:
			nic.IPv4 = &v1alpha1.IPv4{Mode: v1alpha1.IPv4ModeDHCP}
		case 2:
			nic.IPv4 = &v1alpha1.IPv4{Mode: v1alpha1.IPv4ModeStatic, Address: fmt.Sprintf("10.0.%d.%d/24", octet[network], rapid.IntRange(2, 250).Draw(t, draw("host")))}
		}
		s.NICs = append(s.NICs, nic)
	}
	if rapid.Bool().Draw(t, draw("hasAgent")) {
		s.Guest.Agent = ptr(rapid.Bool().Draw(t, draw("agent")))
	}
	if rapid.Bool().Draw(t, draw("cloudInit")) {
		s.Guest.CloudInit = &v1alpha1.CloudInit{
			User:           rapid.SampledFrom([]string{"", "ops", "admin"}).Draw(t, draw("user")),
			AuthorizedKeys: rapid.SliceOfDistinct(rapid.SampledFrom([]string{"ops-team", "alice"}), func(s string) string { return s }).Draw(t, draw("keys")),
		}
		if len(s.Guest.CloudInit.AuthorizedKeys) == 0 {
			s.Guest.CloudInit.AuthorizedKeys = nil
		}
	}
	s.Lifecycle.PowerState = rapid.SampledFrom([]string{"", "running", "stopped", "unmanaged"}).Draw(t, draw("power"))
	if rapid.Bool().Draw(t, draw("hasProtection")) {
		s.Lifecycle.Protection = ptr(rapid.Bool().Draw(t, draw("protection")))
	}
	if rapid.Bool().Draw(t, draw("hasOnBoot")) {
		s.Lifecycle.StartOnBoot = ptr(rapid.Bool().Draw(t, draw("onBoot")))
	}
	s.Proxmox = v1alpha1.ProxmoxSettings{
		Machine:        rapid.SampledFrom([]string{"", "q35"}).Draw(t, draw("machine")),
		BIOS:           rapid.SampledFrom([]string{"", "ovmf", "seabios"}).Draw(t, draw("bios")),
		SCSIController: rapid.SampledFrom([]string{"", "virtio-scsi-single"}).Draw(t, draw("scsi")),
	}
	if rapid.Bool().Draw(t, draw("extraTag")) {
		s.Proxmox.Tags = []string{"frontend"}
	}
	return &v1alpha1.VirtualMachine{Metadata: copyMetadata(fixtureVM.Metadata), Spec: s, Document: fixtureVM.Document}
}

func mustUnits(n int, unit int64) quantity.Quantity {
	q, err := quantity.FromUnits(int64(n), unit)
	if err != nil {
		panic(err)
	}
	return q
}

// lineEdits are edits a person might make to one line of a managed block.
// Some produce values that intent cannot express.
var lineEdits = []struct {
	match *regexp.Regexp
	to    []string
}{
	{regexp.MustCompile(`^(\s*cores\s*=\s*)\d+$`), []string{"${1}7", "${1}var.cores"}},
	{regexp.MustCompile(`^(\s*dedicated\s*=\s*)\d+$`), []string{"${1}3072"}},
	{regexp.MustCompile(`^(\s*floating\s*=\s*)\d+$`), []string{"${1}0", "${1}1024"}},
	{regexp.MustCompile(`^(\s*size\s*=\s*)\d+$`), []string{"${1}64"}},
	{regexp.MustCompile(`^(\s*(?:on_boot|protection|started|ssd|iothread)\s*=\s*)(true|false)$`), []string{"${1}true", "${1}false", "${1}true # nodr:keep"}},
	{regexp.MustCompile(`^(\s*tags\s*=\s*\[)`), []string{`${1}"extra", `}},
	{regexp.MustCompile(`^(\s*tags\s*=\s*)\[.*\]$`), []string{`${1}["only"]`}},
	{regexp.MustCompile(`^(\s*vlan_id\s*=\s*)\d+$`), []string{"${1}20", "${1}30", "${1}99"}},
	{regexp.MustCompile(`^(\s*vm_id\s*=\s*)900\d$`), []string{"${1}9002", "${1}9999", "${1}9002\n    node_name = \"pve1\""}},
	{regexp.MustCompile(`^(\s*interface\s*=\s*)"scsi(\d)"$`), []string{`${1}"virtio${2}"`}},
	{regexp.MustCompile(`^(\s*model\s*=\s*)"virtio"$`), []string{`${1}"e1000"`}},
	{regexp.MustCompile(`^(\s*gateway\s*=\s*)".*"$`), []string{`${1}"10.0.99.1"`}},
	{regexp.MustCompile(`^(\s*discard\s*=\s*)".*"$`), []string{`${1}"on"`, `${1}"ignore"`, `${1}"maybe"`}},
	{regexp.MustCompile(`^(\s*username\s*=\s*)".*"$`), []string{`${1}"root"`}},
	{regexp.MustCompile(`^(\s*address\s*=\s*)"10\.0\.(\d+)\.\d+/24"$`), []string{`${1}"10.0.${2}.77/24"`, `${1}"dhcp"`}},
	{regexp.MustCompile(`^(    node_name\s*=\s*)".*"$`), []string{`${1}"pve2"`, ""}},
	{regexp.MustCompile(`^(\s*node_name\s*=\s*)".*"$`), []string{`${1}"pve1"`}},
	{regexp.MustCompile(`^\s*(?:machine|bios|scsi_hardware|started|mac_address|floating|vlan_id)\s*=.*$`), []string{""}},
}

// mutateVM applies random edits to rendered code. Edits that would make the
// code unparsable are dropped.
func mutateVM(t *rapid.T, src string) string {
	lines := strings.Split(src, "\n")
	for i := range rapid.IntRange(0, 5).Draw(t, "edits") {
		at := rapid.IntRange(0, len(lines)-1).Draw(t, fmt.Sprintf("line%d", i))
		switch kind := rapid.IntRange(0, 2).Draw(t, fmt.Sprintf("kind%d", i)); kind {
		case 0:
			for _, e := range lineEdits {
				if e.match.MatchString(lines[at]) {
					to := rapid.SampledFrom(e.to).Draw(t, fmt.Sprintf("to%d", i))
					lines[at] = e.match.ReplaceAllString(lines[at], to)
					break
				}
			}
		case 1:
			lines = append(lines[:2], append([]string{`  description = "hand-written"`}, lines[2:]...)...)
		case 2:
			last := len(lines) - 2
			lines = append(lines[:last], append([]string{"", "  smbios {", `    serial = "LIC-1"`, "  }"}, lines[last:]...)...)
		}
	}
	out := strings.Join(lines, "\n")
	if _, diags := hclsyntax.ParseConfig([]byte(out), "vms.tf", hcl.InitialPos); diags.HasErrors() {
		return src
	}
	return out
}

func TestLawCreationConsistency(t *testing.T) {
	l, fixtureVM := fixture(t)
	rapid.Check(t, func(t *rapid.T) {
		vm := genVM(t, fixtureVM, "vm")
		base := genVM(t, fixtureVM, "base")
		code, err := l.Render(vm)
		if err != nil {
			t.Fatal(err)
		}
		got, report, err := l.Lift(base, code, "vms.tf")
		if err != nil {
			t.Fatalf("Lift: %v\n%s", err, code)
		}
		if diff := viewDiff(t, l, got, vm); len(diff) > 0 {
			t.Fatalf("lifting rendered code differs at %v\n%s", diff, code)
		}
		for _, f := range report.Fields {
			if f.Owner != lens.Synced {
				t.Fatalf("%s is %s (%s) in rendered code", f.Path, f.Owner, f.Reason)
			}
		}
	})
}

func TestLawStability(t *testing.T) {
	l, fixtureVM := fixture(t)
	rapid.Check(t, func(t *rapid.T) {
		code, err := l.Render(genVM(t, fixtureVM, "vm"))
		if err != nil {
			t.Fatal(err)
		}
		src := mutateVM(t, string(code))
		got, _, err := l.Lift(genVM(t, fixtureVM, "base"), []byte(src), "vms.tf")
		if err != nil {
			t.Fatalf("Lift: %v\n%s", err, src)
		}
		out, err := l.Put(got, []byte(src), "vms.tf")
		if err != nil {
			t.Fatalf("Put: %v\n%s", err, src)
		}
		if string(out) != src {
			t.Fatalf("put(lift(c), c) != c\ncode:\n%s\nafter put:\n%s", src, out)
		}
	})
}

func TestLawFidelity(t *testing.T) {
	l, fixtureVM := fixture(t)
	rapid.Check(t, func(t *rapid.T) {
		code, err := l.Render(genVM(t, fixtureVM, "vm"))
		if err != nil {
			t.Fatal(err)
		}
		src := mutateVM(t, string(code))
		desired := genVM(t, fixtureVM, "desired")
		out, err := l.Put(desired, []byte(src), "vms.tf")
		var conflict *hclmap.ConflictError
		if errors.As(err, &conflict) {
			return // a block with hand-written content would have to go; nothing changed
		}
		if err != nil {
			t.Fatalf("Put: %v\n%s", err, src)
		}
		got, _, err := l.Lift(desired, out, "vms.tf")
		if err != nil {
			t.Fatalf("Lift after Put: %v\n%s", err, out)
		}
		if diff := viewDiff(t, l, got, desired); len(diff) > 0 {
			t.Fatalf("lift(put(vm, c)) differs at %v\ncode:\n%s\nafter put:\n%s", diff, src, out)
		}
		again, err := l.Put(desired, out, "vms.tf")
		if err != nil || string(again) != string(out) {
			t.Fatalf("Put is not idempotent: %v\nfirst:\n%s\nsecond:\n%s", err, out, again)
		}
	})
}

// viewDiff compares two VMs by what they render to.
func viewDiff(t *rapid.T, l Lens, a, b *v1alpha1.VirtualMachine) []string {
	va, err := project(a, l.Resolver)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	vb, err := project(b, l.Resolver)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	return hclmap.Changed(va, vb)
}
