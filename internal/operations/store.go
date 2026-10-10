package operations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite" // registers modernc sqlite driver for database/sql
)

const schema = `
CREATE TABLE IF NOT EXISTS changesets (
  id TEXT PRIMARY KEY,
  workspace TEXT NOT NULL,
  base_sha TEXT NOT NULL,
  draft_sha TEXT NOT NULL,
  revision INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS operations (
  id TEXT PRIMARY KEY,
  changeset_id TEXT NOT NULL REFERENCES changesets(id),
  kind TEXT NOT NULL,
  status TEXT NOT NULL,
  plan_sha TEXT NOT NULL DEFAULT '',
  result_summary TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  idempotency_scope TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  UNIQUE (idempotency_scope, idempotency_key)
);
CREATE TABLE IF NOT EXISTS plan_artifacts (
  operation_id TEXT PRIMARY KEY REFERENCES operations(id),
  draft_sha TEXT NOT NULL,
  json BLOB NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  changeset_id TEXT NULL REFERENCES changesets(id),
  operation_id TEXT NULL REFERENCES operations(id),
  type TEXT NOT NULL,
  data BLOB NOT NULL,
  created_at INTEGER NOT NULL
);`

// Store persists operational changesets, operations, artifacts, and events.
type Store struct {
	db *sql.DB

	mutateMu sync.Mutex
	subsMu   sync.Mutex
	subs     map[chan Event]struct{}
}

// Open opens or creates an operations SQLite store at dbPath.
func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("operations: create database directory: %w", err)
	}
	file, err := os.OpenFile(dbPath, os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("operations: create database file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("operations: close database file: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("operations: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("operations: enable foreign keys: %w", err)
	}
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("operations: initialize schema: %w", err)
	}
	return &Store{db: db, subs: make(map[chan Event]struct{})}, nil
}

// Close waits for any in-flight mutation, closes subscriptions, and then
// closes the underlying SQLite database.
func (s *Store) Close() error {
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	s.subsMu.Lock()
	for ch := range s.subs {
		close(ch)
	}
	s.subs = nil
	s.subsMu.Unlock()
	return s.db.Close()
}

// CreateChangeset inserts an open changeset.
func (s *Store) CreateChangeset(ctx context.Context, in Changeset) (Changeset, error) {
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	now := time.Now().UTC()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = now
	in.Status = ChangesetOpen
	_, err := s.db.ExecContext(ctx, `INSERT INTO changesets
		(id, workspace, base_sha, draft_sha, revision, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Workspace, in.BaseSHA, in.DraftSHA, in.Revision, in.Status,
		in.CreatedAt.UnixNano(), in.UpdatedAt.UnixNano())
	if err != nil {
		return Changeset{}, fmt.Errorf("operations: create changeset: %w", err)
	}
	return in, nil
}

// Changeset returns a changeset by ID.
func (s *Store) Changeset(ctx context.Context, id string) (Changeset, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, workspace, base_sha, draft_sha, revision, status, created_at, updated_at FROM changesets WHERE id = ?`, id)
	return scanChangeset(row)
}

// TransitionChangeset applies a valid changeset transition and records one
// invalidation event atomically.
func (s *Store) TransitionChangeset(ctx context.Context, id string, to ChangesetStatus) (Changeset, error) {
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Changeset{}, fmt.Errorf("operations: begin changeset transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `SELECT id, workspace, base_sha, draft_sha, revision, status, created_at, updated_at FROM changesets WHERE id = ?`, id)
	current, err := scanChangeset(row)
	if err != nil {
		return Changeset{}, err
	}
	if !canTransitionChangeset(current.Status, to) {
		return Changeset{}, fmt.Errorf("changeset %s: %s -> %s: %w", id, current.Status, to, ErrInvalidTransition)
	}
	if current.Status == ChangesetOpen && to == ChangesetPlanned {
		var count int
		err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
			FROM operations AS o
			JOIN plan_artifacts AS a ON a.operation_id = o.id
			WHERE o.changeset_id = ? AND o.kind = 'plan' AND o.status = ?
				AND o.plan_sha = ? AND a.draft_sha = ?`,
			current.ID, OperationSucceeded, current.DraftSHA, current.DraftSHA).Scan(&count)
		if err != nil {
			return Changeset{}, fmt.Errorf("operations: validate changeset plan: %w", err)
		}
		if count == 0 {
			return Changeset{}, fmt.Errorf("changeset %s: %w", current.ID, ErrPlanSHAMismatch)
		}
	}
	current.Status, current.UpdatedAt = to, time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE changesets SET status = ?, updated_at = ? WHERE id = ?`, current.Status, current.UpdatedAt.UnixNano(), id); err != nil {
		return Changeset{}, fmt.Errorf("operations: transition changeset: %w", err)
	}
	event, err := appendEvent(ctx, tx, current.ID, "", "changeset."+string(to))
	if err != nil {
		return Changeset{}, err
	}
	if err := tx.Commit(); err != nil {
		return Changeset{}, fmt.Errorf("operations: commit changeset transition: %w", err)
	}
	s.publish(event)
	return current, nil
}

func scanChangeset(row interface{ Scan(...any) error }) (Changeset, error) {
	var out Changeset
	var createdAt, updatedAt int64
	if err := row.Scan(&out.ID, &out.Workspace, &out.BaseSHA, &out.DraftSHA, &out.Revision, &out.Status, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Changeset{}, fmt.Errorf("changeset: %w", ErrNotFound)
		}
		return Changeset{}, fmt.Errorf("operations: scan changeset: %w", err)
	}
	out.CreatedAt = time.Unix(0, createdAt).UTC()
	out.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return out, nil
}

