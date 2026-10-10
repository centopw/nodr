package sync

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/lens/proxmoxvm"
	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/quantity"
	"github.com/centopw/nodr/internal/resolve"
)

// fixture loads the shared lens test workspace and returns the lens and
// the web-01 VM, exactly as internal/lens/proxmoxvm's own fixture does.
func fixture(t testing.TB) (proxmoxvm.Lens, *v1alpha1.VirtualMachine) {
	t.Helper()
	data, err := os.ReadFile("../lens/proxmoxvm/testdata/workspace.yaml")
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
	return proxmoxvm.Lens{Resolver: resolve.NewIndex(docs)}, vm
}

// address returns the HCL resource address of vm's managed block.
func address(vm *v1alpha1.VirtualMachine) string {
	return proxmoxvm.ResourceType + "." + proxmoxvm.Address(vm.Metadata.Name)
}

// lockFor returns a render.lock whose only entry matches vm's rendered
// block in file.
func lockFor(vm *v1alpha1.VirtualMachine, file string) Lock {
	return Lock{Blocks: []Block{{
		UID:      vm.Metadata.UID,
		Ref:      vmRef(vm.Metadata.Name),
		Aspect:   proxmoxvm.Aspect,
		Engine:   proxmoxvm.Engine,
		File:     file,
		Address:  address(vm),
		Renderer: RendererID,
	}}}
}

func assertZeroOutput(t testing.TB, out Output) {
	t.Helper()
	if out.IntentSrc != nil || out.CodeSrc != nil || len(out.Lock.Blocks) != 0 {
		t.Fatalf("Output is not zero value on error: %+v", out)
	}
}

func TestSynchronize_LockMismatch(t *testing.T) {
	l, vm := fixture(t)
	code, err := l.Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := os.ReadFile("../lens/proxmoxvm/testdata/workspace.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		lock Lock
	}{
		{"no entry for uid", Lock{}},
		{"wrong file", lockFor(vm, "other.tf")},
		{"wrong address", Lock{Blocks: []Block{{
			UID: vm.Metadata.UID, Aspect: proxmoxvm.Aspect, Engine: proxmoxvm.Engine,
			File: "vms.tf", Address: "proxmox_virtual_environment_vm.wrong",
		}}}},
		{"wrong aspect", Lock{Blocks: []Block{{
			UID: vm.Metadata.UID, Aspect: "other", Engine: proxmoxvm.Engine,
			File: "vms.tf", Address: address(vm),
		}}}},
		{"wrong engine", Lock{Blocks: []Block{{
			UID: vm.Metadata.UID, Aspect: proxmoxvm.Aspect, Engine: "other",
			File: "vms.tf", Address: address(vm),
		}}}},
		{"duplicate uid", Lock{Blocks: []Block{
			{
				UID: vm.Metadata.UID, Aspect: proxmoxvm.Aspect, Engine: proxmoxvm.Engine,
				File: "vms.tf", Address: address(vm),
			},
			{
				UID: vm.Metadata.UID, Aspect: proxmoxvm.Aspect, Engine: proxmoxvm.Engine,
				File: "other.tf", Address: address(vm),
			},
		}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := Synchronize(Input{
				Lens: l, Prior: vm, Desired: vm,
				IntentSrc: intent, CodeSrc: code, CodeFile: "vms.tf",
				Lock: c.lock,
			})
			if !errors.Is(err, ErrRenderLockMismatch) {
				t.Fatalf("err = %v, want ErrRenderLockMismatch", err)
			}
			assertZeroOutput(t, out)
		})
	}
}

