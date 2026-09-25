package opentofu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// fakeTofu writes a shell script named tofu that runs body to a new
// directory, and returns its path.
func fakeTofu(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tofu is a shell script")
	}
	path := filepath.Join(t.TempDir(), "tofu")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLookPath(t *testing.T) {
	fake := fakeTofu(t, "exit 0\n")
	found := func(t *testing.T) {
		t.Helper()
		if got, err := LookPath(); err != nil || got != fake {
			t.Errorf("LookPath() = %q, %v; want %q", got, err, fake)
		}
	}
	notFound := func(t *testing.T, wants ...string) {
		t.Helper()
		_, err := LookPath()
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("LookPath() error = %v, want ErrNotFound", err)
		}
		for _, want := range wants {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("LookPath() error = %v, want it to mention %q", err, want)
			}
		}
	}

	t.Run("NODR_TOFU", func(t *testing.T) {
		t.Setenv(EnvBinary, fake)
		t.Setenv("PATH", t.TempDir())
		found(t)
	})
	t.Run("relative NODR_TOFU", func(t *testing.T) {
		t.Chdir(filepath.Dir(fake))
		t.Setenv(EnvBinary, "./tofu")
		found(t)
	})
	t.Run("PATH", func(t *testing.T) {
		t.Setenv(EnvBinary, "")
		t.Setenv("PATH", filepath.Dir(fake))
		found(t)
	})
	t.Run("NODR_TOFU does not exist", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "tofu")
		t.Setenv(EnvBinary, missing)
		t.Setenv("PATH", filepath.Dir(fake))
		notFound(t, EnvBinary, missing)
	})
	t.Run("nothing", func(t *testing.T) {
		t.Setenv(EnvBinary, "")
		t.Setenv("PATH", t.TempDir())
		notFound(t, "install OpenTofu", EnvBinary)
	})
}

// recorder is a fake tofu that appends its arguments and the variables that
// TestRunner passes to calls.log in its working directory. It prints a plan
// with one create for show, and writes a line to stdout and to stderr for
// the other commands.
const recorder = `echo "$* | $PROXMOX_VE_API_TOKEN | $TF_ENCRYPTION | $TF_IN_AUTOMATION" >> calls.log
if [ "$1" = show ]; then
	echo '{"format_version": "1.2", "resource_changes": [{"address": "terraform_data.x", "type": "terraform_data", "change": {"actions": ["create"]}}]}'
else
	echo "running $1"
	echo "warning from $1" >&2
fi
`

func TestRunner(t *testing.T) {
	t.Setenv("PROXMOX_VE_API_TOKEN", "inherited-token")
	t.Setenv("TF_ENCRYPTION", "inherited-encryption")
	t.Setenv(EnvBinary, fakeTofu(t, recorder))
	unit := t.TempDir()
	r, err := NewRunner(unit)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	r.Stdout, r.Stderr = &stdout, &stderr
	r.Env = []string{"TF_ENCRYPTION=runner-encryption"}

	// A relative plan file is relative to the current directory.
	t.Chdir(t.TempDir())
	planFile, err := filepath.Abs("unit.tfplan")
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := r.Plan(ctx, "unit.tfplan")
	if err != nil {
		t.Fatal(err)
	}
	if want := []Change{{Address: "terraform_data.x", Type: "terraform_data", Action: Create}}; !reflect.DeepEqual(p.Changes, want) {
		t.Errorf("Plan changes = %v, want %v", p.Changes, want)
	}
	if err := r.Apply(ctx, "unit.tfplan"); err != nil {
		t.Fatal(err)
	}

	calls, err := os.ReadFile(filepath.Join(unit, "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	const env = " | inherited-token | runner-encryption | 1\n"
	want := "init -input=false -no-color" + env +
		"plan -input=false -no-color -out=" + planFile + env +
		"show -json -no-color " + planFile + env +
		"apply -input=false -no-color " + planFile + env
	if string(calls) != want {
		t.Errorf("calls =\n%s\nwant\n%s", calls, want)
	}
	// The JSON of show is read, not passed on.
	if want := "running init\nrunning plan\nrunning apply\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if want := "warning from init\nwarning from plan\nwarning from apply\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}

	if err := r.Apply(ctx, ""); err == nil {
		t.Error("Apply without a plan file succeeded")
	}
}

func TestRunnerError(t *testing.T) {
	unit := t.TempDir()
	r := &Runner{
		Binary: fakeTofu(t, "echo 'Error: Unsupported argument' >&2\nexit 1\n"),
		Dir:    unit,
	}
	err := r.Init(t.Context())
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("Init error = %v, want a *CommandError", err)
	}
	if want := "tofu init -input=false -no-color"; cmdErr.Command != want {
		t.Errorf("Command = %q, want %q", cmdErr.Command, want)
	}
	if want := "Error: Unsupported argument"; cmdErr.Stderr != want {
		t.Errorf("Stderr = %q, want %q", cmdErr.Stderr, want)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Errorf("Init error = %v, want exit status 1", err)
	}
	want := "tofu init -input=false -no-color in " + unit + ": exit status 1\nError: Unsupported argument"
	if err.Error() != want {
		t.Errorf("Init error =\n%s\nwant\n%s", err, want)
	}
}

