//go:build unix

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// fakeTofu plans to create one resource. Its apply writes "started" to
// $FAKE_TOFU_LOG, runs until it gets an interrupt, and then stops cleanly,
// which takes $FAKE_TOFU_STOP tenths of a second, unless a second interrupt
// makes it exit at once. It logs each interrupt, and how it stopped just
// before it exits, which it could not do if it were killed.
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
	interrupts=0
	trap 'interrupts=$((interrupts + 1)); echo "interrupt $interrupts" >> "$FAKE_TOFU_LOG"; if [ $interrupts -ge 2 ]; then echo "stopped at once" >> "$FAKE_TOFU_LOG"; exit 2; fi' INT
	echo started >> "$FAKE_TOFU_LOG"
	while [ $interrupts -eq 0 ]; do
		echo applying
		sleep 0.1
	done
	i=0
	while [ $i -lt "$FAKE_TOFU_STOP" ]; do
		sleep 0.1
		i=$((i + 1))
	done
	echo "stopped cleanly" >> "$FAKE_TOFU_LOG"
	exit 1
	;;
esac
`

// note is the line that tells the user what a second interrupt does.
const note = "nodr: waiting for OpenTofu to stop; a second interrupt stops it at once and may lose state\n"

// TestInterrupts checks that the first interrupt or termination request
// makes OpenTofu stop cleanly, however long that takes, and that a second
// interrupt reaches OpenTofu, which then exits at once. OpenTofu is never
// killed.
func TestInterrupts(t *testing.T) {
	tests := []struct {
		name    string
		signals []os.Signal
		// stop is the time that a clean stop takes, in tenths of a second.
		stop    string
		wantLog string
	}{
		{"terminate", []os.Signal{syscall.SIGTERM}, "10", "started\ninterrupt 1\nstopped cleanly\n"},
		{"interrupt", []os.Signal{os.Interrupt}, "10", "started\ninterrupt 1\nstopped cleanly\n"},
		// A clean stop would take a minute.
		{"interrupt twice", []os.Signal{os.Interrupt, os.Interrupt}, "600", "started\ninterrupt 1\ninterrupt 2\nstopped at once\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			log := filepath.Join(dir, "tofu.log")

			cmd := exec.Command(os.Args[0], "apply", "--auto-approve", "-w", filepath.Join(dir, "ws"))
			cmd.Env = append(os.Environ(), "NODR_TEST_MAIN=1", "NODR_TOFU="+tofu, "FAKE_TOFU_LOG="+log, "FAKE_TOFU_STOP="+tt.stop)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// Each signal goes out once the fake tofu has handled the one
			// before, so that the shell does not merge them.
			wait := "started\n"
			for i, sig := range tt.signals {
				for deadline := time.Now().Add(time.Minute); ; time.Sleep(10 * time.Millisecond) {
					if data, _ := os.ReadFile(log); strings.HasSuffix(string(data), wait) {
						break
					}
					if time.Now().After(deadline) {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
						t.Fatalf("the fake tofu did not log %q\nstderr:\n%s", wait, &stderr)
					}
				}
				if err := cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
				wait = fmt.Sprintf("interrupt %d\n", i+1)
			}

			err := cmd.Wait()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Errorf("nodr ended with %v, want exit status 1\nstderr:\n%s", err, &stderr)
			}
			// nodr waited for the fake tofu, which exited on its own.
			if data, err := os.ReadFile(log); err != nil || string(data) != tt.wantLog {
				t.Errorf("log of the fake tofu = %q, %v; want %q", data, err, tt.wantLog)
			}
			if n := strings.Count(stderr.String(), note); n != 1 {
				t.Errorf("stderr has %d lines about a second interrupt, want 1:\n%s", n, &stderr)
			}
			if want := "terraform/unit: interrupted\n"; !strings.HasSuffix(stdout.String(), want) {
				t.Errorf("stdout =\n%s\nwant it to end with\n%s", &stdout, want)
			}
		})
	}
}
