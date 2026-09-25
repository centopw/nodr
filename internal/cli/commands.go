package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/centopw/nodr/internal/admission"
	"github.com/centopw/nodr/internal/buildinfo"
	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/lens"
	"github.com/centopw/nodr/internal/lens/proxmoxvm"
	"github.com/centopw/nodr/internal/nrm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/resolve"
)

// terraformDir is where engine code for OpenTofu lives in a workspace.
const terraformDir = "terraform"

func (a *app) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version of nodr",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			fmt.Fprintln(a.stdout, buildinfo.Read())
			return nil
		},
	}
}

func (a *app) validateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check the workspace manifest and intent documents",
		Long: `Validate checks nodr.yaml and every document below intent/: known kinds,
metadata, schemas, semantic rules and references between resources.
It exits with status 1 if there are errors.`,
		Args: noArgs,
		RunE: func(*cobra.Command, []string) error {
			l, diags, err := a.load()
			if err != nil {
				return err
			}
			a.printDiagnostics(diags)
			documents := 0
			if l != nil {
				documents = len(l.ws.Documents)
			}
			errs, warnings := diags.Count(diag.Error), diags.Count(diag.Warning)
			switch {
			case errs > 0:
				fmt.Fprintf(a.stderr, "%s, %s in %s\n", plural(errs, "error"), plural(warnings, "warning"), plural(documents, "document"))
				return errReported
			case warnings > 0:
				fmt.Fprintf(a.stdout, "%s valid, with %s\n", plural(documents, "document"), plural(warnings, "warning"))
			default:
				fmt.Fprintf(a.stdout, "%s valid\n", plural(documents, "document"))
			}
			return nil
		},
	}
}

