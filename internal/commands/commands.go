// Package commands implements nodr's VirtualMachine commands against a
// workspace directory. Every function reloads the workspace from root, so
// results reflect current intent on disk. Failures are reported as *Error,
// which carries the HTTP-facing status, title, detail and field errors.
// Callers serialize concurrent mutations; these functions take no locks.
package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/centopw/nodr/internal/diag"
	"github.com/centopw/nodr/internal/nrm/v1alpha1"
	"github.com/centopw/nodr/internal/workspace"
)

// FieldError locates a failure at a parameter path such as "params.name".
type FieldError struct {
	Path    string
	Message string
}

// Error is a user-facing command failure. The API maps Status, Title, Detail
// and Fields onto an RFC 9457 problem document.
type Error struct {
	Status int
	Title  string
	Detail string
	Fields []FieldError
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Title
	}
	return e.Title + ": " + e.Detail
}

// Run dispatches "vm.create", "vm.start", "vm.stop" and "vm.delete" with raw
// JSON params, decoded strictly (unknown fields are rejected). It returns the
// JSON-serialisable result. Any other command yields a *Error with status 400.
func Run(root, command string, params json.RawMessage) (any, error) {
	switch command {
	case "vm.create":
		p, err := ParseVMCreateParams(params)
		if err != nil {
			return nil, err
		}
		return CreateVM(root, p)
	case "vm.start", "vm.stop":
		name, err := ActionName(params, "")
		if err != nil {
			return nil, err
		}
		state := v1alpha1.PowerStateRunning
		if command == "vm.stop" {
			state = v1alpha1.PowerStateStopped
		}
		return SetVMPowerState(root, name, state)
	case "vm.delete":
		name, err := ActionName(params, "")
		if err != nil {
			return nil, err
		}
		return DeleteVM(root, name)
	}
	message := fmt.Sprintf("command %q is not supported", command)
	return nil, &Error{
		Status: http.StatusBadRequest,
		Title:  "Unknown command",
		Detail: message,
		Fields: []FieldError{{Path: "command", Message: message}},
	}
}

// ParseVMCreateParams strictly decodes vm.create params. Empty params yield
// the zero value, which validation then rejects field by field.
func ParseVMCreateParams(raw json.RawMessage) (VMCreateParams, error) {
	var p VMCreateParams
	if len(raw) > 0 {
		if err := decodeStrict(raw, &p); err != nil {
			return VMCreateParams{}, invalidParams(err)
		}
	}
	return p, nil
}

// ActionName returns the VirtualMachine name addressed by a vm.start, vm.stop
// or vm.delete request. The name comes from params.name, falling back to
// target when params omit it. A missing name is a *Error with status 400.
func ActionName(raw json.RawMessage, target string) (string, error) {
	var p vmActionParams
	if len(raw) > 0 {
		if err := decodeStrict(raw, &p); err != nil {
			return "", invalidParams(err)
		}
	}
	if p.Name == "" && target != "" {
		p.Name = target
	}
	if p.Name == "" {
		message := "name is required"
		return "", &Error{
			Status: http.StatusBadRequest,
			Title:  "Invalid request",
			Detail: message,
			Fields: []FieldError{{Path: "name", Message: message}},
		}
	}
	return p.Name, nil
}

func decodeStrict(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func invalidParams(err error) *Error {
	return &Error{
		Status: http.StatusBadRequest,
		Title:  "Invalid request",
		Detail: fmt.Sprintf("decode params: %v", err),
	}
}

func failure(title string, err error) *Error {
	return &Error{Status: http.StatusInternalServerError, Title: title, Detail: err.Error()}
}

// load reads and validates the workspace at root, reporting failures under
// the generic "Workspace load failed" and "Workspace validation failed" titles.
func load(root string) (*workspace.Workspace, *Error) {
	ws, diags := workspace.Load(root)
	if diags.HasErrors() {
		return nil, diagnosticError(http.StatusInternalServerError, "Workspace load failed", diags)
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		return nil, failure("Internal server error", err)
	}
	diags.Append(ws.Validate(reg))
	if diags.HasErrors() {
		return nil, diagnosticError(http.StatusInternalServerError, "Workspace validation failed", diags)
	}
	return ws, nil
}

// loadFor reads and validates the workspace at root for a named operation.
// Load failures are 500; validation failures are 400. Both use title.
func loadFor(root, title string) (*workspace.Workspace, *Error) {
	ws, diags := workspace.Load(root)
	if diags.HasErrors() {
		return nil, diagnosticError(http.StatusInternalServerError, title, diags)
	}
	reg, err := v1alpha1.NewRegistry()
	if err != nil {
		return nil, failure(title, err)
	}
	diags.Append(ws.Validate(reg))
	if diags.HasErrors() {
		return nil, diagnosticError(http.StatusBadRequest, title, diags)
	}
	return ws, nil
}

// diagnosticError converts error diagnostics into a *Error. Detail joins every
// error message; each error with a path also becomes a field error.
func diagnosticError(status int, title string, diags diag.List) *Error {
	var (
		fields   []FieldError
		messages []string
	)
	for _, d := range diags {
		if d.Severity != diag.Error {
			continue
		}
		messages = append(messages, d.Message)
		if d.Path != "" {
			fields = append(fields, FieldError{Path: d.Path, Message: d.Message})
		}
	}
	return &Error{Status: status, Title: title, Detail: strings.Join(messages, "; "), Fields: fields}
}
