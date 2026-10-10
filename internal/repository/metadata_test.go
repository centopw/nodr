package repository

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/secrets"
)

func makeKEK(seed byte) []byte {
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = seed + byte(i)
	}
	return kek
}

func openMetadataStoreIn(t *testing.T, dir string, kek []byte) (*MetadataStore, *secrets.Store) {
	t.Helper()
	secretStore, err := secrets.Open(filepath.Join(dir, "secrets.db"), kek)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	t.Cleanup(func() { secretStore.Close() })
	ms, err := OpenMetadataStore(filepath.Join(dir, "metadata.db"), secretStore)
	if err != nil {
		t.Fatalf("OpenMetadataStore: %v", err)
	}
	t.Cleanup(func() { ms.Close() })
	return ms, secretStore
}

func newMetadataStore(t *testing.T) (*MetadataStore, *secrets.Store) {
	t.Helper()
	return openMetadataStoreIn(t, t.TempDir(), makeKEK(1))
}

func sampleMetadata() WorkspaceMetadata {
	return WorkspaceMetadata{
		Slug:           "homelab/core",
		Owner:          "acme",
		Repository:     "core",
		DefaultBranch:  "main",
		InstallationID: 42,
		WebhookSecret:  "whsec_redacted123",
	}
}

func TestMetadataStore_PutGetRoundTrip(t *testing.T) {
	ms, _ := newMetadataStore(t)
	ctx := context.Background()
	in := sampleMetadata()
	if err := ms.Put(ctx, in); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := ms.Get(ctx, in.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != in {
		t.Fatalf("Get = %+v, want %+v", got, in)
	}
}

func TestMetadataStore_WebhookSecretEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	ms, secretStore := openMetadataStoreIn(t, dir, makeKEK(1))
	ctx := context.Background()
	in := sampleMetadata()
	if err := ms.Put(ctx, in); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := ms.Close(); err != nil {
		t.Fatalf("Close metadata store: %v", err)
	}
	if err := secretStore.Close(); err != nil {
		t.Fatalf("Close secrets store: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "metadata.db"))
	if err != nil {
		t.Fatalf("stat metadata db: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("metadata db mode = %04o, want 0600", perm)
	}
	for _, name := range []string{"metadata.db", "secrets.db"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if bytes.Contains(raw, []byte(in.WebhookSecret)) {
			t.Fatalf("%s contains plaintext webhook secret", name)
		}
	}
}

func TestMetadataStore_GetUnknownSlug(t *testing.T) {
	ms, _ := newMetadataStore(t)
	_, err := ms.Get(context.Background(), "nope/missing")
	if !errors.Is(err, ErrMetadataNotFound) {
		t.Fatalf("err = %v, want ErrMetadataNotFound", err)
	}
}

func TestMetadataStore_PutUpsertsExistingSlug(t *testing.T) {
	ms, _ := newMetadataStore(t)
	ctx := context.Background()
	first := sampleMetadata()
	if err := ms.Put(ctx, first); err != nil {
		t.Fatalf("Put: %v", err)
	}
	second := sampleMetadata()
	second.Owner = "acme-corp"
	second.DefaultBranch = "trunk"
	second.InstallationID = 99
	second.WebhookSecret = "whsec_rotated"
	second.LastProcessedSHA = "abc123"
	if err := ms.Put(ctx, second); err != nil {
		t.Fatalf("second Put: %v", err)
	}
	got, err := ms.Get(ctx, first.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != second {
		t.Fatalf("Get = %+v, want %+v", got, second)
	}
	list, err := ms.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0] != second {
		t.Fatalf("List = %+v, want single %+v", list, second)
	}
}

func TestMetadataStore_ListSortedBySlug(t *testing.T) {
	ms, _ := newMetadataStore(t)
	ctx := context.Background()
	slugs := []string{"zeta/ws", "alpha/ws", "mid/ws"}
	for i, slug := range slugs {
		in := WorkspaceMetadata{
			Slug:           slug,
			Owner:          "acme",
			Repository:     "repo",
			DefaultBranch:  "main",
			InstallationID: int64(i + 1),
			WebhookSecret:  "whsec_" + slug,
		}
		if err := ms.Put(ctx, in); err != nil {
			t.Fatalf("Put %s: %v", slug, err)
		}
	}
	list, err := ms.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != len(slugs) {
		t.Fatalf("List length = %d, want %d", len(list), len(slugs))
	}
	want := []string{"alpha/ws", "mid/ws", "zeta/ws"}
	for i, got := range list {
		if got.Slug != want[i] {
			t.Fatalf("List[%d].Slug = %q, want %q", i, got.Slug, want[i])
		}
		if got.WebhookSecret != "whsec_"+want[i] {
			t.Fatalf("List[%d].WebhookSecret = %q, want decrypted per-slug secret", i, got.WebhookSecret)
		}
	}
}

func TestMetadataStore_SetLastProcessed(t *testing.T) {
	ms, _ := newMetadataStore(t)
	ctx := context.Background()
	in := sampleMetadata()
	if err := ms.Put(ctx, in); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := ms.Get(ctx, in.Slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastProcessedSHA != "" {
		t.Fatalf("initial LastProcessedSHA = %q, want empty", got.LastProcessedSHA)
	}
	if err := ms.SetLastProcessed(ctx, in.Slug, "deadbeef01"); err != nil {
		t.Fatalf("SetLastProcessed: %v", err)
	}
	got, err = ms.Get(ctx, in.Slug)
	if err != nil {
		t.Fatalf("Get after SetLastProcessed: %v", err)
	}
	if got.LastProcessedSHA != "deadbeef01" {
		t.Fatalf("LastProcessedSHA = %q, want deadbeef01", got.LastProcessedSHA)
	}
}

func TestMetadataStore_DeliveryDedup(t *testing.T) {
	ms, _ := newMetadataStore(t)
	ctx := context.Background()
	seen, err := ms.DeliverySeen(ctx, "delivery-1")
	if err != nil {
		t.Fatalf("DeliverySeen before record: %v", err)
	}
	if seen {
		t.Fatal("DeliverySeen = true before RecordDelivery, want false")
	}
	if err := ms.RecordDelivery(ctx, "delivery-1"); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	seen, err = ms.DeliverySeen(ctx, "delivery-1")
	if err != nil {
		t.Fatalf("DeliverySeen after record: %v", err)
	}
	if !seen {
		t.Fatal("DeliverySeen = false after RecordDelivery, want true")
	}
	// Duplicate record must be an idempotent no-op.
	if err := ms.RecordDelivery(ctx, "delivery-1"); err != nil {
		t.Fatalf("duplicate RecordDelivery: %v", err)
	}
	seen, err = ms.DeliverySeen(ctx, "delivery-1")
	if err != nil {
		t.Fatalf("DeliverySeen after duplicate record: %v", err)
	}
	if !seen {
		t.Fatal("DeliverySeen = false after duplicate record, want true")
	}
	seen, err = ms.DeliverySeen(ctx, "delivery-2")
	if err != nil {
		t.Fatalf("DeliverySeen delivery-2: %v", err)
	}
	if seen {
		t.Fatal("DeliverySeen(delivery-2) = true, want false")
	}
}

func TestMetadataStore_GetFailsWithWrongKEK(t *testing.T) {
	dir := t.TempDir()
	ms, secretStore := openMetadataStoreIn(t, dir, makeKEK(1))
	ctx := context.Background()
	in := sampleMetadata()
	if err := ms.Put(ctx, in); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := ms.Close(); err != nil {
		t.Fatalf("Close metadata store: %v", err)
	}
	if err := secretStore.Close(); err != nil {
		t.Fatalf("Close secrets store: %v", err)
	}
	ms2, _ := openMetadataStoreIn(t, dir, makeKEK(2))
	_, err := ms2.Get(ctx, in.Slug)
	if err == nil {
		t.Fatal("Get with wrong KEK = nil error, want decrypt failure")
	}
	if !errors.Is(err, secrets.ErrDecrypt) {
		t.Fatalf("err = %v, want wrap of secrets.ErrDecrypt", err)
	}
	if !strings.Contains(err.Error(), "repository:") {
		t.Fatalf("err = %v, want repository-prefixed wrap", err)
	}
}
