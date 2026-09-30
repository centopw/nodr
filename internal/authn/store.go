// Package authn implements the single local administrator account and
// session-cookie authentication for nodr's dashboard and API
// (docs/design/10-security.md §10.3).
package authn

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	_ "modernc.org/sqlite" // registers modernc sqlite driver for database/sql
)

// ErrInvalidCredentials reports a wrong username or password, or a wrong
// bootstrap token during Setup.
var ErrInvalidCredentials = errors.New("authn: invalid credentials")

// ErrBootstrapTokenRequired reports that no admin account exists yet and
// NODR_BOOTSTRAP_TOKEN was not set.
var ErrBootstrapTokenRequired = errors.New("authn: bootstrap token required, set NODR_BOOTSTRAP_TOKEN")

// ErrSetupUnavailable reports that /setup was called but there is no
// pending bootstrap token (already consumed, or never initialized).
var ErrSetupUnavailable = errors.New("authn: setup is not available")

// sessionTTL is the fixed absolute session lifetime for this slice; idle
// timeouts are deferred (see the design doc's open risks).
const sessionTTL = 24 * time.Hour

// Store persists the one local administrator account and active sessions
// in a SQLite database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS account (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    username      TEXT NOT NULL,
    password_hash TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS session (
    token      TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL,
    csrf_token TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS bootstrap (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    token_hash  TEXT NOT NULL,
    consumed_at INTEGER
);
`

// Open opens or creates the authn database at dbPath, mode 0600.
func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("authn: create dir: %w", err)
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("authn: create db file: %w", err)
		}
		_ = f.Close()
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("authn: open sqlite db: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("authn: init schema: %w", err)
	}
	return &Store{db: db}, nil
}

// argon2Params follows the OWASP-recommended baseline for Argon2id.
const (
	argon2Time    = 1
	argon2Memory  = 64 * 1024
	argon2Threads = 4
	argon2KeyLen  = 32
	saltLen       = 16
)

func hashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("authn: generate salt: %w", err)
	}
	sum := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argon2Memory, argon2Time, argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum),
	), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var memory, iterTime, threads uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterTime, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterTime, memory, uint8(threads), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// CreateAccount sets the one local admin account, replacing any existing
// one and invalidating all existing sessions.
func (s *Store) CreateAccount(ctx context.Context, username, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("authn: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op
	if _, err := tx.ExecContext(ctx, `DELETE FROM session`); err != nil {
		return fmt.Errorf("authn: clear sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO account (id, username, password_hash) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET username = excluded.username, password_hash = excluded.password_hash
	`, username, hash); err != nil {
		return fmt.Errorf("authn: upsert account: %w", err)
	}
	return tx.Commit()
}

// InitializeBootstrap prepares the one-time bootstrap token used by
// POST /setup to create the first admin account. It is a no-op, returning
// nil regardless of token, if an account already exists. If no account
// exists and token is empty, it returns ErrBootstrapTokenRequired. If no
// account exists and token is non-empty, it hashes token with Argon2id and
// upserts the single bootstrap row, always overwriting token_hash and
// resetting consumed_at to NULL.
func (s *Store) InitializeBootstrap(ctx context.Context, token string) error {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM account WHERE id = 1`).Scan(&exists)
	if err != nil {
		return fmt.Errorf("authn: check existing account: %w", err)
	}
	if exists > 0 {
		return nil
	}
	if token == "" {
		return ErrBootstrapTokenRequired
	}
	hash, err := hashPassword(token)
	if err != nil {
		return fmt.Errorf("authn: hash bootstrap token: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO bootstrap (id, token_hash, consumed_at) VALUES (1, ?, NULL)
		ON CONFLICT(id) DO UPDATE SET token_hash = excluded.token_hash, consumed_at = NULL
	`, hash); err != nil {
		return fmt.Errorf("authn: upsert bootstrap token: %w", err)
	}
	return nil
}

// Authenticate checks username/password and, on success, creates a new
// session and returns its opaque token and absolute expiry.
func (s *Store) Authenticate(ctx context.Context, username, password string) (string, time.Time, error) {
	var storedUsername, hash string
	err := s.db.QueryRowContext(ctx, `SELECT username, password_hash FROM account WHERE id = 1`).Scan(&storedUsername, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrInvalidCredentials
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("authn: query account: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(storedUsername), []byte(username)) != 1 || !verifyPassword(password, hash) {
		return "", time.Time{}, ErrInvalidCredentials
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", time.Time{}, fmt.Errorf("authn: generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expiresAt := time.Now().Add(sessionTTL)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO session (token, expires_at) VALUES (?, ?)`, token, expiresAt.Unix()); err != nil {
		return "", time.Time{}, fmt.Errorf("authn: create session: %w", err)
	}
	return token, expiresAt, nil
}

// ValidateSession reports whether token is a live, unexpired session.
func (s *Store) ValidateSession(ctx context.Context, token string) bool {
	if token == "" {
		return false
	}
	var expiresAt int64
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM session WHERE token = ?`, token).Scan(&expiresAt)
	if err != nil {
		return false
	}
	return time.Now().Before(time.Unix(expiresAt, 0))
}

// Logout deletes the session for token.
func (s *Store) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM session WHERE token = ?`, token)
	if err != nil {
		return fmt.Errorf("authn: delete session: %w", err)
	}
	return nil
}

// Close closes the underlying SQLite database.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the underlying database for tests in this package that need
// to assert on schema or seed rows directly. Not used by production code.
func (s *Store) DB() *sql.DB {
	return s.db
}
