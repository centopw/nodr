//go:build !unix

package opentofu

import "os/exec"

// ownProcessGroup does nothing on systems without Unix process groups.
func ownProcessGroup(*exec.Cmd) {}
