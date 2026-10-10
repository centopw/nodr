package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/centopw/nodr/internal/authn"
	"github.com/centopw/nodr/internal/secrets"
)

// Request is the complete first-run setup input.
type Request struct {
	Token         string
	WorkspaceName string
	Username      string
	Password      string
}

// ValidationError identifies a client-provided setup field that is invalid.
type ValidationError struct{ Field string }

func (e *ValidationError) Error() string { return "bootstrap: invalid " + e.Field }

// SetupField identifies the invalid client field for structured HTTP errors.
func (e *ValidationError) SetupField() string { return e.Field }

// Service coordinates durable workspace creation with first-account setup.
type Service struct {
	Config     Config
	WriteState func(string, State) error
	SetupAuth  func(context.Context, *authn.Store, Request) (sessionToken, csrfToken string, expiresAt time.Time, err error)
	Prepare    func(context.Context, string, []byte) (commit func(), cleanup func(), err error)
}

// Setup creates an attempt-local workspace and its first authenticated session.
// It removes only the workspace made by this attempt if it cannot complete.
func (s Service) Setup(ctx context.Context, req Request) (sessionToken, csrfToken string, expiresAt time.Time, err error) {
	if err := validateRequest(req); err != nil {
		return "", "", time.Time{}, err
	}
	dataDir := s.Config.DataRoot()
	root, rollback, err := CreateWorkspace(ctx, dataDir, req.WorkspaceName)
	if err != nil {
		return "", "", time.Time{}, err
	}
	completed := false
	defer func() {
		if !completed {
			rollback()
		}
	}()

	kek, err := ResolveKEK(s.Config, nil)
	if err != nil {
		return "", "", time.Time{}, err
	}
	secretStore, err := secrets.Open(filepath.Join(root, ".nodr", "secrets.db"), kek)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("bootstrap: open attempt secrets store: %w", err)
	}
	defer secretStore.Close()
	authStore, err := authn.Open(filepath.Join(root, ".nodr", "authn.db"))
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("bootstrap: open attempt auth store: %w", err)
	}
	defer authStore.Close()
	if err := authStore.InitializeBootstrap(ctx, req.Token); err != nil {
		return "", "", time.Time{}, fmt.Errorf("bootstrap: initialize auth setup: %w", err)
	}
	setupAuth := s.SetupAuth
	if setupAuth == nil {
		setupAuth = func(ctx context.Context, store *authn.Store, request Request) (string, string, time.Time, error) {
			return store.Setup(ctx, request.Token, request.Username, request.Password)
		}
	}
	sessionToken, csrfToken, expiresAt, err = setupAuth(ctx, authStore, req)
	if err != nil {
		return "", "", time.Time{}, err
	}
	var commit func()
	if s.Prepare != nil {
		var cleanup func()
		commit, cleanup, err = s.Prepare(ctx, root, kek)
		if err != nil {
			return "", "", time.Time{}, err
		}
		defer func() {
			if !completed {
				cleanup()
			}
		}()
	}
	writeState := s.WriteState
	if writeState == nil {
		writeState = WriteState
	}
	if err := writeState(dataDir, State{Version: 1, Workspace: filepath.Join("workspaces", req.WorkspaceName)}); err != nil {
		return "", "", time.Time{}, fmt.Errorf("bootstrap: write completion state: %w", err)
	}
	completed = true
	if commit != nil {
		commit()
	}
	return sessionToken, csrfToken, expiresAt, nil
}

// SetupWorkspace adapts Service to authn.SetupHandler.
func (s Service) SetupWorkspace(ctx context.Context, token, workspaceName, username, password string) (sessionToken, csrfToken string, expiresAt time.Time, err error) {
	return s.Setup(ctx, Request{Token: token, WorkspaceName: workspaceName, Username: username, Password: password})
}

func validateRequest(req Request) error {
	if err := ValidateName(req.WorkspaceName); err != nil {
		return &ValidationError{Field: "workspaceName"}
	}
	for field, value := range map[string]string{"token": req.Token, "username": req.Username, "password": req.Password} {
		if strings.TrimSpace(value) == "" {
			return &ValidationError{Field: field}
		}
	}
	return nil
}

// IsValidationError reports whether err is caused by invalid setup input.
func IsValidationError(err error) (field string, ok bool) {
	var validation *ValidationError
	if errors.As(err, &validation) {
		return validation.Field, true
	}
	return "", false
}
