// Package yamledit adds fields to YAML documents with minimal edits: it
// inserts the bytes of each new field and leaves every other byte alone, so
// comments, key order, flow and block styles, quoting and the other
// documents of a file stay exactly as they are. nodr uses it to write
// allocated values into intent documents (design §3.9).
package yamledit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// ErrExists reports a field that the document already has. Insert only
// adds fields; it never changes a value.
var ErrExists = errors.New("already exists")

// Edit adds one field to one document of a file.
type Edit struct {
	// Line is the 1-based line where the root node of the document starts,
	// as nrm.Document.Line records it. It picks the document in a file
	// with several documents.
	Line int
	// Path holds the mapping keys and sequence indices of the field, for
	// example ["spec", "nics", "0", "ipv4", "address"]. Mappings on the
	// path that do not exist yet are created; sequence items must exist.
	Path []string
	// Value is the value of the field: a string, a number or a boolean.
	Value any
	// Order overrides the order passed to Insert for this edit.
	Order Order
}

// Order returns the keys of the mapping at path in the order they belong
// in, or nil if the mapping has no preferred order.
type Order func(path []string) []string

// Insert returns src with the fields of edits added, in order. A new key
// goes after the last key of its mapping that comes before it in order,
// or at the end of the mapping, in the style of the mapping: a new line
// with the indentation of its siblings in a block mapping, a new entry in
// a flow mapping. Mappings that Insert creates on the way take the style
// of their parent. order may be nil.
//
// Insert checks that every edit reads back as the old document with only
// the new field added, and returns an error for an edit it cannot make
// that way.
func Insert(src []byte, edits []Edit, order Order) ([]byte, error) {
	f, err := parse(src)
	if err != nil {
		return nil, err
	}
	// Edits move the documents that follow them, so documents are looked
	// up by their lines in src before anything changes.
	docs := make([]int, len(edits))
	for i, e := range edits {
		if docs[i] = f.document(e.Line); docs[i] < 0 {
			return nil, fmt.Errorf("no document starts at line %d", e.Line)
		}
	}
	for i, e := range edits {
		orderForEdit := order
		if e.Order != nil {
			orderForEdit = e.Order
		}
		if f, err = f.insert(docs[i], e.Path, e.Value, orderForEdit); err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", e.Line, name(e.Path), err)
		}
	}
	return f.src, nil
}

// Set returns src with the fields of edits added or replaced, in order.
// If a field already exists and is a scalar, its value is replaced while
// preserving surrounding formatting and comments. If a field does not exist,
// it is inserted just as by Insert.
func Set(src []byte, edits []Edit, order Order) ([]byte, error) {
	f, err := parse(src)
	if err != nil {
		return nil, err
	}
	docs := make([]int, len(edits))
	for i, e := range edits {
		if docs[i] = f.document(e.Line); docs[i] < 0 {
			return nil, fmt.Errorf("no document starts at line %d", e.Line)
		}
	}
	for i, e := range edits {
		orderForEdit := order
		if e.Order != nil {
			orderForEdit = e.Order
		}
		if f, err = f.set(docs[i], e.Path, e.Value, orderForEdit); err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", e.Line, name(e.Path), err)
		}
	}
	return f.src, nil
}

// file is a parsed YAML file.
type file struct {
	src []byte
	// lines holds the offset of the start of each line.
	lines []int
	// nl is the line ending of the file.
	nl   string
	docs []*yaml.Node
}

func parse(src []byte) (*file, error) {
	f := &file{src: src, lines: []int{0}, nl: "\n"}
	for i, c := range src {
		if c == '\n' {
			f.lines = append(f.lines, i+1)
		}
	}
	// The parser also breaks lines at a CR without LF and at NEL, LS and
	// PS. The positions of the nodes after such a break would not match
	// lines.
	for i, r := range string(src) {
		switch {
		case r == '\r' && bytes.HasPrefix(src[i+1:], []byte("\n")):
		case r == '\r', r == '\u0085', r == '\u2028', r == '\u2029':
			return nil, fmt.Errorf("line %d: unsupported line break %U; use LF or CR LF", bytes.Count(src[:i], []byte("\n"))+1, r)
		}
	}
	if bytes.Contains(src, []byte("\r\n")) {
		f.nl = "\r\n"
	}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	for {
		n := new(yaml.Node)
		err := dec.Decode(n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid YAML: %s", strings.TrimPrefix(err.Error(), "yaml: "))
		}
		f.docs = append(f.docs, n)
	}
	return f, nil
}