// failWriter fails every write, like the stream of a client that left.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("connection closed") }

func TestRunnerOutput(t *testing.T) {
	// The fake tofu writes to stdout and stderr in turn, and the two
	// streams are copied concurrently.
	bin := fakeTofu(t, `i=1
while [ $i -le 100 ]; do
	echo "out $i"
	echo "err $i" >&2
	i=$((i + 1))
done
`)
	var out bytes.Buffer
	r := &Runner{Binary: bin, Dir: t.TempDir(), Stdout: &out, Stderr: &out}
	if err := r.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), "\n"); n != 200 {
		t.Errorf("got %d lines of output in one writer, want 200", n)
	}

	r.Stdout, r.Stderr = failWriter{}, failWriter{}
	if err := r.Init(t.Context()); err != nil {
		t.Errorf("Init with failing writers: %v", err)
	}
}

// cancelWriter cancels a context when it is written to.
type cancelWriter context.CancelFunc

func (c cancelWriter) Write(p []byte) (int, error) {
	c()
	return len(p), nil
}

func TestRunnerInterruptsWhenContextIsDone(t *testing.T) {
	// The fake tofu reports an interrupt the way OpenTofu does, after it
	// finishes what it is doing. It is ready once it prints to stdout.
	bin := fakeTofu(t, `trap 'echo "Interrupt received." >&2; exit 1' INT
echo ready
while :; do sleep 1; done
`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := &Runner{Binary: bin, Dir: t.TempDir(), Stdout: cancelWriter(cancel)}
	err := r.Init(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Init error = %v, want context.Canceled", err)
	}
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) || cmdErr.Stderr != "Interrupt received." {
		t.Errorf("Init error = %v, want the fake tofu to stop after an interrupt", err)
	}
}

func TestTail(t *testing.T) {
	var tl tail
	tl.add([]byte("\nError: first\n"))
	for i := 1; i <= 30; i++ {
		tl.add(fmt.Appendf(nil, "line %d\n", i))
	}
	tl.add([]byte("\n\n"))
	lines := strings.Split(tl.String(), "\n")
	if len(lines) != tailLines || lines[0] != "line 11" || lines[len(lines)-1] != "line 30" {
		t.Errorf("String() =\n%s\nwant lines 11 to 30", tl.String())
	}

	// A long stream keeps only its end.
	tl.add(bytes.Repeat([]byte("x"), 3*tailBytes))
	tl.add([]byte("\nError: last\n"))
	if got := tl.String(); len(tl.buf) > tailBytes || !strings.HasSuffix(got, "\nError: last") {
		t.Errorf("after a long stream, the tail holds %d bytes and ends with %q", len(tl.buf), got[max(0, len(got)-20):])
	}

	var empty tail
	if s := empty.String(); s != "" {
		t.Errorf("String() of an empty tail = %q", s)
	}
}
