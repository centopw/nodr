package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeTofuScript is a fake tofu. It appends each call to $FAKE_TOFU_LOG, as
// the name of the unit it runs in followed by its arguments. init prints the
// API token that it gets from the environment. plan saves a plan file that
// names the unit, and show and apply accept only such a file. show prints
// $FAKE_TOFU_PLANS/<unit>.json, or a plan without changes if there is no
// such file. The call "<unit> <command>" in $FAKE_TOFU_FAIL fails.
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
	echo "initialized $unit with the token $PROXMOX_VE_API_TOKEN"
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

// fakeTofu is the fake tofu of a test.
type fakeTofu struct {
	// log is the file with the calls, and plans the directory with the
	// plans that show prints.
	log, plans string
}

// installFakeTofu makes a fake tofu the OpenTofu of nodr for the rest of
// the test.
func installFakeTofu(t *testing.T) *fakeTofu {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tofu is a shell script")
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

// setPlan sets the plan that show prints for a unit. Each change is the
// actions of a resource change in the JSON form of a plan, separated by
// commas, and an address, such as
// "delete,create proxmox_virtual_environment_vm.web_01". The address may be
// followed by "importing" for an instance that the plan imports, or by
// "from <address>" for one that it moves.
func (f *fakeTofu) setPlan(t *testing.T, unit string, changes ...string) {
	t.Helper()
	type resourceChange struct {
		Address         string `json:"address"`
		PreviousAddress string `json:"previous_address,omitempty"`
		Type            string `json:"type"`
		Change          struct {
			Actions   []string          `json:"actions"`
			Importing map[string]string `json:"importing,omitempty"`
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
		switch {
		case len(fields) == 3 && fields[2] == "importing":
			rc.Change.Importing = map[string]string{"id": "imported-id"}
		case len(fields) == 4 && fields[2] == "from":
			rc.PreviousAddress = fields[3]
		case len(fields) != 2:
			t.Fatalf("invalid change %q", c)
		}
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

// planDirRE matches a saved plan in the calls of tofu, and captures its
// directory.
var planDirRE = regexp.MustCompile(`-out=(\S+)/[^/\s]+\.tfplan`)

// checkCalls checks the calls of the fake tofu since the last check. In the
// calls, the directory of the saved plans is <plans>. The directory must be
// gone, since plans can hold secrets.
func (f *fakeTofu) checkCalls(t *testing.T, want ...string) {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Remove(f.log); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	calls := string(data)
	for _, m := range planDirRE.FindAllStringSubmatch(calls, -1) {
		if _, err := os.Stat(m[1]); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the saved plans in %s are left behind: %v", m[1], err)
		}
		calls = strings.ReplaceAll(calls, m[1], "<plans>")
	}
	var got []string
	if calls != "" {
		got = strings.Split(strings.TrimSuffix(calls, "\n"), "\n")
	}
	if !slices.Equal(got, want) {
		t.Errorf("calls of tofu:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// planCalls returns the calls of tofu that plan a unit.
func planCalls(unit string) []string {
	plan := "<plans>/" + unit + ".tfplan"
	return []string{
		unit + " init -input=false -no-color",
		unit + " plan -input=false -no-color -out=" + plan,
		unit + " show -json -no-color " + plan,
	}
}

// applyCall returns the call of tofu that applies the saved plan of a unit.
func applyCall(unit string) string {
	return unit + " apply -input=false -no-color <plans>/" + unit + ".tfplan"
}

// runInTerminal runs the command line as if stdin were a terminal in which
// a person types input.
func runInTerminal(input string, args ...string) result {
	var stdout, stderr bytes.Buffer
	a := &app{stdin: strings.NewReader(input), stdout: &stdout, stderr: &stderr, interactive: true}
	code := a.run(context.Background(), args)
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func readFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// checkNoCode checks that the workspace at root has no engine code.
func checkNoCode(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, terraformDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the workspace has engine code: %v", err)
	}
}

// planFiles is a workspace with an admitted VM and no engine code yet.
var planFiles = map[string]string{
	"nodr.yaml": manifest,
	"intent/platform/pve.yaml": `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata: { name: pve-main }
spec:
  endpoints: [https://10.0.10.11:8006]
  credentialsRef: proxmox/pve-main-token
  nodes: [pve1]
`,
	"intent/compute/web.yaml": `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-01
spec:
  placement: { cluster: pve-main, assignedNode: pve1 }
  identity: { vmid: 1000 }
  resources: { cpu: { cores: 2 }, memory: { size: 2Gi } }
`,
}

// wroteUnit is what plan and apply print when they write the code of the
// VM of planFiles.
const wroteUnit = `wrote terraform/pve-main-compute/providers.tf
wrote terraform/pve-main-compute/versions.tf
wrote terraform/pve-main-compute/vms.tf
`

// addWeb01 is the summary of a plan that creates the VM of planFiles.
const addWeb01 = `terraform/pve-main-compute: 1 to add, 0 to change, 0 to replace, 0 to destroy
  add      proxmox_virtual_environment_vm.web_01
`

func TestPlan(t *testing.T) {
	tofu := installFakeTofu(t)
	t.Setenv("PROXMOX_VE_API_TOKEN", "token-from-the-environment")
	root := writeWorkspace(t, planFiles)
	tofu.setPlan(t, "pve-main-compute", "create proxmox_virtual_environment_vm.web_01")

	r := run("plan", "-w", root)
	r.check(t, exitOK)
	if want := wroteUnit + addWeb01; r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
	// The output of OpenTofu goes to stderr, and OpenTofu gets the
	// environment of nodr.
	for _, want := range []string{"initialized pve-main-compute with the token token-from-the-environment\n", "planned pve-main-compute\n"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.stderr)
		}
	}
	tofu.checkCalls(t, planCalls("pve-main-compute")...)
	// The workspace holds the code that was planned.
	render := run("render", "-w", root, "vm/web-01")
	render.check(t, exitOK)
	if code := readFile(t, root, "terraform/pve-main-compute/vms.tf"); code != render.stdout {
		t.Errorf("vms.tf =\n%s\nwant\n%s", code, render.stdout)
	}

	// The code is in sync now, so planning again writes nothing.
	tofu.setPlan(t, "pve-main-compute")
	r = run("plan", "-w", root)
	r.check(t, exitOK)
	if want := "terraform/pve-main-compute: no changes\n"; r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
	tofu.checkCalls(t, planCalls("pve-main-compute")...)
}

func TestPlanSummary(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, planFiles)
	tofu.setPlan(t, "pve-main-compute",
		"read data.terraform_remote_state.network",
		"create proxmox_virtual_environment_vm.app_01",
		"no-op proxmox_virtual_environment_vm.dns_01",
		"delete proxmox_virtual_environment_vm.old_01",
		"forget proxmox_virtual_environment_vm.pet_01",
		"create,delete proxmox_virtual_environment_vm.web_01",
		"update proxmox_virtual_environment_vm.web_02",
	)
	r := run("plan", "-w", root)
	r.check(t, exitOK)
	want := wroteUnit + `terraform/pve-main-compute: 1 to add, 1 to change, 1 to replace, 1 to destroy, 1 to read, 1 to forget
  read     data.terraform_remote_state.network
  add      proxmox_virtual_environment_vm.app_01
  destroy  proxmox_virtual_environment_vm.old_01
  forget   proxmox_virtual_environment_vm.pet_01
  replace  proxmox_virtual_environment_vm.web_01
  change   proxmox_virtual_environment_vm.web_02
`
	if r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
}

func TestPlanCompileDiagnostics(t *testing.T) {
	t.Run("errors", func(t *testing.T) {
		tofu := installFakeTofu(t)
		files := maps.Clone(planFiles)
		files["intent/compute/web.yaml"] = strings.Replace(planFiles["intent/compute/web.yaml"], ", assignedNode: pve1", "", 1)
		root := writeWorkspace(t, files)
		for _, args := range [][]string{{"plan"}, {"apply", "--auto-approve"}} {
			r := run(append(args, "-w", root)...)
			r.check(t, exitError)
			want := `intent/compute/web.yaml:1: error: render web-01: spec.placement.assignedNode is empty: not admitted yet; run 'nodr admit vm/web-01' first
nodr: compilation failed; no file was changed
`
			if r.stderr != want || r.stdout != "" {
				t.Errorf("%v: stderr =\n%s\nwant\n%s\nstdout = %q", args, r.stderr, want, r.stdout)
			}
		}
		// OpenTofu did not run.
		tofu.checkCalls(t)
		checkNoCode(t, root)
	})

	t.Run("warnings", func(t *testing.T) {
		tofu := installFakeTofu(t)
		files := maps.Clone(planFiles)
		files["terraform/pve-main-compute/old.tf"] = "# nodr:managed vm/old-01\nresource \"proxmox_virtual_environment_vm\" \"old_01\" {\n  name = \"old-01\"\n}\n"
		root := writeWorkspace(t, files)
		r := run("plan", "-w", root)
		r.check(t, exitOK)
		want := "terraform/pve-main-compute/old.tf:2: warning: vm/old-01 does not exist in intent; delete its managed block to delete the VM, or remove the block's nodr:managed comment to keep the VM as code you own\n"
		if !strings.HasPrefix(r.stderr, want) {
			t.Errorf("stderr =\n%s\nwant it to start with\n%s", r.stderr, want)
		}
		if want := wroteUnit + "terraform/pve-main-compute: no changes\n"; r.stdout != want {
			t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
		}
		tofu.checkCalls(t, planCalls("pve-main-compute")...)
	})
}

// unitFiles is planFiles with more code below terraform/: a unit written by
// hand, a local module, which is not a unit, and a directory without .tf
// files.
func unitFiles() map[string]string {
	files := maps.Clone(planFiles)
	files["terraform/network/main.tf"] = "resource \"terraform_data\" \"vlans\" {\n  input = [10, 20]\n}\n"
	files["terraform/modules/vm/main.tf"] = "variable \"name\" {\n  type = string\n}\n"
	files["terraform/notes/README.md"] = "# Notes\n"
	return files
}

func TestPlanUnits(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, unitFiles())

	// Only the named unit is planned, but all code is written.
	r := run("plan", "-w", root, "--unit", "terraform/pve-main-compute/")
	r.check(t, exitOK)
	if want := wroteUnit + "terraform/pve-main-compute: no changes\n"; r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
	tofu.checkCalls(t, planCalls("pve-main-compute")...)

	// Without --unit, and with every unit named, all units are planned in
	// sorted order.
	for _, args := range [][]string{
		{"plan", "-w", root},
		{"plan", "-w", root, "--unit", "terraform/pve-main-compute", "--unit", "terraform/network"},
	} {
		r = run(args...)
		r.check(t, exitOK)
		if want := "terraform/network: no changes\nterraform/pve-main-compute: no changes\n"; r.stdout != want {
			t.Errorf("%v: stdout =\n%s\nwant\n%s", args, r.stdout, want)
		}
		tofu.checkCalls(t, slices.Concat(planCalls("network"), planCalls("pve-main-compute"))...)
	}

	for _, unit := range []string{"terraform/modules", "terraform/notes", "network", "terraform"} {
		r = run("apply", "-w", root, "--auto-approve", "--unit", "terraform/network", "--unit", unit)
		r.check(t, exitError)
		if want := "nodr: " + unit + " is not a state unit; the units are terraform/network, terraform/pve-main-compute\n"; r.stderr != want {
			t.Errorf("--unit %s: stderr = %q, want %q", unit, r.stderr, want)
		}
	}
	tofu.checkCalls(t)
}

func TestPlanWithoutUnits(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, map[string]string{"nodr.yaml": manifest})
	r := run("plan", "-w", root)
	r.check(t, exitOK)
	if want := "no state units: no directory below terraform/ holds .tf files\n"; r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
	r = run("plan", "-w", root, "--unit", "terraform/pve-main-compute")
	r.check(t, exitError)
	if want := "nodr: terraform/pve-main-compute is not a state unit: no directory below terraform/ holds .tf files\n"; r.stderr != want {
		t.Errorf("stderr = %q, want %q", r.stderr, want)
	}
	tofu.checkCalls(t)
}

// TestPlanChecksUnitsFirst checks that plan and apply check --unit before
// they write any file, and accept a unit that only the compiled code
// creates.
func TestPlanChecksUnitsFirst(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, planFiles)
	for _, args := range [][]string{{"plan"}, {"apply", "--auto-approve"}} {
		r := run(append(args, "-w", root, "--unit", "terraform/pve-main-compte")...)
		r.check(t, exitError)
		if want := "nodr: terraform/pve-main-compte is not a state unit; the units are terraform/pve-main-compute\n"; r.stderr != want || r.stdout != "" {
			t.Errorf("%v: stderr = %q, want %q; stdout = %q", args, r.stderr, want, r.stdout)
		}
	}
	tofu.checkCalls(t)
	checkNoCode(t, root)

	r := run("plan", "-w", root, "--unit", "terraform/pve-main-compute")
	r.check(t, exitOK)
	if want := wroteUnit + "terraform/pve-main-compute: no changes\n"; r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
	tofu.checkCalls(t, planCalls("pve-main-compute")...)
}

