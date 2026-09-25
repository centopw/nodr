//go:build unix

package opentofu

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup starts OpenTofu in a process group of its own. An
// interrupt from the terminal then reaches only nodr, which passes it on:
// the first when the context is done, and the later ones through
// Runner.Interrupts. So OpenTofu gets each interrupt once, and stops
// cleanly after the first, or at once after the second, which can lose
// state.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
