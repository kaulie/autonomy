package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	autonomy "github.com/kaulie/autonomy/src"
)

// The tasks page (GET /tasks) is where an instruction is sent and where the tasks that came of it
// are read. Its job, besides being served: letting the account a task runs on be chosen at the
// moment of asking, from the pool's own API — so the pieces that make that work are what the page
// has to name.
func TestTheTasksPageSendsInstructionsOnAChosenAccount(t *testing.T) {
	server := autonomy.NewHTTPServer(nil)

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tasks", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /tasks status=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("GET /tasks content-type=%q want html", ct)
	}
	body := rec.Body.String()
	// Which endpoints it speaks, and the dropdown that decides the account: the pool is read from
	// /api/accounts, the picked id travels as account_id on the instruction, and with nothing
	// picked the pool resolves it (the "pool default" entry).
	for _, want := range []string{
		"/api/tasks", "/api/accounts", "account_id", "pool default", `id="f-account"`, "/api/tasks/",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the tasks page should mention %s", want)
		}
	}
	// It polls on what the shell passes, and opens on its own like every other module.
	if !strings.Contains(body, `"every"`) {
		t.Fatalf("the tasks page should honour ?every=")
	}

	embedded := httptest.NewRecorder()
	server.Handler().ServeHTTP(embedded, httptest.NewRequest(http.MethodGet, "/tasks?embed=1&every=5", nil))
	if embedded.Code != http.StatusOK {
		t.Fatalf("GET /tasks?embed=1 status=%d", embedded.Code)
	}
	if !strings.Contains(embedded.Body.String(), "embedded") {
		t.Fatalf("GET /tasks?embed=1 should handle the embed flag")
	}
}
