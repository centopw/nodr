package nrm

import (
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/centopw/nodr/internal/diag"
)

var testSchemas = fstest.MapFS{
	"metadata.json": {Data: []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["name"],
  "properties": {
    "name": {"type": "string"},
    "uid": {"type": "string"},
    "labels": {"type": "object", "additionalProperties": {"type": "string"}},
    "annotations": {"type": "object", "additionalProperties": {"type": "string"}}
  }
}`)},
	"widget.json": {Data: []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["apiVersion", "kind", "metadata", "spec"],
  "properties": {
    "apiVersion": {"const": "test/v1"},
    "kind": {"const": "Widget"},
    "metadata": {"$ref": "metadata.json"},
    "spec": {
      "type": "object",
      "additionalProperties": false,
      "required": ["size"],
      "properties": {
        "size": {"type": "integer", "minimum": 1},
        "color": {"type": "string", "pattern": "^[a-z]+$"},
        "friend": {"type": "string"}
      }
    }
  }
}`)},
	"config.json": {Data: []byte(`{
  "type": "object",
  "properties": {"apiVersion": {"const": "test/v1"}, "kind": {"const": "Config"}}
}`)},
}

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := r.AddSchemas("test/v1", testSchemas); err != nil {
		t.Fatal(err)
	}
	widget := KindInfo{
		APIVersion: "test/v1",
		Kind:       "Widget",
		ShortName:  "w",
		Schema:     "widget.json",
		References: func(d *Document) ([]FieldRef, error) {
			var spec struct {
				Size   int    `json:"size"`
				Color  string `json:"color"`
				Friend string `json:"friend"`
			}
			if err := d.DecodeSpec(&spec); err != nil {
				return nil, err
			}
			var refs []FieldRef
			if spec.Friend != "" {
				refs = append(refs, FieldRef{Target: Ref{Kind: "Widget", Name: spec.Friend}, Path: []string{"spec", "friend"}})
			}
			refs = append(refs, FieldRef{Target: Ref{Kind: "Unregistered", Name: "x"}, Path: []string{"spec"}})
			return refs, nil
		},
		Check: func(d *Document, diags *diag.List) {
			if d.Metadata.Name == "checked" {
				diags.Warnf(d.File, d.Line, "", "check ran")
			}
		},
	}
	for _, k := range []KindInfo{widget, {APIVersion: "test/v1", Kind: "Config", Schema: "config.json", Manifest: true}} {
		if err := r.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func parseOne(t *testing.T, src string) *Document {
	t.Helper()
	docs, diags := Parse("intent/test.yaml", []byte(src))
	if diags.HasErrors() || len(docs) != 1 {
		t.Fatalf("parse: %v (%d documents)", diags.Err(), len(docs))
	}
	return docs[0]
}

func widget(name, spec string) string {
	return "apiVersion: test/v1\nkind: Widget\nmetadata:\n  name: " + name + "\nspec:\n" + spec
}

func TestRegisterAndLookup(t *testing.T) {
	r := newTestRegistry(t)
	if err := r.Register(KindInfo{APIVersion: "test/v1", Kind: "Widget", Schema: "widget.json"}); err == nil {
		t.Error("registering a kind twice succeeded")
	}
	if err := r.Register(KindInfo{APIVersion: "test/v1", Kind: "Gadget", ShortName: "w", Schema: "widget.json"}); err == nil {
		t.Error("reusing a short name succeeded")
	}
	if err := r.Register(KindInfo{APIVersion: "test/v1", Kind: "Broken", Schema: "missing.json"}); err == nil {
		t.Error("registering a kind with a missing schema succeeded")
	}
	for _, name := range []string{"Widget", "widget", "w"} {
		if k, ok := r.LookupName(name); !ok || k.Kind != "Widget" {
			t.Errorf("LookupName(%q) = %v, %v", name, k, ok)
		}
	}
	ref, err := r.ParseRef("w/big-one")
	if err != nil || ref != (Ref{Kind: "Widget", Name: "big-one"}) {
		t.Errorf("ParseRef = %v, %v", ref, err)
	}
	for _, bad := range []string{"big-one", "w/", "/x", "gizmo/x"} {
		if _, err := r.ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) succeeded", bad)
		}
	}
	if got := len(r.Kinds()); got != 2 {
		t.Errorf("Kinds() has %d kinds, want 2", got)
	}
}

func TestValidateReportsSchemaErrorsAtFieldLines(t *testing.T) {
	r := newTestRegistry(t)
	d := parseOne(t, widget("w1", "  size: 0\n  color: Red\n  shape: round\n"))
	got := r.Validate([]*Document{d})
	want := []string{
		"intent/test.yaml:6: error: spec.size: minimum: got 0, want 1",
		"intent/test.yaml:7: error: spec.color: 'Red' does not match pattern '^[a-z]+$'",
		"intent/test.yaml:8: error: spec.shape: unknown field",
	}
	assertDiags(t, got, want)
}

func TestValidateMissingField(t *testing.T) {
	r := newTestRegistry(t)
	d := parseOne(t, widget("w1", "  color: red\n"))
	assertDiags(t, r.Validate([]*Document{d}), []string{
		"intent/test.yaml:5: error: spec.size: missing required field",
	})
}

