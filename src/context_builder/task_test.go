package context_builder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tasksStub serves the platform's task registry: `GET /api/tasks/{taskId}` answers the task
// and the project it is in, exactly as the control plane does.
func tasksStub(t *testing.T, detail map[string]any) (*httptest.Server, *counter) {
	t.Helper()
	reads := &counter{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.hit()
		w.Header().Set("Content-Type", "application/json")
		if _, ok := detail["task"]; !ok {
			// No such task: the platform answers 404, and that is an answer too.
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(detail)
	}))
	t.Cleanup(server.Close)
	return server, reads
}

// taskDetail is the platform's answer for a task in project-749a0238.
func taskDetail(taskID, projectID string) map[string]any {
	return map[string]any{
		"task": map[string]any{
			"taskId": taskID, "projectId": projectID, "status": "active",
			"title": "想增加一个广播的功能", "description": "同时对某个 project 下面的所有 agent 投递消息",
			"taskType": "general", "goal": "merge",
			// The platform's bookkeeping: not injected.
			"workspace": "/Users/gaolei/agent-workspace/autonomy/" + taskID, "provider": "cline",
			"model": "deepseek-v4-flash", "agentId": "cls-46dc28b1636d4eb5",
			"prUrl": "https://github.com/kaulie/autonomy/pull/118",
		},
		"project": map[string]any{
			"projectId": projectID, "name": "autonomy", "gitRepoUrl": "https://github.com/kaulie/autonomy",
			"department": map[string]string{"departmentId": "D0005", "departmentName": "AI研发部"},
		},
	}
}

func TestTaskRegistryAnswersTheTaskAndNamesItsProject(t *testing.T) {
	server, reads := tasksStub(t, taskDetail("task-2ecd5e15ae3047f0", "project-749a0238"))
	resolver := NewTaskRegistry()
	resolver.URL = server.URL

	fields, ok, err := resolver.Resolve(context.Background(), RefTypeTask, "task-2ecd5e15ae3047f0", map[string]any{})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want the task", ok, err)
	}
	if fields["title"] != "想增加一个广播的功能" || fields["goal"] != "merge" || fields["task_type"] != "general" {
		t.Fatalf("fields=%v, want what the task is", fields)
	}
	if _, found := fields["provider"]; found {
		t.Fatalf("fields=%v, want the platform's bookkeeping left where it is", fields)
	}
	// The project is what this resolver names next, not a field of its own: the project
	// resolvers answer it, the same way they answer a project the task named itself.
	if _, found := fields["project"]; found {
		t.Fatalf("fields=%v, want the project named as a further ref", fields)
	}
	if extra := resolver.ExtraRefs(context.Background(), RefTypeTask, "task-2ecd5e15ae3047f0", fields); extra[RefTypeProject] != "project-749a0238" {
		t.Fatalf("extra=%v, want the project the task is in", extra)
	}

	// One task is read once: the cache is per id, and a cycle resolves its ref every cycle.
	if _, _, err := resolver.Resolve(context.Background(), RefTypeTask, "task-2ecd5e15ae3047f0", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if reads.calls() != 1 {
		t.Fatalf("registry reads=%d, want the answer cached", reads.calls())
	}
}

func TestTaskRegistryKnowsOnlyTasks(t *testing.T) {
	server, reads := tasksStub(t, taskDetail("task-1", "project-1"))
	resolver := NewTaskRegistry()
	resolver.URL = server.URL

	if _, ok, err := resolver.Resolve(context.Background(), RefTypeProject, "project-1", map[string]any{}); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want a project to be none of its business", ok, err)
	}
	if reads.calls() != 0 {
		t.Fatalf("registry reads=%d, want none for a ref it cannot answer", reads.calls())
	}
}

func TestTaskRegistrySaysNothingForATaskNobodyHas(t *testing.T) {
	server, _ := tasksStub(t, map[string]any{})
	resolver := NewTaskRegistry()
	resolver.URL = server.URL

	// The platform answers 404 for a task it does not have: silence, not a failure.
	fields, ok, err := resolver.Resolve(context.Background(), RefTypeTask, "task-nope", map[string]any{})
	if ok || err != nil || fields != nil {
		t.Fatalf("fields=%v ok=%v err=%v, want silence", fields, ok, err)
	}
	if extra := resolver.ExtraRefs(context.Background(), RefTypeTask, "task-nope", map[string]any{}); extra != nil {
		t.Fatalf("extra=%v, want no project named", extra)
	}
}

func TestTaskRegistryReportsAFailure(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer dead.Close()
	resolver := NewTaskRegistry()
	resolver.URL = dead.URL
	resolver.FailureTTL = 0

	_, ok, err := resolver.Resolve(context.Background(), RefTypeTask, "task-1", map[string]any{})
	if err == nil || ok {
		t.Fatalf("ok=%v err=%v, want the failure reported", ok, err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("err=%v, want the registry's status", err)
	}
}

// TestATaskRefResolvesToItsProjectOrganizationAndServices is the requirement end to end: a
// task id goes in, and what comes out is the task, the project it is in, that project's
// organization, and the services that organization has — the same chain a project ref
// walks, entered one step earlier.
func TestATaskRefResolvesToItsProjectOrganizationAndServices(t *testing.T) {
	tasks, _ := tasksStub(t, taskDetail("task-2ecd5e15ae3047f0", "project-749a0238"))
	projects, _ := projectsStub(t, []map[string]any{
		{"projectId": "project-749a0238", "name": "autonomy", "gitRepoUrl": "https://github.com/kaulie/autonomy",
			"department": map[string]string{"departmentId": "D0005", "departmentName": "AI研发部"}},
	})
	departments, _ := departmentsStub(t, "D0005", "AI研发部", "研发")
	services, _ := servicesStub(t, "D0005", []map[string]any{
		{"name": "autonomy", "gitRepoUrl": "https://github.com/kaulie/autonomy", "version": "87e2bd3e"},
	})

	taskRegistry := NewTaskRegistry()
	taskRegistry.URL = tasks.URL
	projectRegistry := NewProjectRegistry()
	projectRegistry.URL = projects.URL
	organization := NewOrganization()
	organization.URL = departments.URL
	serviceRegistry := NewServiceRegistry()
	serviceRegistry.URL = services.URL

	result := New(taskRegistry, projectRegistry, organization, serviceRegistry).
		Build(context.Background(), Ref{RefTypeTask: "task-2ecd5e15ae3047f0"})
	if len(result.Errors) != 0 {
		t.Fatalf("errors=%v, want none", result.Errors)
	}

	task := result.Sections[RefTypeTask]
	if task == nil || task["id"] != "task-2ecd5e15ae3047f0" || task["description"] != "同时对某个 project 下面的所有 agent 投递消息" {
		t.Fatalf("task=%v, want the task the ref named, as the platform has it", task)
	}

	// The project the task is in, expanded out of the ref that named the task.
	project := result.Sections[RefTypeProject]
	if project == nil || project["id"] != "project-749a0238" || project["name"] != "autonomy" {
		t.Fatalf("sections=%v, want the project the task is in", result.Sections)
	}
	organizationSection, ok := project["organization"].(map[string]any)
	if !ok || organizationSection["id"] != "D0005" || organizationSection["type"] != "研发" {
		t.Fatalf("organization=%v, want the organization the project belongs to", project["organization"])
	}
	rows, ok := organizationSection["services"].([]map[string]any)
	if !ok || len(rows) != 1 || rows[0]["name"] != "autonomy" {
		t.Fatalf("services=%v, want the organization's services", organizationSection["services"])
	}
}