func TestApplyAutoApprove(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, planFiles)
	tofu.setPlan(t, "pve-main-compute", "create proxmox_virtual_environment_vm.web_01")
	r := run("apply", "-w", root, "--auto-approve")
	r.check(t, exitOK)
	if want := wroteUnit + addWeb01 + "terraform/pve-main-compute: applied\n"; r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
	if !strings.Contains(r.stderr, "applied pve-main-compute\n") {
		t.Errorf("stderr = %q, want the output of tofu apply", r.stderr)
	}
	tofu.checkCalls(t, append(planCalls("pve-main-compute"), applyCall("pve-main-compute"))...)
}

// TestApplyRunsTheSavedPlans checks that apply applies the plan file that
// it saved for each unit, after planning all units, and does not plan
// again. The fake tofu rejects a plan file that plan did not save for the
// unit.
func TestApplyRunsTheSavedPlans(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, unitFiles())
	tofu.setPlan(t, "network", "update terraform_data.vlans")
	tofu.setPlan(t, "pve-main-compute", "create proxmox_virtual_environment_vm.web_01")
	r := run("apply", "-w", root, "--auto-approve")
	r.check(t, exitOK)
	want := wroteUnit + `terraform/network: 0 to add, 1 to change, 0 to replace, 0 to destroy
  change   terraform_data.vlans
` + addWeb01 + `terraform/network: applied
terraform/pve-main-compute: applied
`
	if r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
	tofu.checkCalls(t, slices.Concat(
		planCalls("network"),
		planCalls("pve-main-compute"),
		[]string{applyCall("network"), applyCall("pve-main-compute")},
	)...)
}

