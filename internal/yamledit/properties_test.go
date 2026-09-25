package yamledit

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// TestInsertProperties generates documents that mix block and flow styles,
// comments, sequences and block scalars, and checks that Insert can add a
// field to any of their mappings, directly or below new mappings, by
// inserting bytes only. Insert itself checks that the result reads back
// with exactly the new field added.
func TestInsertProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g := &docGen{t: t, step: rapid.IntRange(2, 4).Draw(t, "step")}
		docs := rapid.IntRange(1, 3).Draw(t, "documents")
		var lines []int
		for i := range docs {
			if i > 0 {
				g.write(rapid.SampledFrom([]string{"---\n", "...\n---\n", "# next\n---\n"}).Draw(t, "separator"))
			}
			if rapid.Bool().Draw(t, "head comment") {
				g.write("# head\n")
			}
			g.doc = i
			lines = append(lines, g.blockMapping(nil, 0, 0, false))
		}
		src := g.b.String()

		target := rapid.SampledFrom(g.maps).Draw(t, "target")
		path := append(slices.Clone(target.path), "new")
		for i := range rapid.IntRange(0, 2).Draw(t, "new levels") {
			path = append(path, fmt.Sprintf("x%d", i))
		}
		var order Order
		if rapid.Bool().Draw(t, "ordered") {
			keys := rapid.Permutation(target.keys).Draw(t, "keys")
			at := rapid.IntRange(0, len(keys)).Draw(t, "position")
			keys = slices.Insert(keys, at, "new")
			order = func(p []string) []string {
				if slices.Equal(p, target.path) {
					return keys
				}
				return nil
			}
		}
		value := rapid.SampledFrom([]any{7, "a, b", "plain", true, "1234"}).Draw(t, "value")

		out, err := Insert([]byte(src), []Edit{{Line: lines[target.doc], Path: path, Value: value}}, order)
		if err != nil {
			t.Fatalf("Insert: %v\nsource:\n%s", err, src)
		}
		if !isInsertion(src, string(out)) {
			t.Fatalf("Insert changed existing bytes:\n%s\nsource:\n%s", out, src)
		}
	})
}

// isInsertion reports whether out is src with one run of bytes inserted.
func isInsertion(src, out string) bool {
	if len(out) <= len(src) {
		return false
	}
	p := 0
	for p < len(src) && src[p] == out[p] {
		p++
	}
	return strings.HasSuffix(out, src[p:])
}

// genMap is a mapping of a generated document.
type genMap struct {
	doc  int
	path []string
	keys []string
}

// docGen writes random documents and records their mappings.
type docGen struct {
	t    *rapid.T
	b    strings.Builder
	step int
	doc  int
	maps []genMap
}

func (g *docGen) write(s string) { g.b.WriteString(s) }

// line returns the line that the next write starts on.
func (g *docGen) line() int { return strings.Count(g.b.String(), "\n") + 1 }

func (g *docGen) record(path []string, keys []string) {
	g.maps = append(g.maps, genMap{doc: g.doc, path: slices.Clone(path), keys: keys})
}

func (g *docGen) keys(prefix string, least int) []string {
	n := rapid.IntRange(least, 3).Draw(g.t, "entries")
	keys := make([]string, n)
	for i := range keys {
		keys[i] = prefix + strconv.Itoa(i)
	}
	return keys
}

// blockMapping writes a block mapping whose keys are indented by indent,
// and returns the line of its first key. After a sequence dash, the first
// key continues the current line.
func (g *docGen) blockMapping(path []string, indent, depth int, afterDash bool) int {
	keys := g.keys("k", 1)
	g.record(path, keys)
	pad := strings.Repeat(" ", indent)
	first := 0
	for i, k := range keys {
		if i > 0 || !afterDash {
			if rapid.Bool().Draw(g.t, "blank line") {
				g.write("\n")
			}
			if rapid.Bool().Draw(g.t, "comment") {
				g.write(pad + "# about " + k + "\n")
			}
			g.write(pad)
		}
		if i == 0 {
			first = g.line()
		}
		g.write(k + ":")
		g.blockValue(append(path, k), indent, depth)
	}
	return first
}

func (g *docGen) blockValue(path []string, indent, depth int) {
	kind := rapid.IntRange(0, 5).Draw(g.t, "kind")
	if depth >= 3 {
		kind = 0
	}
	inner := indent + g.step
	switch kind {
	case 0:
		g.write(" " + g.scalar() + g.lineComment() + "\n")
	case 1:
		g.write(g.lineComment() + "\n")
		g.blockMapping(path, inner, depth+1, false)
		if rapid.Bool().Draw(g.t, "trailing comment") {
			g.write(strings.Repeat(" ", inner) + "# end of " + path[len(path)-1] + "\n")
		}
	case 2:
		g.write(" ")
		g.flowMapping(path, inner, depth+1)
		g.write(g.lineComment() + "\n")
	case 3:
		g.write("\n")
		dash := rapid.SampledFrom([]int{indent, inner}).Draw(g.t, "dash indent")
		for i := range rapid.IntRange(1, 2).Draw(g.t, "items") {
			g.write(strings.Repeat(" ", dash) + "- ")
			g.blockMapping(append(path, strconv.Itoa(i)), dash+2, depth+1, true)
		}
	case 4:
		pad := strings.Repeat(" ", inner)
		g.write(" |\n" + pad + "text\n" + pad + "# not a comment\n")
	case 5:
		g.write(" [")
		for i := range rapid.IntRange(1, 2).Draw(g.t, "items") {
			if i > 0 {
				g.write(", ")
			}
			g.flowMapping(append(path, strconv.Itoa(i)), inner, depth+1)
		}
		g.write("]" + g.lineComment() + "\n")
	}
}

// flowMapping writes a flow mapping. Its lines after the first are
// indented by indent.
func (g *docGen) flowMapping(path []string, indent, depth int) {
	keys := g.keys("f", 0)
	g.record(path, keys)
	if len(keys) == 0 {
		g.write("{}")
		return
	}
	multiline := rapid.Bool().Draw(g.t, "multi-line")
	sep := ", "
	if multiline {
		sep = ",\n" + strings.Repeat(" ", indent)
	}
	g.write("{ ")
	for i, k := range keys {
		if i > 0 {
			g.write(sep)
		}
		g.write(k + ": ")
		if depth < 3 && rapid.Bool().Draw(g.t, "nested") {
			g.flowMapping(append(path, k), indent+g.step, depth+1)
		} else {
			g.write(g.scalar())
		}
	}
	if rapid.Bool().Draw(g.t, "trailing comma") {
		g.write(",")
	}
	if multiline && rapid.Bool().Draw(g.t, "comment in flow") {
		g.write("  # inside\n" + strings.Repeat(" ", indent))
	}
	g.write(" }")
}

func (g *docGen) scalar() string {
	return rapid.SampledFrom([]string{
		"plain", "two words", "42", "true", "~", `"double, quoted }"`, `'single ''quoted'' ]'`,
		`"esc\"aped"`, "!!str tagged", "grün", "[1, 2]", "{}",
	}).Draw(g.t, "scalar")
}

func (g *docGen) lineComment() string {
	if rapid.Bool().Draw(g.t, "line comment") {
		return "  # note"
	}
	return ""
}
