package opentofu

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The fixtures are the output of tofu show -json from OpenTofu 1.11.4.
// changes.json is a plan with one change of each kind for terraform_data
// resources and a terraform_remote_state data source. no-changes.json is
// the plan for a unit whose only resource is up to date.

func parseFixture(t *testing.T, name string) Plan {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePlan(data)
	if err != nil {
		t.Fatalf("ParsePlan(%s): %v", name, err)
	}
	return p
}

func TestParsePlan(t *testing.T) {
	p := parseFixture(t, "changes.json")
	want := []Change{
		// Read during the apply, because it depends on terraform_data.updated.
		{Address: "data.terraform_remote_state.read", Type: "terraform_remote_state", Action: Read},
		{Address: "terraform_data.created", Type: "terraform_data", Action: Create},
		// Removed from the configuration.
		{Address: "terraform_data.deleted", Type: "terraform_data", Action: Delete},
		{Address: "terraform_data.kept", Type: "terraform_data", Action: NoOp},
		// Moved from terraform_data.old_name, and otherwise unchanged.
		{Address: "terraform_data.new_name", Type: "terraform_data", Action: NoOp},
		// A removed block with destroy = false.
		{Address: "terraform_data.released", Type: "terraform_data", Action: Forget},
		// The actions are ["delete", "create"].
		{Address: "terraform_data.replaced", Type: "terraform_data", Action: Replace},
		// The actions are ["create", "delete"], for create_before_destroy.
		{Address: "terraform_data.replaced_first", Type: "terraform_data", Action: Replace},
		{Address: "terraform_data.updated", Type: "terraform_data", Action: Update},
	}
	if !reflect.DeepEqual(p.Changes, want) {
		t.Errorf("Changes =\n%v\nwant\n%v", p.Changes, want)
	}
	wantSummary := Summary{Create: 1, Update: 1, Replace: 2, Delete: 1, Read: 1, Forget: 1}
	if s := p.Summary(); s != wantSummary {
		t.Errorf("Summary() = %+v, want %+v", s, wantSummary)
	}
	if !p.HasChanges() {
		t.Error("HasChanges() = false, want true")
	}
}

func TestParsePlanWithoutChanges(t *testing.T) {
	p := parseFixture(t, "no-changes.json")
	want := []Change{{Address: "terraform_data.example", Type: "terraform_data", Action: NoOp}}
	if !reflect.DeepEqual(p.Changes, want) {
		t.Errorf("Changes = %v, want %v", p.Changes, want)
	}
	if s := p.Summary(); s != (Summary{}) {
		t.Errorf("Summary() = %+v, want no changes", s)
	}
	if p.HasChanges() {
		t.Error("HasChanges() = true, want false")
	}
}

func TestHasChanges(t *testing.T) {
	for _, a := range []Action{Create, Update, Replace, Delete, Read, Forget} {
		p := Plan{Changes: []Change{
			{Address: "terraform_data.a", Type: "terraform_data", Action: NoOp},
			{Address: "terraform_data.b", Type: "terraform_data", Action: a},
		}}
		if !p.HasChanges() {
			t.Errorf("HasChanges() = false for a plan with %s", a)
		}
	}
	if (Plan{}).HasChanges() {
		t.Error("HasChanges() = true for an empty plan")
	}
}

func TestParsePlanErrors(t *testing.T) {
	withActions := func(actions string) string {
		return `{"format_version": "1.2", "resource_changes": [{"address": "terraform_data.x", "type": "terraform_data", "change": {"actions": ` + actions + `}}]}`
	}
	tests := []struct {
		name string
		data string
		want string
	}{
		{"invalid JSON", `{"format_version": "1.2",`, "invalid plan JSON"},
		{"not a plan", `{}`, `unsupported plan format version ""`},
		{"newer format", `{"format_version": "2.0"}`, `unsupported plan format version "2.0"`},
		{"failed plan", `{"format_version": "1.2", "errored": true}`, "planning failed"},
		{"unknown action", withActions(`["archive"]`), `terraform_data.x: unknown actions ["archive"]`},
		{"no actions", withActions(`[]`), `terraform_data.x: unknown actions []`},
		{"update and delete", withActions(`["update", "delete"]`), `unknown actions ["update" "delete"]`},
		{"replace is not in the JSON form", withActions(`["replace"]`), `unknown actions ["replace"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePlan([]byte(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ParsePlan error = %v, want %q", err, tt.want)
			}
		})
	}
}
