package context_builder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// counter counts the reads a stub registry served, so caching can be asserted.
type counter struct {
	mu sync.Mutex
	n  int
}

func (c *counter) hit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *counter) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// projectsStub serves the registry's project list.
func projectsStub(t *testing.T, rows []map[string]any) (*httptest.Server, *counter) {
	t.Helper()
	reads := &counter{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.hit()
		if r.URL.Path != "/api/projects" {
			t.Errorf("asked %s, want the project registry's list", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rows)
	}))
	t.Cleanup(server.Close)
	return server, reads
}

// departmentsStub serves one department of the organization catalogue.
func departmentsStub(t *testing.T, departmentID, name, kind string) (*httptest.Server, *counter) {
	t.Helper()
	reads := &counter{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.hit()
		if want := "/api/v1/departments/" + departmentID; r.URL.Path != want {
			t.Errorf("asked %s, want %s", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": departmentID, "name": name, "type": kind,
			"members": []any{map[string]string{"id": "user-001"}}})
	}))
	t.Cleanup(server.Close)
	return server, reads
}

func TestProjectRegistryAnswersTheProjectAndItsDepartment(t *testing.T) {
	server, reads := projectsStub(t, []map[string]any{
		{"projectId": "project-749a0238", "name": "autonomy", "gitRepoUrl": "https://github.com/kaulie/autonomy",
			"department": map[string]string{"departmentId": "D0005", "departmentName": "AI研发部"}},
	})
	resolver := NewProjectRegistry()
	resolver.URL = server.URL

	fields, ok, err := resolver.Resolve(context.Background(), RefTypeProject, "project-749a0238", map[string]any{})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want the project", ok, err)
	}
	if fields["name"] != "autonomy" || fields["git_repo_url"] != "https://github.com/kaulie/autonomy" {
		t.Fatalf("fields=%v, want the registry's name and repository", fields)
	}
	organization, found := fields["organization"].(map[string]any)
	if !found || organization["id"] != "D0005" || organization["name"] != "AI研发部" {
		t.Fatalf("organization=%v, want the department the project belongs to", fields["organization"])
	}

	// The list is read once and reused: a decision cycle resolves its ref every cycle.
	if _, _, err := resolver.Resolve(context.Background(), RefTypeProject, "project-749a0238", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if reads.calls() != 1 {
		t.Fatalf("registry reads=%d, want the answer cached", reads.calls())
	}
}

func TestProjectRegistryKnowsOnlyProjects(t *testing.T) {
	server, reads := projectsStub(t, nil)
	resolver := NewProjectRegistry()
	resolver.URL = server.URL

	if _, ok, err := resolver.Resolve(context.Background(), RefTypeTeam, "team-1", map[string]any{}); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want a team to be none of its business", ok, err)
	}
	if reads.calls() != 0 {
		t.Fatalf("registry reads=%d, want none for a ref it cannot answer", reads.calls())
	}
}

func TestProjectRegistryNamesWhatItDoesNotKnow(t *testing.T) {
	server, _ := projectsStub(t, []map[string]any{
		{"projectId": "project-1", "name": "autonomy"},
	})
	resolver := NewProjectRegistry()
	resolver.URL = server.URL

	// An id the registry does not have: nothing to say, and not a failure.
	if fields, ok, err := resolver.Resolve(context.Background(), RefTypeProject, "project-9", map[string]any{}); ok || err != nil || fields != nil {
		t.Fatalf("fields=%v ok=%v err=%v, want silence", fields, ok, err)
	}
	// A project with no department has no organization — rather than an empty one.
	fields, ok, err := resolver.Resolve(context.Background(), RefTypeProject, "project-1", map[string]any{})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if _, found := fields["organization"]; found {
		t.Fatalf("fields=%v, want no organization for a project without one", fields)
	}
}

