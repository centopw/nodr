package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"

	nodrapi "github.com/centopw/nodr/internal/api"
	"github.com/centopw/nodr/internal/authn"
	"github.com/centopw/nodr/internal/githubapp"
	"github.com/centopw/nodr/internal/ledger"
	"github.com/centopw/nodr/internal/operations"
	"github.com/centopw/nodr/internal/repository"
	"github.com/centopw/nodr/internal/secrets"
)

// ledgerEnvironment lists the all-or-none variables that enable ledger mode.
var ledgerEnvironment = []string{
	"NODR_GITHUB_APP_ID",
	"NODR_GITHUB_PRIVATE_KEY_FILE",
	"NODR_GITHUB_WEBHOOK_SECRET",
	"NODR_PUBLIC_URL",
}

// ledgerConfig is the validated GitHub App configuration of a deployment.
type ledgerConfig struct {
	appID         int64
	privateKey    []byte
	webhookSecret string
	publicURL     *url.URL
}

// loadLedgerConfig reads the ledger environment group. It returns nil, nil when
// none of the variables is set and an error naming the missing variables when
// only some are.
func loadLedgerConfig() (*ledgerConfig, error) {
	var values, missing []string
	for _, name := range ledgerEnvironment {
		value := os.Getenv(name)
		values = append(values, value)
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == len(ledgerEnvironment) {
		return nil, nil
	}
	if len(missing) != 0 {
		return nil, fmt.Errorf("github ledger environment missing: %s", strings.Join(missing, ", "))
	}
	appID, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || appID <= 0 {
		return nil, errors.New("NODR_GITHUB_APP_ID must be a positive integer")
	}
	key, err := os.ReadFile(values[1])
	if err != nil {
		return nil, fmt.Errorf("read NODR_GITHUB_PRIVATE_KEY_FILE: %w", err)
	}
	publicURL, err := url.Parse(values[3])
	if err != nil || publicURL.Host == "" || (publicURL.Scheme != "https" && publicURL.Scheme != "http") {
		return nil, errors.New("NODR_PUBLIC_URL must be an absolute http(s) URL")
	}
	return &ledgerConfig{appID: appID, privateKey: key, webhookSecret: values[2], publicURL: publicURL}, nil
}

// webhookURL is the URL the GitHub App must deliver push events to.
func (c *ledgerConfig) webhookURL() string {
	return strings.TrimRight(c.publicURL.String(), "/") + "/api/v1/integrations/github/webhook"
}

// ledgerRuntime owns the stores and service behind the ledger API.
type ledgerRuntime struct {
	service *ledger.Service
	handler func(next http.Handler) http.Handler
	closers []func()
}

// Close stops background operations and closes the stores.
func (r *ledgerRuntime) Close() {
	for i := len(r.closers) - 1; i >= 0; i-- {
		r.closers[i]()
	}
}

// githubVerifier adapts the GitHub App service to the API's LedgerService:
// the ledger service itself does not verify signatures.
type githubVerifier struct {
	*ledger.Service
	verify func(payload []byte, signature string) error
}

// VerifyWebhookSignature implements api.LedgerService.
func (v githubVerifier) VerifyWebhookSignature(payload []byte, signature string) error {
	return v.verify(payload, signature)
}

// openLedger builds the ledger service for the workspace data directory root.
// Its state lives under <root>/.nodr/ledger. ctx is the server lifetime: plan
// and apply run on it, never on a request context.
func openLedger(ctx context.Context, cfg *ledgerConfig, root string, auth *authn.Store, secretsStore *secrets.Store) (*ledgerRuntime, error) {
	app, err := githubapp.NewService(cfg.appID, cfg.privateKey, cfg.webhookSecret)
	if err != nil {
		return nil, fmt.Errorf("configure GitHub App: %w", err)
	}
	dir := filepath.Join(root, ".nodr", "ledger")
	rt := &ledgerRuntime{}
	fail := func(err error) (*ledgerRuntime, error) {
		rt.Close()
		return nil, err
	}

	ops, err := operations.Open(filepath.Join(dir, "operations.db"))
	if err != nil {
		return fail(err)
	}
	rt.closers = append(rt.closers, func() { _ = ops.Close() })
	meta, err := repository.OpenMetadataStore(filepath.Join(dir, "metadata.db"), secretsStore)
	if err != nil {
		return fail(err)
	}
	repo, err := repository.NewLocalStore(dir, repository.WithAuth(func(ctx context.Context, installationID int64) (transport.AuthMethod, error) {
		basic, err := app.GitAuth(ctx, installationID)
		if err != nil {
			return nil, err
		}
		return &basic, nil
	}))
	if err != nil {
		return fail(err)
	}
	svc, err := ledger.New(ctx, ledger.Deps{
		Repo:          repo,
		Meta:          meta,
		Ops:           ops,
		GitHub:        app,
		Secrets:       secretsStore,
		Runner:        &ledger.LocalRunner{Dir: filepath.Join(dir, "plans")},
		WebhookSecret: cfg.webhookSecret,
	})
	if err != nil {
		return fail(err)
	}
	rt.closers = append(rt.closers, func() { _ = svc.Close() })
	rt.service = svc

	ledgerAPI := githubVerifier{Service: svc, verify: app.VerifyWebhookSignature}
	rt.handler = func(next http.Handler) http.Handler {
		return nodrapi.LedgerHandler(nodrapi.LedgerConfig{
			Service: ledgerAPI,
			Auth:    auth,
			Ready: func(ctx context.Context) []string {
				return ledgerReadiness(ctx, ops, secretsStore, svc)
			},
		}, next)
	}
	return rt, nil
}

// ledgerReadiness runs the /readyz checks and returns the names of the failures.
func ledgerReadiness(ctx context.Context, ops *operations.Store, secretsStore *secrets.Store, svc *ledger.Service) []string {
	var failed []string
	if _, err := ops.Changesets(ctx, ""); err != nil {
		failed = append(failed, "operations database")
	}
	if _, err := secretsStore.List(ctx); err != nil {
		failed = append(failed, "encrypted secrets")
	}
	if _, err := svc.Workspaces(ctx); err != nil {
		failed = append(failed, "repository store")
	}
	return failed
}

// legacyWorkspaceAPIGone answers every workspace route the ledger handler did
// not claim: in ledger mode the local-workspace API must not be reachable.
func legacyWorkspaceAPIGone(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/workspaces") {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"type":"about:blank","title":"Not found","status":404,"detail":"the requested API path does not exist"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
