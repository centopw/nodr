package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
)

const manifest = `apiVersion: nodr/v1alpha1
kind: Workspace
metadata:
  name: home
spec: {}
`

const cluster = `apiVersion: nodr/v1alpha1
kind: ProxmoxCluster
metadata:
  name: pve-main
spec:
  endpoints: [https://10.0.10.11:8006]
  credentialsRef: proxmox/pve-main-token
`

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestFindRoot(t *testing.T) {
	root := writeFiles(t, map[string]string{"nodr.yaml": manifest, "intent/compute/.keep": ""})
	got, err := FindRoot(filepath.Join(root, "intent", "compute"))
	if err != nil || got != root {
		t.Errorf("FindRoot = %q, %v; want %q", got, err, root)
	}
	if _, err := FindRoot(t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindRoot outside a workspace: err = %v, want ErrNotFound", err)
	}
}

func TestLoadAndValidate(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"nodr.yaml":                   manifest,
		"intent/platform/pve.yaml":    cluster,
		"intent/network/unknown.yaml": "apiVersion: nodr/v1alpha1\nkind: Gizmo\nmetadata:\n  name: g\nspec: {}\n",
	})
	w, diags := Load(root)
	if diags.HasErrors() {
		t.Fatalf("Load: %v", diags.Err())
	}
	if w.Manifest == nil || w.Manifest.Metadata.Name != "home" {
		t.Fatalf("manifest = %+v", w.Manifest)
	}
	if len(w.Documents) != 2 {
		t.Fatalf("got %d documents, want 2", len(w.Documents))
	}
	if d := w.Find(nrm.Ref{Kind: "ProxmoxCluster", Name: "pve-main"}); d == nil || d.File != "intent/platform/pve.yaml" {
		t.Errorf("Find = %+v", d)
	}
	if got := len(w.OfKind("ProxmoxCluster")); got != 1 {
		t.Errorf("OfKind = %d documents, want 1", got)
	}
	r, err := v1alpha1.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	vd := w.Validate(r)
	if vd.Count(diag.Error) != 1 || !strings.Contains(vd.Err().Error(), "unknown kind Gizmo") {
		t.Errorf("Validate = %v, want one unknown-kind error", vd)
	}
	rel, err := w.Rel(filepath.Join(root, "intent", "platform", "pve.yaml"))
	if err != nil || rel != "intent/platform/pve.yaml" {
		t.Errorf("Rel = %q, %v", rel, err)
	}
}

func TestLoadManifestProblems(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"missing", map[string]string{"intent/a.yaml": cluster}, "read the workspace manifest"},
		{"empty", map[string]string{"nodr.yaml": "# nothing\n"}, "the manifest is empty"},
		{"two documents", map[string]string{"nodr.yaml": manifest + "---\n" + manifest}, "exactly one document"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, diags := Load(writeFiles(t, tt.files))
			if !diags.HasErrors() || !strings.Contains(diags.Err().Error(), tt.want) {
				t.Errorf("Load diagnostics = %v, want %q", diags.Err(), tt.want)
			}
		})
	}
}

func TestLoadWithoutIntentDirectory(t *testing.T) {
	w, diags := Load(writeFiles(t, map[string]string{"nodr.yaml": manifest}))
	if diags.HasErrors() || len(w.Documents) != 0 {
		t.Errorf("Load = %d documents, %v", len(w.Documents), diags.Err())
	}
}
