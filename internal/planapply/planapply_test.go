package planapply

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/centopw/nodr/internal/engine/opentofu"
	"github.com/centopw/nodr/internal/workspace"
)

const fakeTofuScript = `#!/bin/sh
unit=$(pwd -P)
unit=${unit##*/}
echo "$unit $*" >> "$FAKE_TOFU_LOG"
for arg in "$@"; do last=$arg; done
if [ "$FAKE_TOFU_FAIL" = "$unit $1" ]; then
	echo "Error: $1 failed in $unit" >&2
	exit 1
fi
case $1 in
init)
	echo "initialized $unit"
	;;
plan)
	for arg in "$@"; do
		case $arg in -out=*) plan=${arg#-out=} ;; esac
	done
	echo "plan of $unit" > "$plan"
	echo "planned $unit"
	;;
show | apply)
	if [ "$(cat "$last")" != "plan of $unit" ]; then
		echo "Error: $last is not a saved plan of $unit" >&2
		exit 1
	fi
	if [ "$1" = apply ]; then
		echo "applied $unit"
	elif [ -f "$FAKE_TOFU_PLANS/$unit.json" ]; then
		cat "$FAKE_TOFU_PLANS/$unit.json"
	else
		echo '{"format_version": "1.2", "resource_changes": []}'
	fi
	;;
*)
	echo "Error: unexpected command $1" >&2
	exit 1
	;;
esac
`

type fakeTofu struct {
	log, plans string
}

func installFakeTofu(t *testing.T) *fakeTofu {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tofu is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "tofu")
	if err := os.WriteFile(bin, []byte(fakeTofuScript), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeTofu{log: filepath.Join(dir, "calls.log"), plans: filepath.Join(dir, "plans")}
	if err := os.Mkdir(f.plans, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODR_TOFU", bin)
	t.Setenv("FAKE_TOFU_LOG", f.log)
	t.Setenv("FAKE_TOFU_PLANS", f.plans)
	t.Setenv("FAKE_TOFU_FAIL", "")
	return f
}

func (f *fakeTofu) setPlan(t *testing.T, unit string, changes ...string) {
	t.Helper()
	type resourceChange struct {
		Address string `json:"address"`
		Type    string `json:"type"`
		Change  struct {
			Actions []string `json:"actions"`
		} `json:"change"`
	}
	plan := struct {
		FormatVersion   string           `json:"format_version"`
		ResourceChanges []resourceChange `json:"resource_changes"`
	}{FormatVersion: "1.2", ResourceChanges: []resourceChange{}}
	for _, c := range changes {
		fields := strings.Fields(c)
		rc := resourceChange{Address: fields[1]}
		rc.Type, _, _ = strings.Cut(rc.Address, ".")
		rc.Change.Actions = strings.Split(fields[0], ",")
		plan.ResourceChanges = append(plan.ResourceChanges, rc)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.plans, unit+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

const manifest = `apiVersion: nodr/v1alpha1
kind: Workspace
metadata: { name: test }
backend: { local: {} }
`

const platformYAML = `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [https://10.0.10.11:8006]
  credentialsRef: proxmox/pve-main-token
  nodes: [pve1]
`

const computeYAML = `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-01
spec:
  placement: { cluster: pve-main, assignedNode: pve1 }
  identity: { vmid: 1000 }
  resources: { cpu: { cores: 2 }, memory: { size: 2Gi } }
`

func writeWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func loadWorkspace(t *testing.T, root string) *workspace.Workspace {
	t.Helper()
	ws, diags := workspace.Load(root)
	if diags.HasErrors() {
		t.Fatalf("load workspace: %v", diags)
	}
	return ws
}

func TestStateUnitsAndSelectUnits(t *testing.T) {
	fsys := fstest.MapFS{
		"terraform/compute/main.tf":    &fstest.MapFile{Data: []byte("resource \"a\" \"b\" {}")},
		"terraform/network/main.tf":    &fstest.MapFile{Data: []byte("resource \"c\" \"d\" {}")},
		"terraform/modules/vm/main.tf": &fstest.MapFile{Data: []byte("variable \"x\" {}")},
		"terraform/notes/README.md":    &fstest.MapFile{Data: []byte("# info")},
		"terraform/.hidden/main.tf":    &fstest.MapFile{Data: []byte("resource \"e\" \"f\" {}")},
	}
	pending := map[string][]byte{
		"terraform/storage/pools.tf": []byte("resource \"g\" \"h\" {}"),
	}
	units, err := StateUnits(fsys, pending)
	if err != nil {
		t.Fatalf("StateUnits: %v", err)
	}
	want := []string{"terraform/compute", "terraform/network", "terraform/storage"}
	if !slices.Equal(units, want) {
		t.Errorf("units = %v, want %v", units, want)
	}

	selected, err := SelectUnits(units, []string{"terraform/network", "terraform/compute/"})
	if err != nil {
		t.Fatalf("SelectUnits: %v", err)
	}
	wantSelected := []string{"terraform/compute", "terraform/network"}
	if !slices.Equal(selected, wantSelected) {
		t.Errorf("selected = %v, want %v", selected, wantSelected)
	}

	_, err = SelectUnits(units, []string{"terraform/unknown"})
	if err == nil {
		t.Fatalf("expected error for unknown unit")
	}
}

func TestPlanUnitsSuccess(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, map[string]string{
		"nodr.yaml":                manifest,
		"intent/platform/pve.yaml": platformYAML,
		"intent/compute/web.yaml":  computeYAML,
	})
	ws := loadWorkspace(t, root)
	tofu.setPlan(t, "pve-main-compute", "create proxmox_virtual_environment_vm.web_01")

	planDir := t.TempDir()
	var out strings.Builder
	written, plans, err := PlanUnits(context.Background(), ws, nil, planDir, &out, nil)
	if err != nil {
		t.Fatalf("PlanUnits failed: %v", err)
	}
	if len(written) == 0 {
		t.Errorf("expected written files, got 0")
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}
	if plans[0].Dir != "terraform/pve-main-compute" {
		t.Errorf("plans[0].Dir = %q, want terraform/pve-main-compute", plans[0].Dir)
	}
	if !plans[0].Plan.HasChanges() {
		t.Errorf("expected changes in plan")
	}
}

func TestPlanUnitsLookPathError(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"nodr.yaml":                manifest,
		"intent/platform/pve.yaml": platformYAML,
		"intent/compute/web.yaml":  computeYAML,
	})
	ws := loadWorkspace(t, root)
	t.Setenv("NODR_TOFU", "")
	t.Setenv("PATH", t.TempDir())

	planDir := t.TempDir()
	_, _, err := PlanUnits(context.Background(), ws, nil, planDir, nil, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, opentofu.ErrNotFound) {
		t.Errorf("expected opentofu.ErrNotFound, got %v", err)
	}
}

