package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/centopw/nodr/internal/engine/opentofu"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/planapply"
	"github.com/centopw/nodr/internal/workspace"
)

// ErrDestructiveNeedsApproval reports that a plan replaces or destroys
// resources and the apply request did not allow it. The API maps it to 422 on
// the field allowDestroy.
var ErrDestructiveNeedsApproval = errors.New("ledger: destructive changes require approval")

// ErrPlanExpired reports that the saved plan of an operation is no longer
// retained by the runner (it was discarded, never retained, or the process
// restarted). The change must be planned again.
var ErrPlanExpired = errors.New("ledger: plan expired")

// PlanRequest describes one plan run. Root is the checkout that holds the
// workspace; the runner may retain it until Discard. Checkout, when set, is
// the checkout at Root, which the runner closes when it discards the plan.
type PlanRequest struct {
	OperationID string
	Workspace   string
	DraftSHA    string
	Root        string
	Checkout    interface{ Close() error }
}

// PlanResult is the outcome of a successful plan run.
type PlanResult struct {
	// JSON is the normalized PlanSummary.
	JSON           []byte
	HasChanges     bool
	HasDestructive bool
}

// ApplyRequest applies the saved plan of an earlier plan operation.
type ApplyRequest struct {
	PlanOperationID string
	AllowDestroy    bool
}

// Runner plans and applies workspaces. Plan retains what Apply needs under
// the plan operation ID until Discard is called.
type Runner interface {
	// Plan compiles and plans the workspace at req.Root, resolving credentials
	// through resolve (which may be nil).
	Plan(ctx context.Context, req PlanRequest, resolve planapply.Resolver) (PlanResult, error)
	// Has reports whether the plan of planOperationID is retained and can be applied.
	Has(planOperationID string) bool
	// Apply applies the saved plan of req.PlanOperationID. It returns
	// ErrPlanExpired if the plan is not retained.
	Apply(ctx context.Context, req ApplyRequest) error
	// Discard releases everything retained for planOperationID. Unknown IDs are ignored.
	Discard(planOperationID string)
}

// PlanUnitSummary is the plan of one state unit.
type PlanUnitSummary struct {
	Dir     string              `json:"dir"`
	Create  int                 `json:"create"`
	Update  int                 `json:"update"`
	Replace int                 `json:"replace"`
	Delete  int                 `json:"delete"`
	Changes []PlanChangeSummary `json:"changes"`
}

// PlanChangeSummary is one resource change of a state unit plan. Unchanged
// instances are omitted.
type PlanChangeSummary struct {
	Address string `json:"address"`
	Action  string `json:"action"`
}

// PlanSummary is the normalized plan stored as a plan artifact and served to
// clients.
type PlanSummary struct {
	Units          []PlanUnitSummary `json:"units"`
	HasChanges     bool              `json:"hasChanges"`
	HasDestructive bool              `json:"hasDestructive"`
}

// LocalRunner plans and applies on this host with the OpenTofu binary found
// by opentofu.LookPath. It keeps plan files under Dir/<operation ID>/ and the
// planned units in memory, so a restart expires every plan.
type LocalRunner struct {
	// Dir is the directory below which each plan keeps its files.
	Dir string

	mu    sync.Mutex
	plans map[string]*runnerRetained
}

type runnerRetained struct {
	dir      string
	plans    []planapply.UnitPlan
	checkout interface{ Close() error }
}