func TestApplyAsks(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, planFiles)
	tofu.setPlan(t, "pve-main-compute", "create proxmox_virtual_environment_vm.web_01")
	const question = "Apply these changes? Only 'yes' is accepted: "

	for _, answer := range []string{"no\n", "y\n", "YES\n", " yes\n", "yes \n", "yes please\n", "\n", ""} {
		r := runInTerminal(answer, "apply", "-w", root)
		r.check(t, exitError)
		// The question follows the summary that it is about.
		if !strings.HasSuffix(r.stdout, addWeb01+question) {
			t.Errorf("answer %q: stdout =\n%s\nwant it to end with\n%s", answer, r.stdout, addWeb01+question)
		}
		if want := "nodr: apply canceled; nothing was applied\n"; !strings.HasSuffix(r.stderr, want) {
			t.Errorf("answer %q: stderr =\n%s\nwant it to end with\n%s", answer, r.stderr, want)
		}
		tofu.checkCalls(t, planCalls("pve-main-compute")...)
	}

	// The line can end with \r\n, as on Windows, or with the input.
	for _, answer := range []string{"yes\n", "yes\r\n", "yes"} {
		r := runInTerminal(answer, "apply", "-w", root)
		r.check(t, exitOK)
		if want := addWeb01 + question + "terraform/pve-main-compute: applied\n"; r.stdout != want {
			t.Errorf("answer %q: stdout =\n%s\nwant\n%s", answer, r.stdout, want)
		}
		if !strings.Contains(r.stderr, "applied pve-main-compute\n") {
			t.Errorf("answer %q: stderr = %q, want the output of tofu apply", answer, r.stderr)
		}
		tofu.checkCalls(t, append(planCalls("pve-main-compute"), applyCall("pve-main-compute"))...)
	}
}