// ClaimOperation creates an operation exactly once for an idempotency scope
// and key. Repeated claims return the originally created operation.
func (s *Store) ClaimOperation(ctx context.Context, in Operation, scope, key string) (Operation, error) {
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, fmt.Errorf("operations: begin operation claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	changeset, err := scanChangeset(tx.QueryRowContext(ctx, `SELECT id, workspace, base_sha, draft_sha, revision, status, created_at, updated_at FROM changesets WHERE id = ?`, in.ChangesetID))
	if err != nil {
		return Operation{}, err
	}
	if in.Kind == "plan" && in.PlanSHA != changeset.DraftSHA {
		return Operation{}, fmt.Errorf("changeset %s: %w", changeset.ID, ErrPlanSHAMismatch)
	}
	in.UpdatedAt = now
	in.Status = OperationQueued
	result, err := tx.ExecContext(ctx, `INSERT INTO operations
		(id, changeset_id, kind, status, plan_sha, result_summary, created_at, updated_at, idempotency_scope, idempotency_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(idempotency_scope, idempotency_key) DO NOTHING`,
		in.ID, in.ChangesetID, in.Kind, in.Status, in.PlanSHA, in.Result,
		in.CreatedAt.UnixNano(), in.UpdatedAt.UnixNano(), scope, key)
	if err != nil {
		return Operation{}, fmt.Errorf("operations: claim operation: %w", err)
	}
	created, err := result.RowsAffected()
	if err != nil {
		return Operation{}, fmt.Errorf("operations: operation claim rows: %w", err)
	}
	row := tx.QueryRowContext(ctx, `SELECT id, changeset_id, kind, status, plan_sha, result_summary, created_at, updated_at FROM operations WHERE idempotency_scope = ? AND idempotency_key = ?`, scope, key)
	op, err := scanOperation(row)
	if err != nil {
		return Operation{}, err
	}
	var event Event
	if created == 1 {
		event, err = appendEvent(ctx, tx, op.ChangesetID, op.ID, "operation.created")
		if err != nil {
			return Operation{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Operation{}, fmt.Errorf("operations: commit operation claim: %w", err)
	}
	if created == 1 {
		s.publish(event)
	}
	return op, nil
}

// Operation returns an operation by ID.
func (s *Store) Operation(ctx context.Context, id string) (Operation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, changeset_id, kind, status, plan_sha, result_summary, created_at, updated_at FROM operations WHERE id = ?`, id)
	return scanOperation(row)
}

// TransitionOperation applies a valid operation transition and persists its
// invalidation event in the same transaction.
func (s *Store) TransitionOperation(ctx context.Context, id string, to OperationStatus) (Operation, error) {
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, fmt.Errorf("operations: begin operation transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `SELECT id, changeset_id, kind, status, plan_sha, result_summary, created_at, updated_at FROM operations WHERE id = ?`, id)
	op, err := scanOperation(row)
	if err != nil {
		return Operation{}, err
	}
	if !canTransitionOperation(op.Status, to) {
		return Operation{}, fmt.Errorf("operation %s: %s -> %s: %w", id, op.Status, to, ErrInvalidTransition)
	}
	op.Status, op.UpdatedAt = to, time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE operations SET status = ?, updated_at = ? WHERE id = ?`, op.Status, op.UpdatedAt.UnixNano(), op.ID); err != nil {
		return Operation{}, fmt.Errorf("operations: transition operation: %w", err)
	}
	event, err := appendEvent(ctx, tx, op.ChangesetID, op.ID, "operation."+string(to))
	if err != nil {
		return Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Operation{}, fmt.Errorf("operations: commit operation transition: %w", err)
	}
	s.publish(event)
	return op, nil
}

func scanOperation(row interface{ Scan(...any) error }) (Operation, error) {
	var out Operation
	var createdAt, updatedAt int64
	if err := row.Scan(&out.ID, &out.ChangesetID, &out.Kind, &out.Status, &out.PlanSHA, &out.Result, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Operation{}, fmt.Errorf("operation: %w", ErrNotFound)
		}
		return Operation{}, fmt.Errorf("operations: scan operation: %w", err)
	}
	out.CreatedAt = time.Unix(0, createdAt).UTC()
	out.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return out, nil
}

// PutPlanArtifact stores a plan artifact only for its running plan operation
// and only when the artifact draft SHA exactly matches the operation binding.
func (s *Store) PutPlanArtifact(ctx context.Context, in PlanArtifact) (PlanArtifact, error) {
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanArtifact{}, fmt.Errorf("operations: begin plan artifact: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `SELECT id, changeset_id, kind, status, plan_sha, result_summary, created_at, updated_at FROM operations WHERE id = ?`, in.OperationID)
	op, err := scanOperation(row)
	if err != nil {
		return PlanArtifact{}, err
	}
	if op.Kind != "plan" || op.Status != OperationRunning {
		return PlanArtifact{}, fmt.Errorf("operation %s cannot store a plan artifact: %w", op.ID, ErrInvalidTransition)
	}
	if op.PlanSHA != in.DraftSHA {
		return PlanArtifact{}, fmt.Errorf("operation %s: %w", op.ID, ErrPlanSHAMismatch)
	}
	var existing int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plan_artifacts WHERE operation_id = ?`, in.OperationID).Scan(&existing)
	if err != nil {
		return PlanArtifact{}, fmt.Errorf("operations: check plan artifact: %w", err)
	}
	if existing != 0 {
		return PlanArtifact{}, fmt.Errorf("operation %s: %w", op.ID, ErrPlanArtifactExists)
	}
	in.CreatedAt = time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO plan_artifacts (operation_id, draft_sha, json, created_at) VALUES (?, ?, ?, ?)`, in.OperationID, in.DraftSHA, in.JSON, in.CreatedAt.UnixNano()); err != nil {
		return PlanArtifact{}, fmt.Errorf("operations: store plan artifact: %w", err)
	}
	event, err := appendEvent(ctx, tx, op.ChangesetID, op.ID, "plan.artifact.stored")
	if err != nil {
		return PlanArtifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return PlanArtifact{}, fmt.Errorf("operations: commit plan artifact: %w", err)
	}
	s.publish(event)
	in.JSON = append([]byte(nil), in.JSON...)
	return in, nil
}

// PlanArtifact returns the artifact for an operation with an independent byte slice.
func (s *Store) PlanArtifact(ctx context.Context, operationID string) (PlanArtifact, error) {
	var out PlanArtifact
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `SELECT operation_id, draft_sha, json, created_at FROM plan_artifacts WHERE operation_id = ?`, operationID).Scan(&out.OperationID, &out.DraftSHA, &out.JSON, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PlanArtifact{}, fmt.Errorf("plan artifact: %w", ErrNotFound)
	}
	if err != nil {
		return PlanArtifact{}, fmt.Errorf("operations: load plan artifact: %w", err)
	}
	out.CreatedAt = time.Unix(0, createdAt).UTC()
	out.JSON = append([]byte(nil), out.JSON...)
	return out, nil
}

