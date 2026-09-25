package nrm

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/centopw/nodr/internal/diag"
)

// schemaBaseURL is the base URL under which schema files are registered with
// the JSON Schema compiler. Schemas refer to each other with relative
// references, so the same files also work unchanged in editors.
const schemaBaseURL = "file:///nodr/schemas/"

var printer = message.NewPrinter(language.English)

// KindInfo describes a registered kind.
type KindInfo struct {
	// APIVersion and Kind identify the kind, for example nodr/v1alpha1 and
	// VirtualMachine.
	APIVersion string
	Kind       string
	// ShortName is the alias used in references such as vm/web-01.
	ShortName string
	// Schema is the file name of the kind's JSON Schema among the schema
	// files added for its API version.
	Schema string
	// Manifest marks the kind of the workspace manifest (nodr.yaml), which
	// must not appear among intent documents.
	Manifest bool
	// References lists the references a document makes to other resources.
	// It may be nil.
	References func(*Document) ([]FieldRef, error)
	// Check runs semantic checks that a schema cannot express, such as
	// unique names within a list or addresses inside a subnet. It runs
	// only for documents that pass schema validation, and may be nil.
	// Checks that compare several documents are added with
	// Registry.AddCheck.
	Check func(*Document, *diag.List)
}

// FieldRef is a reference from a field of a document to another resource.
type FieldRef struct {
	Target Ref
	// Path holds the segments of the referring field, for example
	// ["spec", "nics", "0", "network"].
	Path []string
}

// Registry holds the kinds nodr understands and validates documents against
// them. It is safe for concurrent use once all kinds are registered and all
// checks added.
type Registry struct {
	mu       sync.Mutex
	kinds    map[string]*KindInfo // key: apiVersion + " " + kind
	byName   map[string]*KindInfo // key: kind, lowercase kind or short name
	checks   []func([]*Document, *diag.List)
	compiler *jsonschema.Compiler
	compiled map[string]*jsonschema.Schema
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(noLoader{})
	return &Registry{
		kinds:    map[string]*KindInfo{},
		byName:   map[string]*KindInfo{},
		compiler: c,
		compiled: map[string]*jsonschema.Schema{},
	}
}

// AddSchemas adds every .json file in the root of fsys as a schema resource
// for apiVersion. It must be called before registering kinds that use them.
func (r *Registry) AddSchemas(apiVersion string, fsys fs.FS) error {
	files, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return err
	}
	for _, name := range files {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("schema %s: %w", name, err)
		}
		if err := r.compiler.AddResource(schemaURL(apiVersion, name), doc); err != nil {
			return fmt.Errorf("schema %s: %w", name, err)
		}
	}
	return nil
}

// Register adds a kind. The kind's schema must compile.
func (r *Registry) Register(k KindInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := k.APIVersion + " " + k.Kind
	if _, dup := r.kinds[key]; dup {
		return fmt.Errorf("kind %s %s is already registered", k.APIVersion, k.Kind)
	}
	for _, name := range []string{k.Kind, strings.ToLower(k.Kind), k.ShortName} {
		if other, dup := r.byName[name]; dup && other.Kind != k.Kind {
			return fmt.Errorf("name %q of kind %s is already used by kind %s", name, k.Kind, other.Kind)
		}
	}
	sch, err := r.compiler.Compile(schemaURL(k.APIVersion, k.Schema))
	if err != nil {
		return fmt.Errorf("compile schema of %s: %w", k.Kind, err)
	}
	info := k
	r.kinds[key] = &info
	r.compiled[key] = sch
	for _, name := range []string{k.Kind, strings.ToLower(k.Kind), k.ShortName} {
		if name != "" {
			r.byName[name] = &info
		}
	}
	return nil
}

// AddCheck adds a check that compares intent documents with each other,
// for example for values that must be unique across documents. Validate
// runs it once with every document that passes schema validation, in the
// order Validate got them, and the check reports problems in diags.
func (r *Registry) AddCheck(check func(docs []*Document, diags *diag.List)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, check)
}

// Lookup returns the kind registered for apiVersion and kind.
func (r *Registry) Lookup(apiVersion, kind string) (*KindInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.kinds[apiVersion+" "+kind]
	return k, ok
}

// LookupName returns the kind with the given name, lowercase name or short
// name, for example VirtualMachine, virtualmachine or vm.
func (r *Registry) LookupName(name string) (*KindInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byName[name]
	return k, ok
}

// ParseRef parses a reference written as <kind>/<name>, where kind may be a
// kind name, a lowercase kind name or a short name, for example vm/web-01.
func (r *Registry) ParseRef(s string) (Ref, error) {
	kindName, name, ok := strings.Cut(s, "/")
	if !ok || kindName == "" || name == "" {
		return Ref{}, fmt.Errorf("invalid reference %q: want <kind>/<name>, for example vm/web-01", s)
	}
	k, found := r.LookupName(kindName)
	if !found {
		return Ref{}, fmt.Errorf("invalid reference %q: unknown kind %q", s, kindName)
	}
	return Ref{Kind: k.Kind, Name: name}, nil
}

