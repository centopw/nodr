package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const testIndexHTML = "<!doctype html><html><body>nodr</body></html>"

func testHandler() http.Handler {
	fsys := fstest.MapFS{
		"index.html":        {Data: []byte(testIndexHTML)},
		"assets/app.js":     {Data: []byte("console.log('app')")},
		"assets/app.js.map": {Data: []byte("{}")},
	}
	return handlerFS(fsys)
}

func request(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	return response
}

func TestIndexHTMLServedAtRoot(t *testing.T) {
	response := request(t, testHandler(), "/")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); body != testIndexHTML {
		t.Errorf("body = %q, want %q", body, testIndexHTML)
	}
}

func TestSPAFallbackServesIndexHTMLForClientRoutes(t *testing.T) {
	for _, target := range []string{"/some/client/route", "/workspaces/homelab/vms/web-01", "/unknown"} {
		t.Run(target, func(t *testing.T) {
			response := request(t, testHandler(), target)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if body := response.Body.String(); body != testIndexHTML {
				t.Errorf("body = %q, want %q", body, testIndexHTML)
			}
		})
	}
}

func TestKnownAssetIsServedInsteadOfIndexHTML(t *testing.T) {
	response := request(t, testHandler(), "/assets/app.js")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); body == testIndexHTML || !strings.Contains(body, "console.log") {
		t.Errorf("body = %q, want the asset content, not the SPA shell", body)
	}
}

// TestAPIPathsAreNeverInterceptedBySPAFallback proves the fallback branch
// checks the path prefix before ever reaching the index.html response: an
// /api/* request must not receive the embedded SPA shell, even though it
// does not match any file in the embedded filesystem (which would
// otherwise trigger the same "serve index.html" fallback as an unknown
// client route).
func TestAPIPathsAreNeverInterceptedBySPAFallback(t *testing.T) {
	for _, target := range []string{"/api", "/api/", "/api/v1/workspaces", "/api/v1/anything"} {
		t.Run(target, func(t *testing.T) {
			response := request(t, testHandler(), target)
			if response.Code == http.StatusOK && response.Body.String() == testIndexHTML {
				t.Fatalf("SPA fallback served index.html for API path %s", target)
			}
			if response.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 for an API path mounted standalone", response.Code)
			}
		})
	}
}
