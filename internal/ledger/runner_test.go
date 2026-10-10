package ledger

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runnerUnit is the state unit the test workspace compiles to.
const runnerUnit = "pve-main-compute"

const runnerFakeTofuScript = `#!/bin/sh
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

// runnerFakeTofu is a fake OpenTofu installed through NODR_TOFU for one test.
type runnerFakeTofu struct {
	plans string
}

// runnerInstallFakeTofu installs the fake tofu script and its environment.
func runnerInstallFakeTofu(t *testing.T) *runnerFakeTofu {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tofu is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "tofu")
	if err := os.WriteFile(bin, []byte(runnerFakeTofuScript), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &runnerFakeTofu{plans: filepath.Join(dir, "plans")}
	if err := os.Mkdir(f.plans, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODR_TOFU", bin)
	t.Setenv("FAKE_TOFU_LOG", filepath.Join(dir, "calls.log"))
	t.Setenv("FAKE_TOFU_PLANS", f.plans)
	t.Setenv("FAKE_TOFU_FAIL", "")
	return f
}

// runnerSetPlan sets the plan the fake shows for the test unit. Each change is
// "<actions> <address>", such as "delete proxmox_virtual_environment_vm.web_01"
// or "delete,create proxmox_virtual_environment_vm.web_01".
func (f *runnerFakeTofu) runnerSetPlan(t *testing.T, changes ...string) {
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
	if err := os.WriteFile(filepath.Join(f.plans, runnerUnit+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

const runnerManifest = `apiVersion: nodr/v1alpha1
kind: Workspace
metadata: { name: homelab }
spec:
  environments:
    prod: { vmidRange: [1012, 1013] }