// Kinds returns the registered kinds sorted by kind name.
func (r *Registry) Kinds() []*KindInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*KindInfo, 0, len(r.kinds))
	for _, k := range r.kinds {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// ValidateManifest validates the workspace manifest document.
func (r *Registry) ValidateManifest(d *Document) diag.List {
	var diags diag.List
	k, ok := r.checkKind(d, &diags)
	if !ok {
		return diags
	}
	if !k.Manifest {
		diags.Errorf(d.File, d.Line, "kind", "the workspace manifest must be a Workspace document, not %s", d.Kind)
		return diags
	}
	r.validateContent(k, d, &diags)
	diags.Sort()
	return diags
}

// validateContent checks the metadata and the schema of a document, and
// reports whether the schema found no new problem. The metadata checks
// explain problems better than the patterns of the schema do, so the schema
// does not report a field again that the metadata checks reported.
func (r *Registry) validateContent(k *KindInfo, d *Document, diags *diag.List) bool {
	var metadata, schema diag.List
	validateMetadata(d, &metadata)
	r.validateSchema(k, d, &schema)
	reported := map[string]bool{}
	for _, dg := range metadata {
		reported[dg.Path] = true
	}
	*diags = append(*diags, metadata...)
	ok := true
	for _, dg := range schema {
		if !reported[dg.Path] {
			*diags = append(*diags, dg)
			ok = false
		}
	}
	return ok
}

// Validate checks intent documents: registered kinds, metadata, schemas,
// the checks of each kind and those added with AddCheck, unique names and
// UIDs, and references between the documents. References to kinds that
// are not registered are not checked.
func (r *Registry) Validate(docs []*Document) diag.List {
	var diags diag.List
	byRef := map[Ref]*Document{}
	byUID := map[string]*Document{}
	// known holds the documents of registered kinds, and valid those of
	// them that also pass schema validation.
	var known, valid []*Document
	for _, d := range docs {
		k, ok := r.checkKind(d, &diags)
		if !ok {
			continue
		}
		if k.Manifest {
			diags.Errorf(d.File, d.Line, "kind", "%s documents belong in nodr.yaml, not among intent documents", d.Kind)
			continue
		}
		if r.validateContent(k, d, &diags) {
			valid = append(valid, d)
			if k.Check != nil {
				k.Check(d, &diags)
			}
		}
		known = append(known, d)

		ref := d.Ref()
		if first, dup := byRef[ref]; dup {
			diags.Errorf(d.File, d.LineOf("metadata", "name"), "metadata.name", "%s is defined twice; it is also defined at %s:%d", ref, first.File, first.Line)
		} else {
			byRef[ref] = d
		}
		if uid := d.Metadata.UID; uid != "" {
			if first, dup := byUID[uid]; dup {
				diags.Errorf(d.File, d.LineOf("metadata", "uid"), "metadata.uid", "UID %s is also used by %s at %s:%d", uid, first.Ref(), first.File, first.Line)
			} else {
				byUID[uid] = d
			}
		}
	}
	r.mu.Lock()
	checks := r.checks
	r.mu.Unlock()
	for _, check := range checks {
		check(valid, &diags)
	}
	for _, d := range known {
		k, _ := r.Lookup(d.APIVersion, d.Kind)
		if k.References == nil {
			continue
		}
		refs, err := k.References(d)
		if err != nil {
			// The document does not match its type; schema validation has
			// already reported why.
			continue
		}
		for _, fr := range refs {
			if _, known := r.LookupName(fr.Target.Kind); !known {
				continue
			}
			if _, exists := byRef[fr.Target]; !exists {
				diags.Errorf(d.File, d.LineOf(fr.Path...), d.FieldPath(fr.Path...), "%s %q does not exist", fr.Target.Kind, fr.Target.Name)
			}
		}
	}
	diags.Sort()
	return diags
}

func (r *Registry) checkKind(d *Document, diags *diag.List) (*KindInfo, bool) {
	switch {
	case d.APIVersion == "":
		diags.Errorf(d.File, d.Line, "apiVersion", "missing apiVersion")
		return nil, false
	case d.Kind == "":
		diags.Errorf(d.File, d.Line, "kind", "missing kind")
		return nil, false
	}
	k, ok := r.Lookup(d.APIVersion, d.Kind)
	if !ok {
		diags.Errorf(d.File, d.LineOf("kind"), "kind", "unknown kind %s in API version %s", d.Kind, d.APIVersion)
		return nil, false
	}
	return k, true
}

func (r *Registry) validateSchema(k *KindInfo, d *Document, diags *diag.List) {
	r.mu.Lock()
	sch := r.compiled[k.APIVersion+" "+k.Kind]
	r.mu.Unlock()
	err := sch.Validate(any(d.Object))
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		if err != nil {
			diags.Errorf(d.File, d.Line, "", "schema validation failed: %v", err)
		}
		return
	}
	for _, leaf := range leaves(ve) {
		loc := leaf.InstanceLocation
		switch e := leaf.ErrorKind.(type) {
		case *kind.AdditionalProperties:
			for _, p := range e.Properties {
				field := append(append([]string{}, loc...), p)
				diags.Errorf(d.File, d.LineOf(field...), d.FieldPath(field...), "unknown field")
			}
		case *kind.Required:
			for _, p := range e.Missing {
				field := append(append([]string{}, loc...), p)
				diags.Errorf(d.File, d.LineOf(loc...), d.FieldPath(field...), "missing required field")
			}
		default:
			diags.Errorf(d.File, d.LineOf(loc...), d.FieldPath(loc...), "%s", leaf.ErrorKind.LocalizedString(printer))
		}
	}
}

// leaves returns the innermost validation errors, which are the ones that
// explain a failure.
func leaves(ve *jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(ve.Causes) == 0 {
		return []*jsonschema.ValidationError{ve}
	}
	var out []*jsonschema.ValidationError
	for _, c := range ve.Causes {
		out = append(out, leaves(c)...)
	}
	return out
}

func schemaURL(apiVersion, file string) string {
	return schemaBaseURL + path.Join(apiVersion, file)
}

// noLoader refuses to load schemas from anywhere: every schema nodr uses is
// embedded and registered up front.
type noLoader struct{}

func (noLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("schema %s is not registered", url)
}
