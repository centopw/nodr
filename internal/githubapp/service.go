// Package githubapp provides GitHub App authentication for nodr's control
// plane (docs/design/10-security.md §10.3): short-lived installation access
// tokens, the Apps API listing used during setup, webhook signature
// verification, and webhook delivery deduplication.
//
// Service.Transport returns an *http.Client whose requests authenticate as a
// GitHub App installation (ghinstallation.Transport); use it as the transport
// of a go-github client. Service.GitAuth returns the BasicAuth credentials
// for go-git HTTPS operations against the installation's repositories
// (username "x-access-token", password is the installation token).
//
// Installation tokens are short-lived, cached in memory for reuse, and never
// persisted to disk. Deliveries deduplicates webhook deliveries;
// MemoryDeliveries is a thread-safe in-memory implementation for tests, while
// the production implementation belongs to the workspace metadata store.
package githubapp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	github "github.com/google/go-github/v75/github"
)

// defaultBaseURL is the GitHub REST API root, overridden with WithBaseURL,
// e.g. for tests against a fake GitHub API.
const defaultBaseURL = "https://api.github.com/"

// gitAuthUser is the username GitHub requires for installation-token HTTPS
// basic authentication.
const gitAuthUser = "x-access-token"

// Token is a short-lived GitHub App installation access token.
type Token struct {
	Token     string
	ExpiresAt time.Time
}

// Service provides GitHub App authentication and Apps API access for one
// configured GitHub App.
type Service struct {
	baseURL       string
	webhookSecret []byte
	apps          *ghinstallation.AppsTransport
	appClient     *github.Client

	mu            sync.Mutex
	installations map[int64]*ghinstallation.Transport
}

// Option configures a Service.
type Option func(*Service)

// WithBaseURL overrides the GitHub API base URL, e.g. for tests against a
// fake GitHub API.
func WithBaseURL(baseURL string) Option {
	return func(s *Service) {
		if !strings.HasSuffix(baseURL, "/") {
			baseURL += "/"
		}
		s.baseURL = baseURL
	}
}

// NewService creates a Service for the GitHub App with appID, authenticating
// API calls with its private key PEM and verifying webhooks with webhookSecret.
func NewService(appID int64, privateKeyPEM []byte, webhookSecret string, opts ...Option) (*Service, error) {
	apps, err := ghinstallation.NewAppsTransport(http.DefaultTransport, appID, privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("githubapp: parse private key: %w", err)
	}
	s := &Service{
		baseURL:       defaultBaseURL,
		webhookSecret: []byte(webhookSecret),
		apps:          apps,
		installations: make(map[int64]*ghinstallation.Transport),
	}
	for _, opt := range opts {
		opt(s)
	}
	apps.BaseURL = s.baseURL
	appClient := github.NewClient(&http.Client{Transport: apps})
	appClient.BaseURL, err = url.Parse(s.baseURL)
	if err != nil {
		return nil, fmt.Errorf("githubapp: parse base URL: %w", err)
	}
	s.appClient = appClient
	return s, nil
}

// InstallationToken returns the current installation access token, minting a
// new one when the cached token is near expiry. Tokens are held in memory
// only and never persisted to disk.
func (s *Service) InstallationToken(ctx context.Context, installationID int64) (Token, error) {
	tr, err := s.installationTransport(installationID)
	if err != nil {
		return Token{}, err
	}
	token, err := tr.Token(ctx)
	if err != nil {
		return Token{}, fmt.Errorf("githubapp: installation %d token: %w", installationID, err)
	}
	expiresAt, _, err := tr.Expiry()
	if err != nil {
		return Token{}, fmt.Errorf("githubapp: installation %d token expiry: %w", installationID, err)
	}
	return Token{Token: token, ExpiresAt: expiresAt}, nil
}

// Transport returns an *http.Client bound to the installation
// (ghinstallation.Transport), for go-github API operations authenticated as
// the installation.
func (s *Service) Transport(installationID int64) (*http.Client, error) {
	tr, err := s.installationTransport(installationID)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: tr}, nil
}

// GitAuth returns BasicAuth credentials for go-git HTTPS operations
// authenticated as the installation: username "x-access-token", password the
// installation token.
func (s *Service) GitAuth(ctx context.Context, installationID int64) (githttp.BasicAuth, error) {
	tok, err := s.InstallationToken(ctx, installationID)
	if err != nil {
		return githttp.BasicAuth{}, err
	}
	return githttp.BasicAuth{Username: gitAuthUser, Password: tok.Token}, nil
}

// ListInstallations returns every installation of the app, following GitHub's
// pagination with a page size of 100.
func (s *Service) ListInstallations(ctx context.Context) ([]*github.Installation, error) {
	var installations []*github.Installation
	opts := &github.ListOptions{PerPage: 100}
	for {
		page, resp, err := s.appClient.Apps.ListInstallations(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("githubapp: list installations: %w", err)
		}
		installations = append(installations, page...)
		if resp.NextPage == 0 {
			return installations, nil
		}
		opts.Page = resp.NextPage
	}
}

// ListRepositories returns every repository accessible to the installation,
// following GitHub's pagination with a page size of 100.
func (s *Service) ListRepositories(ctx context.Context, installationID int64) ([]*github.Repository, error) {
	tr, err := s.installationTransport(installationID)
	if err != nil {
		return nil, err
	}
	client := github.NewClient(&http.Client{Transport: tr})
	client.BaseURL = s.appClient.BaseURL
	var repositories []*github.Repository
	opts := &github.ListOptions{PerPage: 100}
	for {
		page, resp, err := client.Apps.ListRepos(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("githubapp: list repositories for installation %d: %w", installationID, err)
		}
		repositories = append(repositories, page.Repositories...)
		if resp.NextPage == 0 {
			return repositories, nil
		}
		opts.Page = resp.NextPage
	}
}

// installationTransport returns a shared ghinstallation.Transport for the
// installation, so its token is minted once and reused across requests.
func (s *Service) installationTransport(installationID int64) (*ghinstallation.Transport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tr, ok := s.installations[installationID]; ok {
		return tr, nil
	}
	tr := ghinstallation.NewFromAppsTransport(s.apps, installationID)
	s.installations[installationID] = tr
	return tr, nil
}
