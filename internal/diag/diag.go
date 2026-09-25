// Package diag defines the diagnostics that validation, compilation and
// synchronization report to users.
package diag

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Severity classifies a diagnostic.
type Severity int

const (
	// Error blocks the operation that produced the diagnostic.
	Error Severity = iota
	// Warning is allowed after the user acknowledges it.
	Warning
	// Info is informational only.
	Info
)

// String returns the lowercase name of the severity.
func (s Severity) String() string {
	switch s {
	case Error:
		return "error"
	case Warning:
		return "warning"
	case Info:
		return "info"
	default:
		return fmt.Sprintf("severity(%d)", int(s))
	}
}

// Diagnostic is a single finding, located in a file where possible.
type Diagnostic struct {
	Severity Severity
	// File is the workspace-relative path of the file, or empty.
	File string
	// Line is the 1-based line in File, or 0 if unknown.
	Line int
	// Path is the field path the finding refers to, such as
	// spec.resources.memory.size, or empty.
	Path    string
	Message string
}

// String formats the diagnostic as "file:line: severity: path: message",
// leaving out parts that are empty.
func (d Diagnostic) String() string {
	var b strings.Builder
	if d.File != "" {
		b.WriteString(d.File)
		if d.Line > 0 {
			fmt.Fprintf(&b, ":%d", d.Line)
		}
		b.WriteString(": ")
	}
	b.WriteString(d.Severity.String())
	b.WriteString(": ")
	if d.Path != "" {
		b.WriteString(d.Path)
		b.WriteString(": ")
	}
	b.WriteString(d.Message)
	return b.String()
}

// List is a collection of diagnostics.
type List []Diagnostic

// Errorf appends an error diagnostic.
func (l *List) Errorf(file string, line int, path, format string, args ...any) {
	l.add(Error, file, line, path, format, args...)
}

// Warnf appends a warning diagnostic.
func (l *List) Warnf(file string, line int, path, format string, args ...any) {
	l.add(Warning, file, line, path, format, args...)
}

func (l *List) add(s Severity, file string, line int, path, format string, args ...any) {
	*l = append(*l, Diagnostic{
		Severity: s,
		File:     file,
		Line:     line,
		Path:     path,
		Message:  fmt.Sprintf(format, args...),
	})
}

// Append adds all diagnostics from other.
func (l *List) Append(other List) {
	*l = append(*l, other...)
}

// HasErrors reports whether the list contains at least one error.
func (l List) HasErrors() bool {
	for _, d := range l {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

// Count returns the number of diagnostics with the given severity.
func (l List) Count(s Severity) int {
	n := 0
	for _, d := range l {
		if d.Severity == s {
			n++
		}
	}
	return n
}

// Sort orders the diagnostics by file, line, path and message, so output is
// stable across runs.
func (l List) Sort() {
	sort.SliceStable(l, func(i, j int) bool {
		a, b := l[i], l[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Message < b.Message
	})
}

// Err returns an error that lists every error diagnostic, or nil if there
// are none. Warnings and informational diagnostics are not included.
func (l List) Err() error {
	var errs []error
	for _, d := range l {
		if d.Severity == Error {
			errs = append(errs, errors.New(d.String()))
		}
	}
	return errors.Join(errs...)
}
