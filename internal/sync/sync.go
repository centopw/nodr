// Package sync implements the single-direction synchronization seam
// between one VirtualMachine's intent and its managed code block (design
// §4.7): it locates the managed block, lifts code into intent to classify
// ownership, rejects a caller's write to a field code owns, writes the
// caller's synced-field changes into code, and mirrors every synced value
// back into intent with minimal edits.
//
// This package implements only that seam. Three-way regeneration against
// pristine refs, multi-draft field-level merge, outdated-base rebase and
// push-driven external sync are out of scope here; see docs/design/04 for
// the full pipeline they belong to.
package sync

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/centopw/nodr/internal/lens"
	"github.com/centopw/nodr/internal/lens/hclmap"
	"github.com/centopw/nodr/internal/lens/proxmoxvm"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/yamledit"
)

// LiftPutter is the subset of proxmoxvm.Lens that Synchronize needs: the
// two directions of the VM lens (design §4.5). proxmoxvm.Lens satisfies it
// without change; tests substitute a double to exercise paths a real lens
// cannot reach, such as a round-trip that fails.
type LiftPutter interface {
	Lift(vm *v1alpha1.VirtualMachine, src []byte, filename string) (*v1alpha1.VirtualMachine, lens.Report, error)
	Put(vm *v1alpha1.VirtualMachine, src []byte, filename string) ([]byte, error)
}

// Input is what Synchronize reconciles.
type Input struct {
	// Lens renders, lifts and puts the VM's managed block.
	Lens LiftPutter
	// Prior is the VM as it reads from IntentSrc before this sync: the
	// base that Lift starts from.
	Prior *v1alpha1.VirtualMachine
	// Desired is the VM the caller wants after this sync. It is Prior
	// unchanged when the caller only wants to reconcile drift already in
	// CodeSrc, or Prior with edited Spec fields when the caller (the
	// GUI) wants to write a change.
	Desired *v1alpha1.VirtualMachine
	// IntentSrc is the content of Prior.Document's file.
	IntentSrc []byte
	// CodeSrc is the content of the file that holds the VM's managed
	// code block.
	CodeSrc []byte
	// CodeFile is the workspace-relative path of that file, matching
	// Lock's entry for the VM.
	CodeFile string
	// Lock is the current content of .nodr/render.lock.
	Lock Lock
}

// Output is the result of a successful Synchronize. Any field may be
// byte-identical, or equal, to the corresponding Input field if nothing
// changed for this VM.
type Output struct {
	IntentSrc []byte
	CodeSrc   []byte
	Lock      Lock
}

