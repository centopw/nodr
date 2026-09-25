package hclmap

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

// ConflictError reports blocks that Put would have to remove but that hold
// content nodr does not manage, such as extensions or code-owned
// attributes. Put changes nothing when it returns a ConflictError.
type ConflictError struct {
	Paths []string
}

func (e *ConflictError) Error() string {
	return "cannot remove blocks that contain code-owned or extra content: " + strings.Join(e.Paths, ", ")
}

// Options adjust Put.
type Options struct {
	// Keep lists code paths that Put must leave untouched although they hold
	// literals: attributes such as disk[0].interface, or whole blocks such
	// as disk[2], which Put then neither changes nor removes. A lens uses it
	// for values it cannot map back into intent, which makes them
	// code-owned.
	Keep map[string]bool
}

// Put writes desired into the target block of src and returns the new
// source. It changes only synced attributes and blocks whose values differ
// from desired, so comments, extensions and code-owned attributes stay
// exactly as they are, and so does hand formatting: every change is an edit
// of the bytes it concerns. New attributes and blocks are inserted next to
// their siblings. If src was canonically formatted, the result is
// canonically formatted too, which can realign the "=" of neighboring
// attributes.
func Put[T any](src []byte, filename string, target Target, desired T) ([]byte, error) {
	return PutWith(src, filename, target, desired, Options{})
}

// PutWith is Put with options.
func PutWith[T any](src []byte, filename string, target Target, desired T, opts Options) ([]byte, error) {
	sc, err := schemaOf(reflect.TypeOf(desired))
	if err != nil {
		return nil, err
	}
	value := reflect.ValueOf(desired)

	// Pass 1: change and remove existing attributes in place, and check that
	// blocks that have to go hold nothing nodr does not manage.
	synBlock, err := parseTarget(src, filename, target)
	if err != nil {
		return nil, err
	}
	if metaArgument(synBlock.Body) != nil {
		return src, nil
	}
	p := planner{editor: editor{src: src}, keep: opts.Keep}
	if err := p.body(sc, value, synBlock.Body, ""); err != nil {
		return nil, err
	}
	if len(p.conflicts) > 0 {
		return nil, &ConflictError{Paths: p.conflicts}
	}
	out := p.apply()

	// Pass 2: remove blocks and insert attributes and blocks that do not
	// exist yet. Offsets come from the source as it is after pass 1.
	synBlock, err = parseTarget(out, filename, target)
	if err != nil {
		return nil, err
	}
	in := inserter{editor: editor{src: out}, keep: opts.Keep}
	if err := in.body(sc, value, synBlock, 1, ""); err != nil {
		return nil, err
	}
	out = in.apply()

	if isFormatted(src) {
		out = hclwrite.Format(out)
	}
	return out, nil
}

func parseTarget(src []byte, filename string, target Target) (*hclsyntax.Block, error) {
	file, diags := hclsyntax.ParseConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse %s: %w", filename, diags)
	}
	block := findBlock(file.Body.(*hclsyntax.Body), target)
	if block == nil {
		return nil, fmt.Errorf("%s in %s: %w", target, filename, ErrTargetNotFound)
	}
	return block, nil
}

func isFormatted(src []byte) bool {
	return bytes.Equal(hclwrite.Format(src), src)
}

// byteEdit replaces the bytes in [offset, end) with text.
type byteEdit struct {
	offset int
	end    int
	text   string
}

// editor collects byte-level edits of a source and applies them together.
type editor struct {
	src   []byte
	edits []byteEdit
}

func (ed *editor) replace(start, end int, text string) {
	ed.edits = append(ed.edits, byteEdit{offset: start, end: end, text: text})
}

func (ed *editor) add(offset int, text string) { ed.replace(offset, offset, text) }

func (ed *editor) delete(start, end int) { ed.replace(start, end, "") }

// apply returns the source with the edits applied in order of offset, and
// in the order they were added at the same offset. Text inserted inside
// bytes that another edit removes goes where those bytes were.
func (ed *editor) apply() []byte {
	if len(ed.edits) == 0 {
		return ed.src
	}
	sort.SliceStable(ed.edits, func(i, j int) bool { return ed.edits[i].offset < ed.edits[j].offset })
	var out []byte
	pos := 0
	for _, e := range ed.edits {
		if e.offset > pos {
			out = append(out, ed.src[pos:e.offset]...)
		}
		out = append(out, e.text...)
		pos = max(pos, e.end)
	}
	return append(out, ed.src[pos:]...)
}

