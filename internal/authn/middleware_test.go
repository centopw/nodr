package authn_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/authn"
)

func TestMiddleware_RejectsWithoutSession(t *testing.T) {
	s := openStore(t)
	protected := authn.Middleware(s, "/login")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestMiddleware_AllowsLoginPath(t *testing.T) {
	s := openStore(t)
	called := false
	protected := authn.Middleware(s, "/login")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if !called {
		t.Error("login path was blocked")
	}
}

func TestLoginHandler_SetsSessionCookieAndMiddlewareAccepts(t *testing.T) {
	s := openStore(t)
	if err := s.CreateAccount(t.Context(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	login := authn.LoginHandler(s, "/login")
	loginReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"password12345"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	login.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", loginRec.Code, loginRec.Body.String())
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "nodr_session" {
		t.Fatalf("cookies = %v, want one nodr_session cookie", cookies)
	}
	var body struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.NewDecoder(loginRec.Body).Decode(&body); err != nil {
		t.Fatalf("decode login response body: %v", err)
	}
	if body.CSRFToken == "" {
		t.Error("csrfToken is empty in the login response body")
	}

	protected := authn.Middleware(s, "/login")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	req.AddCookie(cookies[0])
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestLoginHandler_WrongPasswordRejected(t *testing.T) {
	s := openStore(t)
	if err := s.CreateAccount(t.Context(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	login := authn.LoginHandler(s, "/login")
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	login.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestLogoutHandler_ClearsSession(t *testing.T) {
	s := openStore(t)
	if err := s.CreateAccount(t.Context(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token, _, err := s.Authenticate(t.Context(), "admin", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	logout := authn.LogoutHandler(s, "/logout")
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: "nodr_session", Value: token})
	rec := httptest.NewRecorder()
	logout.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if s.ValidateSession(t.Context(), token) {
		t.Error("session still valid after logout")
	}
}


func TestMiddleware_RejectsMutatingRequestWithoutCSRFHeader(t *testing.T) {
	s := openStore(t)
	if err := s.CreateAccount(t.Context(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token, _, err := s.Authenticate(t.Context(), "admin", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	protected := authn.Middleware(s, "/login")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/workspaces", nil)
	req.AddCookie(&http.Cookie{Name: "nodr_session", Value: token})
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}

func TestMiddleware_RejectsMutatingRequestWithWrongCSRFHeader(t *testing.T) {
	s := openStore(t)
	if err := s.CreateAccount(t.Context(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token, _, err := s.Authenticate(t.Context(), "admin", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	protected := authn.Middleware(s, "/login")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/workspaces", nil)
	req.AddCookie(&http.Cookie{Name: "nodr_session", Value: token})
	req.Header.Set("X-CSRF-Token", "wrong-value")
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}

func TestMiddleware_AllowsMutatingRequestWithCorrectCSRFHeader(t *testing.T) {
	s := openStore(t)
	if err := s.CreateAccount(t.Context(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token, _, err := s.Authenticate(t.Context(), "admin", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	csrf, ok := s.SessionCSRFToken(t.Context(), token)
	if !ok {
		t.Fatal("SessionCSRFToken: ok = false")
	}
	called := false
	protected := authn.Middleware(s, "/login")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/workspaces", nil)
	req.AddCookie(&http.Cookie{Name: "nodr_session", Value: token})
	req.Header.Set("X-CSRF-Token", csrf)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("status = %d, called = %v, body = %s", rec.Code, called, rec.Body.String())
	}
}

func TestMiddleware_AllowsGETWithoutCSRFHeader(t *testing.T) {
	s := openStore(t)
	if err := s.CreateAccount(t.Context(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token, _, err := s.Authenticate(t.Context(), "admin", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	called := false
	protected := authn.Middleware(s, "/login")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	req.AddCookie(&http.Cookie{Name: "nodr_session", Value: token})
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("status = %d, called = %v, body = %s", rec.Code, called, rec.Body.String())
	}
}