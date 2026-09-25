package diag

import (
	"strings"
	"testing"
)

func TestDiagnosticString(t *testing.T) {
	tests := []struct {
		name string
		d    Diagnostic
		want string
	}{
		{
			name: "full location",
			d:    Diagnostic{Severity: Error, File: "intent/vm.yaml", Line: 12, Path: "spec.cpu", Message: "must be positive"},
			want: "intent/vm.yaml:12: error: spec.cpu: must be positive",
		},
		{
			name: "file without line",
			d:    Diagnostic{Severity: Warning, File: "nodr.yaml", Message: "deprecated field"},
			want: "nodr.yaml: warning: deprecated field",
		},
		{
			name: "no location",
			d:    Diagnostic{Severity: Info, Message: "nothing to do"},
			want: "info: nothing to do",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.d.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestListCountsAndErr(t *testing.T) {
	var l List
	if l.HasErrors() || l.Err() != nil {
		t.Fatal("empty list should have no errors")
	}
	l.Warnf("a.yaml", 1, "", "careful")
	if l.HasErrors() || l.Err() != nil {
		t.Fatal("warnings must not count as errors")
	}
	l.Errorf("b.yaml", 3, "spec.x", "broken %d", 1)
	l.Errorf("a.yaml", 9, "", "also broken")
	if !l.HasErrors() {
		t.Fatal("HasErrors() = false, want true")
	}
	if got := l.Count(Error); got != 2 {
		t.Errorf("Count(Error) = %d, want 2", got)
	}
	if got := l.Count(Warning); got != 1 {
		t.Errorf("Count(Warning) = %d, want 1", got)
	}
	err := l.Err()
	if err == nil || !strings.Contains(err.Error(), "broken 1") || strings.Contains(err.Error(), "careful") {
		t.Errorf("Err() = %v, want only error diagnostics", err)
	}
}

func TestListSort(t *testing.T) {
	l := List{
		{File: "b.yaml", Line: 1, Message: "x"},
		{File: "a.yaml", Line: 5, Message: "y"},
		{File: "a.yaml", Line: 2, Path: "spec.b", Message: "z"},
		{File: "a.yaml", Line: 2, Path: "spec.a", Message: "z"},
	}
	l.Sort()
	got := make([]string, len(l))
	for i, d := range l {
		got[i] = d.String()
	}
	want := []string{
		"a.yaml:2: error: spec.a: z",
		"a.yaml:2: error: spec.b: z",
		"a.yaml:5: error: y",
		"b.yaml:1: error: x",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sorted =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestSeverityString(t *testing.T) {
	if got := Severity(42).String(); got != "severity(42)" {
		t.Errorf("String() = %q", got)
	}
}
