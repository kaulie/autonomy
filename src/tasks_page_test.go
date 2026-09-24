package autonomy

import (
	"strings"
	"testing"
)

// The account field is the point of the tasks page: a task's harness, vendor, model, workspace
// root and credential are a pool choice (src/accounts.go), and this page is where that choice is
// made per task. The page is a string it renders itself, so what is checked here is that the
// three pieces are in it — the dropdown read from the pool's API, the picked id travelling as
// account_id on the instruction, and "pool default" meaning the pool resolves it — plus the two
// endpoints it is built on.
func TestTheTasksPageOffersThePoolAsTheAccountChoice(t *testing.T) {
	server := NewHTTPServer(nil)
	if !strings.Contains(tasksPageHTML, `id="f-account"`) {
		t.Fatalf("the tasks page should have the account dropdown")
	}
	for _, want := range []string{
		`api("GET", "/api/accounts")`,                 // the dropdown's source: the pool, masked
		`body.account_id = select.value`,              // the choice travels on the instruction
		`<option value="">pool default</option>`,      // nothing chosen = the pool decides
		`api("POST", "/api/tasks", body)`,             // the instruction itself
		`/api/tasks/" + encodeURIComponent(openTask)`, // and where a task stands
	} {
		if !strings.Contains(tasksPageHTML, want) {
			t.Fatalf("the tasks page should contain %q", want)
		}
	}
	// A pool that cannot be read is not a reason to refuse to render: the runtime answers the
	// same either way, and the page says so instead of pretending the dropdown is the decision.
	if !strings.Contains(tasksPageHTML, "the pool is empty") {
		t.Fatalf("the tasks page should say what an empty pool means")
	}
	if server.routes() == nil {
		t.Fatalf("the server should have a route table")
	}
}
