package authn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type setupService interface {
	SetupWorkspace(context.Context, string, string, string, string) (sessionToken, csrfToken string, expiresAt time.Time, err error)
}

type setupFieldError interface{ SetupField() string }

type setupRequest struct {
	Token         string `json:"token"`
	WorkspaceName string `json:"workspaceName"`
	Username      string `json:"username"`
	Password      string `json:"password"`
}

// SetupHandler handles POST /setup: it consumes the one-time bootstrap
// token and creates the admin account. Both a wrong token and an
// already-consumed bootstrap are reported as the same 403, with no
// distinguishing detail (design doc §"Bootstrap contract").
func SetupHandler(store setupService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req setupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid request", "request body must be JSON with token, username and password")
			return
		}
		sessionToken, csrfToken, expiresAt, err := store.SetupWorkspace(r.Context(), req.Token, req.WorkspaceName, req.Username, req.Password)
		if err != nil {
			var validation setupFieldError
			if errors.As(err, &validation) {
				writeFieldProblem(w, validation.SetupField())
				return
			}
			if errors.Is(err, ErrSetupUnavailable) || errors.Is(err, ErrInvalidCredentials) {
				writeProblem(w, http.StatusForbidden, "Forbidden", "setup is not available")
				return
			}
			writeProblem(w, http.StatusInternalServerError, "Internal error", "unable to complete setup")
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    sessionToken,
			Path:     "/",
			Expires:  expiresAt,
			HttpOnly: true,
			Secure:   r.TLS != nil,
			SameSite: http.SameSiteStrictMode,
		})
		writeJSON(w, struct {
			CSRFToken string `json:"csrfToken"`
		}{CSRFToken: csrfToken})
	})
}

func writeFieldProblem(w http.ResponseWriter, field string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(struct {
		Type          string `json:"type"`
		Title         string `json:"title"`
		Status        int    `json:"status"`
		InvalidParams []struct {
			Name   string `json:"name"`
			Reason string `json:"reason"`
		} `json:"invalid-params"`
	}{
		Type: "about:blank", Title: "Invalid request", Status: http.StatusBadRequest,
		InvalidParams: []struct {
			Name   string `json:"name"`
			Reason string `json:"reason"`
		}{{Name: field, Reason: "invalid value"}},
	})
}

// SetupStatusHandler handles GET /setup/status: it reports whether the
// admin account has already been created, with no bootstrap-token detail.
func SetupStatusHandler(store *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		initialized, err := store.HasAccount(r.Context())
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Internal error", "unable to check setup status")
			return
		}
		writeJSON(w, struct {
			Initialized bool `json:"initialized"`
		}{Initialized: initialized})
	})
}

// SessionHandler handles GET /auth/session: it returns the current
// session's username and CSRF token so the SPA can recover the CSRF token
// after a page reload without re-authenticating.
func SessionHandler(store *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || !store.ValidateSession(r.Context(), cookie.Value) {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid session is required")
			return
		}
		username, ok := store.SessionUsername(r.Context(), cookie.Value)
		if !ok {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid session is required")
			return
		}
		csrfToken, _ := store.SessionCSRFToken(r.Context(), cookie.Value)
		writeJSON(w, struct {
			Username  string `json:"username"`
			CSRFToken string `json:"csrfToken"`
		}{Username: username, CSRFToken: csrfToken})
	})
}
