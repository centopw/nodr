package opentofu

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Action is what a plan does to a resource instance.
type Action string

// Actions of a plan. Apart from Replace, they are the values that OpenTofu
// writes in the actions of a resource change in the JSON form of a plan.
const (
	// NoOp leaves the instance as it is.
	NoOp   Action = "no-op"
	Create Action = "create"
	// Read reads a data source during the apply instead of during
	// planning, for example because its arguments are not known before.
	Read   Action = "read"
	Update Action = "update"
	// Replace deletes the instance and creates a new one, in either order.
	Replace Action = "replace"
	Delete  Action = "delete"
	// Forget removes the instance from the state without destroying it, as
	// a removed block with destroy = false does.
	Forget Action = "forget"
)

// Change is the planned change of one resource instance.
type Change struct {
	// Address is the address of the instance in the unit, such as
	// proxmox_virtual_environment_vm.web_01 or
	// data.terraform_remote_state.network.
	Address string
	// Type is the resource type, such as proxmox_virtual_environment_vm.
	Type   string
	Action Action
}

// Plan holds the resource changes of a saved plan.
type Plan struct {
	// Changes are in the order of the plan, which sorts them by address.
	// Instances that the plan leaves as they are have the action NoOp.
	Changes []Change
}

// Summary counts the changes of a plan by action. Changes with the action
// NoOp are not counted.
type Summary struct {
	Create, Update, Replace, Delete, Read, Forget int
}

// Summary returns the number of changes of each action.
func (p Plan) Summary() Summary {
	var s Summary
	for _, c := range p.Changes {
		switch c.Action {
		case Create:
			s.Create++
		case Update:
			s.Update++
		case Replace:
			s.Replace++
		case Delete:
			s.Delete++
		case Read:
			s.Read++
		case Forget:
			s.Forget++
		}
	}
	return s
}

// HasChanges reports whether the plan changes any resource instance, that
// is, whether its summary counts anything. A plan that only moves or
// imports instances, or only changes outputs, has no changes by this
// measure, although applying it would still update the state.
func (p Plan) HasChanges() bool {
	return p.Summary() != Summary{}
}

// ParsePlan reads the resource changes from the JSON form of a saved plan,
// the output of tofu show -json <planfile>. It reads version 1 of the
// format, and rejects a plan that failed.
func ParsePlan(data []byte) (Plan, error) {
	var doc struct {
		FormatVersion   string `json:"format_version"`
		Errored         bool   `json:"errored"`
		ResourceChanges []struct {
			Address string `json:"address"`
			Type    string `json:"type"`
			Change  struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return Plan{}, fmt.Errorf("invalid plan JSON: %w", err)
	}
	// Minor versions of the format only add fields.
	if major, _, _ := strings.Cut(doc.FormatVersion, "."); major != "1" {
		return Plan{}, fmt.Errorf("unsupported plan format version %q, want 1.x", doc.FormatVersion)
	}
	if doc.Errored {
		return Plan{}, errors.New("the plan is incomplete because planning failed")
	}
	var p Plan
	for _, rc := range doc.ResourceChanges {
		a, err := action(rc.Change.Actions)
		if err != nil {
			return Plan{}, fmt.Errorf("%s: %w", rc.Address, err)
		}
		p.Changes = append(p.Changes, Change{Address: rc.Address, Type: rc.Type, Action: a})
	}
	return p, nil
}

// action returns the Action for the actions of a resource change in the
// JSON form of a plan.
func action(actions []string) (Action, error) {
	switch strings.Join(actions, ",") {
	case "no-op":
		return NoOp, nil
	case "create":
		return Create, nil
	case "read":
		return Read, nil
	case "update":
		return Update, nil
	case "delete,create", "create,delete":
		return Replace, nil
	case "delete":
		return Delete, nil
	case "forget":
		return Forget, nil
	}
	return "", fmt.Errorf("unknown actions %q", actions)
}