func TestPlanUnitsCompileError(t *testing.T) {
	installFakeTofu(t)
	// Missing assignedNode -> compile failure
	badCompute := strings.Replace(computeYAML, ", assignedNode: pve1", "", 1)
	root := writeWorkspace(t, map[string]string{
		"nodr.yaml":                manifest,
		"intent/platform/pve.yaml": platformYAML,
		"intent/compute/web.yaml":  badCompute,
	})
	ws := loadWorkspace(t, root)
	planDir := t.TempDir()
	_, _, err := PlanUnits(context.Background(), ws, nil, planDir, nil, nil)
	if err == nil {
		t.Fatalf("expected compile error, got nil")
	}
	var compErr *CompileError
	if !errors.As(err, &compErr) {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if !compErr.Diagnostics.HasErrors() {
		t.Errorf("expected diagnostics with errors")
	}
}

func TestPlanUnitsCommandFails(t *testing.T) {
	installFakeTofu(t)
	root := writeWorkspace(t, map[string]string{
		"nodr.yaml":                manifest,
		"intent/platform/pve.yaml": platformYAML,
		"intent/compute/web.yaml":  computeYAML,
	})
	ws := loadWorkspace(t, root)
	t.Setenv("FAKE_TOFU_FAIL", "pve-main-compute init")

	planDir := t.TempDir()
	_, _, err := PlanUnits(context.Background(), ws, nil, planDir, nil, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if unit := UnitDir(err); unit != "terraform/pve-main-compute" {
		t.Errorf("UnitDir(err) = %q, want terraform/pve-main-compute", unit)
	}
	var cmdErr *opentofu.CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected *opentofu.CommandError, got %T: %v", err, err)
	}
	if !strings.Contains(cmdErr.Stderr, "Error: init failed in pve-main-compute") {
		t.Errorf("expected stderr to contain failure message, got %q", cmdErr.Stderr)
	}
}

func TestApplySuccessAndFailure(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, map[string]string{
		"nodr.yaml":                 manifest,
		"intent/platform/pve.yaml":  platformYAML,
		"intent/compute/web.yaml":   computeYAML,
		"terraform/network/main.tf": "resource \"terraform_data\" \"v\" {}\n",
	})
	ws := loadWorkspace(t, root)
	tofu.setPlan(t, "network", "update terraform_data.v")
	tofu.setPlan(t, "pve-main-compute", "create proxmox_virtual_environment_vm.web_01")

	planDir := t.TempDir()
	_, plans, err := PlanUnits(context.Background(), ws, nil, planDir, nil, nil)
	if err != nil {
		t.Fatalf("PlanUnits: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans, got %d", len(plans))
	}

	// Successful apply
	applied, err := Apply(context.Background(), plans, nil, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	wantApplied := []string{"terraform/network", "terraform/pve-main-compute"}
	if !slices.Equal(applied, wantApplied) {
		t.Errorf("applied = %v, want %v", applied, wantApplied)
	}

	// Failure on second unit
	t.Setenv("FAKE_TOFU_FAIL", "pve-main-compute apply")
	applied, err = Apply(context.Background(), plans, nil, nil)
	if err == nil {
		t.Fatalf("expected error from Apply")
	}
	if !slices.Equal(applied, []string{"terraform/network"}) {
		t.Errorf("applied = %v, want [terraform/network]", applied)
	}
	if unit := UnitDir(err); unit != "terraform/pve-main-compute" {
		t.Errorf("UnitDir(err) = %q, want terraform/pve-main-compute", unit)
	}
}

func TestDestructiveChanges(t *testing.T) {
	plans := []UnitPlan{
		{
			Dir: "terraform/compute",
			Plan: opentofu.Plan{
				Changes: []opentofu.Change{
					{Address: "vm.one", Action: opentofu.Create},
					{Address: "vm.two", Action: opentofu.Replace},
					{Address: "vm.three", Action: opentofu.Delete},
					{Address: "vm.four", Action: opentofu.Update},
				},
			},
		},
	}
	destructive := DestructiveChanges(plans)
	want := []string{
		"terraform/compute: replace vm.two",
		"terraform/compute: destroy vm.three",
	}
	if !slices.Equal(destructive, want) {
		t.Errorf("DestructiveChanges = %v, want %v", destructive, want)
	}
}
