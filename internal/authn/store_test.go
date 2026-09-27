package authn_test

import (
	"context"
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
