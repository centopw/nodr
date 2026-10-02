package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	nodrapi "github.com/centopw/nodr/internal/api"
	"github.com/centopw/nodr/internal/authn"
	"github.com/centopw/nodr/internal/bootstrap"
	"github.com/centopw/nodr/internal/secrets"
	"github.com/centopw/nodr/internal/webui"
)

func (a *app) serverCommand() *cobra.Command {
	var addr, kekFile, dataDir string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the nodr API and web UI",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.workspaceDir != "" {
				return a.runWorkspaceServer(cmd.Context(), addr, kekFile)
			}
			return a.runBootstrapServer(cmd.Context(), addr, dataDir, kekFile)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "HTTP listen `address`")
	cmd.Flags().StringVar(&kekFile, "kek-file", "", "path to the 32-byte key encryption key file (or set NODR_KEK to a base64-encoded key)")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "bootstrap data `directory`")
	return cmd
}

func (a *app) runWorkspaceServer(ctx context.Context, addr, kekFile string) error {
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
	if err := authStore.InitializeBootstrap(context.WithoutCancel(ctx), os.Getenv("NODR_BOOTSTRAP_TOKEN")); err != nil {
		return err
	}
	secretsStore, err := secrets.Open(filepath.Join(loaded.ws.Root, ".nodr", "secrets.db"), kek)
	if err != nil {
		return err
	}
	defer secretsStore.Close()
	return runServer(ctx, loaded.ws.Root, addr, a.stdout, authStore, secretsStore)
}

var bootstrapEnvironment = []string{"NODR_BOOTSTRAP_WORKSPACE_NAME", "NODR_BOOTSTRAP_ADMIN_USERNAME", "NODR_BOOTSTRAP_ADMIN_PASSWORD", "NODR_BOOTSTRAP_TOKEN"}