func TestApplyNeedsATerminal(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, planFiles)
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	// Neither an empty stdin nor an answer through a pipe counts.
	for _, stdin := range []io.Reader{devNull, strings.NewReader("yes\n")} {
		r := runWithStdin(stdin, "apply", "-w", root)
		r.check(t, exitUsage)
		if want := "nodr: stdin is not a terminal, so apply cannot ask for confirmation; pass --auto-approve to apply without asking\n"; !strings.HasPrefix(r.stderr, want) {
			t.Errorf("stderr = %q, want it to start with %q", r.stderr, want)
		}
	}
	tofu.checkCalls(t)
	checkNoCode(t, root)
}

func TestApplyWithoutChanges(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, planFiles)
	r := runInTerminal("", "apply", "-w", root)
	r.check(t, exitOK)
	// Apply does not ask.
	if want := wroteUnit + "terraform/pve-main-compute: no changes\nnothing to apply\n"; r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
	tofu.checkCalls(t, planCalls("pve-main-compute")...)
}

// TestApplyImportsAndMoves checks that apply applies a plan that only
// imports and moves resources, and that imports and moves need no
// --allow-destroy.
func TestApplyImportsAndMoves(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, planFiles)
	tofu.setPlan(t, "pve-main-compute",
		"no-op proxmox_virtual_environment_vm.dns_01 importing",
		"no-op proxmox_virtual_environment_vm.web_01 from proxmox_virtual_environment_vm.web",
	)
	r := run("apply", "-w", root, "--auto-approve")
	r.check(t, exitOK)
	want := wroteUnit + `terraform/pve-main-compute: 0 to add, 0 to change, 0 to replace, 0 to destroy, 1 to import, 1 to move
  import   proxmox_virtual_environment_vm.dns_01
  move     proxmox_virtual_environment_vm.web to proxmox_virtual_environment_vm.web_01
terraform/pve-main-compute: applied
`
	if r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
	tofu.checkCalls(t, append(planCalls("pve-main-compute"), applyCall("pve-main-compute"))...)

	// An instance that is imported or moved can have another action too,
	// and only that action can need --allow-destroy.
	tofu.setPlan(t, "pve-main-compute",
		"update proxmox_virtual_environment_vm.dns_01 importing",
		"delete,create proxmox_virtual_environment_vm.web_01 from proxmox_virtual_environment_vm.web",
	)
	r = run("apply", "-w", root, "--auto-approve")
	r.check(t, exitError)
	want = `terraform/pve-main-compute: 0 to add, 1 to change, 1 to replace, 0 to destroy, 1 to import, 1 to move
  import   proxmox_virtual_environment_vm.dns_01
  change   proxmox_virtual_environment_vm.dns_01
  move     proxmox_virtual_environment_vm.web to proxmox_virtual_environment_vm.web_01
  replace  proxmox_virtual_environment_vm.web_01
`
	if r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
	const refusal = `nodr: the plan replaces or destroys resources:
  terraform/pve-main-compute: replace proxmox_virtual_environment_vm.web_01
nodr: nothing was applied; run apply with --allow-destroy to make these changes
`
	if !strings.HasSuffix(r.stderr, refusal) {
		t.Errorf("stderr =\n%s\nwant it to end with\n%s", r.stderr, refusal)
	}
	tofu.checkCalls(t, planCalls("pve-main-compute")...)
}

