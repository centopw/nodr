// Package workspace loads a nodr workspace: the manifest nodr.yaml at its
// root and the intent documents below intent/ (design §3.12).
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/nrm"
)

// Layout of a workspace.
const (
	ManifestFile = "nodr.yaml"
	IntentDir    = "intent"
)

// ErrNotFound reports that no workspace contains the given directory.
var ErrNotFound = errors.New("not inside a nodr workspace: no nodr.yaml found in this directory or any parent")

// Workspace is a loaded workspace.
type Workspace struct {
	// Root is the absolute path of the workspace directory.
	Root string
	// Manifest is the document in nodr.yaml.
	Manifest *nrm.Document
	// Documents are the intent documents, in file order.
	Documents []*nrm.Document
	// FS gives read access to the files of the workspace.
	FS fs.FS
}

// FindRoot returns the closest directory at or above dir that contains
// nodr.yaml.
func FindRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		info, err := os.Stat(filepath.Join(abs, ManifestFile))
		if err == nil && !info.IsDir() {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", ErrNotFound
		}
		abs = parent
	}
}

// Load reads the workspace at root. The returned diagnostics cover files
// that cannot be read or parsed; Validate checks the documents themselves.
func Load(root string) (*Workspace, diag.List) {
	var diags diag.List
	abs, err := filepath.Abs(root)
	if err != nil {
		diags.Errorf("", 0, "", "%v", err)
		return nil, diags
	}
	w := &Workspace{Root: abs, FS: os.DirFS(abs)}

	data, err := fs.ReadFile(w.FS, ManifestFile)
	if err != nil {
		diags.Errorf(ManifestFile, 0, "", "read the workspace manifest: %v", err)
		return nil, diags
	}
	manifest, manifestDiags := nrm.Parse(ManifestFile, data)
	diags.Append(manifestDiags)
	switch len(manifest) {
	case 0:
		if !manifestDiags.HasErrors() {
			diags.Errorf(ManifestFile, 0, "", "the manifest is empty; it must contain one Workspace document")
		}
	case 1:
		w.Manifest = manifest[0]
	default:
		diags.Errorf(ManifestFile, manifest[1].Line, "", "the manifest must contain exactly one document, found %d", len(manifest))
		w.Manifest = manifest[0]
	}

	if info, err := fs.Stat(w.FS, IntentDir); err == nil && info.IsDir() {
		docs, intentDiags := nrm.LoadDir(w.FS, IntentDir)
		w.Documents = docs
		diags.Append(intentDiags)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		diags.Errorf(IntentDir, 0, "", "read intent documents: %v", err)
	}
	return w, diags
}

// Validate checks the manifest and every intent document against the kinds
// in r.
func (w *Workspace) Validate(r *nrm.Registry) diag.List {
	var diags diag.List
	if w.Manifest != nil {
		diags.Append(r.ValidateManifest(w.Manifest))
	}
	diags.Append(r.Validate(w.Documents))
	diags.Sort()
	return diags
}

// Find returns the intent document with the given reference, or nil.
func (w *Workspace) Find(ref nrm.Ref) *nrm.Document {
	for _, d := range w.Documents {
		if d.Ref() == ref {
			return d
		}
	}
	return nil
}

// OfKind returns the intent documents of one kind, in file order.
func (w *Workspace) OfKind(kind string) []*nrm.Document {
	var out []*nrm.Document
	for _, d := range w.Documents {
		if d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}

// Rel returns path relative to the workspace root, with forward slashes.
func (w *Workspace) Rel(path string) (string, error) {
	rel, err := filepath.Rel(w.Root, path)
	if err != nil {
		return "", fmt.Errorf("%s is not inside the workspace: %w", path, err)
	}
	return filepath.ToSlash(rel), nil
}
