package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	autonomy "github.com/kaulie/autonomy/src"
)

// The UI's shell (GET /) is the way in: it hosts the modules this runtime serves, and each module
// stays reachable on its own path. It needs no store, because it renders nothing itself — its
// header's status line comes from /health, which answers without one.
func TestTheUIHomeHostsTheModules(t *testing.T) {
	server := autonomy.NewHTTPServer(nil)

	home := httptest.NewRecorder()
	server.Handler().ServeHTTP(home, httptest.NewRequest(http.MethodGet, "/", nil))
	if home.Code != http.StatusOK {
		t.Fatalf("GET / status=%d", home.Code)
	}
	if ct := home.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("GET / content-type=%q want html", ct)
	}
	body := home.Body.String()
	// The skill is hosting the modules, so the shell has to name them and embed them, and it reads
	// the runtime's own status line.
	for _, want := range []string{"/dashboard", "/accounts", "?embed=1", "/health", "data-every=\"5\""} {
		if !strings.Contains(body, want) {
			t.Fatalf("the shell should mention %s", want)
		}
	}

	// A module is still its own page, and knows when it is embedded.
	for _, path := range []string{"/dashboard?embed=1", "/accounts?embed=1"} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "embedded") {
			t.Fatalf("GET %s should handle the embed flag", path)
		}
	}
}

// The agent views a dashboard row opens are served too: the event stream and the message log.
func TestTheAgentViewPagesAreServed(t *testing.T) {
	server := autonomy.NewHTTPServer(nil)
	for _, path := range []string{
		"/agents/10001/events?task=task-1",
		"/agents/10001/events?task=task-1&embed=1&every=15",
		"/agents/10001/messages?embed=1",
	} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Fatalf("GET %s content-type=%q", path, ct)
		}
	}
}
