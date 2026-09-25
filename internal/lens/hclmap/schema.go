// Package hclmap maps Go structs to HCL blocks and back while preserving
// everything the structs do not describe: comments, formatting, ordering,
// extra attributes and blocks, and attributes set by expressions.
//
// A struct describes one HCL block. Its fields carry `hcl` tags:
//
//	Name   string   `hcl:"name"`              // attribute
//	Tags   []string `hcl:"tags,set"`          // attribute, compared as a set
//	Bios   string   `hcl:"bios,optional"`     // absent means the zero value
//	CPU    *CPU     `hcl:"cpu,block"`         // single nested block
//	Agent  *Agent   `hcl:"agent,block,optional"`
//	Disks  []Disk   `hcl:"disk,block"`        // repeated nested blocks
//
// An attribute or single block without "optional" is always written when
// rendering. If the code leaves it out, the engine's default applies, which
// nodr cannot know, so the field is reported as code-owned and never
// re-added. Repeated blocks are matched to slice elements by position.
//
// The package implements the lens functions of design §4.5 for HCL: Lift
// reads a block, Put writes a struct into an existing block with minimal
// edits, and Render creates a new block.
package hclmap

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/zclconf/go-cty/cty"
)

type fieldKind int

const (
	attrField fieldKind = iota
	blockField
	blockListField
)

type field struct {
	name     string
	index    int
	kind     fieldKind
	optional bool
	set      bool
	goType   reflect.Type
	ctyType  cty.Type
	elem     *structSchema
}

type structSchema struct {
	typ    reflect.Type
	fields []*field
	byName map[string]*field
}

var schemaCache sync.Map // reflect.Type -> *structSchema

func schemaOf(t reflect.Type) (*structSchema, error) {
	if cached, ok := schemaCache.Load(t); ok {
		return cached.(*structSchema), nil
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("hclmap: %s is not a struct", t)
	}
	s := &structSchema{typ: t, byName: map[string]*field{}}
	for i := range t.NumField() {
		sf := t.Field(i)
		tag, ok := sf.Tag.Lookup("hcl")
		if !ok || !sf.IsExported() {
			continue
		}
		parts := strings.Split(tag, ",")
		f := &field{name: parts[0], index: i, goType: sf.Type}
		if f.name == "" {
			return nil, fmt.Errorf("hclmap: %s.%s: empty name in hcl tag", t, sf.Name)
		}
		for _, opt := range parts[1:] {
			switch opt {
			case "block":
				f.kind = blockField
			case "optional":
				f.optional = true
			case "set":
				f.set = true
			default:
				return nil, fmt.Errorf("hclmap: %s.%s: unknown hcl tag option %q", t, sf.Name, opt)
			}
		}
		if err := f.resolveType(t, sf); err != nil {
			return nil, err
		}
		if _, dup := s.byName[f.name]; dup {
			return nil, fmt.Errorf("hclmap: %s: HCL name %q is used twice", t, f.name)
		}
		s.fields = append(s.fields, f)
		s.byName[f.name] = f
	}
	schemaCache.Store(t, s)
	return s, nil
}

func (f *field) resolveType(parent reflect.Type, sf reflect.StructField) error {
	where := parent.String() + "." + sf.Name
	if f.kind == blockField {
		switch {
		case sf.Type.Kind() == reflect.Pointer && sf.Type.Elem().Kind() == reflect.Struct:
			elem, err := schemaOf(sf.Type.Elem())
			if err != nil {
				return err
			}
			f.elem = elem
		case sf.Type.Kind() == reflect.Slice && sf.Type.Elem().Kind() == reflect.Struct:
			elem, err := schemaOf(sf.Type.Elem())
			if err != nil {
				return err
			}
			f.kind = blockListField
			f.elem = elem
			f.optional = true
		default:
			return fmt.Errorf("hclmap: %s: a block must be a pointer to a struct or a slice of structs", where)
		}
		if f.set {
			return fmt.Errorf("hclmap: %s: blocks cannot be compared as sets", where)
		}
		return nil
	}
	base := sf.Type
	if base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	switch base.Kind() {
	case reflect.String:
		f.ctyType = cty.String
	case reflect.Bool:
		f.ctyType = cty.Bool
	case reflect.Int, reflect.Int64, reflect.Int32:
		f.ctyType = cty.Number
	case reflect.Slice:
		if base.Elem().Kind() != reflect.String {
			return fmt.Errorf("hclmap: %s: only lists of strings are supported", where)
		}
		f.ctyType = cty.List(cty.String)
	default:
		return fmt.Errorf("hclmap: %s: unsupported type %s", where, sf.Type)
	}
	if f.set && !f.ctyType.IsListType() {
		return fmt.Errorf("hclmap: %s: only lists can be compared as sets", where)
	}
	return nil
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func indexed(path string, i int) string {
	return fmt.Sprintf("%s[%d]", path, i)
}