// Synchronize runs the seam:
//
//  1. it looks up in.Prior's UID in in.Lock and requires that entry to name
//     in.CodeFile and the block in.Lens locates there; a missing or
//     mismatched entry is ErrRenderLockMismatch, since nodr must not guess
//     the block from labels alone (design §4.7).
//  2. it lifts in.CodeSrc to learn the ownership of every field and to pick
//     up drift the code introduced since in.Prior was last synced.
//  3. for every field where in.Desired differs from in.Prior, it requires
//     the lifted ownership to be Synced; a change to a CodeOwned or
//     Extension field is ErrOwnershipConflict (design §4.8, scenario 4).
//  4. it puts the merged VM into code and mirrors every synced field back
//     into intent with a minimal yamledit diff, never a whole-document
//     marshal.
//  5. it lifts the new code back once more and requires every synced field
//     to read back the value it put; a mismatch is ErrRoundTrip and aborts
//     with nothing written, which overrides design §4.5's literal
//     "fields become code-owned" fallback for this seam.
//
// On any error, Input's sources and lock are left untouched: Synchronize
// never returns a partial Output alongside an error.
func Synchronize(in Input) (Output, error) {
	ref := in.Prior.Ref()

	block, ok := in.Lock.ByUID(in.Prior.Metadata.UID)
	if !ok {
		return Output{}, fmt.Errorf("%s: no render.lock entry for uid %s: %w", ref, in.Prior.Metadata.UID, ErrRenderLockMismatch)
	}
	entries := 0
	for _, candidate := range in.Lock.Blocks {
		if candidate.UID == in.Prior.Metadata.UID {
			entries++
		}
	}
	if entries != 1 {
		return Output{}, fmt.Errorf("%s: render.lock has %d entries for uid %s: %w", ref, entries, in.Prior.Metadata.UID, ErrRenderLockMismatch)
	}
	if block.File != in.CodeFile {
		return Output{}, fmt.Errorf("%s: render.lock names file %s, not %s: %w", ref, block.File, in.CodeFile, ErrRenderLockMismatch)
	}
	target, err := proxmoxvm.Locate(in.CodeSrc, in.CodeFile, in.Prior.Metadata.Name)
	if err != nil {
		return Output{}, fmt.Errorf("%s: %w: %w", ref, err, ErrRenderLockMismatch)
	}
	address := target.Labels[0] + "." + target.Labels[1]
	if block.Address != address || block.Aspect != proxmoxvm.Aspect || block.Engine != proxmoxvm.Engine {
		return Output{}, fmt.Errorf("%s: render.lock entry does not match the managed block: %w", ref, ErrRenderLockMismatch)
	}

	lifted, report, err := in.Lens.Lift(in.Prior, in.CodeSrc, in.CodeFile)
	if err != nil {
		return Output{}, fmt.Errorf("%s: lift: %w", ref, err)
	}

	final := &v1alpha1.VirtualMachine{
		Metadata: lifted.Metadata,
		Spec:     lifted.Spec.DeepCopy(),
		Document: lifted.Document,
	}
	syncedPaths := make(map[string]struct{}, len(report.Fields))
	for _, f := range report.Fields {
		if f.Owner == lens.Synced {
			syncedPaths[f.Path] = struct{}{}
		}
		desiredVal, dOk := valueAt(in.Desired, f.Path)
		priorVal, pOk := valueAt(in.Prior, f.Path)
		if !dOk || !pOk || reflect.DeepEqual(desiredVal, priorVal) {
			continue
		}
		if f.Owner != lens.Synced {
			return Output{}, fmt.Errorf("%s: %s is %s, not synced: %w", ref, f.Path, f.Owner, ErrOwnershipConflict)
		}
		if !setAt(final, f.Path, desiredVal) {
			return Output{}, fmt.Errorf("%s: %s: cannot set this field", ref, f.Path)
		}
	}

	newCodeSrc, err := in.Lens.Put(final, in.CodeSrc, in.CodeFile)
	if err != nil {
		var conflict *hclmap.ConflictError
		if errors.As(err, &conflict) {
			return Output{}, fmt.Errorf("%s: %w", ref, err)
		}
		return Output{}, fmt.Errorf("%s: put: %w", ref, err)
	}

	roundTrip, rtReport, err := in.Lens.Lift(final, newCodeSrc, in.CodeFile)
	if err != nil {
		return Output{}, fmt.Errorf("%s: round-trip lift: %w: %w", ref, err, ErrRoundTrip)
	}
	for path := range syncedPaths {
		field, ok := rtReport.Lookup(path)
		if !ok || field.Owner != lens.Synced {
			return Output{}, fmt.Errorf("%s: %s changed ownership during round-trip: %w", ref, path, ErrRoundTrip)
		}
		want, wOk := valueAt(final, path)
		got, gOk := valueAt(roundTrip, path)
		if wOk != gOk || (wOk && !reflect.DeepEqual(want, got)) {
			return Output{}, fmt.Errorf("%s: %s did not round-trip: %w", ref, path, ErrRoundTrip)
		}
	}

	var edits []yamledit.Edit
	for _, f := range rtReport.Fields {
		if f.Owner != lens.Synced {
			continue
		}
		newVal, ok := valueAt(roundTrip, f.Path)
		if !ok {
			continue
		}
		oldVal, ok := valueAt(in.Prior, f.Path)
		if ok && reflect.DeepEqual(newVal, oldVal) {
			continue
		}
		edits = append(edits, yamledit.Edit{Line: in.Prior.Document.Line, Path: splitIntentPath(f.Path), Value: newVal, Order: keyOrder})
	}
	newIntentSrc := in.IntentSrc
	if len(edits) > 0 {
		if newIntentSrc, err = yamledit.Set(in.IntentSrc, edits, keyOrder); err != nil {
			return Output{}, fmt.Errorf("%s: update intent: %w", ref, err)
		}
	}

	newLock := in.Lock.WithBlock(Block{
		UID:      in.Prior.Metadata.UID,
		Ref:      vmRef(final.Metadata.Name),
		Aspect:   proxmoxvm.Aspect,
		Engine:   proxmoxvm.Engine,
		File:     in.CodeFile,
		Address:  address,
		Renderer: RendererID,
	})

	return Output{IntentSrc: newIntentSrc, CodeSrc: newCodeSrc, Lock: newLock}, nil
}

// vmRef formats the render.lock reference of a VirtualMachine named name,
// for example "vm/web-01" (design §4.6). It mirrors how
// internal/admission.Assignment.String prefixes a VirtualMachine's kind.
func vmRef(name string) string { return "vm/" + name }
