// Package proxmoxtest provides a fake Proxmox VE HTTP server for tests in
// internal/proxmox and in later discovery/adoption slices.
package proxmoxtest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// NewServer returns an httptest.Server configured with the provided handlers.
// The handlers map keys requests by "METHOD /path", for example:
//
//	"GET /api2/json/version"
//	"POST /api2/json/access/ticket"
//
// Any request that does not match an exact key fails the test via t.Errorf
// and returns HTTP 501.
func NewServer(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := fmt.Sprintf("%s %s", r.Method, r.URL.Path)
		handler, ok := handlers[key]
		if !ok {
			t.Errorf("proxmoxtest: unexpected request %s (registered: %s)", key, strings.Join(keys(handlers), ", "))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = fmt.Fprintf(w, `{"message":"unexpected request: %s"}`, key)
			return
		}
		handler(w, r)
	}))
}

// JSONResponse writes a standard Proxmox VE {"data": ...} envelope response.
func JSONResponse(w http.ResponseWriter, statusCode int, dataJSON string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if dataJSON == "" {
		_, _ = w.Write([]byte(`{"data":null}`))
		return
	}
	_, _ = fmt.Fprintf(w, `{"data":%s}`, dataJSON)
}

// ErrorResponse writes a Proxmox VE error response.
func ErrorResponse(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = fmt.Fprintf(w, `{"message":"%s"}`, message)
}

func keys(m map[string]http.HandlerFunc) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
