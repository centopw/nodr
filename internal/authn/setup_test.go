package authn_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/authn"
)

func TestSetupStatusHandler_FalseThenTrue(t *testing.T) {
	s := openStore(t)
	handler := authn.SetupStatusHandler(s)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup/status", nil))
	var status struct {
		Initialized bool `json:"initialized"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Initialized {
		t.Error("initialized = true before setup")
	}

	if err := s.CreateAccount(t.Context(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup/status", nil))
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !status.Initialized {
		t.Error("initialized = false after CreateAccount")
	}
}

func TestSetupHandler_Success(t *testing.T) {
	s := openStore(t)
	if err := s.InitializeBootstrap(t.Context(), "bootstrap-token-1"); err != nil {
		t.Fatalf("InitializeBootstrap: %v", err)
	}
	handler := authn.SetupHandler(s)
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(`{"token":"bootstrap-token-1","username":"admin","password":"password12345"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "nodr_session" {
		t.Fatalf("cookies = %v, want one nodr_session cookie", cookies)
	}
	var body struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.CSRFToken == "" {
		t.Error("csrfToken is empty")
	}
}

func TestSetupHandler_WrongToken_Returns403WithNoDistinguishingDetail(t *testing.T) {
	s := openStore(t)
	if err := s.InitializeBootstrap(t.Context(), "bootstrap-token-1"); err != nil {
		t.Fatalf("InitializeBootstrap: %v", err)
	}
	handler := authn.SetupHandler(s)
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(`{"token":"wrong","username":"admin","password":"password12345"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}

func TestSetupHandler_AlreadyInitialized_Returns403(t *testing.T) {
	s := openStore(t)
	if err := s.CreateAccount(t.Context(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	handler := authn.SetupHandler(s)
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(`{"token":"anything","username":"admin2","password":"password12345"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}

func TestSessionHandler_ReturnsUsernameAndCSRFToken(t *testing.T) {
	s := openStore(t)
	if err := s.CreateAccount(t.Context(), "admin", "password12345"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	token, _, err := s.Authenticate(t.Context(), "admin", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	handler := authn.SessionHandler(s)
	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: "nodr_session", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Username  string `json:"username"`
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Username != "admin" {
		t.Errorf("username = %q, want %q", body.Username, "admin")
	}
	if body.CSRFToken == "" {
		t.Error("csrfToken is empty")
	}
}

func TestSessionHandler_NoCookie_Returns401(t *testing.T) {
	s := openStore(t)
	handler := authn.SessionHandler(s)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/session", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