// lineStart returns the offset of the start of the line that contains
// offset.
func (ed *editor) lineStart(offset int) int {
	return bytes.LastIndexByte(ed.src[:offset], '\n') + 1
}

// lineEnd returns the offset just after the end of the line that contains
// offset.
func (ed *editor) lineEnd(offset int) int {
	if i := bytes.IndexByte(ed.src[offset:], '\n'); i >= 0 {
		return offset + i + 1
	}
	return len(ed.src)
}

func (ed *editor) isBlank(start, end int) bool {
	return len(bytes.TrimSpace(ed.src[start:end])) == 0
}

// leadComments returns the start of the comment lines directly above the
// line that starts at start, or start if there are none.
func (ed *editor) leadComments(start int) int {
	for start > 0 {
		prev := ed.lineStart(start - 1)
		if !isComment(bytes.TrimSpace(ed.src[prev:start])) {
			break
		}
		start = prev
	}
	return start
}

func isComment(text []byte) bool {
	return bytes.HasPrefix(text, []byte("#")) || bytes.HasPrefix(text, []byte("//")) || bytes.HasPrefix(text, []byte("/*"))
}

// planner collects the in-place edits of pass 1.
type planner struct {
	editor
	keep      map[string]bool
	conflicts []string
}

func (p *planner) body(sc *structSchema, v reflect.Value, syn *hclsyntax.Body, prefix string) error {
	byType, dynamic := groupBlocks(syn)
	for _, f := range sc.fields {
		path := join(prefix, f.name)
		switch f.kind {
		case attrField:
			if p.keep[path] {
				continue
			}
			if err := p.attr(f, v.Field(f.index), syn.Attributes[f.name]); err != nil {
				return err
			}
		case blockField:
			if _, dyn := dynamic[f.name]; dyn || len(byType[f.name]) != 1 || p.keep[path] {
				continue
			}
			blk := byType[f.name][0]
			fv := v.Field(f.index)
			if !fv.IsNil() {
				if err := p.body(f.elem, fv.Elem(), blk.Body, path); err != nil {
					return err
				}
				continue
			}
			if f.optional {
				p.checkRemovable(f.elem, blk, path)
			}
		case blockListField:
			if _, dyn := dynamic[f.name]; dyn {
				continue
			}
			fv := v.Field(f.index)
			for i, b := range byType[f.name] {
				itemPath := indexed(path, i)
				if p.keep[itemPath] {
					continue
				}
				if i < fv.Len() {
					if err := p.body(f.elem, fv.Index(i), b.Body, itemPath); err != nil {
						return err
					}
					continue
				}
				p.checkRemovable(f.elem, b, itemPath)
			}
		}
	}
	return nil
}

func (p *planner) attr(f *field, v reflect.Value, attr *hclsyntax.Attribute) error {
	want, present, err := toCty(v, f)
	if err != nil {
		return err
	}
	if attr == nil {
		return nil // insertions happen in pass 2
	}
	cur, ok := literal(attr.Expr)
	if !ok || pinned(p.src, attr.SrcRange) {
		return nil // code-owned
	}
	if cur.IsNull() {
		if present {
			p.setValue(attr, want)
		}
		return nil
	}
	if _, err := convert.Convert(cur, f.ctyType); err != nil {
		return nil // an invalid literal is code-owned
	}
	switch {
	case !present:
		// An optional value field treats its zero value like an absent
		// attribute, so a literal zero can stay. For a pointer field, nil
		// and a pointer to the zero value differ, so the literal must go.
		if f.optional && (f.goType.Kind() == reflect.Pointer || !isZero(cur, f)) {
			p.removeAttr(attr)
		}
	case !equal(cur, want, f):
		p.setValue(attr, want)
	}
	return nil
}

// setValue replaces the expression of an attribute with a value.
func (p *planner) setValue(attr *hclsyntax.Attribute, val cty.Value) {
	rng := attr.Expr.Range()
	p.replace(rng.Start.Byte, rng.End.Byte, string(hclwrite.TokensForValue(val).Bytes()))
}

// removeAttr deletes an attribute together with the comment lines directly
// above it. An attribute that shares its line, as in a single-line block,
// loses only its own text.
func (p *planner) removeAttr(attr *hclsyntax.Attribute) {
	start, end := attr.SrcRange.Start.Byte, attr.SrcRange.End.Byte
	lineStart, lineEnd := p.lineStart(start), p.lineEnd(end)
	after := bytes.TrimSpace(p.src[end:lineEnd])
	if !p.isBlank(lineStart, start) || (len(after) > 0 && !isComment(after)) {
		p.delete(start, end)
		return
	}
	p.delete(p.leadComments(lineStart), lineEnd)
}

