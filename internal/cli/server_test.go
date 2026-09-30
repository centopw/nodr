package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/centopw/nodr/internal/authn"
)

func TestServerCommand_NoAccountNoBootstrapToken_FailsFast(t *testing.T) {
	root := writeWorkspace(t, planFiles)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	code := Run(ctx, nil, []string{"--workspace", root, "server", "--addr", "127.0.0.1:0"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitError {
		t.Fatalf("exit = %d, want %d; stdout = %s; stderr = %s", code, exitError, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "NODR_BOOTSTRAP_TOKEN") {
		t.Errorf("stderr = %q, want it to mention NODR_BOOTSTRAP_TOKEN", stderr.String())
	}
}

func TestServerCommand_ExistingAccount_IgnoresMissingBootstrapToken(t *testing.T) {
	root := writeWorkspace(t, planFiles)
	authStore, err := authn.Open(filepath.Join(root, ".nodr", "authn.db"))
	if err != nil {
		t.Fatalf("authn.Open: %v", err)
	}
	if err := authStore.CreateAccount(context.Background(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := authStore.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	code := Run(ctx, nil, []string{"--workspace", root, "server", "--addr", "127.0.0.1:0"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d; stderr = %s", code, exitOK, stderr.String())
	}
}
