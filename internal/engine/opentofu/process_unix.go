//go:build unix

package opentofu

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup starts OpenTofu in a process group of its own. An
// interrupt from the terminal then reaches only nodr, which passes it on
// once when its context is done. OpenTofu takes a second interrupt as an
// order to exit at once, which can lose state.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
