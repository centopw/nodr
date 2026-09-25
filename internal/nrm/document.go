// Package nrm implements the nodr Resource Model (NRM): the tool-neutral
// intent documents that describe desired state (design §3). It parses
// documents from YAML, checks their metadata, validates them against the
// schemas of registered kinds and checks references between them.
package nrm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Metadata is the metadata section that every document has.
type Metadata struct {
	Name        string            `json:"name"`
	UID         string            `json:"uid,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Ref identifies a resource by kind and name.
type Ref struct {
	Kind string
	Name string
}

// String formats the reference as Kind/name, for example
// VirtualMachine/web-01.
func (r Ref) String() string { return r.Kind + "/" + r.Name }

// Document is one intent document read from a workspace file.
type Document struct {
	APIVersion string
	Kind       string
	Metadata   Metadata

	// Object is the whole document as JSON-compatible values: map[string]any,
	// []any, string, json.Number, bool and nil. Schema validation and typed
	// decoding work on it.
	Object map[string]any

	// File is the slash-separated, workspace-relative path of the file that
	// holds the document. Line is the 1-based line where the document
	// starts.
	File string
	Line int

	node *yaml.Node
}

// Ref returns the reference that identifies the document.
func (d *Document) Ref() Ref { return Ref{Kind: d.Kind, Name: d.Metadata.Name} }

// DecodeSpec decodes the spec section into v, which must be a pointer.
// Fields that v does not declare are an error.
func (d *Document) DecodeSpec(v any) error {
	return decodeStrict(d.Object["spec"], v)
}

// LineOf returns the line of the field at path, where path segments are
// object keys or array indices. If the field does not exist, it returns the
// line of the closest enclosing field that does.
func (d *Document) LineOf(path ...string) int {
	n := d.node
	line := d.Line
	for _, seg := range path {
		next, keyLine := child(n, seg)
		if next == nil {
			break
		}
		n = next
		line = keyLine
	}
	return line
}

// FieldPath formats path segments for display, for example
// spec.nics[0].network. Segments that index arrays are shown in brackets.
func (d *Document) FieldPath(path ...string) string {
	var b strings.Builder
	var cur any = d.Object
	for _, seg := range path {
		switch v := cur.(type) {
		case []any:
			b.WriteString("[" + seg + "]")
			if i, err := strconv.Atoi(seg); err == nil && i >= 0 && i < len(v) {
				cur = v[i]
			} else {
				cur = nil
			}
		case map[string]any:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(seg)
			cur = v[seg]
		default:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(seg)
			cur = nil
		}
	}
	return b.String()
}

// child returns the value node for seg inside n, and the line to report
// for it: the key's line for mappings, the item's line for sequences.
func child(n *yaml.Node, seg string) (*yaml.Node, int) {
	if n == nil {
		return nil, 0
	}
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == seg {
				return n.Content[i+1], n.Content[i].Line
			}
		}
	case yaml.SequenceNode:
		if i, err := strconv.Atoi(seg); err == nil && i >= 0 && i < len(n.Content) {
			return n.Content[i], n.Content[i].Line
		}
	}
	return nil, 0
}

// decodeStrict converts a JSON-compatible value into v through JSON,
// rejecting unknown fields.
func decodeStrict(value, v any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}
