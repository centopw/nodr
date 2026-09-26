package webui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/centopw/nodr/internal/webui"
)

func TestHandler_ServesLoginPageUnauthenticated(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	webui.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<form") {
		t.Errorf("body does not contain a login form: %s", rec.Body.String())
	}
}

func TestHandler_LoginPathNeverFallsThroughToSPA(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	webui.Handler().ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `<div id="root">`) {
		t.Error("served the SPA shell instead of the login form")
	}
}