func TestApplyGuardsDestruction(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, planFiles)
	tofu.setPlan(t, "pve-main-compute",
		"delete proxmox_virtual_environment_vm.old_01",
		"update proxmox_virtual_environment_vm.web_01",
		"delete,create proxmox_virtual_environment_vm.web_02",
		// A replace that forgets the old VM instead of deleting it.
		"create,forget proxmox_virtual_environment_vm.web_03",
	)
	const refusal = `nodr: the plan replaces or destroys resources:
  terraform/pve-main-compute: destroy proxmox_virtual_environment_vm.old_01
  terraform/pve-main-compute: replace proxmox_virtual_environment_vm.web_02
  terraform/pve-main-compute: replace proxmox_virtual_environment_vm.web_03
nodr: nothing was applied; run apply with --allow-destroy to make these changes
`
	// With or without --auto-approve, and before the question.
	for _, r := range []result{
		run("apply", "-w", root, "--auto-approve"),
		runInTerminal("yes\n", "apply", "-w", root),
	} {
		r.check(t, exitError)
		if !strings.HasSuffix(r.stderr, refusal) {
			t.Errorf("stderr =\n%s\nwant it to end with\n%s", r.stderr, refusal)
		}
		if strings.Contains(r.stdout, "Apply these changes?") {
			t.Errorf("apply asked before it refused:\n%s", r.stdout)
		}
	}
	tofu.checkCalls(t, slices.Concat(planCalls("pve-main-compute"), planCalls("pve-main-compute"))...)

	for _, r := range []result{
		run("apply", "-w", root, "--auto-approve", "--allow-destroy"),
		runInTerminal("yes\n", "apply", "-w", root, "--allow-destroy"),
	} {
		r.check(t, exitOK)
		if !strings.HasSuffix(r.stdout, "terraform/pve-main-compute: applied\n") {
			t.Errorf("stdout =\n%s\nwant the unit applied", r.stdout)
		}
	}
	apply := append(planCalls("pve-main-compute"), applyCall("pve-main-compute"))
	tofu.checkCalls(t, slices.Concat(apply, apply)...)
}