// document returns the index of the document whose root node starts at
// line, or -1.
func (f *file) document(line int) int {
	for i, d := range f.docs {
		if len(d.Content) > 0 && d.Content[0].Line == line {
			return i
		}
	}
	return -1
}

// step is one level of the path to a field: a collection, and the index in
// its Content of the key or item that the path continues with.
type step struct {
	node  *yaml.Node
	index int
}

func (f *file) insert(doc int, path []string, value any, order Order) (*file, error) {
	if len(path) == 0 {
		return nil, errors.New("the path is empty")
	}
	var steps []step
	n := f.docs[doc].Content[0]
	for i, seg := range path {
		switch n.Kind {
		case yaml.MappingNode:
			k := keyIndex(n, seg)
			if k >= 0 {
				steps = append(steps, step{node: n, index: k})
				n = n.Content[k+1]
				continue
			}
			var keyOrder []string
			if order != nil {
				keyOrder = order(path[:i])
			}
			out, err := f.add(doc, steps, n, path[i:], value, keyOrder)
			if err != nil {
				return nil, err
			}
			return f.verify(out, doc, path, value)
		case yaml.SequenceNode:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(n.Content) {
				return nil, fmt.Errorf("%s has no item %s", name(path[:i]), seg)
			}
			steps = append(steps, step{node: n, index: idx})
			n = n.Content[idx]
		case yaml.AliasNode:
			return nil, fmt.Errorf("%s is an alias; edit the node it refers to instead", name(path[:i]))
		default:
			return nil, fmt.Errorf("%s is not a mapping", name(path[:i]))
		}
	}
	return nil, ErrExists
}
func (f *file) set(doc int, path []string, value any, order Order) (*file, error) {
	if len(path) == 0 {
		return nil, errors.New("the path is empty")
	}
	var steps []step
	n := f.docs[doc].Content[0]
	for i, seg := range path {
		last := i == len(path)-1
		switch n.Kind {
		case yaml.MappingNode:
			k := keyIndex(n, seg)
			if k < 0 {
				var keyOrder []string
				if order != nil {
					keyOrder = order(path[:i])
				}
				out, err := f.add(doc, steps, n, path[i:], value, keyOrder)
				if err != nil {
					return nil, err
				}
				return f.verify(out, doc, path, value)
			}
			if !last {
				steps = append(steps, step{node: n, index: k})
				n = n.Content[k+1]
				continue
			}
			valNode := n.Content[k+1]
			if valNode.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("%s is not a scalar", name(path))
			}
			out, err := f.replaceScalar(doc, steps, n, k, valNode, value)
			if err != nil {
				return nil, err
			}
			return f.verify(out, doc, path, value)
		case yaml.SequenceNode:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(n.Content) {
				return nil, fmt.Errorf("%s has no item %s", name(path[:i]), seg)
			}
			if !last {
				steps = append(steps, step{node: n, index: idx})
				n = n.Content[idx]
				continue
			}
			valNode := n.Content[idx]
			if valNode.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("%s is not a scalar", name(path))
			}
			out, err := f.replaceScalar(doc, steps, n, idx, valNode, value)
			if err != nil {
				return nil, err
			}
			return f.verify(out, doc, path, value)
		case yaml.AliasNode:
			return nil, fmt.Errorf("%s is an alias; edit the node it refers to instead", name(path[:i]))
		default:
			return nil, fmt.Errorf("%s is not a mapping", name(path[:i]))
		}
	}
	return nil, errors.New("cannot set root")
}

