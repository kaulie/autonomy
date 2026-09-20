package context_builder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// servicesStub serves the service registry's org view and counts its reads.
func servicesStub(t *testing.T, organizationID string, services []map[string]any) (*httptest.Server, *counter) {
	t.Helper()
	reads := &counter{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.hit()
		if want := "/v1/orgs/" + organizationID + "/services"; r.URL.Path != want {
			t.Errorf("asked %s, want %s", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"org":      map[string]any{"id": organizationID, "name": "AI研发部", "resolved": true},
			"services": services,
		})
	}))
	t.Cleanup(server.Close)
	return server, reads
}

// builtOrganization is what the resolvers asked before this one found for the ref: the
// project registry's department by id and name, refined by the catalogue's type.
func builtOrganization() map[string]any {
	return map[string]any{"organization": map[string]any{"id": "D0005", "name": "AI研发部", "type": "研发"}}
}

func TestServiceRegistryAnswersTheOrganizationsServices(t *testing.T) {
	server, reads := servicesStub(t, "D0005", []map[string]any{
		{"namespace": "default", "name": "autonomy", "type": "service",
			"description": "自主 agent runtime", "gitRepoUrl": "https://github.com/kaulie/autonomy", "version": "87e2bd3e"},
		{"namespace": "default", "name": "agent-control-plane"},
		// A row the registry has no name for is not a service anyone can refer to.
		{"namespace": "default", "description": "nameless"},
	})
	resolver := NewServiceRegistry()
	resolver.URL = server.URL

	fields, ok, err := resolver.Resolve(context.Background(), RefTypeProject, "project-749a0238", builtOrganization())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want the organization's services", ok, err)
	}
	organization, found := fields["organization"].(map[string]any)
	if !found {
		t.Fatalf("fields=%v, want the organization replaced with the services in it", fields)
	}
	// The field is replaced wholesale, so what the earlier resolvers found survives.
	if organization["id"] != "D0005" || organization["name"] != "AI研发部" || organization["type"] != "研发" {
		t.Fatalf("organization=%v, want the department the earlier resolvers found", organization)
	}
	services, found := organization["services"].([]map[string]any)
	if !found || len(services) != 2 {
		t.Fatalf("services=%v, want the two named ones", organization["services"])
	}
	if services[0]["name"] != "autonomy" || services[0]["description"] != "自主 agent runtime" ||
		services[0]["git_repo_url"] != "https://github.com/kaulie/autonomy" || services[0]["version"] != "87e2bd3e" {
		t.Fatalf("service=%v, want what the service is and where its code lives", services[0])
	}
	if services[1]["name"] != "agent-control-plane" {
		t.Fatalf("service=%v, want the row with only a name", services[1])
	}
	if _, found := services[1]["git_repo_url"]; found {
		t.Fatalf("service=%v, want no empty fields served as answers", services[1])
	}

	// The list is read once and reused: a decision cycle resolves its ref every cycle.
	if _, _, err := resolver.Resolve(context.Background(), RefTypeProject, "project-749a0238", builtOrganization()); err != nil {
		t.Fatal(err)
	}
	if reads.calls() != 1 {
		t.Fatalf("registry reads=%d, want the answer cached", reads.calls())
	}
}

func TestServiceRegistryKnowsOnlyProjects(t *testing.T) {
	server, reads := servicesStub(t, "D0005", nil)
	resolver := NewServiceRegistry()
	resolver.URL = server.URL

	// A team ref: the organization is not the question, so nothing is read.
	if _, ok, err := resolver.Resolve(context.Background(), RefTypeTeam, "team-1", builtOrganization()); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want a team to be none of its business", ok, err)
	}
	if reads.calls() != 0 {
		t.Fatalf("registry reads=%d, want none for a ref it cannot answer", reads.calls())
	}
}

func TestServiceRegistryNeedsAnOrganizationToLookServicesUpBy(t *testing.T) {
	server, reads := servicesStub(t, "D0005", []map[string]any{{"name": "autonomy"}})
	resolver := NewServiceRegistry()
	resolver.URL = server.URL

	// A project nobody put in a department has no organization, so no list to ask for.
	if _, ok, err := resolver.Resolve(context.Background(), RefTypeProject, "project-1", map[string]any{}); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want nothing to look up", ok, err)
	}
	if reads.calls() != 0 {
		t.Fatalf("registry reads=%d, want the service registry not to be asked", reads.calls())
	}
}

func TestServiceRegistrySaysNothingWhenTheOrganizationHasNone(t *testing.T) {
	server, _ := servicesStub(t, "D0005", []map[string]any{})
	resolver := NewServiceRegistry()
	resolver.URL = server.URL

	// An empty list is not an answer: the section simply has no services.
	if fields, ok, err := resolver.Resolve(context.Background(), RefTypeProject, "project-1", builtOrganization()); ok || err != nil || fields != nil {
		t.Fatalf("fields=%v ok=%v err=%v, want silence", fields, ok, err)
	}
}

func TestServiceRegistryReportsAFailure(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer dead.Close()
	resolver := NewServiceRegistry()
	resolver.URL = dead.URL
	resolver.FailureTTL = 0

	_, ok, err := resolver.Resolve(context.Background(), RefTypeProject, "project-1", builtOrganization())
	if err == nil || ok {
		t.Fatalf("ok=%v err=%v, want the failure reported", ok, err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("err=%v, want the registry's status", err)
	}
}

// TestThePipelineResolvesProjectToItsOrganizationToItsServices is the chain the
// registration order buys: the service registry asks for the department id the two
// resolvers before it found, and the prompt's section carries the services under it.
func TestThePipelineResolvesProjectToItsOrganizationToItsServices(t *testing.T) {
	projects, _ := projectsStub(t, []map[string]any{
		{"projectId": "project-749a0238", "name": "autonomy", "gitRepoUrl": "https://github.com/kaulie/autonomy",
			"department": map[string]string{"departmentId": "D0005", "departmentName": "AI研发部"}},
	})
	departments, _ := departmentsStub(t, "D0005", "AI研发部", "研发")
	services, _ := servicesStub(t, "D0005", []map[string]any{
		{"name": "agent-control-plane", "gitRepoUrl": "https://github.com/kaulie/agent-control-plane.git", "version": "f8d53f2d"},
	})

	project := NewProjectRegistry()
	project.URL = projects.URL
	organization := NewOrganization()
	organization.URL = departments.URL
	serviceRegistry := NewServiceRegistry()
	serviceRegistry.URL = services.URL

	result := New(project, organization, serviceRegistry).Build(context.Background(), Ref{RefTypeProject: "project-749a0238"})
	if len(result.Errors) != 0 {
		t.Fatalf("errors=%v, want none", result.Errors)
	}
	organizationSection, found := result.Sections[RefTypeProject]["organization"].(map[string]any)
	if !found || organizationSection["id"] != "D0005" {
		t.Fatalf("organization=%v, want the department the project belongs to", result.Sections[RefTypeProject]["organization"])
	}
	rows, found := organizationSection["services"].([]map[string]any)
	if !found || len(rows) != 1 || rows[0]["name"] != "agent-control-plane" {
		t.Fatalf("services=%v, want the organization's services in the prompt's section", organizationSection["services"])
	}
	if rows[0]["git_repo_url"] != "https://github.com/kaulie/agent-control-plane.git" {
		t.Fatalf("service=%v, want the repository the registry has", rows[0])
	}
}
