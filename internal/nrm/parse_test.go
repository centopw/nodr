package nrm

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/centopw/nodr/internal/diag"
)

func TestParseDocuments(t *testing.T) {
	src := `---
apiVersion: test/v1
kind: Widget
metadata:
  name: first
spec:
  size: 3
  ratio: 0.5
  when: 2026-12-20
  enabled: true
  nothing: null
  parts: [a, b]
---
---
apiVersion: test/v1
kind: Widget
metadata:
  name: second
  labels:
    app: shop
spec:
  size: 1
`
	docs, diags := Parse("intent/widgets.yaml", []byte(src))
	if diags.HasErrors() {
		t.Fatalf("Parse: %v", diags.Err())
	}
	if len(docs) != 2 {
		t.Fatalf("got %d documents, want 2", len(docs))
	}
	first := docs[0]
	if first.APIVersion != "test/v1" || first.Kind != "Widget" || first.Metadata.Name != "first" {
		t.Errorf("first document = %s %s %s", first.APIVersion, first.Kind, first.Metadata.Name)
	}
	if first.File != "intent/widgets.yaml" || first.Line != 2 {
		t.Errorf("first location = %s:%d, want intent/widgets.yaml:2", first.File, first.Line)
	}
	spec := first.Object["spec"].(map[string]any)
	if got := spec["size"]; got != json.Number("3") {
		t.Errorf("size = %#v, want json.Number 3", got)
	}
	if got := spec["ratio"]; got != json.Number("0.5") {
		t.Errorf("ratio = %#v, want json.Number 0.5", got)
	}
	if got := spec["when"]; got != "2026-12-20" {
		t.Errorf("timestamps must stay strings as written, got %#v", got)
	}
	if got := spec["enabled"]; got != true {
		t.Errorf("enabled = %#v, want true", got)
	}
	if v, ok := spec["nothing"]; !ok || v != nil {
		t.Errorf("nothing = %#v, want nil", v)
	}
	second := docs[1]
	if second.Line != 15 || second.Metadata.Labels["app"] != "shop" {
		t.Errorf("second document at line %d with labels %v", second.Line, second.Metadata.Labels)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"invalid YAML", "a: [1, 2\n", "invalid YAML"},
		{"not a mapping", "- a\n- b\n", "must be a mapping"},
		{"duplicate key", "a: 1\na: 2\n", `duplicate key "a"`},
		{"complex key", "? [a, b]\n: 1\n", "keys must be scalars"},
		{"not a number", "a: .nan\n", "not a valid number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, diags := Parse("x.yaml", []byte(tt.src))
			if !diags.HasErrors() || !strings.Contains(diags.Err().Error(), tt.want) {
				t.Errorf("errors = %v, want one containing %q", diags.Err(), tt.want)
			}
		})
	}
}

func TestParseMergeKeys(t *testing.T) {
	src := `base: &base
  a: 1
  b: 2
item:
  b: 3
  <<: *base
`
	docs, diags := Parse("x.yaml", []byte(src))
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	item := docs[0].Object["item"].(map[string]any)
	if item["a"] != json.Number("1") || item["b"] != json.Number("3") {
		t.Errorf("item = %v, want a=1 from the merge and b=3 written explicitly", item)
	}
}

func TestLineOfAndFieldPath(t *testing.T) {
	src := `apiVersion: test/v1
kind: Widget
metadata:
  name: w
spec:
  size: 3
  parts:
    - a
    - b
`
	docs, _ := Parse("x.yaml", []byte(src))
	d := docs[0]
	tests := []struct {
		path     []string
		line     int
		rendered string
	}{
		{[]string{"spec", "size"}, 6, "spec.size"},
		{[]string{"spec", "parts", "1"}, 9, "spec.parts[1]"},
		{[]string{"spec", "missing", "deeper"}, 5, "spec.missing.deeper"},
		{[]string{"metadata", "name"}, 4, "metadata.name"},
	}
	for _, tt := range tests {
		if got := d.LineOf(tt.path...); got != tt.line {
			t.Errorf("LineOf(%v) = %d, want %d", tt.path, got, tt.line)
		}
		if got := d.FieldPath(tt.path...); got != tt.rendered {
			t.Errorf("FieldPath(%v) = %q, want %q", tt.path, got, tt.rendered)
		}
	}
}

func TestDecodeSpecRejectsUnknownFields(t *testing.T) {
	docs, _ := Parse("x.yaml", []byte("spec:\n  size: 3\n  extra: true\n"))
	var spec struct {
		Size int `json:"size"`
	}
	if err := docs[0].DecodeSpec(&spec); err == nil || !strings.Contains(err.Error(), "extra") {
		t.Errorf("DecodeSpec error = %v, want unknown field extra", err)
	}
}

func TestLoadDir(t *testing.T) {
	fsys := fstest.MapFS{
		"intent/b/two.yml":        {Data: []byte("kind: B\n")},
		"intent/a.yaml":           {Data: []byte("kind: A\n---\nkind: A2\n")},
		"intent/notes.txt":        {Data: []byte("not yaml")},
		"intent/.hidden/c.yaml":   {Data: []byte("kind: Hidden\n")},
		"intent/broken/bad.yaml":  {Data: []byte("kind: [\n")},
		"outside/ignored.yaml":    {Data: []byte("kind: Outside\n")},
		"intent/b/three.yaml":     {Data: []byte("kind: C\n")},
		"intent/b/nested/x.yaml":  {Data: []byte("kind: D\n")},
		"intent/b/nested/y.json5": {Data: []byte("{}")},
	}
	docs, diags := LoadDir(fsys, "intent")
	if got := diags.Count(diag.Error); got != 1 || !strings.Contains(diags.Err().Error(), "intent/broken/bad.yaml") {
		t.Errorf("diagnostics = %v, want one error for intent/broken/bad.yaml", diags)
	}
	var kinds []string
	for _, d := range docs {
		kinds = append(kinds, d.Kind)
	}
	if got, want := strings.Join(kinds, ","), "A,A2,D,C,B"; got != want {
		t.Errorf("kinds = %s, want %s (lexical file order)", got, want)
	}
}
