package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAHandler(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":        {Data: []byte("<html>app</html>")},
		"assets/app-123.js": {Data: []byte("console.log(1)")},
		"favicon.svg":       {Data: []byte("<svg/>")},
	}
	h := spaHandler(fsys)
	for path, want := range map[string]struct {
		body, cache string
	}{
		"/":                  {"app", "no-cache"},
		"/scopes/12":         {"app", "no-cache"},
		"/assets/app-123.js": {"console", "immutable"},
		"/favicon.svg":       {"svg", "no-cache"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want.body) || !strings.Contains(rec.Header().Get("Cache-Control"), want.cache) {
			t.Errorf("%s: %d %q %q", path, rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", rec.Code)
	}
}
