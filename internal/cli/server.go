package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	nodrapi "github.com/centopw/nodr/internal/api"
	"github.com/centopw/nodr/internal/authn"
	"github.com/centopw/nodr/internal/secrets"
	"github.com/centopw/nodr/internal/webui"
)

func (a *app) serverCommand() *cobra.Command {
	var addr, kekFile string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the nodr API and web UI",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			loaded, err := a.mustLoad()
			if err != nil {
				return err
			}
			kek, err := secrets.LoadKEK(secrets.KEKConfig{FilePath: kekFile, EnvVar: "NODR_KEK"})
			if err != nil {
				return err
			}
			authStore, err := authn.Open(filepath.Join(loaded.ws.Root, ".nodr", "authn.db"))
			if err != nil {
				return err
			}
			defer authStore.Close()
			token := os.Getenv("NODR_BOOTSTRAP_TOKEN")
			// Bootstrap must complete even if the command context is already
			// canceled during shutdown: it is a fast local DB write.
			if err := authStore.InitializeBootstrap(context.WithoutCancel(cmd.Context()), token); err != nil {
				return err
			}
			secretsStore, err := secrets.Open(filepath.Join(loaded.ws.Root, ".nodr", "secrets.db"), kek)
			if err != nil {
				return err
			}
			defer secretsStore.Close()
			return runServer(cmd.Context(), loaded.ws.Root, addr, a.stdout, authStore, secretsStore)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "HTTP listen `address`")
	cmd.Flags().StringVar(&kekFile, "kek-file", "", "path to the 32-byte key encryption key file (or set NODR_KEK to a base64-encoded key)")
	return cmd
}

func runServer(ctx context.Context, root, addr string, stdout io.Writer, auth *authn.Store, secretsStore *secrets.Store) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	mux := http.NewServeMux()
	mux.Handle("/api/", nodrapi.Handler(ctx, root, auth, secretsStore))
	mux.Handle("/api", nodrapi.Handler(ctx, root, auth, secretsStore))
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
