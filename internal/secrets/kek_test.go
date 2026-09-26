package secrets_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/centopw/nodr/internal/secrets"
)

func TestLoadKEK_FromFile(t *testing.T) {
	dir := t.TempDir()
	kekFile := filepath.Join(dir, "kek.bin")
	rawKEK := make([]byte, 32)
	for i := range rawKEK {
		rawKEK[i] = byte(i)
	}
	if err := os.WriteFile(kekFile, rawKEK, 0600); err != nil {
		t.Fatalf("write kek file: %v", err)
	}

	got, err := secrets.LoadKEK(secrets.KEKConfig{FilePath: kekFile})
	if err != nil {
		t.Fatalf("LoadKEK: %v", err)
	}
	if string(got) != string(rawKEK) {
		t.Errorf("got %v, want %v", got, rawKEK)
	}
}

func TestLoadKEK_FromEnvVar(t *testing.T) {
	rawKEK := make([]byte, 32)
	for i := range rawKEK {
		rawKEK[i] = byte(i + 10)
	}
	encoded := base64.StdEncoding.EncodeToString(rawKEK)
	t.Setenv("TEST_NODR_KEK", encoded)

	got, err := secrets.LoadKEK(secrets.KEKConfig{EnvVar: "TEST_NODR_KEK"})
	if err != nil {
		t.Fatalf("LoadKEK: %v", err)
	}
	if string(got) != string(rawKEK) {
		t.Errorf("got %v, want %v", got, rawKEK)
	}
}

func TestLoadKEK_FilePermissionsRejection(t *testing.T) {
	dir := t.TempDir()
	kekFile := filepath.Join(dir, "kek_insecure.bin")
	rawKEK := make([]byte, 32)
	// Mode 0644 gives group/other read permissions
	if err := os.WriteFile(kekFile, rawKEK, 0644); err != nil {
		t.Fatalf("write kek file: %v", err)
	}

	_, err := secrets.LoadKEK(secrets.KEKConfig{FilePath: kekFile})
	if err == nil {
		t.Fatal("expected error for file with permissions 0644, got nil")
	}
}

func TestLoadKEK_InvalidLength(t *testing.T) {
	dir := t.TempDir()
	kekFile := filepath.Join(dir, "short_kek.bin")
	if err := os.WriteFile(kekFile, []byte("too-short"), 0600); err != nil {
		t.Fatalf("write kek file: %v", err)
	}

	_, err := secrets.LoadKEK(secrets.KEKConfig{FilePath: kekFile})
	if err == nil {
		t.Fatal("expected error for short KEK, got nil")
	}
}