func (a *app) runBootstrapServer(ctx context.Context, addr, dataDir, kekFile string) error {
	kekEnv := ""
	if os.Getenv("NODR_KEK") != "" {
		kekEnv = "NODR_KEK"
	}
	config := bootstrap.Config{DataDir: dataDir, KEKFile: kekFile, KEKEnv: kekEnv}
	dataDir = config.DataRoot()
	values, missing := bootstrapEnv()
	if len(missing) != 0 && len(missing) != len(bootstrapEnvironment) {
		return fmt.Errorf("bootstrap environment missing: %s", strings.Join(missing, ", "))
	}
	if len(missing) == 0 {
		kek, err := bootstrap.ResolveKEK(config, a.stderr)
		if err != nil {
			return err
		}
		service := bootstrap.Service{Config: config}
		if _, _, _, err := service.Setup(ctx, bootstrap.Request{Token: values[3], WorkspaceName: values[0], Username: values[1], Password: values[2]}); err != nil {
			return err
		}
		return a.runRecordedServer(ctx, addr, dataDir, kek)
	}
	state, err := bootstrap.LoadState(dataDir)
	if err == nil {
		kek, err := bootstrap.ResolveKEK(config, a.stderr)
		if err != nil {
			return err
		}
		return a.runRecordedServer(ctx, addr, filepath.Join(dataDir, state.Workspace), kek)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, readErr := os.ReadDir(filepath.Join(dataDir, "workspaces"))
	if readErr == nil && len(entries) != 0 {
		return fmt.Errorf("bootstrap: incomplete workspace directory %q", entries[0].Name())
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if !isLoopback(addr) {
		fmt.Fprintln(a.stderr, "warning: bootstrap endpoint is remotely reachable; it requires the bootstrap token and should be protected by TLS at a reverse proxy")
	}
	token, err := generatedToken()
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "warning: generated bootstrap token %s; treat it as a secret because anyone with it can create the administrator account\n", token)
	switcher := &switchingHandler{}
	setup := &bootstrapSetup{
		service: bootstrap.Service{Config: config, Prepare: switcher.prepareNormal(ctx)},
		token:   token,
	}
	switcher.handler = bootstrapHandler(setup)
	return runHandlerServer(ctx, addr, a.stdout, switcher)
}

func (a *app) runRecordedServer(ctx context.Context, addr, root string, kek []byte) error {
	authStore, err := authn.Open(filepath.Join(root, ".nodr", "authn.db"))
	if err != nil {
		return err
	}
	defer authStore.Close()
	secretsStore, err := secrets.Open(filepath.Join(root, ".nodr", "secrets.db"), kek)
	if err != nil {
		return err
	}
	defer secretsStore.Close()
	return runServer(ctx, root, addr, a.stdout, authStore, secretsStore)
}

func bootstrapEnv() (values, missing []string) {
	for _, name := range bootstrapEnvironment {
		value, ok := os.LookupEnv(name)
		values = append(values, value)
		if !ok || value == "" {
			missing = append(missing, name)
		}
	}
	return
}

func generatedToken() (string, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(token), nil
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

type switchingHandler struct {
	mu      sync.RWMutex
	handler http.Handler
	auth    *authn.Store
	secrets *secrets.Store
}

func (s *switchingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	h := s.handler
	s.mu.RUnlock()
	h.ServeHTTP(w, r)
}

func (s *switchingHandler) prepareNormal(ctx context.Context) func(context.Context, string, []byte) (func(), func(), error) {
	return func(_ context.Context, root string, kek []byte) (func(), func(), error) {
		authStore, err := authn.Open(filepath.Join(root, ".nodr", "authn.db"))
		if err != nil {
			return nil, nil, err
		}
		secretsStore, err := secrets.Open(filepath.Join(root, ".nodr", "secrets.db"), kek)
		if err != nil {
			authStore.Close()
			return nil, nil, err
		}
		handler := normalHandler(ctx, root, authStore, secretsStore)
		commit := func() {
			s.mu.Lock()
			s.auth, s.secrets, s.handler = authStore, secretsStore, handler
			s.mu.Unlock()
		}
		cleanup := func() {
			_ = authStore.Close()
			_ = secretsStore.Close()
		}
		return commit, cleanup, nil
	}
}

type bootstrapSetup struct {
	service bootstrap.Service
	token   string
}

func bootstrapHandler(setup *bootstrapSetup) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/setup", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Token         string `json:"token"`
			WorkspaceName string `json:"workspaceName"`
			Username      string `json:"username"`
			Password      string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			bootstrapProblem(w, http.StatusBadRequest, "Invalid request", "request body must be JSON")
			return
		}
		if request.Token != setup.token {
			bootstrapProblem(w, http.StatusForbidden, "Forbidden", "setup is not available")
			return
		}
		session, csrf, expiresAt, err := setup.service.Setup(r.Context(), bootstrap.Request{Token: setup.token, WorkspaceName: request.WorkspaceName, Username: request.Username, Password: request.Password})
		if err != nil {
			if field, ok := bootstrap.IsValidationError(err); ok {
				bootstrapFieldProblem(w, field)
				return
			}
			if errors.Is(err, authn.ErrSetupUnavailable) || errors.Is(err, authn.ErrInvalidCredentials) {
				bootstrapProblem(w, http.StatusForbidden, "Forbidden", "setup is not available")
				return
			}
			bootstrapProblem(w, http.StatusInternalServerError, "Internal error", "unable to complete setup")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "nodr_session", Value: session, Path: "/", Expires: expiresAt, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			CSRFToken string `json:"csrfToken"`
		}{CSRFToken: csrf})
	})
	mux.HandleFunc("GET /api/v1/setup/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"initialized":false}`))
	})
	mux.Handle("/", webui.Handler())
	return mux
}

func bootstrapProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": title, "status": status, "detail": detail})
}

func bootstrapFieldProblem(w http.ResponseWriter, field string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": "Invalid request", "status": http.StatusBadRequest, "invalid-params": []map[string]string{{"name": field, "reason": "invalid value"}}})
}

func normalHandler(ctx context.Context, root string, auth *authn.Store, secretsStore *secrets.Store) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", nodrapi.HandlerWithoutSetup(ctx, root, auth, secretsStore))
	mux.Handle("/api", nodrapi.HandlerWithoutSetup(ctx, root, auth, secretsStore))
	mux.Handle("/", webui.Handler())
	return mux
}

func runHandlerServer(ctx context.Context, addr string, stdout io.Writer, handler http.Handler) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
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