func TestProjectRegistryReportsAFailure(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer dead.Close()
	resolver := NewProjectRegistry()
	resolver.URL = dead.URL
	resolver.FailureTTL = 0

	_, ok, err := resolver.Resolve(context.Background(), RefTypeProject, "project-1", map[string]any{})
	if err == nil || ok {
		t.Fatalf("ok=%v err=%v, want the failure reported", ok, err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("err=%v, want the registry's status", err)
	}
}

func TestOrganizationFillsInTheDepartment(t *testing.T) {
	server, reads := departmentsStub(t, "D0005", "AI研发部", "研发")
	resolver := NewOrganization()
	resolver.URL = server.URL

	// What the project registry found first: the department by id and name.
	built := map[string]any{"organization": map[string]any{"id": "D0005", "name": "AI研发部"}}
	fields, ok, err := resolver.Resolve(context.Background(), RefTypeProject, "project-749a0238", built)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want the catalogue's answer", ok, err)
	}
	organization, found := fields["organization"].(map[string]any)
	if !found || organization["id"] != "D0005" || organization["name"] != "AI研发部" || organization["type"] != "研发" {
		t.Fatalf("organization=%v, want the department the catalogue has", fields["organization"])
	}
	if reads.calls() != 1 {
		t.Fatalf("catalogue reads=%d, want one read", reads.calls())
	}

	// No department to look up: nothing to do (and no read).
	if _, ok, err := resolver.Resolve(context.Background(), RefTypeProject, "project-9", map[string]any{}); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want nothing to look up", ok, err)
	}
	if reads.calls() != 1 {
		t.Fatalf("catalogue reads=%d, want the second resolve not to read", reads.calls())
	}
}

func TestRegistriesDegradeToOneAnother(t *testing.T) {
	// The registry knows the project and its department by id; the catalogue is down.
	projects, _ := projectsStub(t, []map[string]any{
		{"projectId": "project-749a0238", "name": "autonomy",
			"department": map[string]string{"departmentId": "D0005", "departmentName": "AI研发部"}},
	})
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer dead.Close()

	registry := NewProjectRegistry()
	registry.URL = projects.URL
	catalogue := NewOrganization()
	catalogue.URL = dead.URL

	result := New(registry, catalogue).Build(context.Background(), Ref{RefTypeProject: "project-749a0238"})
	if len(result.Errors) != 1 {
		t.Fatalf("errors=%v, want the catalogue's failure recorded", result.Errors)
	}
	organization, found := result.Sections[RefTypeProject]["organization"].(map[string]any)
	if !found || organization["id"] != "D0005" || organization["name"] != "AI研发部" {
		t.Fatalf("section=%v, want the project registry's answer to survive", result.Sections[RefTypeProject])
	}
}

func TestRegistryDefaults(t *testing.T) {
	t.Setenv(EnvProjectsAPIURL, "")
	t.Setenv(EnvOrganizationAPIURL, "")
	t.Setenv(EnvServiceRegistryAPIURL, "")
	if got := NewProjectRegistry().BaseURL(); got != DefaultProjectsAPIURL {
		t.Fatalf("project registry url=%q, want %q", got, DefaultProjectsAPIURL)
	}
	if got := NewOrganization().BaseURL(); got != DefaultOrganizationAPIURL {
		t.Fatalf("organization url=%q, want %q", got, DefaultOrganizationAPIURL)
	}
	if got := NewServiceRegistry().BaseURL(); got != DefaultServiceRegistryAPIURL {
		t.Fatalf("service registry url=%q, want %q", got, DefaultServiceRegistryAPIURL)
	}

	t.Setenv(EnvProjectsAPIURL, "http://registry.test/")
	if got := NewProjectRegistry().BaseURL(); got != "http://registry.test" {
		t.Fatalf("project registry url=%q, want the configured one without its trailing slash", got)
	}
	t.Setenv(EnvServiceRegistryAPIURL, "http://services.test/")
	if got := NewServiceRegistry().BaseURL(); got != "http://services.test" {
		t.Fatalf("service registry url=%q, want the configured one without its trailing slash", got)
	}
	explicit := NewProjectRegistry()
	explicit.URL = "http://elsewhere.test"
	if got := explicit.BaseURL(); got != "http://elsewhere.test" {
		t.Fatalf("project registry url=%q, want the resolver's own URL to win", got)
	}
}