func TestOpenTofuNotFound(t *testing.T) {
	root := writeWorkspace(t, planFiles)
	t.Setenv("NODR_TOFU", "")
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{{"plan"}, {"apply", "--auto-approve"}} {
		r := run(append(args, "-w", root)...)
		r.check(t, exitError)
		if want := "nodr: OpenTofu not found: no tofu on PATH; install OpenTofu (https://opentofu.org/docs/intro/install/), or set NODR_TOFU to the path of the tofu binary\n"; r.stderr != want || r.stdout != "" {
			t.Errorf("%v: stderr = %q, want %q; stdout = %q", args, r.stderr, want, r.stdout)
		}
	}

	missing := filepath.Join(t.TempDir(), "tofu")
	t.Setenv("NODR_TOFU", missing)
	r := run("plan", "-w", root)
	r.check(t, exitError)
	if want := "nodr: OpenTofu not found: NODR_TOFU is set to \"" + missing + "\""; !strings.HasPrefix(r.stderr, want) {
		t.Errorf("stderr = %q, want it to start with %q", r.stderr, want)
	}
	// nodr fails before it writes code that it cannot plan.
	checkNoCode(t, root)
}

func TestOpenTofuFails(t *testing.T) {
	tofu := installFakeTofu(t)
	files := unitFiles()
	files["terraform/storage/main.tf"] = "resource \"terraform_data\" \"pools\" {\n  input = [\"ceph-vm\"]\n}\n"
	root := writeWorkspace(t, files)
	tofu.setPlan(t, "network", "update terraform_data.vlans")
	tofu.setPlan(t, "pve-main-compute", "create proxmox_virtual_environment_vm.web_01")
	tofu.setPlan(t, "storage", "update terraform_data.pools")

	// Plan stops at the first unit that fails.
	t.Setenv("FAKE_TOFU_FAIL", "network init")
	r := run("plan", "-w", root)
	r.check(t, exitError)
	if !strings.Contains(r.stderr, "Error: init failed in network\n") || !strings.HasSuffix(r.stderr, "nodr: terraform/network: tofu init -input=false -no-color: exit status 1\n") {
		t.Errorf("stderr = %q, want the output of tofu and the command that failed", r.stderr)
	}
	tofu.checkCalls(t, "network init -input=false -no-color")

	// Apply keeps the units applied before a failure, and applies no unit
	// after it.
	t.Setenv("FAKE_TOFU_FAIL", "pve-main-compute apply")
	r = run("apply", "-w", root, "--auto-approve")
	r.check(t, exitError)
	if want := "terraform/network: applied\nterraform/pve-main-compute: failed\nterraform/storage: not applied\n"; !strings.HasSuffix(r.stdout, want) {
		t.Errorf("stdout =\n%s\nwant it to end with\n%s", r.stdout, want)
	}
	failed := regexp.MustCompile(`nodr: terraform/pve-main-compute: tofu apply -input=false -no-color \S+/pve-main-compute\.tfplan: exit status 1\n$`)
	if !strings.Contains(r.stderr, "Error: apply failed in pve-main-compute\n") || !failed.MatchString(r.stderr) {
		t.Errorf("stderr = %q, want the output of tofu and the command that failed", r.stderr)
	}
	tofu.checkCalls(t, slices.Concat(
		planCalls("network"), planCalls("pve-main-compute"), planCalls("storage"),
		[]string{applyCall("network"), applyCall("pve-main-compute")},
	)...)
}

