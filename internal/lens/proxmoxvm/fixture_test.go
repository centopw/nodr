package proxmoxvm

import (
	"os"
	"testing"

	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/resolve"
)

// fixture loads the test workspace, checks that it is valid, and returns
// the lens and the web-01 VM.
func fixture(t testing.TB) (Lens, *v1alpha1.VirtualMachine) {
	t.Helper()
	data, err := os.ReadFile("testdata/workspace.yaml")
	if err != nil {
		t.Fatal(err)
	}
	docs, diags := nrm.Parse("workspace.yaml", data)
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if diags := reg.Validate(docs); diags.HasErrors() {
		t.Fatalf("the fixture is invalid: %v", diags.Err())
	}
	var vm *v1alpha1.VirtualMachine
	for _, d := range docs {
		if d.Kind == v1alpha1.KindVirtualMachine {
			vm, err = v1alpha1.Decode[v1alpha1.VirtualMachineSpec](d)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return Lens{Resolver: resolve.NewIndex(docs)}, vm
}