func (f *file) replaceScalar(doc int, steps []step, parent *yaml.Node, k int, valNode *yaml.Node, value any) ([]byte, error) {
	flow := parent.Style&yaml.FlowStyle != 0
	text, err := scalar(value, flow)
	if err != nil {
		return nil, err
	}
	if flow {
		if valNode.Value == "" {
			off := f.offset(valNode.Line, valNode.Column)
			start := off
			if start < len(f.src) && f.src[start] == ' ' {
				start++
			} else {
				text = " " + text
			}
			return f.splice(start, start, text), nil
		}
		start := f.skipProperties(f.offset(valNode.Line, valNode.Column))
		end, err := f.flowEnd(valNode)
		if err != nil {
			return nil, err
		}
		return f.splice(start, end, text), nil
	}

	if valNode.Value == "" {
		if parent.Kind == yaml.MappingNode {
			kNode := parent.Content[k]
			kStart := f.offset(kNode.Line, kNode.Column)
			colon := bytes.IndexByte(f.src[kStart:], ':')
			if colon < 0 {
				return nil, f.unexpected(kNode.Line)
			}
			afterColon := kStart + colon + 1
			p := afterColon
			for p < len(f.src) && f.src[p] == ' ' {
				p++
			}
			if p < len(f.src) && f.src[p] == '#' {
				return f.splice(afterColon, afterColon, " "+text), nil
			}
			return f.splice(afterColon, p, " "+text), nil
		}
		off := f.offset(valNode.Line, valNode.Column)
		return f.splice(off, off, text), nil
	}

	start := f.skipProperties(f.offset(valNode.Line, valNode.Column))
	var end int
	switch {
	case valNode.Style&yaml.DoubleQuotedStyle != 0:
		end, err = f.quotedEnd(start, '"', valNode.Line)
		if err != nil {
			return nil, err
		}
	case valNode.Style&yaml.SingleQuotedStyle != 0:
		end, err = f.quotedEnd(start, '\'', valNode.Line)
		if err != nil {
			return nil, err
		}
	case valNode.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		indent := valNode.Column - 1
		end = f.backOver(f.next(doc, steps, parent, k), indent)
		for end > start && (f.src[end-1] == '\n' || f.src[end-1] == '\r') {
			end--
		}
	default:
		end = start
		for i := start; i < len(f.src); i++ {
			c := f.src[i]
			if c == '\n' || c == '\r' {
				break
			}
			if c == '#' && i > start && isSpace(f.src[i-1]) {
				break
			}
			if !isSpace(c) {
				end = i + 1
			}
		}
	}
	return f.splice(start, end, text), nil
}

func keyIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i]; k.Kind == yaml.ScalarNode && k.Value == key {
			return i
		}
	}
	return -1
}

// add inserts the first of keys into the mapping m, with the value at the
// end of the chain of new mappings that the other keys name.
func (f *file) add(doc int, steps []step, m *yaml.Node, keys []string, value any, order []string) ([]byte, error) {
	flow := m.Style&yaml.FlowStyle != 0
	text, err := scalar(value, flow)
	if err != nil {
		return nil, err
	}
	formatted := make([]string, len(keys))
	for i, k := range keys {
		if formatted[i], err = scalar(k, flow); err != nil {
			return nil, err
		}
	}
	if flow {
		return f.addFlow(m, keys[0], formatted, text, order)
	}
	return f.addBlock(doc, steps, m, keys[0], formatted, text, order)
}

// addFlow inserts an entry into a flow mapping, right after the value of
// the key it follows: { mode: auto } becomes { mode: auto, address: ... }.
func (f *file) addFlow(m *yaml.Node, key string, keys []string, value string, order []string) ([]byte, error) {
	entry := value
	for i := len(keys) - 1; i >= 0; i-- {
		if i < len(keys)-1 {
			entry = "{ " + entry + " }"
		}
		entry = keys[i] + ": " + entry
	}
	if len(m.Content) == 0 {
		open := f.skipProperties(f.offset(m.Line, m.Column))
		end := f.skipBlank(open + 1)
		if open >= len(f.src) || f.src[open] != '{' || end >= len(f.src) || f.src[end] != '}' {
			return nil, f.unexpected(m.Line)
		}
		if len(bytes.Trim(f.src[open+1:end], " \t")) == 0 {
			return f.splice(open+1, end, " "+entry+" "), nil
		}
		return f.splice(open+1, open+1, " "+entry+","), nil
	}
	end, err := f.flowEnd(m.Content[after(m, key, order)+1])
	if err != nil {
		return nil, err
	}
	return f.splice(end, end, ", "+entry), nil
}

// addBlock inserts new lines into a block mapping, right after the lines
// of the entry that the new key follows, with the indentation of the
// mapping's keys.
func (f *file) addBlock(doc int, steps []step, m *yaml.Node, key string, keys []string, value string, order []string) ([]byte, error) {
	indent := m.Content[0].Column - 1
	step := indentStep(f.docs[doc])
	var b strings.Builder
	for i, k := range keys {
		b.WriteString(strings.Repeat(" ", indent+i*step) + k + ":")
		if i == len(keys)-1 {
			b.WriteString(" " + value)
		}
		b.WriteString(f.nl)
	}
	text := b.String()
	at := f.backOver(f.next(doc, steps, m, after(m, key, order)), indent)
	if at == len(f.src) && at > 0 && f.src[at-1] != '\n' {
		text = f.nl + strings.TrimSuffix(text, f.nl)
	}
	return f.splice(at, at, text), nil
}

// after returns the index of the key in m that key goes after: the last
// key of m that comes before it in order, or else the last key of m.
func after(m *yaml.Node, key string, order []string) int {
	rank := make(map[string]int, len(order))
	for i, k := range order {
		rank[k] = i
	}
	best, bestRank := len(m.Content)-2, -1
	if r, ok := rank[key]; ok {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if kr, ok := rank[m.Content[i].Value]; ok && kr < r && kr > bestRank {
				best, bestRank = i, kr
			}
		}
	}
	return best
}