func TestValidateKinds(t *testing.T) {
	r := newTestRegistry(t)
	docs := []*Document{
		parseOne(t, "kind: Widget\nmetadata:\n  name: a\n"),
		parseOne(t, "apiVersion: test/v1\nmetadata:\n  name: a\n"),
		parseOne(t, "apiVersion: test/v2\nkind: Widget\nmetadata:\n  name: a\n"),
		parseOne(t, "apiVersion: test/v1\nkind: Config\nmetadata:\n  name: a\n"),
	}
	assertDiags(t, r.Validate(docs), []string{
		"intent/test.yaml:1: error: apiVersion: missing apiVersion",
		"intent/test.yaml:1: error: kind: missing kind",
		"intent/test.yaml:1: error: kind: Config documents belong in nodr.yaml, not among intent documents",
		"intent/test.yaml:2: error: kind: unknown kind Widget in API version test/v2",
	})
}

func TestValidateDuplicatesAndReferences(t *testing.T) {
	r := newTestRegistry(t)
	uid := NewUID()
	docs := []*Document{
		parseOne(t, "apiVersion: test/v1\nkind: Widget\nmetadata:\n  name: a\n  uid: "+uid+"\nspec:\n  size: 1\n  friend: b\n"),
		parseOne(t, "apiVersion: test/v1\nkind: Widget\nmetadata:\n  name: b\n  uid: "+uid+"\nspec:\n  size: 1\n  friend: ghost\n"),
		parseOne(t, widget("a", "  size: 2\n")),
	}
	docs[1].File = "intent/b.yaml"
	docs[2].File = "intent/c.yaml"
	assertDiags(t, r.Validate(docs), []string{
		"intent/b.yaml:5: error: metadata.uid: UID " + uid + " is also used by Widget/a at intent/test.yaml:1",
		`intent/b.yaml:8: error: spec.friend: Widget "ghost" does not exist`,
		"intent/c.yaml:4: error: metadata.name: Widget/a is defined twice; it is also defined at intent/test.yaml:1",
	})
}

func TestValidateRunsChecksOnlyAfterSchemaPasses(t *testing.T) {
	r := newTestRegistry(t)
	good := parseOne(t, widget("checked", "  size: 1\n"))
	bad := parseOne(t, widget("checked", "  size: nope\n"))
	bad.File = "intent/bad.yaml"
	got := r.Validate([]*Document{good, bad})
	if got.Count(diag.Warning) != 1 {
		t.Errorf("check ran %d times, want once: %v", got.Count(diag.Warning), got)
	}
}

func TestValidateManifest(t *testing.T) {
	r := newTestRegistry(t)
	ok := parseOne(t, "apiVersion: test/v1\nkind: Config\nmetadata:\n  name: home\n")
	if diags := r.ValidateManifest(ok); diags.HasErrors() {
		t.Errorf("ValidateManifest: %v", diags.Err())
	}
	wrong := parseOne(t, widget("w", "  size: 1\n"))
	assertDiags(t, r.ValidateManifest(wrong), []string{
		"intent/test.yaml:1: error: kind: the workspace manifest must be a Workspace document, not Widget",
	})
}

func TestValidateMetadata(t *testing.T) {
	r := newTestRegistry(t)
	d := parseOne(t, `apiVersion: test/v1
kind: Widget
metadata:
  name: Web_01
  uid: not-a-ulid
  labels:
    nodr/environment: prod
    nodr/secret: x
    bad key: x
    app: "no spaces"
  annotations:
    nodr/description: fine
    nodr/binding.provision: ansible
    nodr/other: x
spec:
  size: 1
`)
	assertDiags(t, r.Validate([]*Document{d}), []string{
		"intent/test.yaml:4: error: metadata.name: must contain only lowercase letters, digits and hyphens, and start and end with a letter or digit",
		"intent/test.yaml:5: error: metadata.uid: must be a ULID assigned by nodr: ulid: bad data size when unmarshaling",
		`intent/test.yaml:9: error: metadata.labels.bad key: invalid key: name "bad key" must be at most 63 letters, digits, '-', '_' or '.', starting and ending with a letter or digit`,
		"intent/test.yaml:8: error: metadata.labels.nodr/secret: the nodr/ prefix is reserved; the only label nodr defines is nodr/environment",
		`intent/test.yaml:10: error: metadata.labels.app: invalid value "no spaces": use at most 63 letters, digits, '-', '_' or '.', starting and ending with a letter or digit`,
		"intent/test.yaml:14: error: metadata.annotations.nodr/other: the nodr/ prefix is reserved; nodr defines nodr/description and nodr/binding.<aspect>",
	})
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"a", "web-01", "0abc", strings.Repeat("a", 63)} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("ValidateName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a_b", "a.b", strings.Repeat("a", 64)} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("ValidateName(%q) succeeded", bad)
		}
	}
}

func TestNewUIDIsValid(t *testing.T) {
	r := newTestRegistry(t)
	d := parseOne(t, "apiVersion: test/v1\nkind: Widget\nmetadata:\n  name: a\n  uid: "+NewUID()+"\nspec:\n  size: 1\n")
	if diags := r.Validate([]*Document{d}); diags.HasErrors() {
		t.Errorf("a new UID is rejected: %v", diags.Err())
	}
}

func assertDiags(t *testing.T, got diag.List, want []string) {
	t.Helper()
	lines := make([]string, len(got))
	for i, d := range got {
		lines[i] = d.String()
	}
	sort.Strings(lines)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("diagnostics:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
