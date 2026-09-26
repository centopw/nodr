package secrets_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/centopw/nodr/internal/secrets"
)

func TestStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "secrets.db")
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 1)
	}

	store, err := secrets.Open(dbPath, kek)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	secretName := "proxmox/pve-main-token"
	secretValue := []byte("pve-token-secret-uuid-12345")

	if err := store.Put(ctx, secretName, secretValue); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Resolve(ctx, secretName)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !bytes.Equal(got, secretValue) {
		t.Errorf("got %q, want %q", got, secretValue)
	}

	// List
	names, err := store.List(ctx)
	if err != nil || len(names) != 1 || names[0] != secretName {
		t.Fatalf("List: %v, %v", names, err)
	}

	// Delete
	if err := store.Delete(ctx, secretName); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = store.Resolve(ctx, secretName)
	if !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestStore_WrongKEK(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "secrets.db")
	kek1 := make([]byte, 32)
	kek2 := make([]byte, 32)
	kek2[0] = 0xFF

	store1, err := secrets.Open(dbPath, kek1)
	if err != nil {
		t.Fatalf("Open store1: %v", err)
	}
	ctx := context.Background()
	if err := store1.Put(ctx, "test/key", []byte("secret-payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	store1.Close()

	store2, err := secrets.Open(dbPath, kek2)
	if err != nil {
		t.Fatalf("Open store2: %v", err)
	}
	defer store2.Close()

	_, err = store2.Resolve(ctx, "test/key")
	if !errors.Is(err, secrets.ErrDecrypt) {
		t.Fatalf("expected ErrDecrypt with wrong KEK, got %v", err)
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "secrets.db")
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 2)
	}

	store, err := secrets.Open(dbPath, kek)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	const numGoroutines = 10
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			name := "key"
			val := []byte("val")
			_ = store.Put(ctx, name, val)
			_, _ = store.Resolve(ctx, name)
		}(i)
	}
	wg.Wait()
}
