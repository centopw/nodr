package opentofu

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testBinary returns the OpenTofu binary for the tests that run it: the one
// that NODR_TEST_TOFU names, or else tofu on PATH. It skips the test if
// there is neither.
func testBinary(t *testing.T) string {
	t.Helper()
	name := os.Getenv("NODR_TEST_TOFU")
	if name == "" {
		path, err := exec.LookPath("tofu")
		if err != nil {
			t.Skip("OpenTofu not found: set NODR_TEST_TOFU or put tofu on PATH to run this test")
		}
		return path
	}
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("NODR_TEST_TOFU: %v", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// writeUnit writes a unit with one terraform_data resource. OpenTofu has
// that resource built in, so it downloads no provider.
func writeUnit(t *testing.T, dir, trigger string) {
	t.Helper()
	src := fmt.Sprintf(`resource "terraform_data" "example" {
  input            = "hello"
  triggers_replace = [%q]
}
`, trigger)
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// planUnit plans the unit of writeUnit and checks the action for its
// resource and the summary.
func planUnit(t *testing.T, r *Runner, planFile string, wantAction Action, wantSummary Summary) {
	t.Helper()
	p, err := r.Plan(t.Context(), planFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Change{{Address: "terraform_data.example", Type: "terraform_data", Action: wantAction}}; !reflect.DeepEqual(p.Changes, want) {
		t.Errorf("Plan changes = %v, want %v", p.Changes, want)
	}
	if s := p.Summary(); s != wantSummary {
		t.Errorf("Plan summary = %+v, want %+v", s, wantSummary)
	}
	if got, want := p.HasChanges(), wantSummary != (Summary{}); got != want {
		t.Errorf("HasChanges() = %v, want %v", got, want)
	}
}

func TestPlanAndApply(t *testing.T) {
	bin := testBinary(t)
	root := t.TempDir()
	unit := filepath.Join(root, "unit")
	if err := os.Mkdir(unit, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, unit, "one")
	var stdout bytes.Buffer
	r := &Runner{
		Binary: bin,
		Dir:    unit,
		// Keep the working files of OpenTofu in the test directory, whatever
		// the environment of the test says.
		Env:    []string{"TF_DATA_DIR=" + filepath.Join(root, "data")},
		Stdout: &stdout,
	}
	ctx := t.Context()

	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "OpenTofu has been successfully initialized") {
		t.Errorf("init output = %q", stdout.String())
	}

	stdout.Reset()
	create := filepath.Join(root, "create.tfplan")
	planUnit(t, r, create, Create, Summary{Create: 1})
	if out := stdout.String(); !strings.Contains(out, "terraform_data.example will be created") || strings.Contains(out, "format_version") {
		t.Errorf("plan output = %q, want the plan without its JSON form", out)
	}
	if err := r.Apply(ctx, create); err != nil {
		t.Fatal(err)
	}
	// Apply takes only the saved plan, which is stale now.
	err := r.Apply(ctx, create)
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) || !strings.Contains(cmdErr.Stderr, "Saved plan is stale") {
		t.Errorf("second Apply of %s: error = %v, want a stale plan", create, err)
	}

	planUnit(t, r, filepath.Join(root, "no-changes.tfplan"), NoOp, Summary{})

	writeUnit(t, unit, "two")
	planUnit(t, r, filepath.Join(root, "replace.tfplan"), Replace, Summary{Replace: 1})
}

// TestApplyImportsAndMoves checks that a plan that only imports and moves
// resources has changes, and that applying it records them in the state.
func TestApplyImportsAndMoves(t *testing.T) {
	bin := testBinary(t)
	root := t.TempDir()
	unit := filepath.Join(root, "unit")
	if err := os.Mkdir(unit, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, unit, "one")
	r := &Runner{Binary: bin, Dir: unit, Env: []string{"TF_DATA_DIR=" + filepath.Join(root, "data")}}
	ctx := t.Context()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	create := filepath.Join(root, "create.tfplan")
	planUnit(t, r, create, Create, Summary{Create: 1})
	if err := r.Apply(ctx, create); err != nil {
		t.Fatal(err)
	}

	// terraform_data accepts any ID for an import.
	src := `resource "terraform_data" "renamed" {
  input            = "hello"
  triggers_replace = ["one"]
}

moved {
  from = terraform_data.example
  to   = terraform_data.renamed
}

resource "terraform_data" "imported" {
}

import {
  to = terraform_data.imported
  id = "imported-id"
}
`
	if err := os.WriteFile(filepath.Join(unit, "main.tf"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	importAndMove := filepath.Join(root, "import-and-move.tfplan")
	p, err := r.Plan(ctx, importAndMove)
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{
		{Address: "terraform_data.imported", Type: "terraform_data", Action: NoOp, Importing: true},
		{Address: "terraform_data.renamed", Type: "terraform_data", Action: NoOp, PreviousAddress: "terraform_data.example"},
	}
	if !reflect.DeepEqual(p.Changes, want) {
		t.Errorf("Plan changes = %v, want %v", p.Changes, want)
	}
	if s := p.Summary(); s != (Summary{Import: 1, Move: 1}) || !p.HasChanges() {
		t.Errorf("Plan summary = %+v, HasChanges() = %v; want an import and a move", s, p.HasChanges())
	}
	if err := r.Apply(ctx, importAndMove); err != nil {
		t.Fatal(err)
	}

	// The state holds the import and the move now.
	p, err = r.Plan(ctx, filepath.Join(root, "no-changes.tfplan"))
	if err != nil {
		t.Fatal(err)
	}
	if p.HasChanges() {
		t.Errorf("Plan after the apply has changes: %v", p.Changes)
	}
}
