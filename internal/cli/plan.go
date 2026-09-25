package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/centopw/nodr/internal/compile"
	"github.com/centopw/nodr/internal/engine/opentofu"
)

func (a *app) planCommand() *cobra.Command {
	var units []string
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Write the engine code for intent and plan it with OpenTofu",
		Long: `Plan compiles the intent of the workspace into OpenTofu code, writes the
files that change and lists them, and plans each state unit: each
directory right below terraform/ that holds .tf files, or only the units
that --unit names. For each unit it prints the number of resources to add,
change, replace and destroy, and a line for each change. Plan changes no
infrastructure; nodr apply does.

OpenTofu runs in the workspace on disk: the program that NODR_TOFU names,
or else tofu on PATH. It gets the environment of nodr, so provider
credentials such as PROXMOX_VE_API_TOKEN reach it, and its output goes to
stderr.`,
		Example: "  nodr plan --unit terraform/pve-main-compute",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withPlanDir(func(planDir string) error {
				_, err := a.planUnits(cmd.Context(), units, planDir)
				return err
			})
		},
	}
	cmd.Flags().StringArrayVar(&units, "unit", nil, "plan only the state unit in `dir`, relative to the workspace, such as terraform/pve-main-compute; repeat it for more units")
	return cmd
}

func (a *app) applyCommand() *cobra.Command {
	var (
		units                     []string
		autoApprove, allowDestroy bool
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Write the engine code for intent, plan it and apply the plan with OpenTofu",
		Long: `Apply plans like nodr plan, and then applies the saved plan of each unit
with changes, never a new one, so it makes exactly the changes that it
printed. First it asks for confirmation, which only 'yes' gives; it asks on
stdin, which must be a terminal unless --auto-approve skips the question.
If a change replaces or destroys a resource, apply refuses to apply
anything unless --allow-destroy is given, with or without --auto-approve.

Units are applied in order, and apply stops at the first unit that fails;
the units applied before it stay applied. Like plan, apply runs OpenTofu in
the workspace on disk.`,
		Example: "  nodr apply --unit terraform/pve-main-compute",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Fail before planning, which can take a while.
			if !autoApprove && !a.interactive {
				return usageError{errors.New("stdin is not a terminal, so apply cannot ask for confirmation; pass --auto-approve to apply without asking")}
			}
			return a.withPlanDir(func(planDir string) error {
				return a.apply(cmd.Context(), planDir, units, autoApprove, allowDestroy)
			})
		},
	}
	cmd.Flags().StringArrayVar(&units, "unit", nil, "apply only the state unit in `dir`, relative to the workspace, such as terraform/pve-main-compute; repeat it for more units")
	cmd.Flags().BoolVar(&autoApprove, "auto-approve", false, "apply without asking for confirmation")
	cmd.Flags().BoolVar(&allowDestroy, "allow-destroy", false, "apply plans that replace or destroy resources")
	return cmd
}

// unitPlan is the saved plan of a state unit.
type unitPlan struct {
	// dir is the directory of the unit in the workspace, such as
	// terraform/pve-main-compute.
	dir    string
	runner *opentofu.Runner
	// file is the path of the saved plan.
	file string
	plan opentofu.Plan
}

// withPlanDir calls f with a new private directory for saved plans, and
// removes the directory afterwards, since plans can hold secrets.
func (a *app) withPlanDir(f func(planDir string) error) error {
	dir, err := os.MkdirTemp("", "nodr-plan-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(a.stderr, "nodr: warning: cannot remove the saved plans: %v\n", err)
		}
	}()
	return f(dir)
}

