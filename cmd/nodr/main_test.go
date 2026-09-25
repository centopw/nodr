//go:build unix

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestMain runs the test binary as nodr if NODR_TEST_MAIN is set, so that
// tests can send signals to a nodr process.
func TestMain(m *testing.M) {
	if os.Getenv("NODR_TEST_MAIN") != "" {
		main()
	}
	os.Exit(m.Run())
}

// fakeTofu plans to create one resource. Its apply writes to
// $FAKE_TOFU_STARTED when it starts, runs until it gets an interrupt, and
// then writes to $FAKE_TOFU_STOPPED.
const fakeTofu = `#!/bin/sh
case $1 in
plan)
	for arg in "$@"; do
		case $arg in -out=*) echo "saved plan" > "${arg#-out=}" ;; esac
	done
	;;
show)
	echo '{"format_version": "1.2", "resource_changes": [{"address": "terraform_data.x", "type": "terraform_data", "change": {"actions": ["create"]}}]}'
	;;
apply)
	trap 'echo interrupted > "$FAKE_TOFU_STOPPED"; exit 1' INT
	echo started > "$FAKE_TOFU_STARTED"
	while :; do
		echo applying
		sleep 0.1
	done
	;;
esac
`

// TestTerminate checks that nodr stops OpenTofu with an interrupt when it
// is asked to terminate, and waits for it.
func TestTerminate(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, perm os.FileMode) string {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), perm); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tofu := write("bin/tofu", fakeTofu, 0o755)
	write("ws/nodr.yaml", "apiVersion: nodr/v1alpha1\nkind: Workspace\nmetadata:\n  name: test\nspec: {}\n", 0o644)
	write("ws/terraform/unit/main.tf", "resource \"terraform_data\" \"x\" {\n}\n", 0o644)
	started, stopped := filepath.Join(dir, "started"), filepath.Join(dir, "stopped")

	cmd := exec.Command(os.Args[0], "apply", "--auto-approve", "-w", filepath.Join(dir, "ws"))
	cmd.Env = append(os.Environ(), "NODR_TEST_MAIN=1", "NODR_TOFU="+tofu, "FAKE_TOFU_STARTED="+started, "FAKE_TOFU_STOPPED="+stopped)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("the apply of OpenTofu did not start\nstdout:\n%s\nstderr:\n%s", &stdout, &stderr)
		}
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Errorf("nodr ended with %v, want exit status 1\nstderr:\n%s", err, &stderr)
	}
	if data, err := os.ReadFile(stopped); err != nil || string(data) != "interrupted\n" {
		t.Errorf("OpenTofu was not interrupted: %q, %v", data, err)
	}
	if want := "terraform/unit: interrupted\n"; !bytes.HasSuffix(stdout.Bytes(), []byte(want)) {
		t.Errorf("stdout =\n%s\nwant it to end with\n%s", &stdout, want)
	}
}
