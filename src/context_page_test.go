package autonomy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Context page is the UI for `autonomy context register|sync` plus the read path: it is a
// string it renders itself, so what is checked here is that GET /context serves it (embedded in
// the shell too), that the shell offers it as a module, and that the page is built on the
// Context Service endpoints — register, sync, list, search, section, delete.
func TestTheContextPageIsServedAndOfferedByTheShell(t *testing.T) {
	server := NewHTTPServer(nil)
	for _, path := range []string{"/context", "/context?embed=1"} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("GET %s = %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		if rec.Body.String() != contextPageHTML {
			t.Fatalf("GET %s should serve the Context page", path)
		}
	}
	for _, want := range []string{`data-module="context"`, `context: "/context"`} {
		if !strings.Contains(uiHomeHTML, want) {
			t.Fatalf("the UI shell should offer the Context module: missing %q", want)
		}
	}
}

func TestTheContextPageIsBuiltOnTheContextServiceAPI(t *testing.T) {
	for _, want := range []string{
		`id="f-project" value="project-749a0238"`, // the project the page works on
		`id="rows"`, // the resource table
		`<th>last sync</th>`,
		`id="register"`, `id="f-kind"`, `id="f-provider"`, `value="filesystem"`, `value="source_code_repository"`, `value="cloud"`,
		`id="f-shape"`, `name="f-shape"`, `value="file"`, `value="directory"`,
		`id="f-name"`, `id="f-type"`,
		`id="f-location"`, `id="f-suffixes"`, `id="f-path"`, `id="f-uri"`, `id="f-sync"`,
		`function knowledgeKind()`, `function shape()`, `shape: kind`, `kind: kb`, `body.suffixes = suffixes`,
		`id="search"`, `id="f-query"`, `id="results"`, `id="section"`, // search and the section view
		`async function api(method, path, body)`, // its own fetch helper, relative paths
		`function say(text, kind)`,
		`"embedded"`, // drops its title inside the shell
		`api("GET", "/api/context/resources?project_id=" + encodeURIComponent(project()))`,
		`api("POST", "/api/context/resources", body)`,
		`api("POST", "/api/context/resources/" + encodeURIComponent(id) + "/sync")`,
		`api("DELETE", "/api/context/resources/" + encodeURIComponent(id))`,
		`"/api/context/search?project_id="`,
		`api("GET", "/api/context/sections/" + encodeURIComponent(hit.dataset.section))`,
		`confirm("Delete resource "`,         // a delete is confirmed first
		`requires the postgres store engine`, // the 503 says what is missing
	} {
		if !strings.Contains(contextPageHTML, want) {
			t.Errorf("the Context page should contain %q", want)
		}
	}
	if strings.Contains(contextPageHTML, "http://") || strings.Contains(contextPageHTML, "https://") {
		t.Error("the Context page should only call relative paths and load no external asset")
	}
}