// checkRemovable records a conflict if a block that has to be removed holds
// content that nodr does not manage. Pass 2 removes the block.
func (p *planner) checkRemovable(sc *structSchema, syn *hclsyntax.Block, path string) {
	if !p.removable(sc, syn.Body, path) {
		p.conflicts = append(p.conflicts, path)
	}
}

// removable reports whether a block holds only synced content, so that
// removing it loses nothing the user wrote by hand.
func (p *planner) removable(sc *structSchema, body *hclsyntax.Body, path string) bool {
	for name, attr := range body.Attributes {
		f, ok := sc.byName[name]
		if !ok || f.kind != attrField || p.keep[join(path, name)] {
			return false
		}
		if _, lit := literal(attr.Expr); !lit || pinned(p.src, attr.SrcRange) {
			return false
		}
	}
	byType, _ := groupBlocks(body)
	for _, b := range body.Blocks {
		f, ok := sc.byName[b.Type]
		if !ok || f.kind == attrField {
			return false
		}
		childPath := join(path, b.Type)
		if f.kind == blockListField {
			for i, sibling := range byType[b.Type] {
				if sibling == b {
					childPath = indexed(childPath, i)
				}
			}
		}
		if p.keep[childPath] || !p.removable(f.elem, b.Body, childPath) {
			return false
		}
	}
	return true
}

// isZero reports whether val is the zero value of the field's type, which
// is what an absent optional attribute means.
func isZero(val cty.Value, f *field) bool {
	var zero cty.Value
	switch f.ctyType {
	case cty.String:
		zero = cty.StringVal("")
	case cty.Bool:
		zero = cty.False
	case cty.Number:
		zero = cty.Zero
	default:
		zero = cty.ListValEmpty(cty.String)
	}
	return equal(val, zero, f)
}

// inserter collects the byte-level edits of pass 2.
type inserter struct {
	editor
	keep map[string]bool
}

// removeBlock deletes a block together with the comment lines directly
// above it and one blank line next to it, so no gap is left behind.
func (in *inserter) removeBlock(b *hclsyntax.Block) {
	start := in.leadComments(in.lineStart(b.TypeRange.Start.Byte))
	end := in.lineEnd(b.CloseBraceRange.End.Byte - 1)
	switch {
	case start > 0 && in.isBlank(in.lineStart(start-1), start):
		start = in.lineStart(start - 1)
	case end < len(in.src) && in.isBlank(end, in.lineEnd(end)):
		end = in.lineEnd(end)
	}
	in.delete(start, end)
}

// body inserts what is missing from the body of blk, whose content is at
// the given indentation depth.
func (in *inserter) body(sc *structSchema, v reflect.Value, blk *hclsyntax.Block, depth int, prefix string) error {
	syn := blk.Body
	// A single-line block, such as agent { enabled = true }, holds at most
	// one attribute. Everything new goes to its end, and the block is
	// spread over several lines.
	var tail *strings.Builder
	if blk.OpenBraceRange.Start.Line == blk.CloseBraceRange.Start.Line {
		tail = &strings.Builder{}
	}

	var lines strings.Builder
	for _, f := range sc.fields {
		if f.kind != attrField || !f.optional || syn.Attributes[f.name] != nil || in.keep[join(prefix, f.name)] {
			continue
		}
		want, present, err := toCty(v.Field(f.index), f)
		if err != nil {
			return err
		}
		if present {
			fmt.Fprintf(&lines, "%s%s = %s\n", indent(depth), f.name, hclwrite.TokensForValue(want).Bytes())
		}
	}
	switch {
	case lines.Len() == 0:
	case tail != nil:
		tail.WriteString(lines.String())
	default:
		in.add(in.attrAnchor(syn, blk.OpenBraceRange), lines.String())
	}

	byType, dynamic := groupBlocks(syn)
	for _, f := range sc.fields {
		if f.kind == attrField {
			continue
		}
		if _, dyn := dynamic[f.name]; dyn {
			continue
		}
		fv := v.Field(f.index)
		blocks := byType[f.name]
		path := join(prefix, f.name)
		switch f.kind {
		case blockField:
			if in.keep[path] {
				continue
			}
			switch {
			case fv.IsNil():
				if f.optional && len(blocks) == 1 {
					in.removeBlock(blocks[0])
				}
			case len(blocks) == 1:
				if err := in.body(f.elem, fv.Elem(), blocks[0], depth+1, path); err != nil {
					return err
				}
			case len(blocks) == 0 && f.optional:
				if err := in.block(sc, f, fv.Elem(), blk, depth, tail); err != nil {
					return err
				}
			}
		case blockListField:
			for i := fv.Len(); i < len(blocks); i++ {
				if !in.keep[indexed(path, i)] {
					in.removeBlock(blocks[i])
				}
			}
			for i := range fv.Len() {
				if i < len(blocks) {
					if in.keep[indexed(path, i)] {
						continue
					}
					if err := in.body(f.elem, fv.Index(i), blocks[i], depth+1, indexed(path, i)); err != nil {
						return err
					}
					continue
				}
				if err := in.block(sc, f, fv.Index(i), blk, depth, tail); err != nil {
					return err
				}
			}
		}
	}
	if tail != nil && tail.Len() > 0 {
		in.spread(blk, depth, tail.String())
	}
	return nil
}

