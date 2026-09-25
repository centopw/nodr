package proxmoxvm

import (
	"errors"
	"flag"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/lens"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/quantity"
)

var update = flag.Bool("update", false, "rewrite golden files")

func render(t *testing.T, l Lens, vm *v1alpha1.VirtualMachine) string {
	t.Helper()
	out, err := l.Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestRenderGolden(t *testing.T) {
	l, vm := fixture(t)
	got := render(t, l, vm)
	if *update {
		if err := os.WriteFile("testdata/web-01.tf", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/web-01.tf")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("Render =\n%s\nwant (testdata/web-01.tf)\n%s", got, want)
	}
}

func TestLiftOfRenderedCodeChangesNothing(t *testing.T) {
	l, vm := fixture(t)
	got, report, err := l.Lift(vm, []byte(render(t, l, vm)), "vms.tf")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Spec, vm.Spec) || !reflect.DeepEqual(got.Metadata, vm.Metadata) {
		t.Errorf("lift(render(vm)) changed the VM:\n got %+v\nwant %+v", got.Spec, vm.Spec)
	}
	for _, f := range report.Fields {
		if f.Owner != lens.Synced {
			t.Errorf("%s is %s (%s) in rendered code", f.Path, f.Owner, f.Reason)
		}
	}
	for _, path := range []string{"spec.resources.cpu.cores", "spec.resources.memory.size", "spec.nics[0].network", "spec.guest.cloudInit.authorizedKeys"} {
		if f, ok := report.Lookup(path); !ok || f.Line == 0 {
			t.Errorf("report has no line for %s: %+v", path, f)
		}
	}
}

// TestWorkedExample follows the worked example of the design
// (docs/design/04-dual-mode-and-sync.md, section 4.9).
func TestWorkedExample(t *testing.T) {
	l, vm := fixture(t)
	code := render(t, l, vm)

	// Step 2: memory changed in the GUI. Only the one literal changes.
	vm.Spec.Resources.Memory.Size = quantity.MustParse("8Gi")
	out, err := l.Put(vm, []byte(code), "vms.tf")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(code, "dedicated = 4096", "dedicated = 8192", 1); string(out) != want {
		t.Fatalf("step 2: Put =\n%s", out)
	}
	code = string(out)

	// Step 3: a developer disables ballooning and adds SMBIOS data, which
	// the schema does not model.
	code = strings.Replace(code, "floating  = 2048", "floating  = 0", 1)
	code = strings.Replace(code, "  clone {", "  # The vendor license is bound to this serial number\n  smbios {\n    serial = \"LIC-2231-7781\"\n  }\n\n  clone {", 1)
	vm, report := lift(t, l, vm, code)
	if vm.Spec.Resources.Memory.Minimum != nil {
		t.Errorf("step 3: ballooning should be disabled, minimum = %v", vm.Spec.Resources.Memory.Minimum)
	}
	assertOwner(t, report, "smbios", lens.Extension, "")

	// Step 4: the developer makes the core count configurable.
	code = strings.Replace(code, "cores   = 2", "cores   = var.web_cores", 1)
	vm, report = lift(t, l, vm, code)
	assertOwner(t, report, "spec.resources.cpu.cores", lens.CodeOwned, "set by an expression")
	if vm.Spec.Resources.CPU.Cores != 2 {
		t.Errorf("step 4: a code-owned field keeps its intent value, got %d", vm.Spec.Resources.CPU.Cores)
	}

	// A GUI change to the code-owned field does not touch the expression.
	gui := *vm
	gui.Spec = vm.Spec.DeepCopy()
	gui.Spec.Resources.CPU.Cores = 6
	out, err = l.Put(&gui, []byte(code), "vms.tf")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != code {
		t.Errorf("step 4: Put changed code-owned code:\n%s", out)
	}

	// Step 5: take over. The literal returns, and the field is synced again.
	code = strings.Replace(code, "cores   = var.web_cores", "cores   = 4", 1)
	vm, report = lift(t, l, vm, code)
	assertOwner(t, report, "spec.resources.cpu.cores", lens.Synced, "")
	if vm.Spec.Resources.CPU.Cores != 4 {
		t.Errorf("step 5: cores = %d, want 4", vm.Spec.Resources.CPU.Cores)
	}
	assertOwner(t, report, "smbios", lens.Extension, "")
	if vm.Spec.Resources.Memory.Size.String() != "8Gi" || vm.Spec.Resources.Memory.Minimum != nil {
		t.Errorf("step 5: memory = %s, minimum = %v", vm.Spec.Resources.Memory.Size, vm.Spec.Resources.Memory.Minimum)
	}
}

func TestUnmappableValuesStayInCode(t *testing.T) {
	l, vm := fixture(t)
	code := render(t, l, vm)
	edits := []struct {
		from, to string
		path     string
		reason   string
	}{
		{"vm_id = 9001", "vm_id = 9999", "spec.source.template", `no template with guest ID 9999 on cluster "pve-main": not found`},
		{"full  = true", "full  = true\n    node_name = \"pve3\"", "spec.source.template", "the template debian-12-cloud does not name its node; set spec.node of the template instead"},
		{`gateway = "10.0.20.1"`, `gateway = "10.0.20.254"`, "spec.nics[0].network", `the gateway comes from the network dmz ("10.0.20.1")`},
		{`interface    = "scsi0"`, `interface    = "virtio0"`, "spec.disks[0]", "nodr attaches disks in order as scsi0, scsi1 and so on; this disk has to be scsi0"},
		{`model       = "virtio"`, `model       = "e1000"`, "spec.nics[0]", "nodr uses virtio network devices"},
		{`discard      = "on"`, `discard      = "sometimes"`, "spec.disks[0].options.discard", `discard must be "on" or "ignore"`},
		{`["app-website", "env-prod", "nodr"]`, `["app-website", "env-prod"]`, "spec.proxmox.tags", "nodr sets the tags nodr from labels; change the labels instead"},
	}
	for _, e := range edits {
		t.Run(e.path, func(t *testing.T) {
			edited := strings.Replace(code, e.from, e.to, 1)
			if edited == code {
				t.Fatalf("edit %q did not apply", e.from)
			}
			got, report := lift(t, l, vm, edited)
			assertOwner(t, report, e.path, lens.CodeOwned, e.reason)
			if !reflect.DeepEqual(got.Spec, vm.Spec) {
				t.Errorf("an unmappable value changed intent: %+v", got.Spec)
			}
			// A GUI change elsewhere keeps the unmappable value in code.
			gui := *vm
			gui.Spec = vm.Spec.DeepCopy()
			gui.Spec.Resources.CPU.Cores = 3
			out, err := l.Put(&gui, []byte(edited), "vms.tf")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), e.to) || !strings.Contains(string(out), "cores   = 3") {
				t.Errorf("Put lost the edit or the GUI change:\n%s", out)
			}
		})
	}
}

