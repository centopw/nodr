package sync

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
)

// splitIntentPath splits an intent path such as "spec.disks[1].size" into
// yamledit segments: ["spec", "disks", "1", "size"]. Each dotted component
// may have a trailing [N] index, which becomes its own segment.
func splitIntentPath(path string) []string {
	var out []string
	for _, part := range strings.Split(path, ".") {
		name, rest := part, ""
		if i := strings.IndexByte(part, '['); i >= 0 {
			name, rest = part[:i], part[i:]
		}
		if name != "" {
			out = append(out, name)
		}
		for len(rest) > 0 {
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				break
			}
			out = append(out, rest[1:end])
			rest = rest[end+1:]
		}
	}
	return out
}

// vmDocument has the layout of a VirtualMachine document: metadata and spec
// keys in schema order, so that keyOrder gives new keys a place next to
// their neighbors. It mirrors internal/admission's unexported vmDocument;
// duplicated here because admission does not export it.
type vmDocument struct {
	APIVersion string                      `json:"apiVersion"`
	Kind       string                      `json:"kind"`
	Metadata   nrm.Metadata                `json:"metadata"`
	Spec       v1alpha1.VirtualMachineSpec `json:"spec"`
}

// keyOrder returns the keys of the mapping at path in a VirtualMachine
// document, in schema order. It is a yamledit.Order for VM intent files.
func keyOrder(path []string) []string {
	return keyOrderOf(reflect.TypeOf(vmDocument{}), path)
}

// keyOrderOf returns the keys of the mapping at path in the document type
// t, in schema order. Mappings without fixed keys, such as sequence
// elements, have no order.
func keyOrderOf(t reflect.Type, path []string) []string {
	for _, seg := range path {
		switch t = indirect(t); t.Kind() {
		case reflect.Slice:
			t = t.Elem()
		case reflect.Struct:
			f, ok := fieldByKey(t, seg)
			if !ok {
				return nil
			}
			t = f.Type
		default:
			return nil
		}
	}
	if t = indirect(t); t.Kind() != reflect.Struct {
		return nil
	}
	var keys []string
	for i := range t.NumField() {
		if k := jsonKey(t.Field(i)); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

func indirect(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func fieldByKey(t reflect.Type, key string) (reflect.StructField, bool) {
	for i := range t.NumField() {
		if f := t.Field(i); jsonKey(f) == key {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

func jsonKey(f reflect.StructField) string {
	k, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if k == "-" || !f.IsExported() {
		return ""
	}
	return k
}

// parseIndex parses a yamledit sequence-index segment. It is used by the
// getter to turn a segment such as "1" back into an int.
func parseIndex(seg string) (int, bool) {
	n, err := strconv.Atoi(seg)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
