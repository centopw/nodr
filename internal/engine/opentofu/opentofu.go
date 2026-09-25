// Package opentofu runs the OpenTofu command line on a state unit: the root
// module in one directory, with its own state (design §5.5). A Runner
// initializes the unit, saves a plan to a file, reads the resource changes
// in that plan, and applies exactly the saved plan, so what runs is what was
// shown.
package opentofu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// EnvBinary is the environment variable that selects the OpenTofu binary.
const EnvBinary = "NODR_TOFU"

// ErrNotFound reports that the OpenTofu binary cannot be found.
var ErrNotFound = errors.New("OpenTofu not found")

// LookPath returns the absolute path of the OpenTofu binary: the program
// that NODR_TOFU names, as a path or as a name to look up on PATH, or else
// tofu on PATH. An empty NODR_TOFU counts as unset.
func LookPath() (string, error) {
	if name := os.Getenv(EnvBinary); name != "" {
		path, err := exec.LookPath(name)
		if err != nil {
			var execErr *exec.Error
			if errors.As(err, &execErr) {
				err = execErr.Err
			}
			return "", fmt.Errorf("%w: %s is set to %q: %w", ErrNotFound, EnvBinary, name, err)
		}
		// OpenTofu runs in the unit, where a relative path would point
		// elsewhere.
		return filepath.Abs(path)
	}
	path, err := exec.LookPath("tofu")
	if err != nil {
		return "", fmt.Errorf("%w: no tofu on PATH; install OpenTofu (https://opentofu.org/docs/intro/install/), or set %s to the path of the tofu binary", ErrNotFound, EnvBinary)
	}
	return filepath.Abs(path)
}

// Runner runs OpenTofu commands on one state unit. Every command gets
// -no-color, and -input=false where the command takes it, so OpenTofu never
// waits for input. The commands inherit the environment of nodr, so
// provider credentials such as PROXMOX_VE_API_TOKEN and the encryption
// settings in TF_ENCRYPTION reach OpenTofu. They also get TF_IN_AUTOMATION,
// so OpenTofu does not suggest commands to run next.
type Runner struct {
	// Binary is the path of the tofu binary, as LookPath returns it.
	Binary string
	// Dir is the directory of the unit: its root module.
	Dir string
	// Env holds variables in the form "key=value" that are added to the
	// environment of the commands. They take precedence over inherited
	// variables.
	Env []string
	// Stdout and Stderr receive the output of OpenTofu while it runs.
	// Either may be nil, and both may be the same writer. A writer that
	// fails gets no more output, but the command goes on: stopping
	// OpenTofu in the middle of an apply would do more harm than losing
	// its output.
	Stdout, Stderr io.Writer
	// Interrupts, if not nil, delivers the interrupts that come after the
	// one that makes the context of a command done, such as a second
	// Ctrl-C. Each one is passed on to OpenTofu, which takes a second
	// interrupt as an order to exit at once, even if that loses state.
	// When the context is done, Stderr also gets a line that says so.
	Interrupts <-chan struct{}
}

// NewRunner returns a Runner for the unit in dir, with the binary that
// LookPath finds.
func NewRunner(dir string) (*Runner, error) {
	bin, err := LookPath()
	if err != nil {
		return nil, err
	}
	return &Runner{Binary: bin, Dir: dir}, nil
}

// Init initializes the unit, as tofu init does: it configures the backend
// and installs the providers and modules that the unit needs.
func (r *Runner) Init(ctx context.Context) error {
	return r.run(ctx, r.Stdout, "init", "-input=false", "-no-color")
}

// Plan plans the changes to the unit, saves the plan to planFile and
// returns the resource changes in the saved plan. A relative planFile is
// relative to the current directory, not to the unit. The saved plan holds
// the values of the resources, secrets included, unless TF_ENCRYPTION
// encrypts plans.
func (r *Runner) Plan(ctx context.Context, planFile string) (Plan, error) {
	path, err := absPlanFile(planFile)
	if err != nil {
		return Plan{}, err
	}
	if err := r.run(ctx, r.Stdout, "plan", "-input=false", "-no-color", "-out="+path); err != nil {
		return Plan{}, err
	}
	// show has no -input flag; it never asks for input.
	var out bytes.Buffer
	if err := r.run(ctx, &out, "show", "-json", "-no-color", path); err != nil {
		return Plan{}, err
	}
	p, err := ParsePlan(out.Bytes())
	if err != nil {
		return Plan{}, fmt.Errorf("plan %s: %w", path, err)
	}
	return p, nil
}

// Apply applies the plan that Plan saved in planFile. It never plans again,
// so it makes exactly the changes that the plan shows, and OpenTofu refuses
// a plan that is stale because the state changed after it was made. A
// relative planFile is relative to the current directory.
func (r *Runner) Apply(ctx context.Context, planFile string) error {
	path, err := absPlanFile(planFile)
	if err != nil {
		return err
	}
	return r.run(ctx, r.Stdout, "apply", "-input=false", "-no-color", path)
}

