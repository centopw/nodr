package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/centopw/nodr/internal/engine/opentofu"
	"github.com/centopw/nodr/internal/planapply"
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
func (a *app) planUnits(ctx context.Context, only []string, planDir string) ([]planapply.UnitPlan, error) {
	l, err := a.mustLoad()
	if err != nil {
		return nil, err
	}
	written, plans, err := planapply.PlanUnits(ctx, l.ws, only, planDir, a.stderr, a.interrupts)
	for _, p := range slices.Sorted(maps.Keys(written)) {
		fmt.Fprintf(a.stdout, "wrote %s\n", p)
	}
	if err != nil {
		var compErr *planapply.CompileError
		if errors.As(err, &compErr) {
			fmt.Fprintf(a.stderr, "nodr: compilation failed; no file was changed\n")
			return nil, errReported
		}
		return nil, err
	}
	if len(plans) == 0 {
		fmt.Fprintf(a.stdout, "no state units: no directory below %s/ holds .tf files\n", terraformDir)
		return nil, nil
	}
	for _, u := range plans {
		a.printPlan(u.Dir, u.Plan)
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
	plans = slices.DeleteFunc(plans, func(u planapply.UnitPlan) bool { return !u.Plan.HasChanges() })
	if len(plans) == 0 {
		fmt.Fprintln(a.stdout, "nothing to apply")
		return nil
	}
	if destroyed := planapply.DestructiveChanges(plans); len(destroyed) > 0 && !allowDestroy {
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
	applied, err := planapply.Apply(ctx, plans, a.stderr, a.interrupts)
	for _, dir := range applied {
		fmt.Fprintf(a.stdout, "%s: applied\n", dir)
	}
	if err != nil {
		failedDir := plans[len(applied)].Dir
		result := "failed"
		if ctx.Err() != nil {
			result = "interrupted"
		}
		fmt.Fprintf(a.stdout, "%s: %s\n", failedDir, result)
		for _, rest := range plans[len(applied)+1:] {
			fmt.Fprintf(a.stdout, "%s: not applied\n", rest.Dir)
		}
		return err
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