// Plan implements Runner. It loads and validates the workspace at req.Root,
// plans every state unit into Dir/<OperationID>/, and retains the plans.
// On failure it removes the plan directory and retains nothing.
func (r *LocalRunner) Plan(ctx context.Context, req PlanRequest, resolve planapply.Resolver) (PlanResult, error) {
	ws, diags := workspace.Load(req.Root)
	if diags.HasErrors() {
		return PlanResult{}, fmt.Errorf("ledger: load workspace: %w", diags.Err())
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		return PlanResult{}, fmt.Errorf("ledger: build registry: %w", err)
	}
	diags.Append(ws.Validate(reg))
	if diags.HasErrors() {
		return PlanResult{}, fmt.Errorf("ledger: validate workspace: %w", diags.Err())
	}
	planDir := filepath.Join(r.Dir, req.OperationID)
	if err := os.MkdirAll(planDir, 0o700); err != nil {
		return PlanResult{}, fmt.Errorf("ledger: create plan directory: %w", err)
	}
	_, plans, err := planapply.PlanUnits(ctx, ws, nil, planDir, nil, nil, resolve)
	if err != nil {
		_ = os.RemoveAll(planDir)
		return PlanResult{}, fmt.Errorf("ledger: plan: %w", err)
	}
	summary := planSummaryOf(plans)
	data, err := json.Marshal(summary)
	if err != nil {
		_ = os.RemoveAll(planDir)
		return PlanResult{}, fmt.Errorf("ledger: encode plan summary: %w", err)
	}
	r.mu.Lock()
	if r.plans == nil {
		r.plans = make(map[string]*runnerRetained)
	}
	r.plans[req.OperationID] = &runnerRetained{dir: planDir, plans: plans, checkout: req.Checkout}
	r.mu.Unlock()
	return PlanResult{JSON: data, HasChanges: summary.HasChanges, HasDestructive: summary.HasDestructive}, nil
}

// Has implements Runner. It reports whether the plan of planOperationID is retained.
func (r *LocalRunner) Has(planOperationID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.plans[planOperationID]
	return ok
}

// Apply implements Runner. It applies the retained plan units in order.
// Destructive plans need req.AllowDestroy.
func (r *LocalRunner) Apply(ctx context.Context, req ApplyRequest) error {
	r.mu.Lock()
	kept, ok := r.plans[req.PlanOperationID]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("ledger: plan %s: %w", req.PlanOperationID, ErrPlanExpired)
	}
	if !req.AllowDestroy && len(planapply.DestructiveChanges(kept.plans)) > 0 {
		return fmt.Errorf("ledger: plan %s: %w", req.PlanOperationID, ErrDestructiveNeedsApproval)
	}
	if _, err := planapply.Apply(ctx, kept.plans, nil, nil, nil); err != nil {
		return fmt.Errorf("ledger: apply: %w", err)
	}
	return nil
}

// Discard implements Runner. It removes the plan directory and closes the
// checkout retained with the plan.
func (r *LocalRunner) Discard(planOperationID string) {
	r.mu.Lock()
	kept, ok := r.plans[planOperationID]
	delete(r.plans, planOperationID)
	r.mu.Unlock()
	if !ok {
		return
	}
	_ = os.RemoveAll(kept.dir)
	if kept.checkout != nil {
		_ = kept.checkout.Close()
	}
}

// planSummaryOf normalizes planned units into a PlanSummary. Unchanged
// instances are left out of each unit's changes.
func planSummaryOf(plans []planapply.UnitPlan) PlanSummary {
	sum := PlanSummary{Units: make([]PlanUnitSummary, 0, len(plans))}
	for _, u := range plans {
		counts := u.Plan.Summary()
		unit := PlanUnitSummary{
			Dir:     u.Dir,
			Create:  counts.Create,
			Update:  counts.Update,
			Replace: counts.Replace,
			Delete:  counts.Delete,
			Changes: []PlanChangeSummary{},
		}
		for _, c := range u.Plan.Changes {
			if c.Action != opentofu.NoOp {
				unit.Changes = append(unit.Changes, PlanChangeSummary{Address: c.Address, Action: string(c.Action)})
			}
			if c.Action == opentofu.Replace || c.Action == opentofu.Delete {
				sum.HasDestructive = true
			}
		}
		if u.Plan.HasChanges() {
			sum.HasChanges = true
		}
		sum.Units = append(sum.Units, unit)
	}
	return sum
}