// cancelOn is a writer that cancels a context when text is written to it.
type cancelOn struct {
	text   string
	cancel context.CancelFunc
	mu     sync.Mutex
	buf    bytes.Buffer
}

func (w *cancelOn) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if bytes.Contains(p, []byte(w.text)) {
		w.cancel()
	}
	return w.buf.Write(p)
}

func (w *cancelOn) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestApplyInterrupted(t *testing.T) {
	tofu := installFakeTofu(t)
	root := writeWorkspace(t, planFiles)
	tofu.setPlan(t, "pve-main-compute", "create proxmox_virtual_environment_vm.web_01")

	// Nobody answers the question, and an interrupt comes while apply
	// waits.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdin, w := io.Pipe()
	defer w.Close()
	stdout := &cancelOn{text: "Apply these changes?", cancel: cancel}
	var stderr bytes.Buffer
	a := &app{stdin: stdin, stdout: stdout, stderr: &stderr, interactive: true}
	r := result{code: a.run(ctx, []string{"apply", "-w", root}), stdout: stdout.String(), stderr: stderr.String()}
	r.check(t, exitError)
	if want := "Apply these changes? Only 'yes' is accepted: \n"; !strings.HasSuffix(r.stdout, want) {
		t.Errorf("stdout =\n%s\nwant it to end with\n%s", r.stdout, want)
	}
	if want := "nodr: interrupted; nothing was applied: context canceled\n"; !strings.HasSuffix(r.stderr, want) {
		t.Errorf("stderr =\n%s\nwant it to end with\n%s", r.stderr, want)
	}
	tofu.checkCalls(t, planCalls("pve-main-compute")...)

	// OpenTofu does not start once the context is done.
	var out, errOut bytes.Buffer
	a = &app{stdin: stdin, stdout: &out, stderr: &errOut}
	r = result{code: a.run(ctx, []string{"apply", "-w", root, "--auto-approve"}), stdout: out.String(), stderr: errOut.String()}
	r.check(t, exitError)
	if want := "nodr: terraform/pve-main-compute: tofu init -input=false -no-color: context canceled\n"; r.stderr != want {
		t.Errorf("stderr = %q, want %q", r.stderr, want)
	}
	tofu.checkCalls(t)
}