// absPlanFile returns the absolute path of a plan file, because OpenTofu
// runs in the unit.
func absPlanFile(planFile string) (string, error) {
	if planFile == "" {
		return "", errors.New("the path of the plan file is empty")
	}
	return filepath.Abs(planFile)
}

// CommandError reports an OpenTofu command that failed.
type CommandError struct {
	// Command is the command line, such as
	// "tofu init -input=false -no-color".
	Command string
	// Dir is the directory of the unit that the command ran in.
	Dir string
	// Stderr holds the last lines that the command wrote to stderr.
	Stderr string
	// Err is the cause: usually an *exec.ExitError, or the error of the
	// context if it was done.
	Err error
}

// Error returns the command, its directory and the cause on one line,
// followed by the last lines of stderr.
func (e *CommandError) Error() string {
	msg := fmt.Sprintf("%s in %s: %v", e.Command, e.Dir, e.Err)
	if e.Stderr != "" {
		msg += "\n" + e.Stderr
	}
	return msg
}

// Unwrap returns the cause.
func (e *CommandError) Unwrap() error { return e.Err }

// interruptNote is the line that Stderr gets when the context of a command
// is done while OpenTofu runs, if the Runner passes on more interrupts.
const interruptNote = "nodr: waiting for OpenTofu to stop; a second interrupt stops it at once and may lose state"

// run runs tofu with args in the unit. The output on stdout goes to stdout,
// and the output on stderr to r.Stderr.
//
// When ctx is done, OpenTofu gets one interrupt, so it can stop cleanly:
// finish the operations in progress, save the state and release its lock.
// run waits for that as long as it takes, and never kills OpenTofu, since
// that can lose state. The interrupts in r.Interrupts reach OpenTofu too.
func (r *Runner) run(ctx context.Context, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1")
	cmd.Env = append(cmd.Env, r.Env...)
	ownProcessGroup(cmd)

	var mu sync.Mutex
	stderr := &output{mu: &mu, w: r.Stderr, tail: new(tail)}
	cmd.Stderr = stderr
	if stdout != nil {
		cmd.Stdout = &output{mu: &mu, w: stdout}
	}
	// exec calls Cancel once, when ctx is done. cmd.WaitDelay stays zero,
	// so exec does not kill OpenTofu afterwards.
	cmd.Cancel = func() error {
		err := cmd.Process.Signal(os.Interrupt)
		if r.Interrupts != nil && r.Stderr != nil && !errors.Is(err, os.ErrProcessDone) {
			mu.Lock()
			fmt.Fprintln(r.Stderr, interruptNote)
			mu.Unlock()
		}
		return err
	}
	err := cmd.Start()
	if err == nil {
		stop := r.forwardInterrupts(cmd.Process)
		err = cmd.Wait()
		stop()
	}
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		// OpenTofu stopped because it was interrupted.
		err = ctx.Err()
	}
	return &CommandError{
		Command: strings.Join(append([]string{filepath.Base(r.Binary)}, args...), " "),
		Dir:     r.Dir,
		Stderr:  stderr.tail.String(),
		Err:     err,
	}
}

// forwardInterrupts passes each interrupt in r.Interrupts on to OpenTofu,
// which runs in p, until the function it returns is called.
func (r *Runner) forwardInterrupts(p *os.Process) (stop func()) {
	if r.Interrupts == nil {
		return func() {}
	}
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			case <-r.Interrupts:
				// This fails if OpenTofu has exited, and on Windows, where
				// OpenTofu gets the interrupts from the console instead.
				_ = p.Signal(os.Interrupt)
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// output passes the output of a command on to w, which may be nil, and
// keeps the end of it in tail, unless tail is nil. The output of stdout and
// stderr is copied concurrently, so writes hold mu: the caller may pass the
// same writer for both.
type output struct {
	mu     *sync.Mutex
	w      io.Writer
	failed bool
	tail   *tail
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.tail != nil {
		o.tail.add(p)
	}
	if o.w != nil && !o.failed {
		if _, err := o.w.Write(p); err != nil {
			o.failed = true
		}
	}
	return len(p), nil
}

// Size of the tail of stderr that a CommandError holds.
const (
	tailLines = 20
	// tailBytes bounds the memory that the tail takes up.
	tailBytes = 8 << 10
)

// tail keeps the end of a stream.
type tail struct {
	buf []byte
}

func (t *tail) add(p []byte) {
	t.buf = append(t.buf, p...)
	if n := len(t.buf) - tailBytes; n > 0 {
		t.buf = append(t.buf[:0], t.buf[n:]...)
	}
}

// String returns the last tailLines lines, without the blank lines around
// them.
func (t *tail) String() string {
	lines := strings.Split(string(t.buf), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	lines = lines[max(0, len(lines)-tailLines):]
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}