func TestSynchronize_SyncedFieldEdit(t *testing.T) {
	l, vm := fixture(t)
	code, err := l.Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := os.ReadFile("../lens/proxmoxvm/testdata/workspace.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lock := lockFor(vm, "vms.tf")

	desired := &v1alpha1.VirtualMachine{Metadata: vm.Metadata, Spec: vm.Spec.DeepCopy(), Document: vm.Document}
	desired.Spec.Resources.Memory.Size = quantity.MustParse("8Gi")

	out, err := Synchronize(Input{
		Lens: l, Prior: vm, Desired: desired,
		IntentSrc: intent, CodeSrc: code, CodeFile: "vms.tf",
		Lock: lock,
	})
	if err != nil {
		t.Fatalf("Synchronize: %v", err)
	}
	if !strings.Contains(string(out.CodeSrc), "dedicated = 8192") {
		t.Errorf("CodeSrc did not get the new memory literal:\n%s", out.CodeSrc)
	}
	if !strings.Contains(string(out.IntentSrc), "size: 8Gi") {
		t.Errorf("IntentSrc did not get the new memory literal:\n%s", out.IntentSrc)
	}
	if _, ok := out.Lock.ByUID(vm.Metadata.UID); !ok {
		t.Errorf("Lock has no entry for the VM after sync")
	}
	// The rest of the intent document is untouched.
	if !strings.Contains(string(out.IntentSrc), "cores: 2") {
		t.Errorf("IntentSrc lost an unrelated field:\n%s", out.IntentSrc)
	}
}

func TestSynchronize_CodeOwnedFieldRejected(t *testing.T) {
	l, vm := fixture(t)
	code, err := l.Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	code = []byte(strings.Replace(string(code), "cores   = 2", "cores   = var.web_cores", 1))
	intent, err := os.ReadFile("../lens/proxmoxvm/testdata/workspace.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lock := lockFor(vm, "vms.tf")

	desired := &v1alpha1.VirtualMachine{Metadata: vm.Metadata, Spec: vm.Spec.DeepCopy(), Document: vm.Document}
	desired.Spec.Resources.CPU.Cores = 6

	out, err := Synchronize(Input{
		Lens: l, Prior: vm, Desired: desired,
		IntentSrc: intent, CodeSrc: code, CodeFile: "vms.tf",
		Lock: lock,
	})
	if !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("err = %v, want ErrOwnershipConflict", err)
	}
	assertZeroOutput(t, out)
}

// TestSynchronize_CodeExpressionBecomesCodeOwned covers conflict-table
// scenario 3: a developer can replace a synced literal with an expression.
// Synchronize accepts that code drift, keeps the prior intent value, and
// leaves the expression untouched; only a later GUI edit is a conflict.
func TestSynchronize_CodeExpressionBecomesCodeOwned(t *testing.T) {
	l, vm := fixture(t)
	code, err := l.Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	code = []byte(strings.Replace(string(code), "cores   = 2", "cores   = var.web_cores", 1))
	intent, err := os.ReadFile("../lens/proxmoxvm/testdata/workspace.yaml")
	if err != nil {
		t.Fatal(err)
	}

	out, err := Synchronize(Input{
		Lens: l, Prior: vm, Desired: vm,
		IntentSrc: intent, CodeSrc: code, CodeFile: "vms.tf",
		Lock: lockFor(vm, "vms.tf"),
	})
	if err != nil {
		t.Fatalf("Synchronize: %v", err)
	}
	if string(out.CodeSrc) != string(code) {
		t.Errorf("CodeSrc changed a code-owned expression:\n%s", out.CodeSrc)
	}
	if string(out.IntentSrc) != string(intent) {
		t.Errorf("IntentSrc changed without a synced code drift:\n%s", out.IntentSrc)
	}
}

func TestSynchronize_CodeDriftPropagatesToIntent(t *testing.T) {
	l, vm := fixture(t)
	code, err := l.Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	code = []byte(strings.Replace(string(code), "started       = true", "started       = false", 1))
	intent, err := os.ReadFile("../lens/proxmoxvm/testdata/workspace.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lock := lockFor(vm, "vms.tf")

	out, err := Synchronize(Input{
		Lens: l, Prior: vm, Desired: vm,
		IntentSrc: intent, CodeSrc: code, CodeFile: "vms.tf",
		Lock: lock,
	})
	if err != nil {
		t.Fatalf("Synchronize: %v", err)
	}
	if !strings.Contains(string(out.IntentSrc), "powerState: stopped") {
		t.Errorf("IntentSrc did not pick up the code's power state:\n%s", out.IntentSrc)
	}
}

func TestSynchronize_UnmarkedBlockUntouched(t *testing.T) {
	l, vm := fixture(t)
	code, err := l.Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	extra := "\n  # The vendor license is bound to this serial number\n  smbios {\n    serial = \"LIC-2231-7781\"\n  }\n"
	code = []byte(strings.Replace(string(code), "  clone {", extra+"\n  clone {", 1))
	intent, err := os.ReadFile("../lens/proxmoxvm/testdata/workspace.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lock := lockFor(vm, "vms.tf")

	out, err := Synchronize(Input{
		Lens: l, Prior: vm, Desired: vm,
		IntentSrc: intent, CodeSrc: code, CodeFile: "vms.tf",
		Lock: lock,
	})
	if err != nil {
		t.Fatalf("Synchronize: %v", err)
	}
	if !strings.Contains(string(out.CodeSrc), "smbios {") {
		t.Errorf("CodeSrc lost the unmanaged smbios block:\n%s", out.CodeSrc)
	}
	if !strings.Contains(string(out.CodeSrc), "LIC-2231-7781") {
		t.Errorf("CodeSrc lost the unmanaged block's content:\n%s", out.CodeSrc)
	}
}

func TestSynchronize_UnprovenancedSiblingResourceUntouched(t *testing.T) {
	l, vm := fixture(t)
	code, err := l.Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	sibling := "\nresource \"terraform_data\" \"user_owned\" {\n  input = \"do not change\"\n}\n"
	code = append(code, sibling...)
	intent, err := os.ReadFile("../lens/proxmoxvm/testdata/workspace.yaml")
	if err != nil {
		t.Fatal(err)
	}

	out, err := Synchronize(Input{
		Lens: l, Prior: vm, Desired: vm,
		IntentSrc: intent, CodeSrc: code, CodeFile: "vms.tf",
		Lock: lockFor(vm, "vms.tf"),
	})
	if err != nil {
		t.Fatalf("Synchronize: %v", err)
	}
	if !strings.Contains(string(out.CodeSrc), sibling) {
		t.Errorf("CodeSrc lost the unprovenanced sibling resource:\n%s", out.CodeSrc)
	}
}

// fakeLens.Put returns code unchanged, so a real caller change never makes
// it into code: the round-trip lift reads back the old, unwritten value,
// which must fail as ErrRoundTrip rather than being silently downgraded to
// code-owned.
type fakeLens struct{ proxmoxvm.Lens }

func (f fakeLens) Put(_ *v1alpha1.VirtualMachine, src []byte, _ string) ([]byte, error) {
	return src, nil
}

// ownershipFlipLens replaces the requested literal with an expression. The
// second Lift therefore reports the field code-owned; Synchronize must
// still reject this lost write as ErrRoundTrip.
type ownershipFlipLens struct{ proxmoxvm.Lens }

func (f ownershipFlipLens) Put(_ *v1alpha1.VirtualMachine, src []byte, _ string) ([]byte, error) {
	return []byte(strings.Replace(string(src), "dedicated = 4096", "dedicated = var.web_memory", 1)), nil
}

func TestSynchronize_RoundTripOwnershipFlipAborts(t *testing.T) {
	l, vm := fixture(t)
	code, err := l.Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := os.ReadFile("../lens/proxmoxvm/testdata/workspace.yaml")
	if err != nil {
		t.Fatal(err)
	}
	desired := &v1alpha1.VirtualMachine{Metadata: vm.Metadata, Spec: vm.Spec.DeepCopy(), Document: vm.Document}
	desired.Spec.Resources.Memory.Size = quantity.MustParse("8Gi")

	out, err := Synchronize(Input{
		Lens: ownershipFlipLens{l}, Prior: vm, Desired: desired,
		IntentSrc: intent, CodeSrc: code, CodeFile: "vms.tf",
		Lock: lockFor(vm, "vms.tf"),
	})
	if !errors.Is(err, ErrRoundTrip) {
		t.Fatalf("err = %v, want ErrRoundTrip", err)
	}
	assertZeroOutput(t, out)
}

func TestSynchronize_RoundTripFailureAborts(t *testing.T) {
	l, vm := fixture(t)
	code, err := l.Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := os.ReadFile("../lens/proxmoxvm/testdata/workspace.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lock := lockFor(vm, "vms.tf")

	desired := &v1alpha1.VirtualMachine{Metadata: vm.Metadata, Spec: vm.Spec.DeepCopy(), Document: vm.Document}
	desired.Spec.Resources.Memory.Size = quantity.MustParse("8Gi")

	out, err := Synchronize(Input{
		Lens: fakeLens{l}, Prior: vm, Desired: desired,
		IntentSrc: intent, CodeSrc: code, CodeFile: "vms.tf",
		Lock: lock,
	})
	if !errors.Is(err, ErrRoundTrip) {
		t.Fatalf("err = %v, want ErrRoundTrip", err)
	}
	assertZeroOutput(t, out)
	if !strings.Contains(string(intent), "size: 4Gi") {
		t.Fatalf("test setup: IntentSrc already changed")
	}
}
