package githubapp_test

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	jwt "github.com/golang-jwt/jwt/v4"
	github "github.com/google/go-github/v75/github"

	"github.com/centopw/nodr/internal/githubapp"
)

const (
	webhookTestSecret = "hooksecret"
	fakeInstallToken  = "ghs_faketoken"
	fakeAppID         = int64(12345)
	fakeInstallID     = int64(6789)
)

// testKey generates an RSA key pair for tests: the PEM-encoded private key
// (as configured on a real GitHub App) and the matching public key used by
// the fake GitHub API to verify minted JWTs.
func testKey(t *testing.T) ([]byte, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return pemBytes, &key.PublicKey
}

// fakeGitHub serves the small slice of the GitHub Apps API the service
// uses, and verifies that requests carry correctly signed app JWTs or
// installation tokens.
type fakeGitHub struct {
	t         *testing.T
	server    *httptest.Server
	publicKey *rsa.PublicKey
	expiresAt time.Time

	mu           sync.Mutex
	tokenFetches int
}

func newFakeGitHub(t *testing.T, _ int64, publicKey *rsa.PublicKey) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{
		t:         t,
		publicKey: publicKey,
		expiresAt: time.Now().Add(time.Hour).UTC().Round(time.Second),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", f.handleInstallationToken)
	mux.HandleFunc("GET /app/installations", f.handleListInstallations)
	mux.HandleFunc("GET /installation/repositories", f.handleListRepos)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitHub) handleInstallationToken(w http.ResponseWriter, r *http.Request) {
	if got, want := r.PathValue("id"), strconv.FormatInt(fakeInstallID, 10); got != want {
		f.t.Errorf("access_tokens path installation id = %q, want %q", got, want)
	}
	f.verifyAppJWT(r)
	f.mu.Lock()
	f.tokenFetches++
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"token":%q,"expires_at":%q}`, fakeInstallToken, f.expiresAt.Format(time.RFC3339))
}

func (f *fakeGitHub) handleListInstallations(w http.ResponseWriter, r *http.Request) {
	f.assertPerPage100(r)
	f.verifyAppJWT(r)
	switch page := r.URL.Query().Get("page"); page {
	case "", "1":
		w.Header().Set("Link", fmt.Sprintf(`<%s/app/installations?page=2&per_page=100>; rel="next"`, f.server.URL))
		f.respond(w, []*github.Installation{{ID: github.Ptr(int64(1))}, {ID: github.Ptr(int64(2))}})
	case "2":
		f.respond(w, []*github.Installation{{ID: github.Ptr(int64(3))}})
	default:
		f.t.Errorf("list installations unexpected page %q", page)
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (f *fakeGitHub) handleListRepos(w http.ResponseWriter, r *http.Request) {
	f.assertPerPage100(r)
	if got, want := r.Header.Get("Authorization"), "token "+fakeInstallToken; got != want {
		f.t.Errorf("list repos Authorization = %q, want %q", got, want)
	}
	switch page := r.URL.Query().Get("page"); page {
	case "", "1":
		w.Header().Set("Link", fmt.Sprintf(`<%s/installation/repositories?page=2&per_page=100>; rel="next"`, f.server.URL))
		f.respond(w, &github.ListRepositories{
			TotalCount: github.Ptr(3),
			Repositories: []*github.Repository{
				{Name: github.Ptr("repo-1"), FullName: github.Ptr("acme/repo-1")},
				{Name: github.Ptr("repo-2"), FullName: github.Ptr("acme/repo-2")},
			},
		})
	case "2":
		f.respond(w, &github.ListRepositories{
			TotalCount: github.Ptr(3),
			Repositories: []*github.Repository{
				{Name: github.Ptr("repo-3"), FullName: github.Ptr("acme/repo-3")},
			},
		})
	default:
		f.t.Errorf("list repos unexpected page %q", page)
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (f *fakeGitHub) assertPerPage100(r *http.Request) {
	if got := r.URL.Query().Get("per_page"); got != "100" {
		f.t.Errorf("per_page = %q, want 100", got)
	}
}

// verifyAppJWT asserts the request authenticates as the GitHub App with a
// valid RS256 JWT whose issuer is the app ID.
func (f *fakeGitHub) verifyAppJWT(r *http.Request) {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		f.t.Errorf("Authorization = %q, want %q-prefixed JWT", auth, prefix)
		return
	}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"}))
	claims := &jwt.RegisteredClaims{}
	tok, err := parser.ParseWithClaims(strings.TrimPrefix(auth, prefix), claims, func(*jwt.Token) (any, error) {
		return f.publicKey, nil
	})
	if err != nil || !tok.Valid {
		f.t.Errorf("invalid app JWT: %v (valid=%t)", err, err == nil && tok.Valid)
		return
	}
	if want := strconv.FormatInt(fakeAppID, 10); claims.Issuer != want {
		f.t.Errorf("JWT iss = %q, want %q", claims.Issuer, want)
	}
}

func (f *fakeGitHub) respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		f.t.Errorf("encode response: %v", err)
	}
}

func (f *fakeGitHub) tokenRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenFetches
}

func mustService(t *testing.T, keyPEM []byte, fake *fakeGitHub) *githubapp.Service {
	t.Helper()
	svc, err := githubapp.NewService(fakeAppID, keyPEM, webhookTestSecret, githubapp.WithBaseURL(fake.server.URL))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestNewService_RejectsInvalidPrivateKeyPEM(t *testing.T) {
	if _, err := githubapp.NewService(1, []byte("not a pem"), webhookTestSecret); err == nil {
		t.Fatal("NewService with invalid private key PEM succeeded, want error")
	}
}

func TestInstallationToken_MintsInstallationAccessToken(t *testing.T) {
	keyPEM, pub := testKey(t)
	fake := newFakeGitHub(t, fakeAppID, pub)
	svc := mustService(t, keyPEM, fake)

	ctx := context.Background()
	tok, err := svc.InstallationToken(ctx, fakeInstallID)
	if err != nil {
		t.Fatalf("InstallationToken: %v", err)
	}
	if tok.Token != fakeInstallToken {
		t.Errorf("token = %q, want %q", tok.Token, fakeInstallToken)
	}
	if !tok.ExpiresAt.Equal(fake.expiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", tok.ExpiresAt, fake.expiresAt)
	}
	if got := fake.tokenRequestCount(); got != 1 {
		t.Errorf("access_tokens requests = %d, want 1", got)
	}
}

func TestInstallationToken_CachesTokenInMemory(t *testing.T) {
	keyPEM, pub := testKey(t)
	fake := newFakeGitHub(t, fakeAppID, pub)
	svc := mustService(t, keyPEM, fake)

	ctx := context.Background()
	if _, err := svc.InstallationToken(ctx, fakeInstallID); err != nil {
		t.Fatalf("first InstallationToken: %v", err)
	}
	if _, err := svc.InstallationToken(ctx, fakeInstallID); err != nil {
		t.Fatalf("second InstallationToken: %v", err)
	}
	if got := fake.tokenRequestCount(); got != 1 {
		t.Errorf("access_tokens requests after two calls = %d, want 1 (token cached in memory)", got)
	}
}

func TestTransport_SendsInstallationToken(t *testing.T) {
	keyPEM, pub := testKey(t)
	fake := newFakeGitHub(t, fakeAppID, pub)
	svc := mustService(t, keyPEM, fake)

	client, err := svc.Transport(fakeInstallID)
	if err != nil {
		t.Fatalf("Transport: %v", err)
	}
	resp, err := client.Get(fake.server.URL + "/installation/repositories?per_page=100")
	if err != nil {
		t.Fatalf("GET /installation/repositories: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := fake.tokenRequestCount(); got != 1 {
		t.Errorf("access_tokens requests = %d, want 1", got)
	}
}

func TestListInstallations_FollowsPagination(t *testing.T) {
	keyPEM, pub := testKey(t)
	fake := newFakeGitHub(t, fakeAppID, pub)
	svc := mustService(t, keyPEM, fake)

	installations, err := svc.ListInstallations(context.Background())
	if err != nil {
		t.Fatalf("ListInstallations: %v", err)
	}
	ids := make([]int64, 0, len(installations))
	for _, in := range installations {
		if in.ID == nil {
			t.Error("installation with nil ID")
			continue
		}
		ids = append(ids, *in.ID)
	}
	want := []int64{1, 2, 3}
	if len(ids) != len(want) {
		t.Fatalf("installation ids = %v, want %v", ids, want)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("installation ids = %v, want %v", ids, want)
			break
		}
	}
}

func TestListRepositories_FollowsPagination(t *testing.T) {
	keyPEM, pub := testKey(t)
	fake := newFakeGitHub(t, fakeAppID, pub)
	svc := mustService(t, keyPEM, fake)

	repos, err := svc.ListRepositories(context.Background(), fakeInstallID)
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		if repo.Name == nil {
			t.Error("repository with nil Name")
			continue
		}
		names = append(names, *repo.Name)
	}
	want := []string{"repo-1", "repo-2", "repo-3"}
	if len(names) != len(want) {
		t.Fatalf("repository names = %v, want %v", names, want)
	}
	for i, name := range want {
		if names[i] != name {
			t.Errorf("repository names = %v, want %v", names, want)
			break
		}
	}
	if got := fake.tokenRequestCount(); got != 1 {
		t.Errorf("access_tokens requests = %d, want 1", got)
	}
}

func TestGitAuth_UsesAccessTokenUser(t *testing.T) {
	keyPEM, pub := testKey(t)
	fake := newFakeGitHub(t, fakeAppID, pub)
	svc := mustService(t, keyPEM, fake)

	auth, err := svc.GitAuth(context.Background(), fakeInstallID)
	if err != nil {
		t.Fatalf("GitAuth: %v", err)
	}
	want := githttp.BasicAuth{Username: "x-access-token", Password: fakeInstallToken}
	if auth != want {
		t.Errorf("GitAuth = %+v, want %+v", auth, want)
	}
}