func TestCodeEditsFlowIntoIntent(t *testing.T) {
	l, vm := fixture(t)
	code := render(t, l, vm)
	code = strings.NewReplacer(
		`["app-website", "env-prod", "nodr"]`, `["frontend", "app-website", "env-prod", "nodr"]`,
		"vlan_id     = 20", "vlan_id     = 30",
		`gateway = "10.0.20.1"`, `gateway = "10.0.30.1"`,
		`address = "10.0.20.21/24"`, `address = "10.0.30.21/24"`,
		"started       = true", "started       = false",
		"vm_id = 9001", "vm_id     = 9002\n    node_name = \"pve1\"",
		`username = "ops"`, `username = "admin"`,
		`node_name     = "pve2"`, `node_name     = "pve3"`,
	).Replace(code)
	got, report := lift(t, l, vm, code)
	for _, f := range report.Fields {
		if f.Owner == lens.CodeOwned {
			t.Errorf("%s is code-owned: %s", f.Path, f.Reason)
		}
	}
	s := got.Spec
	checks := map[string][2]any{
		"extra tags":    {s.Proxmox.Tags, []string{"frontend"}},
		"network":       {s.NICs[0].Network, "iot"},
		"address":       {*s.NICs[0].IPv4, v1alpha1.IPv4{Mode: v1alpha1.IPv4ModeStatic, Address: "10.0.30.21/24"}},
		"power state":   {s.Lifecycle.PowerState, v1alpha1.PowerStateStopped},
		"template":      {s.Source.Template, "ubuntu-24-cloud"},
		"cloud-init":    {s.Guest.CloudInit.User, "admin"},
		"assigned node": {s.Placement.AssignedNode, "pve3"},
	}
	for name, c := range checks {
		if !reflect.DeepEqual(c[0], c[1]) {
			t.Errorf("%s = %#v, want %#v", name, c[0], c[1])
		}
	}
	// Writing the lifted VM back changes nothing.
	out, err := l.Put(got, []byte(code), "vms.tf")
	if err != nil || string(out) != code {
		t.Errorf("Put(lift(code)) changed the code: %v\n%s", err, out)
	}
}

