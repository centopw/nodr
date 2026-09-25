// Command nodr is the command line of nodr, a hybrid infrastructure-as-code
// and GUI platform for SME and homelab infrastructure.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/centopw/nodr/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
