package autonomy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/context_builder"
)

// A task detail answers "which project is this task in, and which organization is
// that project in" from the same resolution a decision cycle gets: this runtime's own
// world (a registered context container) and the platform's registries (the project
// registry, and the organization catalogue it points at).

// detailOf is the progress a caller would get for this task, through a store and a
// runtime wired the way bootstrap wires one.
func detailOf(t *testing.T, task *Task, containers *ContextContainerManager) *TaskProgress {
	t.Helper()
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}
	runtime := &Autonomy{Store: store, ContextContainerManager: containers}
	// Fresh resolvers per test: the registries' caches are per resolver instance, so a
	// stub is never answered from another test's read.
	runtime.ContextBuilder = newContextBuilder(runtime)
	progress, err := runtime.TaskProgress(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if progress == nil {
		t.Fatalf("no progress for %s", task.ID)
	}
	return progress
}

// registryStub serves a project registry with one known project and the organization
// catalogue it belongs to, so a detail can be asserted without a platform.
func registryStub(t *testing.T, projectID, name, repoURL, departmentID, departmentName string) {
	t.Helper()
	projects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects" {
			t.Errorf("asked %s, want the project registry's list", r.URL.Path)
		}
		row := map[string]any{"projectId": projectID, "name": name, "gitRepoUrl": repoURL,
			"department": map[string]string{"departmentId": departmentID, "departmentName": departmentName}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{row})
	}))
	t.Cleanup(projects.Close)
	t.Setenv(context_builder.EnvProjectsAPIURL, projects.URL)

	organization := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/api/v1/departments/" + departmentID; r.URL.Path != want {
			t.Errorf("asked %s, want %s", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": departmentID, "name": departmentName, "type": "研发"})
	}))
	t.Cleanup(organization.Close)
	t.Setenv(context_builder.EnvOrganizationAPIURL, organization.URL)
}

// deadRegistries points both registries at a port nothing listens on.
func deadRegistries(t *testing.T) {
	t.Helper()
	t.Setenv(context_builder.EnvProjectsAPIURL, "http://127.0.0.1:1")
	t.Setenv(context_builder.EnvOrganizationAPIURL, "http://127.0.0.1:1")
}

func TestTaskDetailCarriesTheProjectAndItsOrganization(t *testing.T) {
	registryStub(t, "project-1", "autonomy", "https://github.com/kaulie/autonomy", "D0005", "AI研发部")

	// The runtime has the container registered too: what the registries do not say (its
	// description and domain) comes from here, and the registry still wins on what a
	// project *is* (its name).
	containers := NewContextContainerManager()
	containers.Upsert(ContextContainer{
		ID: "project-1", Name: "the container's own name", Description: "Project 1 description",
		DomainType: TaskDomainSoftwareDevelopment, ContextContainerType: ContextContainerTypeProject,
	})

	progress := detailOf(t, &Task{
		ID: "task-1", Description: "开放入口", Domain: TaskDomainSoftwareDevelopment, Status: TaskStatusRunning,
		GoalType:   GoalType_FEATURE,
		ContextRef: map[ContextContainerType]string{ContextContainerTypeProject: "project-1"},
	}, containers)

	if progress.GoalType != string(GoalType_FEATURE) {
		t.Fatalf("goal_type=%q, want the goal the task was accepted as", progress.GoalType)
	}
	if progress.ContextRef["project"] != "project-1" {
		t.Fatalf("context_ref=%v, want the ref the task carries", progress.ContextRef)
	}
	project := progress.Project
	if project == nil {
		t.Fatal("no project in the detail")
	}
	if project.ID != "project-1" || project.Name != "autonomy" || project.GitRepoURL != "https://github.com/kaulie/autonomy" {
		t.Fatalf("project=%+v, want the registry's own name and repository", project)
	}
	if project.Description != "Project 1 description" || project.Domain != string(TaskDomainSoftwareDevelopment) {
		t.Fatalf("project=%+v, want the container's description and domain", project)
	}
	if project.Organization == nil || project.Organization.ID != "D0005" || project.Organization.Name != "AI研发部" {
		t.Fatalf("organization=%+v, want the department the project belongs to", project.Organization)
	}

	// The contract is the JSON: the fields a caller reads.
	raw, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"goal_type":"dev_feature"`, `"context_ref":{"project":"project-1"}`,
		`"organization":{"id":"D0005","name":"AI研发部"}`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("json=%s, want %s", raw, want)
		}
	}
}

func TestTaskDetailNamesTheProjectItCannotResolve(t *testing.T) {
	registryStub(t, "project-1", "autonomy", "https://github.com/kaulie/autonomy", "D0005", "AI研发部")

	// An id nobody knows: the detail still says which project the task named, and
	// invents nothing (no organization, no repository).
	progress := detailOf(t, &Task{
		ID: "task-2", Description: "do it", Status: TaskStatusPending,
		ContextRef: map[ContextContainerType]string{ContextContainerTypeProject: "project-9"},
	}, NewContextContainerManager())
	project := progress.Project
	if project == nil || project.ID != "project-9" {
		t.Fatalf("project=%+v, want the id the task named", project)
	}
	if project.Name != "" || project.GitRepoURL != "" || project.Organization != nil {
		t.Fatalf("project=%+v, want nothing invented about an unknown project", project)
	}

	// No project ref at all: no project.
	none := detailOf(t, &Task{ID: "task-3", Description: "do it", Status: TaskStatusPending}, NewContextContainerManager())
	if none.Project != nil {
		t.Fatalf("project=%+v, want none for a task that names none", none.Project)
	}
}

func TestTaskDetailSurvivesRegistriesThatAreDown(t *testing.T) {
	// Nothing is listening there: the detail is the runtime's own answer, not an error.
	deadRegistries(t)

	containers := NewContextContainerManager()
	containers.Upsert(ContextContainer{
		ID: "project-2", Name: "Project 2", Description: "Project 2 description",
		DomainType: TaskDomainSoftwareDevelopment, ContextContainerType: ContextContainerTypeProject,
	})
	progress := detailOf(t, &Task{
		ID: "task-4", Description: "do it", Status: TaskStatusRunning,
		ContextRef: map[ContextContainerType]string{ContextContainerTypeProject: "project-2"},
	}, containers)
	project := progress.Project
	if project == nil || project.ID != "project-2" || project.Name != "Project 2" {
		t.Fatalf("project=%+v, want what this runtime knows on its own", project)
	}
	if project.Organization != nil {
		t.Fatalf("organization=%+v, want none without a registry", project.Organization)
	}
}