// AdvanceDraft sets DraftSHA=newSHA and Revision++ iff the changeset is open or
// planned and DraftSHA==expectedSHA. A planned changeset returns to open because
// its plan no longer matches the new draft. A closed changeset yields
// ErrInvalidTransition; a mismatched expectedSHA yields ErrStaleDraft. Records
// one "changeset.updated" event atomically.
func (s *Store) AdvanceDraft(ctx context.Context, id, expectedSHA, newSHA string) (Changeset, error) {
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Changeset{}, fmt.Errorf("operations: begin draft advance: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `SELECT id, workspace, base_sha, draft_sha, revision, status, created_at, updated_at FROM changesets WHERE id = ?`, id)
	current, err := scanChangeset(row)
	if err != nil {
		return Changeset{}, err
	}
	if current.Status != ChangesetOpen && current.Status != ChangesetPlanned {
		return Changeset{}, fmt.Errorf("changeset %s: %s: advance draft: %w", id, current.Status, ErrInvalidTransition)
	}
	if current.DraftSHA != expectedSHA {
		return Changeset{}, fmt.Errorf("changeset %s: %w", id, ErrStaleDraft)
	}
	current.DraftSHA = newSHA
	current.Revision++
	current.Status = ChangesetOpen
	current.UpdatedAt = time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE changesets SET draft_sha = ?, revision = ?, status = ?, updated_at = ? WHERE id = ?`,
		current.DraftSHA, current.Revision, current.Status, current.UpdatedAt.UnixNano(), id); err != nil {
		return Changeset{}, fmt.Errorf("operations: advance draft: %w", err)
	}
	event, err := appendEvent(ctx, tx, current.ID, "", "changeset.updated")
	if err != nil {
		return Changeset{}, err
	}
	if err := tx.Commit(); err != nil {
		return Changeset{}, fmt.Errorf("operations: commit draft advance: %w", err)
	}
	s.publish(event)
	return current, nil
}

// Changesets lists a workspace's changesets, newest first.
func (s *Store) Changesets(ctx context.Context, workspace string) ([]Changeset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace, base_sha, draft_sha, revision, status, created_at, updated_at FROM changesets WHERE workspace = ? ORDER BY created_at DESC, id DESC`, workspace)
	if err != nil {
		return nil, fmt.Errorf("operations: list changesets: %w", err)
	}
	defer rows.Close()
	var out []Changeset
	for rows.Next() {
		changeset, err := scanChangeset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, changeset)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("operations: iterate changesets: %w", err)
	}
	return out, nil
}

