package hclmap

import (
	"bytes"
	"fmt"
	"reflect"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/gocty"
)

// PinComment is the marker that pins a literal as code-owned when it
// appears in a comment on the same line (design §4.4).
const PinComment = "nodr:keep"

// toCty converts a Go field value to its cty value. present is false when
// the value means "not set": a nil pointer, or the zero value of an optional
// field.
func toCty(v reflect.Value, f *field) (cty.Value, bool, error) {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return cty.NilVal, false, nil
		}
		v = v.Elem()
	} else if f.optional && v.IsZero() {
		return cty.NilVal, false, nil
	}
	if f.ctyType.IsListType() {
		if v.Len() == 0 {
			if f.optional {
				return cty.NilVal, false, nil
			}
			return cty.ListValEmpty(cty.String), true, nil
		}
	}
	val, err := gocty.ToCtyValue(v.Interface(), f.ctyType)
	if err != nil {
		return cty.NilVal, false, fmt.Errorf("%s: %w", f.name, err)
	}
	return val, true, nil
}

// fromCty converts a literal from code into a Go value of the field's type.
func fromCty(val cty.Value, f *field) (reflect.Value, error) {
	converted, err := convert.Convert(val, f.ctyType)
	if err != nil {
		return reflect.Value{}, err
	}
	target := f.goType
	isPtr := target.Kind() == reflect.Pointer
	if isPtr {
		target = target.Elem()
	}
	out := reflect.New(target)
	if err := gocty.FromCtyValue(converted, out.Interface()); err != nil {
		return reflect.Value{}, err
	}
	if isPtr {
		return out, nil
	}
	return out.Elem(), nil
}

// equal compares two values of a field, treating lists tagged "set" as
// unordered.
func equal(a, b cty.Value, f *field) bool {
	ty := f.ctyType
	if f.set {
		ty = cty.Set(ty.ElementType())
	}
	ca, errA := convert.Convert(a, ty)
	cb, errB := convert.Convert(b, ty)
	if errA != nil || errB != nil {
		return false
	}
	return ca.RawEquals(cb)
}

// literal returns the value of an expression that is a plain literal: a
// number, bool, null or string without interpolation, or a list or object
// of such literals. Anything else, including variables, function calls,
// conditionals and templates, is not a literal.
func literal(e hclsyntax.Expression) (cty.Value, bool) {
	if !isLiteral(e) {
		return cty.NilVal, false
	}
	v, diags := e.Value(nil)
	if diags.HasErrors() || !v.IsWhollyKnown() {
		return cty.NilVal, false
	}
	return v, true
}

func isLiteral(e hclsyntax.Expression) bool {
	switch e := e.(type) {
	case *hclsyntax.LiteralValueExpr:
		return true
	case *hclsyntax.TemplateExpr:
		for _, part := range e.Parts {
			if _, ok := part.(*hclsyntax.LiteralValueExpr); !ok {
				return false
			}
		}
		return true
	case *hclsyntax.TupleConsExpr:
		for _, x := range e.Exprs {
			if !isLiteral(x) {
				return false
			}
		}
		return true
	case *hclsyntax.ObjectConsExpr:
		for _, item := range e.Items {
			if !isLiteralKey(item.KeyExpr) || !isLiteral(item.ValueExpr) {
				return false
			}
		}
		return true
	case *hclsyntax.UnaryOpExpr:
		lit, ok := e.Val.(*hclsyntax.LiteralValueExpr)
		return ok && e.Op == hclsyntax.OpNegate && lit.Val.Type() == cty.Number
	case *hclsyntax.ParenthesesExpr:
		return isLiteral(e.Expression)
	default:
		return false
	}
}

func isLiteralKey(e hclsyntax.Expression) bool {
	key, ok := e.(*hclsyntax.ObjectConsKeyExpr)
	if !ok {
		return isLiteral(e)
	}
	if key.ForceNonLiteral {
		return false
	}
	if trav, ok := key.Wrapped.(*hclsyntax.ScopeTraversalExpr); ok {
		return len(trav.Traversal) == 1
	}
	return isLiteral(key.Wrapped)
}

// pinned reports whether the rest of the line after an attribute carries a
// comment with the pin marker.
func pinned(src []byte, rng hcl.Range) bool {
	if rng.End.Byte > len(src) {
		return false
	}
	rest := src[rng.End.Byte:]
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[:i]
	}
	return bytes.Contains(rest, []byte(PinComment))
}

// deepCopy returns a copy of v that shares no pointers, slices or maps
// with it.
func deepCopy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(deepCopy(v.Elem()))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(deepCopy(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), deepCopy(iter.Value()))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		for i := range v.NumField() {
			if out.Field(i).CanSet() {
				out.Field(i).Set(deepCopy(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}
