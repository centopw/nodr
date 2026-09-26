package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	nodrapi "github.com/centopw/nodr/internal/api"
	"github.com/centopw/nodr/internal/webui"
)

func (a *app) serverCommand() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the nodr API and web UI",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			loaded, err := a.mustLoad()
			if err != nil {
				return err
			}
			return runServer(cmd.Context(), loaded.ws.Root, addr, a.stdout)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "HTTP listen `address`")
	return cmd
}

func runServer(ctx context.Context, root, addr string, stdout io.Writer) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	mux := http.NewServeMux()
	mux.Handle("/api/", nodrapi.Handler(root))
	mux.Handle("/api", nodrapi.Handler(root))
	mux.Handle("/", webui.Handler())
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	fmt.Fprintf(stdout, "listening on %s\n", listener.Addr())

	select {
	case err := <-serveDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		err := <-serveDone
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
