package nrm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/centopw/nodr/internal/diag"
)

// Parse reads all documents from YAML data. name is the workspace-relative
// path of the file, used in diagnostics and recorded in each document. Empty
// documents are skipped.
func Parse(name string, data []byte) ([]*Document, diag.List) {
	var (
		docs  []*Document
		diags diag.List
	)
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			diags.Errorf(name, 0, "", "invalid YAML: %s", strings.TrimPrefix(err.Error(), "yaml: "))
			break
		}
		if len(n.Content) == 0 {
			continue
		}
		root := n.Content[0]
		if root.Kind == yaml.ScalarNode && root.ShortTag() == "!!null" {
			continue // an empty document, such as one between two "---" lines
		}
		if root.Kind != yaml.MappingNode {
			diags.Errorf(name, root.Line, "", "a document must be a mapping with apiVersion, kind, metadata and spec")
			continue
		}
		value, err := toJSON(root)
		if err != nil {
			diags.Errorf(name, root.Line, "", "%v", err)
			continue
		}
		obj := value.(map[string]any)
		doc := &Document{Object: obj, File: name, Line: root.Line, node: root}
		doc.APIVersion, _ = obj["apiVersion"].(string)
		doc.Kind, _ = obj["kind"].(string)
		// Metadata that does not match the expected shape is reported by
		// schema validation, so decoding it here is best effort.
		_ = decodeLenient(obj["metadata"], &doc.Metadata)
		docs = append(docs, doc)
	}
	return docs, diags
}

// LoadDir parses every .yaml and .yml file below dir in fsys, recursively,
// in lexical order. Paths in documents and diagnostics are relative to the
// root of fsys.
func LoadDir(fsys fs.FS, dir string) ([]*Document, diag.List) {
	var (
		docs  []*Document
		diags diag.List
		files []string
	)
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if ext := path.Ext(p); ext == ".yaml" || ext == ".yml" {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		diags.Errorf(dir, 0, "", "read directory: %v", err)
		return nil, diags
	}
	sort.Strings(files)
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			diags.Errorf(f, 0, "", "read file: %v", err)
			continue
		}
		fileDocs, fileDiags := Parse(f, data)
		docs = append(docs, fileDocs...)
		diags.Append(fileDiags)
	}
	return docs, diags
}

// toJSON converts a YAML node into JSON-compatible values. Scalars keep the
// type YAML resolves them to, except timestamps and other non-JSON types,
// which stay strings exactly as written.
func toJSON(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return toJSON(n.Content[0])
	case yaml.AliasNode:
		return toJSON(n.Alias)
	case yaml.MappingNode:
		obj := make(map[string]any, len(n.Content)/2)
		var merges []*yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("line %d: mapping keys must be scalars", k.Line)
			}
			if k.Tag == "!!merge" {
				merges = append(merges, v)
				continue
			}
			if _, dup := obj[k.Value]; dup {
				return nil, fmt.Errorf("line %d: duplicate key %q", k.Line, k.Value)
			}
			val, err := toJSON(v)
			if err != nil {
				return nil, err
			}
			obj[k.Value] = val
		}
		// Keys written explicitly take precedence over merged keys,
		// wherever the merge key appears.
		for _, v := range merges {
			if err := merge(obj, v); err != nil {
				return nil, err
			}
		}
		return obj, nil
	case yaml.SequenceNode:
		arr := make([]any, len(n.Content))
		for i, item := range n.Content {
			val, err := toJSON(item)
			if err != nil {
				return nil, err
			}
			arr[i] = val
		}
		return arr, nil
	case yaml.ScalarNode:
		return scalar(n)
	default:
		return nil, fmt.Errorf("line %d: unsupported YAML node", n.Line)
	}
}

func scalar(n *yaml.Node) (any, error) {
	switch n.ShortTag() {
	case "!!null":
		return nil, nil
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err != nil {
			return nil, fmt.Errorf("line %d: %w", n.Line, err)
		}
		return b, nil
	case "!!int":
		var i int64
		if err := n.Decode(&i); err != nil {
			return nil, fmt.Errorf("line %d: %w", n.Line, err)
		}
		return json.Number(strconv.FormatInt(i, 10)), nil
	case "!!float":
		var f float64
		if err := n.Decode(&f); err != nil {
			return nil, fmt.Errorf("line %d: %w", n.Line, err)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("line %d: %s is not a valid number", n.Line, n.Value)
		}
		return json.Number(strconv.FormatFloat(f, 'g', -1, 64)), nil
	default:
		return n.Value, nil
	}
}

// merge applies a YAML merge key: keys from the merged mappings are added
// unless the mapping already defines them.
func merge(obj map[string]any, v *yaml.Node) error {
	sources := []*yaml.Node{v}
	if v.Kind == yaml.SequenceNode {
		sources = v.Content
	}
	for _, src := range sources {
		val, err := toJSON(src)
		if err != nil {
			return err
		}
		m, ok := val.(map[string]any)
		if !ok {
			return fmt.Errorf("line %d: merge keys need mappings", src.Line)
		}
		for k, kv := range m {
			if _, exists := obj[k]; !exists {
				obj[k] = kv
			}
		}
	}
	return nil
}

func decodeLenient(value, v any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
