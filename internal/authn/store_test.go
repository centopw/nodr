package authn_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
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