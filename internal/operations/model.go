// Package operations persists durable changeset and operation state outside
// the Git desired-state ledger.
package operations

import (
	"errors"
	"time"
)

var (
	// ErrInvalidTransition reports an attempted state change not permitted by
	// the changeset or operation state machine.
	ErrInvalidTransition = errors.New("operations: invalid transition")
	// ErrNotFound reports an unknown changeset, operation, or plan artifact.
	ErrNotFound = errors.New("operations: not found")
	// ErrPlanSHAMismatch reports an artifact whose draft SHA differs from the
	// exact draft SHA the plan operation is bound to.
	ErrPlanSHAMismatch = errors.New("operations: plan SHA mismatch")
	// ErrPlanArtifactExists reports a second artifact for one plan operation.
	ErrPlanArtifactExists = errors.New("operations: plan artifact already exists")
	// ErrStaleDraft reports a draft advance whose expected draft SHA no longer
	// matches the changeset's current draft SHA.
	ErrStaleDraft = errors.New("operations: stale draft")
)

// ChangesetStatus is the lifecycle state of a Git-backed desired-state draft.
type ChangesetStatus string

const (
	// ChangesetOpen accepts desired-state edits.
	ChangesetOpen ChangesetStatus = "open"
	// ChangesetPlanned has an exact-SHA successful plan.
	ChangesetPlanned ChangesetStatus = "planned"
	// ChangesetApplied has been applied by a later runner integration.
	ChangesetApplied ChangesetStatus = "applied"
	// ChangesetAbandoned is permanently closed without applying.
	ChangesetAbandoned ChangesetStatus = "abandoned"
)

// OperationStatus is the durable state of one plan or apply operation.
type OperationStatus string

const (
	// OperationQueued has been accepted but not started.
	OperationQueued OperationStatus = "queued"
	// OperationRunning is executing against its exact inputs.
	OperationRunning OperationStatus = "running"
	// OperationSucceeded completed successfully.
	OperationSucceeded OperationStatus = "succeeded"
	// OperationFailed completed unsuccessfully.
	OperationFailed OperationStatus = "failed"
)

// Changeset is the operational record of one Git desired-state draft.
type Changeset struct {
	ID        string
	Workspace string
	BaseSHA   string
	DraftSHA  string
	Revision  int64
	Status    ChangesetStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Operation is a durable request to plan or apply one changeset.
type Operation struct {
	ID          string
	ChangesetID string
	Kind        string
	Status      OperationStatus
	PlanSHA     string
	Result      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PlanArtifact is normalized saved-plan data bound to one plan operation.
type PlanArtifact struct {
	OperationID string
	DraftSHA    string
	JSON        []byte
	CreatedAt   time.Time
}

// Event invalidates a changeset or operation representation after a durable
// state change. Payload is opaque JSON owned by the later API layer.
type Event struct {
	ID          int64
	ChangesetID string
	OperationID string
	Type        string
	Data        []byte
	CreatedAt   time.Time
}

func canTransitionChangeset(from, to ChangesetStatus) bool {
	switch from {
	case ChangesetOpen:
		return to == ChangesetPlanned || to == ChangesetAbandoned
	case ChangesetPlanned:
		return to == ChangesetApplied || to == ChangesetAbandoned
	default:
		return false
	}
}

func canTransitionOperation(from, to OperationStatus) bool {
	return (from == OperationQueued && to == OperationRunning) ||
		(from == OperationRunning && (to == OperationSucceeded || to == OperationFailed))
}
