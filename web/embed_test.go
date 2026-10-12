package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesDashboardOnlyAtKnownPaths(t *testing.T) {
	h := Handler()
	cases := map[string]int{"/": 200, "/index.html": 200, "/missing.js": 404, "/api/v1/x": 404}
	for path, want := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s = %d, want %d", path, rec.Code, want)
		}
		if want == 200 && !strings.Contains(rec.Body.String(), "EventSource('/api/v1/stream')") {
			t.Errorf("%s: dashboard should subscribe to the SSE stream", path)
		}
	}
}
