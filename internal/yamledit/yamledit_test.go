package yamledit

import (
	"errors"
	"strings"
	"testing"
)

// vmOrder is the key order of the mappings the tests insert into.
func vmOrder(path []string) []string {
	switch strings.Join(path, ".") {
	case "":
		return []string{"apiVersion", "kind", "metadata", "spec"}
	case "metadata":
		return []string{"name", "uid", "labels", "annotations"}
	case "spec":
		return []string{"placement", "identity", "source", "resources", "nics"}
	case "spec.placement":
		return []string{"cluster", "node", "assignedNode", "affinity"}
	}
	if len(path) > 0 && path[len(path)-1] == "ipv4" {
		return []string{"mode", "address"}
	}
	return nil
}

func TestInsert(t *testing.T) {
	tests := []struct {
		name  string
		src   string
		edits []Edit
		want  string
	}{
		{
			name: "block mapping, after the preceding key",
			src: `# A web server.
apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-02   # the second one
  labels:
    app: website
spec: {}
`,
			edits: []Edit{{Line: 2, Path: []string{"metadata", "uid"}, Value: "01J9Z3K4T7M2Q8V5X6N0B1C2D4"}},
			want: `# A web server.
apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-02   # the second one
  uid: 01J9Z3K4T7M2Q8V5X6N0B1C2D4
  labels:
    app: website
spec: {}
`,
		},
		{
			name: "block mapping, at its end before comments and blank lines",
			src: `spec:
  placement:
    cluster: pve-main
    node: auto   # let nodr choose
      # more about the node
    # Anti-affinity comes later.

  # Where the guest comes from.
  source:
    template: debian-12-cloud
`,
			edits: []Edit{{Line: 1, Path: []string{"spec", "placement", "assignedNode"}, Value: "pve2"}},
			want: `spec:
  placement:
    cluster: pve-main
    node: auto   # let nodr choose
      # more about the node
    assignedNode: pve2
    # Anti-affinity comes later.

  # Where the guest comes from.
  source:
    template: debian-12-cloud
`,
		},
		{
			name: "flow mapping",
			src: `spec:
  nics:
    - network: dmz
      ipv4: { mode: auto }   # allocate
`,
			edits: []Edit{{Line: 1, Path: []string{"spec", "nics", "0", "ipv4", "address"}, Value: "10.0.20.22/24"}},
			want: `spec:
  nics:
    - network: dmz
      ipv4: { mode: auto, address: 10.0.20.22/24 }   # allocate
`,
		},
		{
			name:  "flow mapping, before a key that follows",
			src:   "ipv4: {mode: \"auto\", gateway: 'x, y'}\n",
			edits: []Edit{{Line: 1, Path: []string{"ipv4", "address"}, Value: "10.0.20.22/24"}},
			want:  "ipv4: {mode: \"auto\", address: 10.0.20.22/24, gateway: 'x, y'}\n",
		},
		{
			name: "flow mapping over several lines with a trailing comma",
			src: `ipv4: {
  mode: auto,   # comment
}
`,
			edits: []Edit{{Line: 1, Path: []string{"ipv4", "address"}, Value: "10.0.20.22/24"}},
			want: `ipv4: {
  mode: auto, address: 10.0.20.22/24,   # comment
}
`,
		},
		{
			name: "flow mapping after nested collections, aliases and multi-line scalars",
			src: `a: &x 1
m: { n: { o: [1, "]"] }, p: *x, q: hello
  world }
`,
			edits: []Edit{{Line: 1, Path: []string{"m", "r"}, Value: "v"}},
			want: `a: &x 1
m: { n: { o: [1, "]"] }, p: *x, q: hello
  world, r: v }
`,
		},
		{
			name: "flow syntax that ends values in unusual places",
			src: "\uFEFFm: { a: &x !!str \"q\\\"}\", b: 'it''s }', c: [x, { y: z }], d: *x, e: [p: 1] }  # c\n" +
				"n: !!map {}\n" +
				"o: {  # nothing yet\n}\n" +
				"p: {\n  a: 1,  # one\n  b: 2\n}\n",
			edits: []Edit{
				{Line: 1, Path: []string{"m", "z"}, Value: 1},
				{Line: 1, Path: []string{"n", "z"}, Value: 2},
				{Line: 1, Path: []string{"o", "z"}, Value: 3},
				{Line: 1, Path: []string{"p", "z"}, Value: 4},
			},
			want: "\uFEFFm: { a: &x !!str \"q\\\"}\", b: 'it''s }', c: [x, { y: z }], d: *x, e: [p: 1], z: 1 }  # c\n" +
				"n: !!map { z: 2 }\n" +
				"o: { z: 3,  # nothing yet\n}\n" +
				"p: {\n  a: 1,  # one\n  b: 2, z: 4\n}\n",
		},
		{
			name:  "empty flow mapping",
			src:   "spec:\n  identity: {}\n  source: {}\n",
			edits: []Edit{{Line: 1, Path: []string{"spec", "identity", "vmid"}, Value: 1013}},
			want:  "spec:\n  identity: { vmid: 1013 }\n  source: {}\n",
		},
		{
			name: "missing parent in a block mapping",
			src: `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-02
spec:
  placement:
    cluster: pve-main
  resources:
    cpu: { cores: 2 }
`,
			edits: []Edit{{Line: 1, Path: []string{"spec", "identity", "vmid"}, Value: 1013}},
			want: `apiVersion: nodr/v1alpha1
kind: VirtualMachine
metadata:
  name: web-02
spec:
  placement:
    cluster: pve-main
  identity:
    vmid: 1013
  resources:
    cpu: { cores: 2 }
`,
		},
		{
			name:  "missing parents in a flow mapping",
			src:   "spec: { placement: { cluster: pve-main }, resources: { cpu: { cores: 2 } } }\n",
			edits: []Edit{{Line: 1, Path: []string{"spec", "identity", "vmid"}, Value: 1013}},
			want:  "spec: { placement: { cluster: pve-main }, identity: { vmid: 1013 }, resources: { cpu: { cores: 2 } } }\n",
		},
		{
			name:  "several missing parents, with the indentation of the document",
			src:   "spec:\n    placement:\n        cluster: pve-main\n",
			edits: []Edit{{Line: 1, Path: []string{"spec", "one", "two", "three"}, Value: true}},
			want:  "spec:\n    placement:\n        cluster: pve-main\n    one:\n        two:\n            three: true\n",
		},
		{
			name: "sequence items",
			src: `nics:
- network: dmz
  ipv4:
    mode: auto
- network: lan
  ipv4:
    mode: auto
`,
			edits: []Edit{
				{Line: 1, Path: []string{"nics", "0", "ipv4", "address"}, Value: "10.0.20.22/24"},
				{Line: 1, Path: []string{"nics", "1", "ipv4", "address"}, Value: "10.0.10.10/24"},
			},
			want: `nics:
- network: dmz
  ipv4:
    mode: auto
    address: 10.0.20.22/24
- network: lan
  ipv4:
    mode: auto
    address: 10.0.10.10/24
`,
		},
		{
			name: "several documents",
			src: `apiVersion: nodr/v1alpha1
metadata:
  name: web-01
...
# The second guest.
---
apiVersion: nodr/v1alpha1
metadata:
  name: web-02
---
metadata: { name: web-03 }
`,
			edits: []Edit{
				{Line: 7, Path: []string{"metadata", "uid"}, Value: "B"},
				{Line: 1, Path: []string{"metadata", "uid"}, Value: "A"},
				{Line: 11, Path: []string{"metadata", "uid"}, Value: "C"},
			},
			want: `apiVersion: nodr/v1alpha1
metadata:
  name: web-01
  uid: A
...
# The second guest.
---
apiVersion: nodr/v1alpha1
metadata:
  name: web-02
  uid: B
---
metadata: { name: web-03, uid: C }
`,
		},
		{
			name:  "no final line break",
			src:   "metadata:\n  name: web-02\n# end",
			edits: []Edit{{Line: 1, Path: []string{"metadata", "uid"}, Value: "A"}},
			want:  "metadata:\n  name: web-02\n  uid: A\n# end",
		},
		{
			name:  "no final line break after the last key",
			src:   "metadata:\n  name: web-02",
			edits: []Edit{{Line: 1, Path: []string{"metadata", "uid"}, Value: "A"}},
			want:  "metadata:\n  name: web-02\n  uid: A",
		},
		{
			name:  "Windows line endings",
			src:   "metadata:\r\n  name: web-02\r\nspec:\r\n  placement:\r\n    cluster: pve-main\r\n",
			edits: []Edit{{Line: 1, Path: []string{"spec", "identity", "vmid"}, Value: 1013}},
			want:  "metadata:\r\n  name: web-02\r\nspec:\r\n  placement:\r\n    cluster: pve-main\r\n  identity:\r\n    vmid: 1013\r\n",
		},
		{
			name:  "characters that take several bytes",
			src:   "labels: { größe: groß }\nipv4: { mode: auto } # ÄÖÜ\n",
			edits: []Edit{{Line: 1, Path: []string{"labels", "ärger"}, Value: "ja"}},
			want:  "labels: { größe: groß, ärger: ja }\nipv4: { mode: auto } # ÄÖÜ\n",
		},
		{
			name:  "values that need quotes",
			src:   "a:\n  b: 1\nc: { d: 1 }\n",
			edits: []Edit{{Line: 1, Path: []string{"a", "node"}, Value: "1234"}, {Line: 1, Path: []string{"c", "e"}, Value: "x, y"}},
			want:  "a:\n  b: 1\n  node: \"1234\"\nc: { d: 1, e: \"x, y\" }\n",
		},
		{
			name: "after a block scalar with lines that look like comments",
			src: `a:
  script: |
    echo hi
    # not a comment
  # but this is one
b: 1
`,
			edits: []Edit{{Line: 1, Path: []string{"a", "x"}, Value: 1}},
			want: `a:
  script: |
    echo hi
    # not a comment
  x: 1
  # but this is one
b: 1
`,
		},
		{
			name:  "keys without an order go to the end",
			src:   "labels:\n  b: 1\n  a: 2\n",
			edits: []Edit{{Line: 1, Path: []string{"labels", "c"}, Value: 3}},
			want:  "labels:\n  b: 1\n  a: 2\n  c: 3\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Insert([]byte(tt.src), tt.edits, vmOrder)
			if err != nil {
				t.Fatalf("Insert: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("Insert =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// TestInsertOnlyAddsBytes checks that removing the inserted text restores
// the source byte for byte.
func TestInsertOnlyAddsBytes(t *testing.T) {
	src := "# head\nmetadata:\n  name: web-02 # name\n\n  labels: {app: 'x'}\n"
	got, err := Insert([]byte(src), []Edit{{Line: 2, Path: []string{"metadata", "uid"}, Value: "A"}}, vmOrder)
	if err != nil {
		t.Fatal(err)
	}
	if restored := strings.Replace(string(got), "  uid: A\n", "", 1); restored != src {
		t.Errorf("Insert changed more than the new line:\n%s", got)
	}
}

func TestInsertWithoutOrder(t *testing.T) {
	got, err := Insert([]byte("spec:\n  placement:\n    cluster: c\n  resources: {}\n"), []Edit{{Line: 1, Path: []string{"spec", "identity", "vmid"}, Value: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "spec:\n  placement:\n    cluster: c\n  resources: {}\n  identity:\n    vmid: 1\n"; string(got) != want {
		t.Errorf("Insert =\n%s\nwant\n%s", got, want)
	}
}

func TestInsertErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		edit Edit
		want string
	}{
		{"existing field", "a:\n  b: 1\n", Edit{Line: 1, Path: []string{"a", "b"}, Value: 2}, "line 1: a.b: already exists"},
		{"null field", "a:\n  b:\n", Edit{Line: 1, Path: []string{"a", "b"}, Value: 2}, "already exists"},
		{"scalar on the path", "a: 1\n", Edit{Line: 1, Path: []string{"a", "b"}, Value: 2}, "a is not a mapping"},
		{"missing item", "a: [x]\n", Edit{Line: 1, Path: []string{"a", "1", "b"}, Value: 2}, "a has no item 1"},
		{"alias on the path", "a: &x { b: 1 }\nc: *x\n", Edit{Line: 1, Path: []string{"c", "d"}, Value: 2}, "c is an alias"},
		{"unknown document", "a: 1\n", Edit{Line: 2, Path: []string{"b"}, Value: 2}, "no document starts at line 2"},
		{"empty path", "a: 1\n", Edit{Line: 1, Value: 2}, "the path is empty"},
		{"not a scalar", "a: 1\n", Edit{Line: 1, Path: []string{"b"}, Value: []int{1}}, "is not a string, a number or a boolean"},
		{"invalid YAML", "a: [\n", Edit{Line: 1, Path: []string{"b"}, Value: 1}, "invalid YAML"},
		// The parser counts these line breaks, so node positions would not
		// match the lines of the file.
		{"CR line breaks", "a:\r  b: 1\rc:\r  d: 2\r", Edit{Line: 1, Path: []string{"a", "z"}, Value: 1}, "line 1: unsupported line break U+000D; use LF or CR LF"},
		{"line separator", "a:\n  b: \"x\u2028y\"\nc:\n  d: 2\n", Edit{Line: 1, Path: []string{"a", "z"}, Value: 1}, "line 2: unsupported line break U+2028"},
		// Kept trailing line breaks belong to the block scalar, so the new
		// key cannot go after them without changing it.
		{"unsafe edit", "a:\n  s: |+\n    text\n\nb: 1\n", Edit{Line: 1, Path: []string{"a", "t"}, Value: 1}, "would change other content"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Insert([]byte(tt.src), []Edit{tt.edit}, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Insert = %q, %v; want an error with %q", got, err, tt.want)
			}
		})
	}
	_, err := Insert([]byte("a:\n  b: 1\n"), []Edit{{Line: 1, Path: []string{"a", "b"}, Value: 2}}, nil)
	if !errors.Is(err, ErrExists) {
		t.Errorf("err = %v, want ErrExists", err)
	}
}
func TestSet(t *testing.T) {
	tests := []struct {
		name  string
		src   string
		edits []Edit
		want  string
	}{
		{
			name: "replace plain scalar in block mapping, preserve comment",
			src: `spec:
  lifecycle:
    powerState: running # keep running
    protection: true
`,
			edits: []Edit{{Line: 1, Path: []string{"spec", "lifecycle", "powerState"}, Value: "stopped"}},
			want: `spec:
  lifecycle:
    powerState: stopped # keep running
    protection: true
`,
		},
		{
			name: "replace double-quoted scalar",
			src: `spec:
  lifecycle:
    powerState: "running" # double quoted
`,
			edits: []Edit{{Line: 1, Path: []string{"spec", "lifecycle", "powerState"}, Value: "stopped"}},
			want: `spec:
  lifecycle:
    powerState: stopped # double quoted
`,
		},
		{
			name: "replace single-quoted scalar",
			src: `spec:
  lifecycle:
    powerState: 'running'
`,
			edits: []Edit{{Line: 1, Path: []string{"spec", "lifecycle", "powerState"}, Value: "stopped"}},
			want: `spec:
  lifecycle:
    powerState: stopped
`,
		},
		{
			name:  "replace flow mapping scalar",
			src:   "ipv4: { mode: auto, dhcp: false }\n",
			edits: []Edit{{Line: 1, Path: []string{"ipv4", "mode"}, Value: "manual"}},
			want:  "ipv4: { mode: manual, dhcp: false }\n",
		},
		{
			name: "replace empty scalar in block mapping",
			src: `spec:
  lifecycle:
    powerState:
    protection: true
`,
			edits: []Edit{{Line: 1, Path: []string{"spec", "lifecycle", "powerState"}, Value: "running"}},
			want: `spec:
  lifecycle:
    powerState: running
    protection: true
`,
		},
		{
			name: "insert new key into existing mapping if not present",
			src: `spec:
  lifecycle:
    protection: true
`,
			edits: []Edit{{Line: 1, Path: []string{"spec", "lifecycle", "powerState"}, Value: "running"}},
			want: `spec:
  lifecycle:
    protection: true
    powerState: running
`,
		},
		{
			name: "insert mapping and key if not present",
			src: `spec:
  source:
    template: debian-12
`,
			edits: []Edit{{Line: 1, Path: []string{"spec", "lifecycle", "powerState"}, Value: "running"}},
			want: `spec:
  source:
    template: debian-12
  lifecycle:
    powerState: running
`,
		},
		{
			name: "replace scalar in sequence",
			src: `tags:
  - prod
  - web
`,
			edits: []Edit{{Line: 1, Path: []string{"tags", "0"}, Value: "stage"}},
			want: `tags:
  - stage
  - web
`,
		},
		{
			name: "multiple edits inserting and updating",
			src: `spec:
  lifecycle:
    powerState: running
`,
			edits: []Edit{
				{Line: 1, Path: []string{"spec", "lifecycle", "powerState"}, Value: "stopped"},
				{Line: 1, Path: []string{"spec", "lifecycle", "protection"}, Value: true},
			},
			want: `spec:
  lifecycle:
    powerState: stopped
    protection: true
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Set([]byte(tt.src), tt.edits, nil)
			if err != nil {
				t.Fatalf("Set failed: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("Set =\n%s\nwant:\n%s", string(got), tt.want)
			}
		})
	}
}

func TestSetErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		edit Edit
		want string
	}{
		{"target is not a scalar", "a:\n  b: { c: 1 }\n", Edit{Line: 1, Path: []string{"a", "b"}, Value: 2}, "a.b is not a scalar"},
		{"empty path", "a: 1\n", Edit{Line: 1, Value: 2}, "the path is empty"},
		{"unknown document", "a: 1\n", Edit{Line: 2, Path: []string{"b"}, Value: 2}, "no document starts at line 2"},
		{"alias on path", "a: &x { b: 1 }\nc: *x\n", Edit{Line: 1, Path: []string{"c", "d"}, Value: 2}, "c is an alias"},
		{"not a scalar value", "a: 1\n", Edit{Line: 1, Path: []string{"a"}, Value: []int{1}}, "is not a string, a number or a boolean"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Set([]byte(tt.src), []Edit{tt.edit}, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Set = %q, %v; want an error with %q", got, err, tt.want)
			}
		})
	}
}