func TestDisksAddedInCode(t *testing.T) {
	l, vm := fixture(t)
	code := render(t, l, vm)
	extra := "  disk {\n    datastore_id = \"local-lvm\"\n    interface    = \"scsi1\"\n    size         = 100\n    discard      = \"ignore\"\n  }\n\n  network_device {"
	withDisk := strings.Replace(code, "  network_device {", extra, 1)
	got, _ := lift(t, l, vm, withDisk)
	if len(got.Spec.Disks) != 2 {
		t.Fatalf("got %d disks, want 2", len(got.Spec.Disks))
	}
	want := v1alpha1.Disk{Name: "disk1", Storage: "local-lvm", Size: quantity.MustParse("100Gi")}
	if !reflect.DeepEqual(got.Spec.Disks[1], want) {
		t.Errorf("new disk = %+v, want %+v", got.Spec.Disks[1], want)
	}

	// A new disk that intent cannot describe stays in code, untouched.
	bad := strings.Replace(withDisk, `interface    = "scsi1"`, `interface    = "virtio1"`, 1)
	got, report := lift(t, l, vm, bad)
	if len(got.Spec.Disks) != 1 {
		t.Errorf("an unmappable disk was added to intent: %+v", got.Spec.Disks)
	}
	assertOwner(t, report, "spec.disks[1]", lens.CodeOwned, "nodr attaches disks in order as scsi0, scsi1 and so on; this disk has to be scsi1")
	out, err := l.Put(vm, []byte(bad), "vms.tf")
	if err != nil || string(out) != bad {
		t.Errorf("Put touched an unmappable disk: %v\n%s", err, out)
	}
}

func TestInitializationRemovedInCode(t *testing.T) {
	l, vm := fixture(t)
	code := render(t, l, vm)
	start := strings.Index(code, "\n  initialization {")
	end := strings.LastIndex(code, "\n}")
	got, _ := lift(t, l, vm, code[:start]+code[end:])
	if got.Spec.Guest.CloudInit != nil || got.Spec.NICs[0].IPv4 != nil {
		t.Errorf("removing initialization should remove cloud-init settings: %+v %+v", got.Spec.Guest.CloudInit, got.Spec.NICs[0].IPv4)
	}
}

func TestLocate(t *testing.T) {
	l, vm := fixture(t)
	code := render(t, l, vm)
	renamed := strings.Replace(code, `"web_01"`, `"frontend"`, 1)
	tgt, err := Locate([]byte(renamed), "vms.tf", "web-01")
	if err != nil || tgt.Labels[1] != "frontend" {
		t.Errorf("Locate by marker = %v, %v", tgt, err)
	}
	noMarker := strings.Replace(code, "# nodr:managed vm/web-01\n", "", 1)
	if tgt, err := Locate([]byte(noMarker), "vms.tf", "web-01"); err != nil || tgt.Labels[1] != "web_01" {
		t.Errorf("Locate by address = %v, %v", tgt, err)
	}
	if _, err := Locate([]byte(code), "vms.tf", "db-01"); !errors.Is(err, ErrNotManaged) {
		t.Errorf("Locate of another VM: err = %v", err)
	}
}

func TestManagedBlocks(t *testing.T) {
	l, vm := fixture(t)
	src := "# Owned by the team.\nresource \"proxmox_virtual_environment_vm\" \"mine\" {\n}\n\n" + render(t, l, vm)
	blocks, err := ManagedBlocks([]byte(src), "vms.tf")
	if want := []ManagedBlock{{Name: "web-01", Line: 6}}; err != nil || !reflect.DeepEqual(blocks, want) {
		t.Errorf("ManagedBlocks = %+v, %v; want %+v", blocks, err, want)
	}
	if _, err := ManagedBlocks([]byte("resource {"), "vms.tf"); err == nil {
		t.Error("ManagedBlocks of invalid HCL: no error")
	}
}

func TestAddress(t *testing.T) {
	for name, want := range map[string]string{"web-01": "web_01", "db": "db", "01-web": "vm_01_web"} {
		if got := Address(name); got != want {
			t.Errorf("Address(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestNotAdmitted(t *testing.T) {
	l, vm := fixture(t)
	vm.Spec.Identity.VMID = 0
	if _, err := l.Render(vm); !errors.Is(err, ErrNotAdmitted) {
		t.Errorf("Render without a guest ID: err = %v", err)
	}
	_, vm = fixture(t)
	vm.Spec.NICs[0].IPv4.Address = ""
	if _, err := l.Render(vm); !errors.Is(err, ErrNotAdmitted) {
		t.Errorf("Render without an allocated address: err = %v", err)
	}
}

func lift(t *testing.T, l Lens, vm *v1alpha1.VirtualMachine, code string) (*v1alpha1.VirtualMachine, lens.Report) {
	t.Helper()
	got, report, err := l.Lift(vm, []byte(code), "vms.tf")
	if err != nil {
		t.Fatalf("Lift: %v\n%s", err, code)
	}
	return got, report
}

func assertOwner(t *testing.T, r lens.Report, path string, owner lens.Owner, reason string) {
	t.Helper()
	f, ok := r.Lookup(path)
	if !ok {
		t.Errorf("no report row for %s", path)
		return
	}
	if f.Owner != owner || f.Reason != reason {
		t.Errorf("%s = %s (%q), want %s (%q)", path, f.Owner, f.Reason, owner, reason)
	}
}