// next returns the offset of the line on which the content after the entry
// at index k of the block mapping m starts: the next key of m, the next
// key or item of an enclosing collection, or the next document. It
// returns the end of the file if nothing follows.
func (f *file) next(doc int, steps []step, m *yaml.Node, k int) int {
	width := 2
	if m.Kind == yaml.SequenceNode {
		width = 1
	}
	if k+width < len(m.Content) {
		return f.lines[m.Content[k+width].Line-1]
	}
	for i := len(steps) - 1; i >= 0; i-- {
		s := steps[i]
		width := 1
		if s.node.Kind == yaml.MappingNode {
			width = 2
		}
		if j := s.index + width; j < len(s.node.Content) {
			return f.lines[s.node.Content[j].Line-1]
		}
	}
	if doc+1 < len(f.docs) {
		return f.lines[f.docs[doc+1].Line-1]
	}
	return len(f.src)
}

// backOver moves off, the start of a line, up over the blank lines, the
// comment lines indented by at most indent and the document end markers
// above it. Such lines belong to what follows, so a new entry goes above
// them, directly after the content it follows. Comments that are indented
// more belong to the entry above.
func (f *file) backOver(off, indent int) int {
	for off > 0 {
		start := bytes.LastIndexByte(f.src[:off-1], '\n') + 1
		line := f.src[start:off]
		text := bytes.TrimSpace(line)
		switch {
		case len(text) == 0:
		case text[0] == '#' && len(line)-len(bytes.TrimLeft(line, " \t")) <= indent:
		case bytes.Equal(text, []byte("...")) || bytes.HasPrefix(text, []byte("... ")):
		default:
			return off
		}
		off = start
	}
	return off
}

// indentStep returns how many spaces the document indents a block mapping
// nested in another one by, or 2 if it has no such mappings.
func indentStep(n *yaml.Node) int {
	var find func(n *yaml.Node) int
	find = func(n *yaml.Node) int {
		if n.Kind == yaml.MappingNode && n.Style&yaml.FlowStyle == 0 {
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if v.Kind == yaml.MappingNode && v.Style&yaml.FlowStyle == 0 &&
					v.Content[0].Line > k.Line && v.Content[0].Column > k.Column {
					return v.Content[0].Column - k.Column
				}
			}
		}
		for _, c := range n.Content {
			if s := find(c); s > 0 {
				return s
			}
		}
		return 0
	}
	if s := find(n); s > 0 {
		return s
	}
	return 2
}

// flowEnd returns the offset just after the text of n, a node inside a
// flow collection.
func (f *file) flowEnd(n *yaml.Node) (int, error) {
	off := f.skipProperties(f.offset(n.Line, n.Column))
	switch n.Kind {
	case yaml.MappingNode, yaml.SequenceNode:
		open, closing := byte('{'), byte('}')
		if n.Kind == yaml.SequenceNode {
			open, closing = '[', ']'
		}
		if off >= len(f.src) || f.src[off] != open {
			// A single pair in a flow sequence, as in [a: 1], has no braces.
			if n.Kind == yaml.MappingNode && len(n.Content) == 2 {
				return f.flowEnd(n.Content[1])
			}
			return 0, f.unexpected(n.Line)
		}
		end := off + 1
		if len(n.Content) > 0 {
			var err error
			if end, err = f.flowEnd(n.Content[len(n.Content)-1]); err != nil {
				return 0, err
			}
		}
		end = f.skipBlank(end)
		if end < len(f.src) && f.src[end] == ',' {
			end = f.skipBlank(end + 1)
		}
		if end >= len(f.src) || f.src[end] != closing {
			return 0, f.unexpected(n.Line)
		}
		return end + 1, nil
	case yaml.ScalarNode:
		switch {
		case n.Style&yaml.DoubleQuotedStyle != 0:
			return f.quotedEnd(off, '"', n.Line)
		case n.Style&yaml.SingleQuotedStyle != 0:
			return f.quotedEnd(off, '\'', n.Line)
		}
	}
	return f.plainEnd(off), nil
}

// plainEnd returns the offset just after a plain scalar or an alias that
// starts at off in a flow collection. Such a scalar ends before a flow
// indicator or a comment, and can span lines.
func (f *file) plainEnd(off int) int {
	end := off
	for i := off; i < len(f.src); i++ {
		switch c := f.src[i]; {
		case strings.IndexByte(",[]{}", c) >= 0:
			return end
		case c == '#' && i > off && isSpace(f.src[i-1]):
			return end
		case !isSpace(c):
			end = i + 1
		}
	}
	return end
}

