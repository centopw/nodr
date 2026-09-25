//go:build !unix

package opentofu

import "os/exec"

// ownProcessGroup does nothing on systems without Unix process groups.
//
// On Windows, OpenTofu shares the console of nodr, so a Ctrl-C in the
// console reaches OpenTofu itself, the first and the second, as when it
// runs on its own. nodr cannot pass interrupts on there, since
// os.Process.Signal does not support os.Interrupt on Windows: when the
// context of a command is done for another reason than a Ctrl-C, OpenTofu
// goes on, and the Runner waits until it finishes.
func ownProcessGroup(*exec.Cmd) {}