// planUnits compiles the workspace, writes the files that change, and plans
// the state units whose directories are in only, or all units if only is
// empty, into plan files in planDir. It prints the written files and a
// summary of each plan to stdout; the output of OpenTofu goes to stderr.
// It stops at the first unit that fails.
func (a *app) planUnits(ctx context.Context, only []string, planDir string) ([]unitPlan, error) {
	// Without OpenTofu there is nothing to plan, and no file should change.
	bin, err := opentofu.LookPath()
	if err != nil {
		return nil, err
	}
	l, err := a.mustLoad()
	if err != nil {
		return nil, err
	}
	files, diags := compile.Compile(l.ws)
	a.printDiagnostics(diags)
	if diags.HasErrors() {
		fmt.Fprintf(a.stderr, "nodr: compilation failed; no file was changed\n")
		return nil, errReported
	}
	// Check --unit before any file changes.
	units, err := stateUnits(l.ws.FS, files)
	if err != nil {
		return nil, err
	}
	if units, err = selectUnits(units, only); err != nil {
		return nil, err
	}
	// Git is the ledger, so what OpenTofu plans is the code in the workspace.
	if err := compile.Write(l.ws, files); err != nil {
		return nil, err
	}
	for _, p := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(a.stdout, "wrote %s\n", p)
	}

	if len(units) == 0 {
		fmt.Fprintf(a.stdout, "no state units: no directory below %s/ holds .tf files\n", terraformDir)
		return nil, nil
	}
	plans := make([]unitPlan, 0, len(units))
	for _, dir := range units {
		u := unitPlan{
			dir: dir,
			runner: &opentofu.Runner{
				Binary:     bin,
				Dir:        filepath.Join(l.ws.Root, filepath.FromSlash(dir)),
				Stdout:     a.stderr,
				Stderr:     a.stderr,
				Interrupts: a.interrupts,
			},
			// Units are right below terraform/, so their names differ.
			file: filepath.Join(planDir, path.Base(dir)+".tfplan"),
		}
		if err := u.runner.Init(ctx); err != nil {
			return nil, unitError(dir, err)
		}
		if u.plan, err = u.runner.Plan(ctx, u.file); err != nil {
			return nil, unitError(dir, err)
		}
		a.printPlan(dir, u.plan)
		plans = append(plans, u)
	}
	return plans, nil
}

// apply plans the units like planUnits, checks the plans, asks for
// confirmation unless autoApprove is set, and applies the saved plan of
// each unit with changes.
func (a *app) apply(ctx context.Context, planDir string, only []string, autoApprove, allowDestroy bool) error {
	plans, err := a.planUnits(ctx, only, planDir)
	if err != nil {
		return err
	}
	plans = slices.DeleteFunc(plans, func(u unitPlan) bool { return !u.plan.HasChanges() })
	if len(plans) == 0 {
		fmt.Fprintln(a.stdout, "nothing to apply")
		return nil
	}
	if destroyed := destructiveChanges(plans); len(destroyed) > 0 && !allowDestroy {
		fmt.Fprintln(a.stderr, "nodr: the plan replaces or destroys resources:")
		for _, line := range destroyed {
			fmt.Fprintf(a.stderr, "  %s\n", line)
		}
		fmt.Fprintln(a.stderr, "nodr: nothing was applied; run apply with --allow-destroy to make these changes")
		return errReported
	}
	if !autoApprove {
		approved, err := a.confirm(ctx)
		if err != nil {
			return err
		}
		if !approved {
			fmt.Fprintln(a.stderr, "nodr: apply canceled; nothing was applied")
			return errReported
		}
	}
	for i, u := range plans {
		if err := u.runner.Apply(ctx, u.file); err != nil {
			result := "failed"
			if ctx.Err() != nil {
				result = "interrupted"
			}
			fmt.Fprintf(a.stdout, "%s: %s\n", u.dir, result)
			for _, rest := range plans[i+1:] {
				fmt.Fprintf(a.stdout, "%s: not applied\n", rest.dir)
			}
			return unitError(u.dir, err)
		}
		fmt.Fprintf(a.stdout, "%s: applied\n", u.dir)
	}
	return nil
}

// confirm asks whether to apply the changes, and reads the answer from
// stdin. Only "yes" approves them. The question goes to stdout, next to the
// summary of the plans that it is about, so it shows when the output of
// OpenTofu on stderr is hidden. If ctx is done before the answer comes,
// confirm returns an error.
func (a *app) confirm(ctx context.Context) (bool, error) {
	fmt.Fprint(a.stdout, "Apply these changes? Only 'yes' is accepted: ")
	type answer struct {
		text string
		err  error
	}
	answers := make(chan answer, 1)
	// A read of stdin cannot be interrupted, so it runs on its own and is
	// abandoned if ctx is done first.
	go func() {
		text, err := bufio.NewReader(a.stdin).ReadString('\n')
		answers <- answer{text, err}
	}()
	select {
	case ans := <-answers:
		if ans.err != nil && !errors.Is(ans.err, io.EOF) {
			return false, fmt.Errorf("read the answer: %w", ans.err)
		}
		// Only the end of the line may follow, as \n or \r\n.
		return strings.TrimSuffix(strings.TrimSuffix(ans.text, "\n"), "\r") == "yes", nil
	case <-ctx.Done():
		// End the line of the question.
		fmt.Fprintln(a.stdout)
		return false, fmt.Errorf("interrupted; nothing was applied: %w", ctx.Err())
	}
}

