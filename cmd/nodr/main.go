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
	// of nodr do not reach it. The first interrupt or termination request
	// cancels ctx: nodr interrupts OpenTofu once and waits for it to stop
	// cleanly, however long that takes. Every later one is passed on to
	// OpenTofu, which takes a second interrupt as an order to exit at once.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	interrupts := make(chan struct{}, 1)
	go func() {
		<-signals
		cancel()
		for range signals {
			select {
			case interrupts <- struct{}{}:
			default:
			}
		}
	}()
	os.Exit(cli.Run(ctx, interrupts, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
