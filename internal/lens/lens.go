// Package lens defines the ownership model shared by all lenses. A lens is
// the bidirectional mapping between intent and engine code for one kind,
// aspect and engine (design §4.4 and §4.5).
package lens

import "fmt"

// Owner is the ownership state of a field of a managed resource.
type Owner int

const (
	// Synced fields live in intent and are mirrored as literals in code.
	// They can be edited from the GUI and from code.
	Synced Owner = iota
	// CodeOwned fields are defined in code by an expression, a reference or
	// a pinned literal. The GUI shows them read-only.
	CodeOwned
	// Extension fields exist only in code because the schema does not model
	// them. They are preserved and shown read-only.
	Extension
	// Ignored fields are excluded from management.
	Ignored
)

// String returns the name used in reports, such as "code-owned".
func (o Owner) String() string {
	switch o {
	case Synced:
		return "synced"
	case CodeOwned:
		return "code-owned"
	case Extension:
		return "extension"
	case Ignored:
		return "ignored"
	default:
		return fmt.Sprintf("owner(%d)", int(o))
	}
}

// Field reports the ownership of one field.
type Field struct {
	// Path is the intent path of the field, such as
	// spec.resources.cpu.cores. For extensions it is the path in code.
	Path string
	// CodePath is the path in engine code, such as cpu.cores.
	CodePath string
	Owner    Owner
	// Reason explains why a field is code-owned.
	Reason string
	// Line is the 1-based line of the field in its file, or 0 if the code
	// does not set the field.
	Line int
}

// Report lists the ownership of the fields of one managed block.
type Report struct {
	// File is the workspace-relative path of the file with the block.
	File   string
	Fields []Field
}

// Lookup returns the field with the given intent path.
func (r Report) Lookup(path string) (Field, bool) {
	for _, f := range r.Fields {
		if f.Path == path {
			return f, true
		}
	}
	return Field{}, false
}

// Count returns the number of fields with the given owner.
func (r Report) Count(o Owner) int {
	n := 0
	for _, f := range r.Fields {
		if f.Owner == o {
			n++
		}
	}
	return n
}
