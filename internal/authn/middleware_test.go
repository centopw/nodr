package authn_test

import (
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