// printPlan prints the summary of the plan of the unit in dir: a header
// with the number of resources to add, change, replace and destroy, and a
// line for each change. An instance that the plan moves or imports gets a
// line for that before the line for its action, if it has one.
func (a *app) printPlan(dir string, p opentofu.Plan) {
	if !p.HasChanges() {
		fmt.Fprintf(a.stdout, "%s: no changes\n", dir)
		return
	}
	s := p.Summary()
	header := fmt.Sprintf("%s: %d to add, %d to change, %d to replace, %d to destroy", dir, s.Create, s.Update, s.Replace, s.Delete)
	// Reading a data source, and forgetting, importing and moving a
	// resource change no infrastructure, so they are counted only if the
	// plan has them.
	if s.Read > 0 {
		header += fmt.Sprintf(", %d to read", s.Read)
	}
	if s.Forget > 0 {
		header += fmt.Sprintf(", %d to forget", s.Forget)
	}
	if s.Import > 0 {
		header += fmt.Sprintf(", %d to import", s.Import)
	}
	if s.Move > 0 {
		header += fmt.Sprintf(", %d to move", s.Move)
	}
	fmt.Fprintln(a.stdout, header)
	for _, c := range p.Changes {
		if c.PreviousAddress != "" {
			fmt.Fprintf(a.stdout, "  %-7s  %s to %s\n", "move", c.PreviousAddress, c.Address)
		}
		if c.Importing {
			fmt.Fprintf(a.stdout, "  %-7s  %s\n", "import", c.Address)
		}
		if c.Action != opentofu.NoOp {
			fmt.Fprintf(a.stdout, "  %-7s  %s\n", actionName(c.Action), c.Address)
		}
	}
}

// actionName returns the word for an action in the summary of a plan.
func actionName(action opentofu.Action) string {
	switch action {
	case opentofu.Create:
		return "add"
	case opentofu.Update:
		return "change"
	case opentofu.Delete:
		return "destroy"
	default:
		// replace, read and forget
		return string(action)
	}
}

// destructiveChanges returns a line for each change in plans that replaces
// or destroys a resource.
func destructiveChanges(plans []unitPlan) []string {
	var lines []string
	for _, u := range plans {
		for _, c := range u.plan.Changes {
			if c.Action == opentofu.Replace || c.Action == opentofu.Delete {
				lines = append(lines, fmt.Sprintf("%s: %s %s", u.dir, actionName(c.Action), c.Address))
			}
		}
	}
	return lines
}

// unitError describes a failure of OpenTofu in the unit in dir. OpenTofu
// wrote its output to stderr already, so a failed command is named without
// the end of that output, which a CommandError holds.
func unitError(dir string, err error) error {
	var cmdErr *opentofu.CommandError
	if errors.As(err, &cmdErr) {
		return fmt.Errorf("%s: %s: %w", dir, cmdErr.Command, cmdErr.Err)
	}
	return fmt.Errorf("%s: %w", dir, err)
}

// stateUnits returns the directories of the state units in fsys, the
// directories right below terraform/ that hold .tf files, in sorted order
// (design §5.5). Directories further down, such as those of local modules,
// are not units. pending holds the files that are about to be written, by
// workspace-relative path, and they can add units.
func stateUnits(fsys fs.FS, pending map[string][]byte) ([]string, error) {
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

// selectUnits returns the units whose directories are in only, in the order
// of units, or all units if only is empty. The directories in only are
// relative to the workspace, as nodr prints them, and may end with a slash.
func selectUnits(units, only []string) ([]string, error) {
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
