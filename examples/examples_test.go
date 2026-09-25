// Package examples checks the example workspaces: they must be valid, and
// their engine code must agree with their intent, so that nodr would change
// neither when it syncs them.
package examples

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/centopw/nodr/internal/lens"
	"github.com/centopw/nodr/internal/lens/proxmoxvm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/resolve"
	"github.com/centopw/nodr/internal/workspace"
)

func TestHomelab(t *testing.T) {
	ws, diags := workspace.Load("homelab")
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ws.Validate(reg) {
		t.Errorf("%s", d)
	}

	vmLens := proxmoxvm.Lens{Resolver: resolve.NewIndex(ws.Documents)}
	reports := map[string]lens.Report{}
	for _, d := range ws.OfKind(v1alpha1.KindVirtualMachine) {
		vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](d)
		if err != nil {
			t.Fatal(err)
		}
		name := vm.Metadata.Name
		file, src, err := proxmoxvm.FindManaged(ws.FS, "terraform", name)
		if err != nil {
			t.Fatalf("vm/%s: %v", name, err)
		}
		if !bytes.Equal(hclwrite.Format(src), src) {
			t.Errorf("%s is not formatted; run tofu fmt", file)
		}
		// Intent asks for no change in code...
		out, err := vmLens.Put(vm, src, file)
		if err != nil {
			t.Fatalf("vm/%s: %v", name, err)
		}
		if !bytes.Equal(out, src) {
			t.Errorf("vm/%s: the intent differs from %s, which would become:\n%s", name, file, out)
		}
		// ...and code asks for no change in intent.
		lifted, report, err := vmLens.Lift(vm, src, file)
		if err != nil {
			t.Fatalf("vm/%s: %v", name, err)
		}
		if lifted.Metadata.Name != name || !reflect.DeepEqual(lifted.Spec, vm.Spec) {
			t.Errorf("vm/%s: %s differs from the intent in %s", name, file, d.File)
		}
		reports[name] = report
	}

	// web-01 carries the customizations of the design's worked example
	// (docs/design/04-dual-mode-and-sync.md, section 4.9).
	web := reports["web-01"]
	for path, want := range map[string]lens.Owner{
		"spec.resources.cpu.cores":   lens.CodeOwned,
		"smbios":                     lens.Extension,
		"spec.resources.memory.size": lens.Synced,
	} {
		if f, ok := web.Lookup(path); !ok || f.Owner != want {
			t.Errorf("web-01: %s is %v, want %v", path, f.Owner, want)
		}
	}
	if n := reports["dns-01"].Count(lens.Synced); n == 0 || n != len(reports["dns-01"].Fields) {
		t.Errorf("dns-01: %d of %d fields synced, want all", n, len(reports["dns-01"].Fields))
	}
}
