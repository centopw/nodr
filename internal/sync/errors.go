package sync

import "errors"

// Errors that Synchronize returns. On any of them, in.IntentSrc, in.CodeSrc
// and in.Lock are left untouched: nothing is written.
var (
	// ErrOwnershipConflict reports a desired VM that changes a field the
	// render.lock's code owns (design §4.8, scenario 4): the GUI tried to
	// edit a code-owned or extension field.
	ErrOwnershipConflict = errors.New("ownership conflict")
	// ErrRenderLockMismatch reports a missing or mismatched
	// .nodr/render.lock entry for the VM's managed block (design §4.7):
	// nodr must not guess the block from labels alone.
	ErrRenderLockMismatch = errors.New("render.lock mismatch")
	// ErrRoundTrip reports that writing the final VM into code and lifting
	// it back did not reproduce the same synced values (the lens's
	// stability/fidelity laws, design §4.5). This overrides the literal
	// "fields become code-owned" fallback in §4.5: for this seam, a failed
	// round trip aborts the whole synchronization instead.
	ErrRoundTrip = errors.New("lens round-trip failed")
)