// spread rewrites a single-line block as a multi-line block that ends with
// text.
func (in *inserter) spread(blk *hclsyntax.Block, depth int, text string) {
	open, closing := blk.OpenBraceRange.End.Byte, blk.CloseBraceRange.Start.Byte
	var attr *hclsyntax.Attribute
	for _, a := range blk.Body.Attributes {
		attr = a
	}
	end := "\n" + text + indent(depth-1)
	if attr == nil {
		if rest := bytes.TrimSpace(in.src[open:closing]); len(rest) > 0 {
			end = "\n" + indent(depth) + string(rest) + end
		}
		in.replace(open, closing, end)
		return
	}
	in.replace(open, attr.SrcRange.Start.Byte, "\n"+indent(depth))
	if rest := bytes.TrimSpace(in.src[attr.SrcRange.End.Byte:closing]); len(rest) > 0 {
		end = " " + string(rest) + end
	}
	in.replace(attr.SrcRange.End.Byte, closing, end)
}

// block inserts a new nested block for field f after its siblings, or at
// the end of tail if the parent is a single-line block.
func (in *inserter) block(sc *structSchema, f *field, v reflect.Value, parent *hclsyntax.Block, depth int, tail *strings.Builder) error {
	text, err := renderNested(f, v, depth)
	if err != nil {
		return err
	}
	if tail != nil {
		if tail.Len() > 0 {
			tail.WriteString("\n")
		}
		tail.WriteString(text)
		return nil
	}
	offset, first := in.blockAnchor(sc, f, parent.Body, parent.OpenBraceRange)
	if !first {
		text = "\n" + text
	}
	in.add(offset, text)
	return nil
}

// attrAnchor returns where new attributes go: after the last attribute of
// the body, or at the start of the body.
func (in *inserter) attrAnchor(syn *hclsyntax.Body, open hcl.Range) int {
	last := -1
	for _, a := range syn.Attributes {
		if a.SrcRange.End.Byte > last {
			last = a.SrcRange.End.Byte
		}
	}
	if last >= 0 {
		return in.lineEnd(last)
	}
	return in.lineEnd(open.End.Byte)
}

// blockAnchor returns where a new block for field f goes: after the last
// block of the same type or of a type declared before it, else after the
// last attribute, else at the start of the body. first reports that
// nothing precedes the block in the body.
func (in *inserter) blockAnchor(sc *structSchema, f *field, syn *hclsyntax.Body, open hcl.Range) (offset int, first bool) {
	last := -1
	for _, b := range syn.Blocks {
		typ := b.Type
		if typ == "dynamic" && len(b.Labels) > 0 {
			typ = b.Labels[0]
		}
		if bf, ok := sc.byName[typ]; ok && bf.kind != attrField && bf.index <= f.index && b.CloseBraceRange.End.Byte > last {
			last = b.CloseBraceRange.End.Byte
		}
	}
	if last >= 0 {
		return in.lineEnd(last), false
	}
	if len(syn.Attributes) > 0 {
		return in.attrAnchor(syn, open), false
	}
	return in.lineEnd(open.End.Byte), true
}

func indent(depth int) string { return strings.Repeat("  ", depth) }

func indentLines(text string, depth int) string {
	lines := strings.SplitAfter(text, "\n")
	var b strings.Builder
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			b.WriteString(indent(depth))
		}
		b.WriteString(line)
	}
	return b.String()
}
