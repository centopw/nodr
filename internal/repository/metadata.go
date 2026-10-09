package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers modernc sqlite driver for database/sql

	"github.com/centopw/nodr/internal/secrets"
)

// ErrMetadataNotFound reports that no workspace metadata row exists for a slug.
var ErrMetadataNotFound = errors.New("repository: workspace metadata not found")

// WorkspaceMetadata is the operational registration state for one Git-backed
// workspace: which GitHub App installation mirrors it, its default branch, and
// the last default-branch SHA a webhook handler processed. This is operational
// SQLite state per docs/design/02-architecture.md §2.2; the workspace Git
// repository remains the authority for desired state. WebhookSecret is
// plaintext only in memory: at rest it is envelope-encrypted through
// internal/secrets and the metadata row stores only the secret's reference.
type WorkspaceMetadata struct {
	Slug             string
	Owner            string
	Repository       string
	DefaultBranch    string
	InstallationID   int64
	WebhookSecret    string
	LastProcessedSHA string
}

// MetadataStore persists per-workspace registration metadata and webhook
// delivery dedup state in SQLite. It complements LocalStore: the mirror
// configuration LocalStore keeps on disk is a local-cache mechanism, not the
// authority for workspace registration.
type MetadataStore struct {
	db      *sql.DB
	secrets *secrets.Store
}

const metadataSchema = `
CREATE TABLE IF NOT EXISTS workspace_metadata (
    slug               TEXT PRIMARY KEY,
    owner              TEXT NOT NULL,
    repository         TEXT NOT NULL,
    default_branch     TEXT NOT NULL,
    installation_id    INTEGER NOT NULL,
    webhook_secret_enc BLOB NOT NULL,
    last_processed_sha TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS webhook_deliveries (
    delivery_id TEXT PRIMARY KEY,
    received_at INTEGER NOT NULL
);
`

// webhookSecretRef is the secrets-store reference under which the
// envelope-encrypted webhook secret for slug is stored.
func webhookSecretRef(slug string) string {
	return "webhook_secret/" + slug
}

// OpenMetadataStore opens or creates the workspace metadata database at dbPath
// (file mode 0600, parent directory 0700). secretStore provides envelope
// encryption for webhook secrets; the metadata database itself never stores
// webhook secret plaintext.
func OpenMetadataStore(dbPath string, secretStore *secrets.Store) (*MetadataStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("repository: create dir: %w", err)
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("repository: create db file: %w", err)
		}
		_ = f.Close()
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("repository: open sqlite db: %w", err)
	}
	if _, err := db.Exec(metadataSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("repository: init schema: %w", err)
	}
	return &MetadataStore{db: db, secrets: secretStore}, nil
}

// Put inserts or replaces the metadata row for md.Slug and stores its webhook
// secret envelope-encrypted in the secrets store under a slug-derived
// reference.
func (s *MetadataStore) Put(ctx context.Context, md WorkspaceMetadata) error {
	ref := webhookSecretRef(md.Slug)
	if err := s.secrets.Put(ctx, ref, []byte(md.WebhookSecret)); err != nil {
		return fmt.Errorf("repository: encrypt webhook secret for %s: %w", md.Slug, err)
	}
	const query = `
INSERT INTO workspace_metadata
    (slug, owner, repository, default_branch, installation_id, webhook_secret_enc, last_processed_sha)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(slug) DO UPDATE SET
    owner = excluded.owner,
    repository = excluded.repository,
    default_branch = excluded.default_branch,
    installation_id = excluded.installation_id,
    webhook_secret_enc = excluded.webhook_secret_enc,
    last_processed_sha = excluded.last_processed_sha;
`
	_, err := s.db.ExecContext(ctx, query,
		md.Slug, md.Owner, md.Repository, md.DefaultBranch, md.InstallationID, []byte(ref), md.LastProcessedSHA)
	if err != nil {
		return fmt.Errorf("repository: upsert workspace metadata %s: %w", md.Slug, err)
	}
	return nil
}

// Get returns the metadata for slug with its webhook secret decrypted.
func (s *MetadataStore) Get(ctx context.Context, slug string) (WorkspaceMetadata, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT owner, repository, default_branch, installation_id, webhook_secret_enc, last_processed_sha
FROM workspace_metadata WHERE slug = ?`, slug)

	var md WorkspaceMetadata
	var secretRef []byte
	err := row.Scan(&md.Owner, &md.Repository, &md.DefaultBranch, &md.InstallationID, &secretRef, &md.LastProcessedSHA)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceMetadata{}, fmt.Errorf("repository: %s: %w", slug, ErrMetadataNotFound)
	}
	if err != nil {
		return WorkspaceMetadata{}, fmt.Errorf("repository: query workspace metadata %s: %w", slug, err)
	}
	md.Slug = slug
	secret, err := s.secrets.Resolve(ctx, string(secretRef))
	if err != nil {
		return WorkspaceMetadata{}, fmt.Errorf("repository: decrypt webhook secret for %s: %w", slug, err)
	}
	md.WebhookSecret = string(secret)
	return md, nil
}

// List returns all workspace metadata rows sorted by slug, with each webhook
// secret decrypted.
func (s *MetadataStore) List(ctx context.Context) ([]WorkspaceMetadata, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT slug, owner, repository, default_branch, installation_id, webhook_secret_enc, last_processed_sha
FROM workspace_metadata ORDER BY slug ASC`)
	if err != nil {
		return nil, fmt.Errorf("repository: list workspace metadata: %w", err)
	}
	defer rows.Close()

	var out []WorkspaceMetadata
	for rows.Next() {
		var md WorkspaceMetadata
		var secretRef []byte
		if err := rows.Scan(&md.Slug, &md.Owner, &md.Repository, &md.DefaultBranch, &md.InstallationID, &secretRef, &md.LastProcessedSHA); err != nil {
			return nil, fmt.Errorf("repository: scan workspace metadata: %w", err)
		}
		secret, err := s.secrets.Resolve(ctx, string(secretRef))
		if err != nil {
			return nil, fmt.Errorf("repository: decrypt webhook secret for %s: %w", md.Slug, err)
		}
		md.WebhookSecret = string(secret)
		out = append(out, md)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repository: list workspace metadata: %w", err)
	}
	return out, nil
}

// SetLastProcessed records the default-branch SHA last processed for slug.
func (s *MetadataStore) SetLastProcessed(ctx context.Context, slug, sha string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE workspace_metadata SET last_processed_sha = ? WHERE slug = ?`, sha, slug)
	if err != nil {
		return fmt.Errorf("repository: set last processed sha for %s: %w", slug, err)
	}
	return nil
}

// RecordDelivery records a webhook delivery ID. The delivery_id primary key
// makes duplicate deliveries idempotent no-ops instead of errors.
func (s *MetadataStore) RecordDelivery(ctx context.Context, deliveryID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO webhook_deliveries (delivery_id, received_at) VALUES (?, ?)
		 ON CONFLICT(delivery_id) DO NOTHING`, deliveryID, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("repository: record webhook delivery %s: %w", deliveryID, err)
	}
	return nil
}

// DeliverySeen reports whether deliveryID has already been recorded.
func (s *MetadataStore) DeliverySeen(ctx context.Context, deliveryID string) (bool, error) {
	var seen bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM webhook_deliveries WHERE delivery_id = ?)`, deliveryID).Scan(&seen)
	if err != nil {
		return false, fmt.Errorf("repository: check webhook delivery %s: %w", deliveryID, err)
	}
	return seen, nil
}

// Close closes the underlying SQLite database.
func (s *MetadataStore) Close() error {
	return s.db.Close()
}