// FinishOperation moves a running operation to succeeded or failed and stores
// result atomically with the status change. Any other transition yields
// ErrInvalidTransition. Records one "operation.<to>" event in the same
// transaction.
func (s *Store) FinishOperation(ctx context.Context, id string, to OperationStatus, result string) (Operation, error) {
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, fmt.Errorf("operations: begin operation finish: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `SELECT id, changeset_id, kind, status, plan_sha, result_summary, created_at, updated_at FROM operations WHERE id = ?`, id)
	op, err := scanOperation(row)
	if err != nil {
		return Operation{}, err
	}
	if !canTransitionOperation(op.Status, to) || (to != OperationSucceeded && to != OperationFailed) {
		return Operation{}, fmt.Errorf("operation %s: %s -> %s: %w", id, op.Status, to, ErrInvalidTransition)
	}
	op.Status, op.Result, op.UpdatedAt = to, result, time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE operations SET status = ?, result_summary = ?, updated_at = ? WHERE id = ?`, op.Status, op.Result, op.UpdatedAt.UnixNano(), op.ID); err != nil {
		return Operation{}, fmt.Errorf("operations: finish operation: %w", err)
	}
	event, err := appendEvent(ctx, tx, op.ChangesetID, op.ID, "operation."+string(to))
	if err != nil {
		return Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Operation{}, fmt.Errorf("operations: commit operation finish: %w", err)
	}
	s.publish(event)
	return op, nil
}

// OperationByKey returns the operation claimed under the idempotency scope and
// key, or an error wrapping ErrNotFound.
func (s *Store) OperationByKey(ctx context.Context, scope, key string) (Operation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, changeset_id, kind, status, plan_sha, result_summary, created_at, updated_at FROM operations WHERE idempotency_scope = ? AND idempotency_key = ?`, scope, key)
	return scanOperation(row)
}

// LatestPlan returns the newest succeeded plan operation of a changeset and its
// artifact, or an error wrapping ErrNotFound when none exists.
func (s *Store) LatestPlan(ctx context.Context, changesetID string) (Operation, PlanArtifact, error) {
	var op Operation
	var artifact PlanArtifact
	var opCreatedAt, opUpdatedAt, artifactCreatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT o.id, o.changeset_id, o.kind, o.status, o.plan_sha, o.result_summary, o.created_at, o.updated_at,
			a.operation_id, a.draft_sha, a.json, a.created_at
		FROM operations AS o
		JOIN plan_artifacts AS a ON a.operation_id = o.id
		WHERE o.changeset_id = ? AND o.kind = 'plan' AND o.status = ?
		ORDER BY o.created_at DESC, o.id DESC
		LIMIT 1`, changesetID, OperationSucceeded).Scan(
		&op.ID, &op.ChangesetID, &op.Kind, &op.Status, &op.PlanSHA, &op.Result, &opCreatedAt, &opUpdatedAt,
		&artifact.OperationID, &artifact.DraftSHA, &artifact.JSON, &artifactCreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, PlanArtifact{}, fmt.Errorf("changeset %s plan: %w", changesetID, ErrNotFound)
	}
	if err != nil {
		return Operation{}, PlanArtifact{}, fmt.Errorf("operations: load latest plan: %w", err)
	}
	op.CreatedAt = time.Unix(0, opCreatedAt).UTC()
	op.UpdatedAt = time.Unix(0, opUpdatedAt).UTC()
	artifact.CreatedAt = time.Unix(0, artifactCreatedAt).UTC()
	artifact.JSON = append([]byte(nil), artifact.JSON...)
	return op, artifact, nil
}
