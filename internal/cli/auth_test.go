package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"github.com/centopw/nodr/internal/authn"
)

func TestAuthCreateAdmin_PasswordStdin(t *testing.T) {
	root := writeWorkspace(t, planFiles)
	var stdout, stderr bytes.Buffer
	stdin := bytes.NewBufferString("admin\nsecretpass123\n")
	code := Run(context.Background(), nil, []string{"--workspace", root, "auth", "create-admin", "--password-stdin"}, stdin, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	store, err := authn.Open(filepath.Join(root, ".nodr", "authn.db"))
	if err != nil {
		t.Fatalf("authn.Open: %v", err)
	}
	defer store.Close()
	if _, _, err := store.Authenticate(context.Background(), "admin", "secretpass123"); err != nil {
		t.Errorf("Authenticate: %v", err)
	}
}
