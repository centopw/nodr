package authn_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/centopw/nodr/internal/authn"
)

func openStore(t *testing.T) *authn.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := authn.Open(filepath.Join(dir, "authn.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStore_CreateAndAuthenticate(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateAccount(ctx, "admin", "correct horse battery staple"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token, expiresAt, err := s.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("expiresAt = %v, want future", expiresAt)
	}
	if !s.ValidateSession(ctx, token) {
		t.Error("ValidateSession = false, want true")
	}
}

func TestStore_WrongPasswordRejected(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateAccount(ctx, "admin", "correct horse battery staple"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, _, err := s.Authenticate(ctx, "admin", "wrong password")
	if !errors.Is(err, authn.ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestStore_LogoutInvalidatesSession(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateAccount(ctx, "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token, _, err := s.Authenticate(ctx, "admin", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if err := s.Logout(ctx, token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if s.ValidateSession(ctx, token) {
		t.Error("ValidateSession = true after Logout, want false")
	}
}

func TestStore_CreateAccountReplacesExisting(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateAccount(ctx, "admin", "first-password"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := s.CreateAccount(ctx, "admin2", "second-password"); err != nil {
		t.Fatalf("CreateAccount (replace): %v", err)
	}
	if _, _, err := s.Authenticate(ctx, "admin", "first-password"); !errors.Is(err, authn.ErrInvalidCredentials) {
		t.Errorf("old account still authenticates: %v", err)
	}
	if _, _, err := s.Authenticate(ctx, "admin2", "second-password"); err != nil {
		t.Errorf("new account: %v", err)
	}
}

func TestStore_SchemaHasBootstrapTableAndCSRFColumn(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO bootstrap (id, token_hash, consumed_at) VALUES (1, 'x', NULL)`); err != nil {
		t.Fatalf("insert into bootstrap: %v", err)
	}
	if err := s.CreateAccount(ctx, "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO session (token, expires_at, csrf_token) VALUES ('tok', 0, 'csrf')`); err != nil {
		t.Fatalf("insert session with csrf_token: %v", err)
	}
}

func TestSentinelErrors_AreDistinct(t *testing.T) {
	if errors.Is(authn.ErrBootstrapTokenRequired, authn.ErrInvalidCredentials) {
		t.Error("ErrBootstrapTokenRequired must not alias ErrInvalidCredentials")
	}
	if errors.Is(authn.ErrSetupUnavailable, authn.ErrInvalidCredentials) {
		t.Error("ErrSetupUnavailable must not alias ErrInvalidCredentials")
	}
	if errors.Is(authn.ErrBootstrapTokenRequired, authn.ErrSetupUnavailable) {
		t.Error("ErrBootstrapTokenRequired must not alias ErrSetupUnavailable")
	}
}

func TestInitializeBootstrap_NoAccountEmptyToken_ReturnsError(t *testing.T) {
	s := openStore(t)
	if err := s.InitializeBootstrap(context.Background(), ""); !errors.Is(err, authn.ErrBootstrapTokenRequired) {
		t.Fatalf("err = %v, want ErrBootstrapTokenRequired", err)
	}
}

func TestInitializeBootstrap_NoAccountWithToken_StoresHash(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.InitializeBootstrap(ctx, "bootstrap-token-1"); err != nil {
		t.Fatalf("InitializeBootstrap: %v", err)
	}
	var hash string
	var consumedAt sql.NullInt64
	if err := s.DB().QueryRowContext(ctx, `SELECT token_hash, consumed_at FROM bootstrap WHERE id = 1`).Scan(&hash, &consumedAt); err != nil {
		t.Fatalf("query bootstrap row: %v", err)
	}
	if hash == "" || hash == "bootstrap-token-1" {
		t.Errorf("token_hash = %q, want a hash, not the plaintext token", hash)
	}
	if consumedAt.Valid {
		t.Error("consumed_at should be NULL for a fresh bootstrap token")
	}
}

func TestInitializeBootstrap_ExistingAccount_IsNoOpRegardlessOfToken(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateAccount(ctx, "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := s.InitializeBootstrap(ctx, ""); err != nil {
		t.Fatalf("InitializeBootstrap with empty token and existing account: %v", err)
	}
	if err := s.InitializeBootstrap(ctx, "anything"); err != nil {
		t.Fatalf("InitializeBootstrap with nonempty token and existing account: %v", err)
	}
	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM bootstrap`).Scan(&count); err != nil {
		t.Fatalf("count bootstrap rows: %v", err)
	}
	if count != 0 {
		t.Errorf("bootstrap rows = %d, want 0 when an account already exists", count)
	}
}

func TestInitializeBootstrap_RerunBeforeSetup_OverwritesHash(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.InitializeBootstrap(ctx, "first-token"); err != nil {
		t.Fatalf("InitializeBootstrap (first): %v", err)
	}
	var firstHash string
	if err := s.DB().QueryRowContext(ctx, `SELECT token_hash FROM bootstrap WHERE id = 1`).Scan(&firstHash); err != nil {
		t.Fatalf("query first hash: %v", err)
	}
	if err := s.InitializeBootstrap(ctx, "second-token"); err != nil {
		t.Fatalf("InitializeBootstrap (second): %v", err)
	}
	var secondHash string
	if err := s.DB().QueryRowContext(ctx, `SELECT token_hash FROM bootstrap WHERE id = 1`).Scan(&secondHash); err != nil {
		t.Fatalf("query second hash: %v", err)
	}
	if firstHash == secondHash {
		t.Error("token_hash unchanged after re-running InitializeBootstrap with a different token")
	}
}

func TestAuthenticate_GeneratesCSRFToken(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateAccount(ctx, "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token, _, err := s.Authenticate(ctx, "admin", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	csrf, ok := s.SessionCSRFToken(ctx, token)
	if !ok {
		t.Fatal("SessionCSRFToken: ok = false, want true")
	}
	if csrf == "" {
		t.Error("csrf token is empty")
	}
}

func TestAuthenticate_CSRFTokenDiffersPerSession(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateAccount(ctx, "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token1, _, err := s.Authenticate(ctx, "admin", "password12345")
	if err != nil {
		t.Fatalf("Authenticate (1): %v", err)
	}
	token2, _, err := s.Authenticate(ctx, "admin", "password12345")
	if err != nil {
		t.Fatalf("Authenticate (2): %v", err)
	}
	csrf1, _ := s.SessionCSRFToken(ctx, token1)
	csrf2, _ := s.SessionCSRFToken(ctx, token2)
	if csrf1 == csrf2 {
		t.Error("two sessions got the same csrf token")
	}
}

func TestSessionCSRFToken_UnknownSession_ReturnsFalse(t *testing.T) {
	s := openStore(t)
	if _, ok := s.SessionCSRFToken(context.Background(), "no-such-token"); ok {
		t.Error("ok = true for an unknown session token")
	}
}

func TestHasAccount_FalseThenTrue(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	has, err := s.HasAccount(ctx)
	if err != nil {
		t.Fatalf("HasAccount: %v", err)
	}
	if has {
		t.Error("HasAccount = true before any account is created")
	}
	if err := s.CreateAccount(ctx, "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	has, err = s.HasAccount(ctx)
	if err != nil {
		t.Fatalf("HasAccount (after create): %v", err)
	}
	if !has {
		t.Error("HasAccount = false after CreateAccount")
	}
}

func TestSessionUsername_ReturnsAccountUsername(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateAccount(ctx, "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token, _, err := s.Authenticate(ctx, "admin", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	username, ok := s.SessionUsername(ctx, token)
	if !ok {
		t.Fatal("SessionUsername: ok = false, want true")
	}
	if username != "admin" {
		t.Errorf("username = %q, want %q", username, "admin")
	}
}

func TestSessionUsername_UnknownSession_ReturnsFalse(t *testing.T) {
	s := openStore(t)
	if _, ok := s.SessionUsername(context.Background(), "no-such-token"); ok {
		t.Error("ok = true for an unknown session token")
	}
}

func TestSetup_Success_CreatesAccountAndSession(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.InitializeBootstrap(ctx, "bootstrap-token-1"); err != nil {
		t.Fatalf("InitializeBootstrap: %v", err)
	}
	sessionToken, csrfToken, expiresAt, err := s.Setup(ctx, "bootstrap-token-1", "admin", "password12345")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if sessionToken == "" || csrfToken == "" {
		t.Fatalf("sessionToken = %q, csrfToken = %q, want both non-empty", sessionToken, csrfToken)
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("expiresAt = %v, want future", expiresAt)
	}
	if !s.ValidateSession(ctx, sessionToken) {
		t.Error("ValidateSession = false after Setup")
	}
	has, err := s.HasAccount(ctx)
	if err != nil {
		t.Fatalf("HasAccount: %v", err)
	}
	if !has {
		t.Error("HasAccount = false after Setup")
	}
	if _, _, err := s.Authenticate(ctx, "admin", "password12345"); err != nil {
		t.Errorf("Authenticate with the account Setup created: %v", err)
	}
}

func TestSetup_WrongToken_RejectedWithoutConsuming(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.InitializeBootstrap(ctx, "bootstrap-token-1"); err != nil {
		t.Fatalf("InitializeBootstrap: %v", err)
	}
	if _, _, _, err := s.Setup(ctx, "wrong-token", "admin", "password12345"); !errors.Is(err, authn.ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
	has, err := s.HasAccount(ctx)
	if err != nil {
		t.Fatalf("HasAccount: %v", err)
	}
	if has {
		t.Error("HasAccount = true after a rejected Setup call")
	}
	if _, _, _, err := s.Setup(ctx, "bootstrap-token-1", "admin", "password12345"); err != nil {
		t.Errorf("Setup with the correct token after a wrong attempt: %v", err)
	}
}

func TestSetup_NoBootstrapRow_ReturnsSetupUnavailable(t *testing.T) {
	s := openStore(t)
	if _, _, _, err := s.Setup(context.Background(), "anything", "admin", "password12345"); !errors.Is(err, authn.ErrSetupUnavailable) {
		t.Fatalf("err = %v, want ErrSetupUnavailable", err)
	}
}

func TestSetup_AlreadyConsumed_ReturnsSetupUnavailable(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.InitializeBootstrap(ctx, "bootstrap-token-1"); err != nil {
		t.Fatalf("InitializeBootstrap: %v", err)
	}
	if _, _, _, err := s.Setup(ctx, "bootstrap-token-1", "admin", "password12345"); err != nil {
		t.Fatalf("Setup (first): %v", err)
	}
	if _, _, _, err := s.Setup(ctx, "bootstrap-token-1", "admin2", "password67890"); !errors.Is(err, authn.ErrSetupUnavailable) {
		t.Fatalf("err = %v, want ErrSetupUnavailable", err)
	}
}

func TestSetup_ConcurrentDoubleSetup_ExactlyOneWins(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.InitializeBootstrap(ctx, "bootstrap-token-1"); err != nil {
		t.Fatalf("InitializeBootstrap: %v", err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, username := range []string{"admin", "admin2"} {
		wg.Add(1)
		go func(username string) {
			defer wg.Done()
			_, _, _, err := s.Setup(ctx, "bootstrap-token-1", username, "password12345")
			results <- err
		}(username)
	}
	wg.Wait()
	close(results)
	successes, failures := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, authn.ErrSetupUnavailable):
			failures++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("successes = %d, failures = %d, want 1 and 1", successes, failures)
	}
	var accountCount, sessionCount int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM account`).Scan(&accountCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM session`).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if accountCount != 1 {
		t.Errorf("account rows = %d, want 1", accountCount)
	}
	if sessionCount != 1 {
		t.Errorf("session rows = %d, want 1", sessionCount)
	}
}