func (a *app) admitCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "admit [vm/<name>...]",
		Short: "Fill in the values nodr allocates for virtual machines",
		Long: `Admit fills in the fields that nodr allocates for the given virtual
machines, or for all of them, when they are empty: the UID, the node, the
guest ID from the range of the VM's environment in nodr.yaml, and IPv4
addresses for network interfaces with mode auto. It prints one line per
value and, unless --dry-run is given, writes the values into the intent
files. Values that are set never change, and only the new fields are
written, so comments and formatting stay as they are. If a value cannot
be allocated or written, admit changes no file and exits with status 1.
This is the local counterpart of the admission that nodr runs on every
change.`,
		Example: "  nodr admit vm/web-02 --dry-run",
		RunE: func(_ *cobra.Command, args []string) error {
			l, err := a.mustLoad()
			if err != nil {
				return err
			}
			vms, err := l.virtualMachines(args)
			if err != nil {
				return err
			}
			assignments, diags := admission.Plan(l.ws, vms, admission.Options{})
			a.printDiagnostics(diags)
			if diags.HasErrors() {
				fmt.Fprintf(a.stderr, "nodr: admission failed; no file was changed\n")
				return errReported
			}
			// A dry run prepares the edits too, so it fails when a real run
			// would.
			if err := admission.Apply(l.ws, assignments, dryRun); err != nil {
				return err
			}
			for _, as := range assignments {
				fmt.Fprintln(a.stdout, as)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the values without writing them")
	return cmd
}

func (a *app) renderCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "render [vm/<name>...]",
		Short: "Print the engine code nodr generates for virtual machines",
		Long: `Render prints the OpenTofu code that nodr generates for the given virtual
machines, or for all of them. It does not change any file.`,
		Example: "  nodr render vm/web-01",
		RunE: func(_ *cobra.Command, args []string) error {
			l, err := a.mustLoad()
			if err != nil {
				return err
			}
			vms, err := l.virtualMachines(args)
			if err != nil {
				return err
			}
			vmLens := proxmoxvm.Lens{Resolver: resolve.NewIndex(l.ws.Documents)}
			for i, vm := range vms {
				out, err := vmLens.Render(vm)
				if errors.Is(err, proxmoxvm.ErrNotAdmitted) {
					return fmt.Errorf("%w; run 'nodr admit vm/%s' first", err, vm.Metadata.Name)
				}
				if err != nil {
					return err
				}
				if i > 0 {
					fmt.Fprintln(a.stdout)
				}
				if _, err := a.stdout.Write(out); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func (a *app) describeCommand() *cobra.Command {
	var (
		ownership bool
		output    string
	)
	cmd := &cobra.Command{
		Use:   "describe vm/<name>",
		Short: "Show a resource and who owns each of its fields",
		Long: `Describe shows where a resource is defined and where its managed block is.
With --ownership it lists every field with its owner: synced (editable
from the GUI and from code), code-owned (set in code, read-only in the
GUI), extension (only in code) or ignored.`,
		Example: "  nodr describe vm/web-01 --ownership",
		Args:    exactlyOneArg,
		RunE: func(_ *cobra.Command, args []string) error {
			if output != "text" && output != "json" {
				return usageError{fmt.Errorf("unknown output format %q: use text or json", output)}
			}
			l, err := a.mustLoad()
			if err != nil {
				return err
			}
			vms, err := l.virtualMachines(args)
			if err != nil {
				return err
			}
			return a.describe(l, vms[0], ownership, output)
		},
	}
	cmd.Flags().BoolVar(&ownership, "ownership", false, "list the owner of every field")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")
	return cmd
}

func (a *app) describe(l *loaded, vm *v1alpha1.VirtualMachine, ownership bool, output string) error {
	d := vm.Document
	file, src, err := proxmoxvm.FindManaged(l.ws.FS, terraformDir, vm.Metadata.Name)
	notManaged := errors.Is(err, proxmoxvm.ErrNotManaged)
	if err != nil && !notManaged {
		return err
	}
	var report lens.Report
	if !notManaged {
		vmLens := proxmoxvm.Lens{Resolver: resolve.NewIndex(l.ws.Documents)}
		if _, report, err = vmLens.Lift(vm, src, file); err != nil {
			return err
		}
	}
	if output == "json" {
		return a.describeJSON(d, file, report, ownership)
	}

	rows := [][]string{
		{"Name:", d.Metadata.Name},
		{"Kind:", d.Kind},
	}
	if d.Metadata.UID != "" {
		rows = append(rows, []string{"UID:", d.Metadata.UID})
	}
	rows = append(rows, []string{"Intent:", fmt.Sprintf("%s:%d", d.File, d.Line)})
	if notManaged {
		rows = append(rows, []string{"Code:", fmt.Sprintf("no managed block below %s/", terraformDir)})
	} else {
		rows = append(rows,
			[]string{"Code:", fmt.Sprintf("%s (%s %s)", file, proxmoxvm.ResourceType, proxmoxvm.Address(d.Metadata.Name))},
			[]string{"Fields:", fmt.Sprintf("%d synced, %d code-owned, %s",
				report.Count(lens.Synced), report.Count(lens.CodeOwned), plural(report.Count(lens.Extension), "extension"))},
		)
	}
	if err := writeTable(a.stdout, 2, rows); err != nil {
		return err
	}
	if !ownership || notManaged {
		return nil
	}
	fmt.Fprintln(a.stdout)
	rows = [][]string{{"FIELD", "OWNER", "SOURCE", "DETAIL"}}
	for _, f := range report.Fields {
		source := "-"
		if f.Line > 0 {
			source = fmt.Sprintf("%s:%d", file, f.Line)
		}
		rows = append(rows, []string{f.Path, f.Owner.String(), source, f.Reason})
	}
	return writeTable(a.stdout, 3, rows)
}

// writeTable writes rows with aligned columns that are separated by at
// least padding spaces, without trailing spaces.
func writeTable(out io.Writer, padding int, rows [][]string) error {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, padding, ' ', 0)
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, line := range strings.SplitAfter(buf.String(), "\n") {
		if line == "" {
			continue
		}
		if _, err := io.WriteString(out, strings.TrimRight(line, " \n")+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) describeJSON(d *nrm.Document, file string, report lens.Report, ownership bool) error {
	type field struct {
		Path     string `json:"path"`
		CodePath string `json:"codePath"`
		Owner    string `json:"owner"`
		Reason   string `json:"reason,omitempty"`
		Line     int    `json:"line,omitempty"`
	}
	out := struct {
		Name   string  `json:"name"`
		Kind   string  `json:"kind"`
		UID    string  `json:"uid,omitempty"`
		Intent string  `json:"intent"`
		Code   string  `json:"code,omitempty"`
		Fields []field `json:"fields,omitempty"`
	}{Name: d.Metadata.Name, Kind: d.Kind, UID: d.Metadata.UID, Intent: d.File, Code: file}
	if ownership {
		for _, f := range report.Fields {
			out.Fields = append(out.Fields, field{Path: f.Path, CodePath: f.CodePath, Owner: f.Owner.String(), Reason: f.Reason, Line: f.Line})
		}
	}
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// virtualMachines returns the virtual machines named by args, as
// references such as vm/web-01, or all of them in name order if args is
// empty.
func (l *loaded) virtualMachines(args []string) ([]*v1alpha1.VirtualMachine, error) {
	var docs []*nrm.Document
	if len(args) == 0 {
		docs = l.ws.OfKind(v1alpha1.KindVirtualMachine)
		sort.Slice(docs, func(i, j int) bool { return docs[i].Metadata.Name < docs[j].Metadata.Name })
	}
	for _, arg := range args {
		ref, err := l.reg.ParseRef(arg)
		if err != nil {
			return nil, usageError{err}
		}
		if ref.Kind != v1alpha1.KindVirtualMachine {
			return nil, usageError{fmt.Errorf("%s: only virtual machines are supported so far", arg)}
		}
		d := l.ws.Find(ref)
		if d == nil {
			return nil, fmt.Errorf("%s does not exist in the workspace", arg)
		}
		docs = append(docs, d)
	}
	vms := make([]*v1alpha1.VirtualMachine, len(docs))
	for i, d := range docs {
		vm, err := v1alpha1.Decode[v1alpha1.VirtualMachineSpec](d)
		if err != nil {
			return nil, err
		}
		vms[i] = vm
	}
	return vms, nil
}

func noArgs(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageError{fmt.Errorf("unexpected argument %q", args[0])}
	}
	return nil
}

func exactlyOneArg(_ *cobra.Command, args []string) error {
	if len(args) != 1 {
		return usageError{fmt.Errorf("expected one resource such as vm/web-01, got %d arguments", len(args))}
	}
	return nil
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