// quotedEnd returns the offset just after the quoted scalar that starts at
// off with the quote q.
func (f *file) quotedEnd(off int, q byte, line int) (int, error) {
	if off >= len(f.src) || f.src[off] != q {
		return 0, f.unexpected(line)
	}
	for i := off + 1; i < len(f.src); i++ {
		switch {
		case q == '"' && f.src[i] == '\\':
			i++
		case f.src[i] == q && q == '\'' && i+1 < len(f.src) && f.src[i+1] == '\'':
			i++
		case f.src[i] == q:
			return i + 1, nil
		}
	}
	return 0, f.unexpected(line)
}

// skipProperties returns the offset after the anchor and tag in front of a
// node that starts at off.
func (f *file) skipProperties(off int) int {
	for off < len(f.src) && (f.src[off] == '&' || f.src[off] == '!') {
		for off < len(f.src) && !isSpace(f.src[off]) && strings.IndexByte(",[]{}", f.src[off]) < 0 {
			off++
		}
		off = f.skipBlank(off)
	}
	return off
}

// skipBlank returns the offset of the first character at or after off that
// is not white space, a line break or part of a comment.
func (f *file) skipBlank(off int) int {
	for off < len(f.src) {
		switch c := f.src[off]; {
		case isSpace(c):
			off++
		case c == '#':
			for off < len(f.src) && f.src[off] != '\n' {
				off++
			}
		default:
			return off
		}
	}
	return off
}

// bom is the byte order mark of UTF-8, which the YAML parser skips.
var bom = []byte{0xEF, 0xBB, 0xBF}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

// offset converts a node position to a byte offset. Columns count
// characters, not bytes.
func (f *file) offset(line, column int) int {
	off := f.lines[line-1]
	if line == 1 && bytes.HasPrefix(f.src, bom) {
		off += len(bom)
	}
	for c := 1; c < column && off < len(f.src); c++ {
		_, size := utf8.DecodeRune(f.src[off:])
		off += size
	}
	return off
}

func (f *file) splice(start, end int, text string) []byte {
	out := make([]byte, 0, len(f.src)+len(text))
	out = append(out, f.src[:start]...)
	out = append(out, text...)
	return append(out, f.src[end:]...)
}

func (f *file) unexpected(line int) error {
	return fmt.Errorf("unexpected syntax in the flow collection at line %d", line)
}

// verify checks that out reads back as the source of f with the field at
// path set to value and nothing else changed, and returns out parsed.
func (f *file) verify(out []byte, doc int, path []string, value any) (*file, error) {
	want, err := decodeAll(f.src)
	if err != nil {
		return nil, err
	}
	got, err := decodeAll(out)
	if err != nil {
		return nil, fmt.Errorf("the edit would break the file: %w", err)
	}
	raw, err := yaml.Marshal(value)
	if err != nil {
		return nil, err
	}
	var v any
	if err := yaml.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	if !set(want[doc], path, v) || !reflect.DeepEqual(want, got) {
		return nil, errors.New("inserting the field would change other content; add it by hand")
	}
	return parse(out)
}

func decodeAll(src []byte) ([]any, error) {
	var docs []any
	dec := yaml.NewDecoder(bytes.NewReader(src))
	for {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, v)
	}
}

// set sets the field at path of a decoded document to v, creating the
// mappings on the way, and reports whether it could.
func set(doc any, path []string, v any) bool {
	cur := doc
	for i, seg := range path {
		last := i == len(path)-1
		switch c := cur.(type) {
		case map[string]any:
			if last {
				c[seg] = v
				return true
			}
			if _, ok := c[seg]; !ok {
				c[seg] = map[string]any{}
			}
			cur = c[seg]
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(c) {
				return false
			}
			if last {
				c[idx] = v
				return true
			}
			cur = c[idx]
		default:
			return false
		}
	}
	return false
}

// scalar formats v as a YAML scalar on one line. In a flow collection,
// strings with flow indicators are quoted too.
func scalar(v any, flow bool) (string, error) {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return "", err
	}
	if n.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("%v is not a string, a number or a boolean", v)
	}
	if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 || n.Style == 0 && flow && strings.ContainsAny(n.Value, ",[]{}") {
		n.Style = yaml.DoubleQuotedStyle
	}
	out, err := yaml.Marshal(&n)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// name formats a path for messages.
func name(path []string) string {
	if len(path) == 0 {
		return "the document"
	}
	return strings.Join(path, ".")
}
