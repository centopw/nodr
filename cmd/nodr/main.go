// Command nodr is the command line of nodr, a hybrid infrastructure-as-code
// and GUI platform for SME and homelab infrastructure.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/centopw/nodr/internal/cli"
)

func main() {
	// OpenTofu runs in a process group of its own, so signals for the group
	// of nodr do not reach it. A termination request is handled like an
	// interrupt: nodr interrupts OpenTofu and waits for it to stop, rather
	// than exit at once and leave OpenTofu to die on its next write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