`

const runnerPlatform = `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [https://10.0.20.11:8006]
  credentialsRef: proxmox/pve-main-token
  nodes: [pve1]
`

const runnerVM = `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-01
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C2D3
  labels: { nodr/environment: prod }
spec:
  placement: { cluster: pve-main, assignedNode: pve1 }
  identity: { vmid: 1012 }
  resources: { cpu: { cores: 2 }, memory: { size: 8Gi } }
`

// runnerWorkspace writes a valid workspace with one VM and returns its root.
func runnerWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"nodr.yaml":                  runnerManifest,
		"intent/platform/pve.yaml":   runnerPlatform,
		"intent/compute/web-01.yaml": runnerVM,
	}
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

func TestLocalRunnerPlanAndApply(t *testing.T) {
	tests := []struct {
		name            string
		change          string
		wantChanges     bool
		wantDestructive bool
	}{
		{name: "create is not destructive", change: "create proxmox_virtual_environment_vm.web_01", wantChanges: true},
		{name: "delete is destructive", change: "delete proxmox_virtual_environment_vm.web_01", wantChanges: true, wantDestructive: true},
		{name: "replace is destructive", change: "delete,create proxmox_virtual_environment_vm.web_01", wantChanges: true, wantDestructive: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tofu := runnerInstallFakeTofu(t)
			tofu.runnerSetPlan(t, tt.change)
			r := &LocalRunner{Dir: t.TempDir()}

			res, err := r.Plan(t.Context(), PlanRequest{OperationID: "op-1", Workspace: "homelab", DraftSHA: "d1", Root: runnerWorkspace(t)}, nil)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if res.HasChanges != tt.wantChanges || res.HasDestructive != tt.wantDestructive {
				t.Fatalf("result = changes %t destructive %t, want %t %t", res.HasChanges, res.HasDestructive, tt.wantChanges, tt.wantDestructive)
			}
			var sum PlanSummary
			if err := json.Unmarshal(res.JSON, &sum); err != nil {
				t.Fatalf("decode result JSON: %v", err)
			}
			if len(sum.Units) != 1 || sum.Units[0].Dir != "terraform/"+runnerUnit {
				t.Fatalf("units = %+v", sum.Units)
			}
			if sum.HasDestructive != tt.wantDestructive || sum.HasChanges != tt.wantChanges {
				t.Fatalf("summary flags = %+v", sum)
			}
			if _, err := os.Stat(filepath.Join(r.Dir, "op-1")); err != nil {
				t.Fatalf("plan directory not written: %v", err)
			}
			if !r.Has("op-1") {
				t.Fatal("Has(op-1) = false after a successful plan")
			}

			if err := r.Apply(t.Context(), ApplyRequest{PlanOperationID: "op-1", AllowDestroy: tt.wantDestructive}); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			r.Discard("op-1")
			if _, err := os.Stat(filepath.Join(r.Dir, "op-1")); !os.IsNotExist(err) {
				t.Fatalf("plan directory after Discard: err = %v, want not exist", err)
			}
			if r.Has("op-1") {
				t.Fatal("Has(op-1) = true after Discard")
			}
		})
	}
}

func TestLocalRunnerApplyDestructiveNeedsApproval(t *testing.T) {
	tofu := runnerInstallFakeTofu(t)
	tofu.runnerSetPlan(t, "delete proxmox_virtual_environment_vm.web_01")
	r := &LocalRunner{Dir: t.TempDir()}
	if _, err := r.Plan(t.Context(), PlanRequest{OperationID: "op-d", Root: runnerWorkspace(t)}, nil); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := r.Apply(t.Context(), ApplyRequest{PlanOperationID: "op-d", AllowDestroy: false}); !errors.Is(err, ErrDestructiveNeedsApproval) {
		t.Fatalf("Apply err = %v, want ErrDestructiveNeedsApproval", err)
	}
	if err := r.Apply(t.Context(), ApplyRequest{PlanOperationID: "op-d", AllowDestroy: true}); err != nil {
		t.Fatalf("Apply with approval: %v", err)
	}
}

func TestLocalRunnerUnknownPlanExpired(t *testing.T) {
	r := &LocalRunner{Dir: t.TempDir()}
	if r.Has("missing") {
		t.Fatal("Has(missing) = true for an unknown plan")
	}
	if err := r.Apply(t.Context(), ApplyRequest{PlanOperationID: "missing"}); !errors.Is(err, ErrPlanExpired) {
		t.Fatalf("Apply err = %v, want ErrPlanExpired", err)
	}
	r.Discard("missing")
}

func TestLocalRunnerPlanFailureRetainsNothing(t *testing.T) {
	tofu := runnerInstallFakeTofu(t)
	tofu.runnerSetPlan(t, "create proxmox_virtual_environment_vm.web_01")
	t.Setenv("FAKE_TOFU_FAIL", runnerUnit+" init")
	r := &LocalRunner{Dir: t.TempDir()}
	if _, err := r.Plan(t.Context(), PlanRequest{OperationID: "op-f", Root: runnerWorkspace(t)}, nil); err == nil {
		t.Fatal("Plan succeeded, want error")
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "op-f")); !os.IsNotExist(err) {
		t.Fatalf("plan directory after failure: err = %v, want not exist", err)
	}
	if r.Has("op-f") {
		t.Fatal("Has(op-f) = true after a failed plan")
	}
	if err := r.Apply(t.Context(), ApplyRequest{PlanOperationID: "op-f"}); !errors.Is(err, ErrPlanExpired) {
		t.Fatalf("Apply err = %v, want ErrPlanExpired", err)
	}
}

func TestLocalRunnerPlanRejectsInvalidWorkspace(t *testing.T) {
	runnerInstallFakeTofu(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "nodr.yaml"), []byte("apiVersion: nodr/v1alpha1\nkind: NotAWorkspace\nmetadata: { name: x }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &LocalRunner{Dir: t.TempDir()}
	if _, err := r.Plan(t.Context(), PlanRequest{OperationID: "op-v", Root: root}, nil); err == nil {
		t.Fatal("Plan succeeded for an invalid workspace, want error")
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "op-v")); !os.IsNotExist(err) {
		t.Fatalf("plan directory after invalid workspace: err = %v, want not exist", err)
	}
}

// runnerCloseRecorder records how often the retained checkout was closed.
type runnerCloseRecorder struct{ closed int }

func (c *runnerCloseRecorder) Close() error {
	c.closed++
	return nil
}

func TestLocalRunnerDiscardClosesCheckoutOnce(t *testing.T) {
	tofu := runnerInstallFakeTofu(t)
	tofu.runnerSetPlan(t, "create proxmox_virtual_environment_vm.web_01")
	rec := &runnerCloseRecorder{}
	r := &LocalRunner{Dir: t.TempDir()}
	if _, err := r.Plan(t.Context(), PlanRequest{OperationID: "op-c", Root: runnerWorkspace(t), Checkout: rec}, nil); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	r.Discard("op-c")
	r.Discard("op-c")
	if rec.closed != 1 {
		t.Fatalf("checkout closed %d times, want 1", rec.closed)
	}
}

func TestPlanSummaryOmitsNoOpAndCounts(t *testing.T) {
	tofu := runnerInstallFakeTofu(t)
	tofu.runnerSetPlan(t,
		"create proxmox_virtual_environment_vm.web_01",
		"delete proxmox_virtual_environment_vm.old_01",
		"no-op proxmox_virtual_environment_vm.kept_01",
	)
	r := &LocalRunner{Dir: t.TempDir()}
	res, err := r.Plan(t.Context(), PlanRequest{OperationID: "op-s", Root: runnerWorkspace(t)}, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var sum PlanSummary
	if err := json.Unmarshal(res.JSON, &sum); err != nil {
		t.Fatal(err)
	}
	if len(sum.Units) != 1 {
		t.Fatalf("units = %d, want 1", len(sum.Units))
	}
	u := sum.Units[0]
	if u.Create != 1 || u.Delete != 1 || u.Replace != 0 {
		t.Fatalf("counts = %+v", u)
	}
	if len(u.Changes) != 2 {
		t.Fatalf("changes = %+v, want the two non-no-op changes", u.Changes)
	}
	for _, c := range u.Changes {
		if c.Action == "no-op" {
			t.Fatalf("no-op change leaked: %+v", c)
		}
	}
}
