package authn

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

const sessionCookieName = "nodr_session"

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: "about:blank", Title: title, Status: status, Detail: detail})
}

// Middleware wraps h, requiring a valid session cookie for every request
// whose path is not exactly one of allowPaths.
func Middleware(store *Store, allowPaths ...string) func(http.Handler) http.Handler {
	allow := make(map[string]bool, len(allowPaths))
	for _, p := range allowPaths {
		allow[p] = true
	}
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if allow[r.URL.Path] {
				h.ServeHTTP(w, r)
				return
			}
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil || !store.ValidateSession(r.Context(), cookie.Value) {
				writeProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid session is required")
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				csrf, ok := store.SessionCSRFToken(r.Context(), cookie.Value)
				header := r.Header.Get("X-CSRF-Token")
				if !ok || header == "" || subtle.ConstantTimeCompare([]byte(header), []byte(csrf)) != 1 {
					writeProblem(w, http.StatusForbidden, "Forbidden", "a valid X-CSRF-Token header is required for this request")
					return
				}
			}
			h.ServeHTTP(w, r)
		})
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginHandler handles a login POST: decodes {"username","password"},
// authenticates, and on success sets the session cookie.
func LoginHandler(store *Store, _ string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid request", "request body must be JSON with username and password")
			return
		}
		token, expiresAt, err := store.Authenticate(r.Context(), req.Username, req.Password)
		if err != nil {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "invalid username or password")
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    token,
			Path:     "/",
			Expires:  expiresAt,
			HttpOnly: true,
			Secure:   r.TLS != nil,
			SameSite: http.SameSiteStrictMode,
		})
		csrfToken, _ := store.SessionCSRFToken(r.Context(), token)
		writeJSON(w, http.StatusOK, struct {
			CSRFToken string `json:"csrfToken"`
		}{CSRFToken: csrfToken})
	})
}

// LogoutHandler handles a logout POST: deletes the session and clears the cookie.
func LogoutHandler(store *Store, _ string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			_ = store.Logout(r.Context(), cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		w.WriteHeader(http.StatusOK)
	})
}
