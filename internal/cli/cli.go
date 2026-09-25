// Package cli implements the nodr command line (design §11.7).
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/workspace"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// errReported marks an error whose details were already written, so Run
// only sets the exit code.
var errReported = errors.New("reported")

// usageError is an error in how the command was called.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// Run executes the nodr command line with args, and returns the exit code.
// Commands that ask for confirmation read the answer from stdin, and ask
// only if stdin is a terminal. The first interrupt cancels ctx, and
// interrupts, which may be nil, delivers the later ones, which reach
// OpenTofu (see opentofu.Runner.Interrupts).
func Run(ctx context.Context, interrupts <-chan struct{}, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr, interactive: isTerminal(stdin), interrupts: interrupts}
	return a.run(ctx, args)
}

// run executes the command line with args, and returns the exit code.
func (a *app) run(ctx context.Context, args []string) int {
	root := a.rootCommand()
	root.SetArgs(args)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	err := root.ExecuteContext(ctx)
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errReported):
		return exitError
	case errors.As(err, new(usageError)):
		fmt.Fprintf(a.stderr, "nodr: %v\nRun 'nodr --help' for usage.\n", err)
		return exitUsage
	default:
		fmt.Fprintf(a.stderr, "nodr: %v\n", err)
		return exitError
	}
}

// isTerminal reports whether r is a terminal.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	// interactive reports whether stdin is a terminal, where a person can
	// answer questions.
	interactive bool
	// interrupts delivers the interrupts after the first, which cancels
	// the context.
	interrupts   <-chan struct{}
	workspaceDir string
}

func (a *app) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "nodr",
		Short: "Manage Proxmox VE, OpenWrt and container infrastructure as code or through a GUI",
		Long: `nodr keeps infrastructure intent and engine code in one Git workspace and
keeps the two in sync. These commands work on a workspace checked out on
disk.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&a.workspaceDir, "workspace", "w", "", "workspace directory (default: the closest directory with a nodr.yaml)")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.AddCommand(
		a.versionCommand(),
		a.validateCommand(),
		a.admitCommand(),
		a.renderCommand(),
		a.describeCommand(),
		a.planCommand(),
		a.applyCommand(),
	)
	return root
}

// loaded is a workspace together with the kinds it is validated against.
type loaded struct {
	ws  *workspace.Workspace
	reg *nrm.Registry
}

// load reads and validates the workspace. Diagnostics go to stderr; any
// error stops the command.
func (a *app) load() (*loaded, diag.List, error) {
	root := a.workspaceDir
	if root == "" {
		found, err := workspace.FindRoot(".")
		if err != nil {
			return nil, nil, err
		}
		root = found
	}
	ws, diags := workspace.Load(root)
	if diags.HasErrors() {
		return nil, diags, nil
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		return nil, nil, err
	}
	diags.Append(ws.Validate(reg))
	diags.Sort()
	return &loaded{ws: ws, reg: reg}, diags, nil
}

// mustLoad is load for commands that need a valid workspace: it prints the
// diagnostics and fails if there are errors.
func (a *app) mustLoad() (*loaded, error) {
	l, diags, err := a.load()
	if err != nil {
		return nil, err
	}
	if diags.HasErrors() {
		a.printDiagnostics(diags)
		fmt.Fprintf(a.stderr, "nodr: the workspace has errors; run 'nodr validate' after fixing them\n")
		return nil, errReported
	}
	return l, nil
}

func (a *app) printDiagnostics(diags diag.List) {
	for _, d := range diags {
		fmt.Fprintln(a.stderr, d)
	}
}
