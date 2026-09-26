package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Store is an envelope-encrypted, SQLite-backed store for secrets.
type Store struct {
	db  *sql.DB
	kek []byte
}

const schema = `
CREATE TABLE IF NOT EXISTS secrets (
    name             TEXT PRIMARY KEY,
    wrapped_key      BLOB NOT NULL,
    wrapped_nonce    BLOB NOT NULL,
    ciphertext       BLOB NOT NULL,
    ciphertext_nonce BLOB NOT NULL
);
`

// Open opens or creates an encrypted secrets store at dbPath.
// If the file does not exist, it is created with file mode 0600.
func Open(dbPath string, kek []byte) (*Store, error) {
	if len(kek) != 32 {
		return nil, fmt.Errorf("secrets: KEK must be exactly 32 bytes (got %d): %w", len(kek), ErrInvalidKEK)
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("secrets: create dir: %w", err)
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("secrets: create db file: %w", err)
		}
		_ = f.Close()
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("secrets: open sqlite db: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secrets: init schema: %w", err)
	}

	return &Store{db: db, kek: kek}, nil
}

// Put encrypts value using a freshly generated 32-byte data key, wraps the
// data key with the store's KEK, and inserts or replaces the secret row.
func (s *Store) Put(ctx context.Context, name string, value []byte) error {
	dataKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dataKey); err != nil {
		return fmt.Errorf("secrets: generate data key: %w", err)
	}

	// 1. Wrap dataKey with KEK (AES-256-GCM)
	kekBlock, err := aes.NewCipher(s.kek)
	if err != nil {
		return fmt.Errorf("secrets: cipher KEK: %w", err)
	}
	kekGCM, err := cipher.NewGCM(kekBlock)
	if err != nil {
		return fmt.Errorf("secrets: gcm KEK: %w", err)
	}
	wrappedNonce := make([]byte, kekGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, wrappedNonce); err != nil {
		return fmt.Errorf("secrets: generate wrapped nonce: %w", err)
	}
	wrappedKey := kekGCM.Seal(nil, wrappedNonce, dataKey, nil)

	// 2. Encrypt value with dataKey (AES-256-GCM)
	dataBlock, err := aes.NewCipher(dataKey)
	if err != nil {
		return fmt.Errorf("secrets: cipher data key: %w", err)
	}
	dataGCM, err := cipher.NewGCM(dataBlock)
	if err != nil {
		return fmt.Errorf("secrets: gcm data key: %w", err)
	}
	ciphertextNonce := make([]byte, dataGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, ciphertextNonce); err != nil {
		return fmt.Errorf("secrets: generate ciphertext nonce: %w", err)
	}
	ciphertext := dataGCM.Seal(nil, ciphertextNonce, value, nil)

	query := `
INSERT INTO secrets (name, wrapped_key, wrapped_nonce, ciphertext, ciphertext_nonce)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    wrapped_key=excluded.wrapped_key,
    wrapped_nonce=excluded.wrapped_nonce,
    ciphertext=excluded.ciphertext,
    ciphertext_nonce=excluded.ciphertext_nonce;
`
	_, err = s.db.ExecContext(ctx, query, name, wrappedKey, wrappedNonce, ciphertext, ciphertextNonce)
	if err != nil {
		return fmt.Errorf("secrets: insert secret %q: %w", name, err)
	}
	return nil
}

// Resolve unwraps the data key for ref using KEK, decrypts ciphertext, and returns the plaintext secret.
func (s *Store) Resolve(ctx context.Context, ref string) ([]byte, error) {
	query := `SELECT wrapped_key, wrapped_nonce, ciphertext, ciphertext_nonce FROM secrets WHERE name = ?`
	row := s.db.QueryRowContext(ctx, query, ref)

	var wrappedKey, wrappedNonce, ciphertext, ciphertextNonce []byte
	err := row.Scan(&wrappedKey, &wrappedNonce, &ciphertext, &ciphertextNonce)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("secrets: %s: %w", ref, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: query %s: %w", ref, err)
	}

	// 1. Unwrap data key using KEK
	kekBlock, err := aes.NewCipher(s.kek)
	if err != nil {
		return nil, fmt.Errorf("secrets: cipher KEK: %w", err)
	}
	kekGCM, err := cipher.NewGCM(kekBlock)
	if err != nil {
		return nil, fmt.Errorf("secrets: gcm KEK: %w", err)
	}
	dataKey, err := kekGCM.Open(nil, wrappedNonce, wrappedKey, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: unwrap key for %s: %w", ref, ErrDecrypt)
	}

	// 2. Decrypt ciphertext using data key
	dataBlock, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, fmt.Errorf("secrets: cipher data key: %w", err)
	}
	dataGCM, err := cipher.NewGCM(dataBlock)
	if err != nil {
		return nil, fmt.Errorf("secrets: gcm data key: %w", err)
	}
	value, err := dataGCM.Open(nil, ciphertextNonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: decrypt ciphertext for %s: %w", ref, ErrDecrypt)
	}

	return value, nil
}

// Delete removes a secret by name.
func (s *Store) Delete(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("secrets: delete %s: %w", name, err)
	}
	return nil
}

// List returns the names of all stored secrets.
func (s *Store) List(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM secrets ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("secrets: list: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("secrets: scan name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// Close closes the underlying SQLite database.
func (s *Store) Close() error {
	return s.db.Close()
}
