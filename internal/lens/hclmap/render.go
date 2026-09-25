package hclmap

import (
	"reflect"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// Render returns the source of a new top-level block for desired, in
// canonical format. Each line of comment, if any, is written above the
// block as a # comment.
func Render[T any](target Target, comment string, desired T) ([]byte, error) {
	sc, err := schemaOf(reflect.TypeOf(desired))
	if err != nil {
		return nil, err
	}
	file := hclwrite.NewEmptyFile()
	if comment != "" {
		var toks hclwrite.Tokens
		for _, line := range strings.Split(strings.TrimRight(comment, "\n"), "\n") {
			toks = append(toks, &hclwrite.Token{Type: hclsyntax.TokenComment, Bytes: []byte("# " + line + "\n")})
		}
		file.Body().AppendUnstructuredTokens(toks)
	}
	block := file.Body().AppendNewBlock(target.Type, target.Labels)
	if err := renderBody(sc, reflect.ValueOf(desired), block.Body()); err != nil {
		return nil, err
	}
	return hclwrite.Format(file.Bytes()), nil
}

// renderNested renders a nested block for field f, indented for insertion
// into a body at the given depth.
func renderNested(f *field, v reflect.Value, depth int) (string, error) {
	file := hclwrite.NewEmptyFile()
	block := file.Body().AppendNewBlock(f.name, nil)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if err := renderBody(f.elem, v, block.Body()); err != nil {
		return "", err
	}
	return indentLines(string(hclwrite.Format(file.Bytes())), depth), nil
}

// renderBody writes every field of v into body: attributes first, then
// nested blocks separated by blank lines.
func renderBody(sc *structSchema, v reflect.Value, body *hclwrite.Body) error {
	hasContent := false
	for _, f := range sc.fields {
		if f.kind != attrField {
			continue
		}
		val, present, err := toCty(v.Field(f.index), f)
		if err != nil {
			return err
		}
		if present {
			body.SetAttributeValue(f.name, val)
			hasContent = true
		}
	}
	appendBlock := func(f *field, item reflect.Value) error {
		if hasContent {
			body.AppendNewline()
		}
		hasContent = true
		return renderBody(f.elem, item, body.AppendNewBlock(f.name, nil).Body())
	}
	for _, f := range sc.fields {
		fv := v.Field(f.index)
		switch f.kind {
		case blockField:
			if !fv.IsNil() {
				if err := appendBlock(f, fv.Elem()); err != nil {
					return err
				}
			}
		case blockListField:
			for i := range fv.Len() {
				if err := appendBlock(f, fv.Index(i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Changed returns the code paths whose values differ between a and b:
// attributes, the presence of single blocks and the number of repeated
// blocks. It returns nil if T is not a struct with hcl tags.
func Changed[T any](a, b T) []string {
	sc, err := schemaOf(reflect.TypeOf(a))
	if err != nil {
		return nil
	}
	var out []string
	changed(sc, reflect.ValueOf(a), reflect.ValueOf(b), "", &out)
	return out
}

func changed(sc *structSchema, a, b reflect.Value, prefix string, out *[]string) {
	for _, f := range sc.fields {
		path := join(prefix, f.name)
		fa, fb := a.Field(f.index), b.Field(f.index)
		switch f.kind {
		case attrField:
			va, pa, errA := toCty(fa, f)
			vb, pb, errB := toCty(fb, f)
			if errA != nil || errB != nil || pa != pb || (pa && !equal(va, vb, f)) {
				*out = append(*out, path)
			}
		case blockField:
			switch {
			case fa.IsNil() != fb.IsNil():
				*out = append(*out, path)
			case !fa.IsNil():
				changed(f.elem, fa.Elem(), fb.Elem(), path, out)
			}
		case blockListField:
			if fa.Len() != fb.Len() {
				*out = append(*out, path)
			}
			for i := range min(fa.Len(), fb.Len()) {
				changed(f.elem, fa.Index(i), fb.Index(i), indexed(path, i), out)
			}
		}
	}
}
