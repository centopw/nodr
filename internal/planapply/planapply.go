// Package planapply coordinates compilation, OpenTofu planning, and apply
// operations across state units.
package planapply

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/centopw/nodr/internal/compile"
	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/engine/opentofu"
	"github.com/centopw/nodr/internal/workspace"
)

const terraformDir = "terraform"

// UnitPlan is the saved plan of one state unit.
type UnitPlan struct {
	Dir    string
	Runner *opentofu.Runner
	File   string // path of the saved plan file
	Plan   opentofu.Plan
}

// CompileError indicates that compiling intent produced error diagnostics.
type CompileError struct {
	Diagnostics diag.List
}

func (e *CompileError) Error() string {
	return "compilation failed"
}

type unitError struct {
	dir string
	err error
}

func (e *unitError) Error() string {
	var cmdErr *opentofu.CommandError
	if errors.As(e.err, &cmdErr) {
		return fmt.Sprintf("%s: %s: %v", e.dir, cmdErr.Command, cmdErr.Err)
	}
	return fmt.Sprintf("%s: %v", e.dir, e.err)
}

func (e *unitError) Unwrap() error {
	return e.err
}

// UnitDir returns the unit directory if err is a wrapped unitError.
func UnitDir(err error) string {
	var u *unitError
	if errors.As(err, &u) {
		return u.dir
	}
	return ""
}

// UnitError wraps err with the directory of the unit it happened in, for
// error messages.
func UnitError(dir string, err error) error {
	if err == nil {
		return nil
	}
	return &unitError{dir: dir, err: err}
}

// StateUnits returns the directories of the state units in fsys, the
// directories right below terraform/ that hold .tf files, in sorted order.
// Directories further down, such as those of local modules, are not units.
// pending holds the files that are about to be written, by workspace-relative
// path, and they can add units.
func StateUnits(fsys fs.FS, pending map[string][]byte) ([]string, error) {
	entries, err := fs.ReadDir(fsys, terraformDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var units []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := path.Join(terraformDir, e.Name())
		files, err := fs.ReadDir(fsys, dir)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(files, func(f fs.DirEntry) bool { return !f.IsDir() && path.Ext(f.Name()) == ".tf" }) {
			units = append(units, dir)
		}
	}
	for p := range pending {
		if dir := path.Dir(p); path.Dir(dir) == terraformDir && path.Ext(p) == ".tf" && !slices.Contains(units, dir) {
			units = append(units, dir)
		}
	}
	slices.Sort(units)
	return units, nil
}

// SelectUnits returns the units whose directories are in only, in the order
// of units, or all units if only is empty. The directories in only are
// relative to the workspace, as nodr prints them, and may end with a slash.
func SelectUnits(units, only []string) ([]string, error) {
	if len(only) == 0 {
		return units, nil
	}
	selected := map[string]bool{}
	for _, dir := range only {
		dir = path.Clean(filepath.ToSlash(dir))
		if !slices.Contains(units, dir) {
			if len(units) == 0 {
				return nil, fmt.Errorf("%s is not a state unit: no directory below %s/ holds .tf files", dir, terraformDir)
			}
			return nil, fmt.Errorf("%s is not a state unit; the units are %s", dir, strings.Join(units, ", "))
		}
		selected[dir] = true
	}
	return slices.DeleteFunc(slices.Clone(units), func(u string) bool { return !selected[u] }), nil
}

// PlanUnits compiles ws, writes the files that change, and plans each
// selected state unit (all units if only is empty) into planDir.
// Output from each unit's OpenTofu commands (init, plan, show) goes to
// out, which may be nil to discard it. Returns the files that were
// written (workspace-relative path -> content) so the caller can report
// them, and the plans, in unit order. It stops at the first unit that
// fails.
func PlanUnits(ctx context.Context, ws *workspace.Workspace, only []string, planDir string, out io.Writer, interrupts <-chan struct{}) (written map[string][]byte, plans []UnitPlan, err error) {
	bin, err := opentofu.LookPath()
	if err != nil {
		return nil, nil, err
	}
	files, diags := compile.Compile(ws)
	if out != nil {
		for _, d := range diags {
			fmt.Fprintln(out, d)
		}
	}
	if diags.HasErrors() {
		return nil, nil, &CompileError{Diagnostics: diags}
	}
	units, err := StateUnits(ws.FS, files)
	if err != nil {
		return nil, nil, err
	}
	units, err = SelectUnits(units, only)
	if err != nil {
		return nil, nil, err
	}
	if err := compile.Write(ws, files); err != nil {
		return nil, nil, err
	}
	if len(units) == 0 {
		return files, nil, nil
	}
	plans = make([]UnitPlan, 0, len(units))
	for _, dir := range units {
		u := UnitPlan{
			Dir: dir,
			Runner: &opentofu.Runner{
				Binary:     bin,
				Dir:        filepath.Join(ws.Root, filepath.FromSlash(dir)),
				Stdout:     out,
				Stderr:     out,
				Interrupts: interrupts,
			},
			File: filepath.Join(planDir, path.Base(dir)+".tfplan"),
		}
		if err := u.Runner.Init(ctx); err != nil {
			return files, nil, UnitError(dir, err)
		}
		if u.Plan, err = u.Runner.Plan(ctx, u.File); err != nil {
			return files, nil, UnitError(dir, err)
		}
		plans = append(plans, u)
	}
	return files, plans, nil
}

// Apply applies the saved plan of each unit in plans, in order, stopping
// at the first failure. Output goes to out (may be nil). Returns the
// directories that were applied successfully before any failure.
func Apply(ctx context.Context, plans []UnitPlan, out io.Writer, interrupts <-chan struct{}) (applied []string, err error) {
	applied = make([]string, 0, len(plans))
	for _, u := range plans {
		runner := u.Runner
		if runner == nil {
			var err error
			runner, err = opentofu.NewRunner(u.Dir)
			if err != nil {
				return applied, UnitError(u.Dir, err)
			}
		}
		runner.Stdout = out
		runner.Stderr = out
		runner.Interrupts = interrupts
		if err := runner.Apply(ctx, u.File); err != nil {
			return applied, UnitError(u.Dir, err)
		}
		applied = append(applied, u.Dir)
	}
	return applied, nil
}

// DestructiveChanges returns a line for each change across plans that
// replaces or destroys a resource, formatted "<dir>: <action> <address>".
func DestructiveChanges(plans []UnitPlan) []string {
	var lines []string
	for _, u := range plans {
		for _, c := range u.Plan.Changes {
			if c.Action == opentofu.Replace || c.Action == opentofu.Delete {
				action := "destroy"
				if c.Action == opentofu.Replace {
					action = "replace"
				}
				lines = append(lines, fmt.Sprintf("%s: %s %s", u.Dir, action, c.Address))
			}
		}
	}
	return lines
}
