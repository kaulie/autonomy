package autonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/context_builder"
)

// A decision cycle resolves its task's context_ref before the prompt is rendered
// (fillContextSections, called from Agent.decide): what the process knows, plus the
// platform's registries. These tests are that ordering, and what the prompt ends up
// carrying.

// withRuntime installs a runtime as this process's runtime — the globals the prompt and
// the hook read — with a context builder wired the way BootstrapAutonomy wires one. Tasks
// are this process's own rows, for the tests that resolve a ref naming a task; without
// them the runtime has no store, exactly as a hand-assembled one does not.
func withRuntime(t *testing.T, containers *ContextContainerManager, tasks ...*Task) *Autonomy {
	t.Helper()
	previous := _autonomy
	runtime := &Autonomy{ContextContainerManager: containers}
	if len(tasks) > 0 {
		store, err := openStore(filepath.Join(t.TempDir(), "autonomy.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		for _, task := range tasks {
			if err := store.UpsertTask(task); err != nil {
				t.Fatal(err)
			}
		}
		runtime.Store = store
	}
	runtime.ContextBuilder = newContextBuilder(runtime)
	_autonomy = runtime
	t.Cleanup(func() { _autonomy = previous })
	return runtime
}

// registriesStub points the builder's registries at stubs and counts their reads. The task
// registry answers for whatever id it is asked about — a task the panel has, in the project
// this stub's project registry knows — because a task id is what those resolvers are given.
func registriesStub(t *testing.T, projectID, departmentID string) *counter {
	t.Helper()
	reads := &counter{}
	projects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.hit()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"projectId": projectID, "name": "autonomy", "gitRepoUrl": "https://github.com/kaulie/autonomy",
			"department": map[string]string{"departmentId": departmentID, "departmentName": "AI研发部"},
		}})
	}))
	t.Cleanup(projects.Close)
	t.Setenv(context_builder.EnvProjectsAPIURL, projects.URL)

	departments := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.hit()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": departmentID, "name": "AI研发部", "type": "研发"})
	}))
	t.Cleanup(departments.Close)
	t.Setenv(context_builder.EnvOrganizationAPIURL, departments.URL)

	// The service registry answers for whatever organization it is asked about: the
	// department the two stubs above named, which is the point of the chain.
	services := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.hit()
		if want := "/v1/orgs/" + departmentID + "/services"; r.URL.Path != want {
			t.Errorf("asked %s, want %s", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"services": []map[string]any{{
			"namespace": "default", "name": "agent-control-plane", "type": "service",
			"description": "控制面", "gitRepoUrl": "https://github.com/kaulie/agent-control-plane.git", "version": "f8d53f2d",
		}}})
	}))
	t.Cleanup(services.Close)
	t.Setenv(context_builder.EnvServiceRegistryAPIURL, services.URL)

	tasks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.hit()
		if want := "/api/tasks/" + panelTaskID; r.URL.Path != want {
			// A task this process has and the panel does not: the panel says so, and the
			// row this process owns is the whole answer.
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task": map[string]any{
				"taskId": panelTaskID, "projectId": projectID, "status": "active",
				"title": "面板上的那条任务", "description": "面板上的那条任务的原话",
				"taskType": "general", "goal": "merge",
			},
			"project": map[string]any{
				"projectId": projectID, "name": "autonomy", "gitRepoUrl": "https://github.com/kaulie/autonomy",
				"department": map[string]string{"departmentId": departmentID, "departmentName": "AI研发部"},
			},
		})
	}))
	t.Cleanup(tasks.Close)
	t.Setenv(context_builder.EnvTaskRegistryAPIURL, tasks.URL)
	return reads
}

// panelTaskID is the one task the stub task registry has: a task the panel owns.
const panelTaskID = "task-from-the-panel"

// counter counts calls, shared for the two stubs of one test.
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

// contextEntityBlock is the prompt's context_entity block as data: an assertion is
// then about what the prompt says, not about how it is indented.
func contextEntityBlock(t *testing.T, decision DecisionContext) []map[string]any {
	t.Helper()
	raw := formatContextEntitiesJSON(decision)
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("context_entity=%s: %v", raw, err)
	}
	return entries
}

// captureContextReasoner is a reasoner that keeps the context it was decided on.
type captureContextReasoner struct{ seen DecisionContext }

func (r *captureContextReasoner) Reason(ctx DecisionContext, _ ReasoningInput) (ReasoningResult, error) {
	r.seen = ctx
	return ReasoningResult{Decision: Decision{Type: "plan", Reason: "ok", Actions: []Action{NothingAction{}}, Ctx: ctx}}, nil
}

func TestADecisionCycleResolvesItsContextBeforeThePrompt(t *testing.T) {
	reads := registriesStub(t, "project-749a0238", "D0005")

	containers := NewContextContainerManager()
	containers.Upsert(ContextContainer{
		ID: "project-749a0238", Name: "the container's own name", Description: "the container's own description",
		DomainType: TaskDomainSoftwareDevelopment, ContextContainerType: ContextContainerTypeProject,
	})
	withRuntime(t, containers)

	task := &Task{
		ID: "task-ctx", Description: "开放入口", Domain: TaskDomainSoftwareDevelopment,
		GoalType: GoalType_FEATURE, Status: TaskStatusRunning,
		ContextRef: map[ContextContainerType]string{ContextContainerTypeProject: "project-749a0238"},
	}
	reasoner := &captureContextReasoner{}
	agent := &Agent{CurrentTask: task, DecideMaker: &DecisionMaker{reasoner: reasoner}}
	if _, err := agent.decide(context.Background(), 1, nil, "do it", nil); err != nil {
		t.Fatal(err)
	}

	// The cycle decided on a resolved world, not on the reference string.
	section := reasoner.seen.ContextSections[context_builder.RefTypeProject]
	if section == nil {
		t.Fatalf("sections=%v, want the ref resolved before the decision", reasoner.seen.ContextSections)
	}
	if section["name"] != "autonomy" {
		t.Fatalf("name=%v, want the registry's name to win", section["name"])
	}
	if section["description"] != "the container's own description" || section["domain"] != string(TaskDomainSoftwareDevelopment) {
		t.Fatalf("section=%v, want what only this process knows", section)
	}
	if section["git_repo_url"] != "https://github.com/kaulie/autonomy" {
		t.Fatalf("section=%v, want the project's repository", section)
	}
	organization, ok := section["organization"].(map[string]any)
	if !ok || organization["id"] != "D0005" || organization["name"] != "AI研发部" || organization["type"] != "研发" {
		t.Fatalf("organization=%v, want the department the project belongs to", section["organization"])
	}
	// And the services that department registered, with the repository each one's code
	// lives in: project -> organization -> service registry, in one resolution.
	services, ok := organization["services"].([]map[string]any)
	if !ok || len(services) != 1 || services[0]["name"] != "agent-control-plane" ||
		services[0]["git_repo_url"] != "https://github.com/kaulie/agent-control-plane.git" {
		t.Fatalf("services=%v, want the services the project's organization has", organization["services"])
	}
	if reads.calls() == 0 {
		t.Fatal("no registry was read, want the platform asked")
	}

	// And this is what the prompt carries: the delta's context_entity block.
	t.Logf("context_entity=%s", formatContextEntitiesJSON(reasoner.seen))
	entries := contextEntityBlock(t, reasoner.seen)
	if len(entries) != 1 {
		t.Fatalf("context_entity=%v, want one entry", entries)
	}
	entry := entries[0]
	if entry["id"] != "project-749a0238" || entry["type"] != context_builder.RefTypeProject {
		t.Fatalf("entry=%v, want the ref the task stated", entry)
	}
	if entry["name"] != "autonomy" || entry["git_repo_url"] != "https://github.com/kaulie/autonomy" {
		t.Fatalf("entry=%v, want the project's name and repository", entry)
	}
	if entry["description"] != "the container's own description" || entry["domain"] != string(TaskDomainSoftwareDevelopment) {
		t.Fatalf("entry=%v, want what only this process knows", entry)
	}
	blockOrganization, ok := entry["organization"].(map[string]any)
	if !ok || blockOrganization["id"] != "D0005" || blockOrganization["name"] != "AI研发部" || blockOrganization["type"] != "研发" {
		t.Fatalf("entry=%v, want the organization in the prompt", entry)
	}
	// The services ride along into the prompt: what the planner reads is the world —
	// which services this organization has, and where each one's code lives.
	prompted := promptServices(t, blockOrganization)
	if len(prompted) != 1 || prompted[0]["name"] != "agent-control-plane" ||
		prompted[0]["git_repo_url"] != "https://github.com/kaulie/agent-control-plane.git" {
		t.Fatalf("entry=%v, want the organization's services in the prompt", entry)
	}
}

// promptServices is a section's services as the prompt's JSON carries them: everything
// went through encoding/json, so a list of objects reads back as []any.
func promptServices(t *testing.T, organization map[string]any) []map[string]any {
	t.Helper()
	rows, ok := organization["services"].([]any)
	if !ok {
		t.Fatalf("organization=%v, want the services in the prompt", organization)
	}
	services := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		service, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("service=%v, want an object", row)
		}
		services = append(services, service)
	}
	return services
}

// byType reads the prompt's context_entity block the way a reader does: one entry per
// container the task's ref named and resolved to.
func byType(entries []map[string]any) map[string]map[string]any {
	indexed := map[string]map[string]any{}
	for _, entry := range entries {
		if refType, ok := entry["type"].(string); ok {
			indexed[refType] = entry
		}
	}
	return indexed
}

// TestATaskRefResolvesIntoTheWorldTheTaskNames is the requirement at the prompt: a ref that
// names a task answers what that task is (its row in this process), and — because a task's
// world is written on its row — the project it is in, that project's organization and the
// services that organization has.
func TestATaskRefResolvesIntoTheWorldTheTaskNames(t *testing.T) {
	registriesStub(t, "project-749a0238", "D0005")
	withRuntime(t, NewContextContainerManager(), &Task{
		ID: "task-b", Description: "上线窗口那件事", Status: TaskStatusCompleted,
		Domain: TaskDomainSoftwareDevelopment, GoalType: GoalType_FEATURE,
		ContextRef: map[ContextContainerType]string{ContextContainerTypeProject: "project-749a0238"},
	})

	reasoner := &captureContextReasoner{}
	agent := &Agent{
		CurrentTask: &Task{ID: "task-a", ContextRef: map[ContextContainerType]string{context_builder.RefTypeTask: "task-b"}},
		DecideMaker: &DecisionMaker{reasoner: reasoner},
	}
	if _, err := agent.decide(context.Background(), 1, nil, "do it", nil); err != nil {
		t.Fatal(err)
	}

	entries := byType(contextEntityBlock(t, reasoner.seen))
	task := entries[context_builder.RefTypeTask]
	if task == nil || task["id"] != "task-b" || task["description"] != "上线窗口那件事" || task["goal_type"] != "dev_feature" {
		t.Fatalf("task=%v, want the row of the task the ref named", task)
	}
	project := entries[context_builder.RefTypeProject]
	if project == nil || project["id"] != "project-749a0238" || project["name"] != "autonomy" {
		t.Fatalf("context_entity=%v, want the project that task's row names", entries)
	}
	organization, ok := project["organization"].(map[string]any)
	if !ok || organization["id"] != "D0005" || organization["type"] != "研发" {
		t.Fatalf("organization=%v, want the project's organization", project["organization"])
	}
	if prompted := promptServices(t, organization); len(prompted) != 1 || prompted[0]["name"] != "agent-control-plane" {
		t.Fatalf("services=%v, want the organization's services in the prompt", organization["services"])
	}
}

// TestATaskRefThisProcessDoesNotHaveIsAnsweredByThePlatform: a task the panel owns and this
// runtime never ran is answered by the platform's task registry, and its world resolves the
// same way — the registration order is the pipeline, not a second mechanism.
func TestATaskRefThisProcessDoesNotHaveIsAnsweredByThePlatform(t *testing.T) {
	registriesStub(t, "project-749a0238", "D0005")
	withRuntime(t, NewContextContainerManager())

	reasoner := &captureContextReasoner{}
	agent := &Agent{
		CurrentTask: &Task{ID: "task-c", ContextRef: map[ContextContainerType]string{
			context_builder.RefTypeTask: panelTaskID,
		}},
		DecideMaker: &DecisionMaker{reasoner: reasoner},
	}
	if _, err := agent.decide(context.Background(), 1, nil, "do it", nil); err != nil {
		t.Fatal(err)
	}

	entries := byType(contextEntityBlock(t, reasoner.seen))
	task := entries[context_builder.RefTypeTask]
	if task == nil || task["id"] != panelTaskID || task["description"] != "面板上的那条任务的原话" {
		t.Fatalf("task=%v, want the task the platform has", task)
	}
	project := entries[context_builder.RefTypeProject]
	if project == nil || project["id"] != "project-749a0238" || project["name"] != "autonomy" {
		t.Fatalf("context_entity=%v, want the project the platform put that task in", entries)
	}
	organization, ok := project["organization"].(map[string]any)
	if !ok || organization["id"] != "D0005" {
		t.Fatalf("organization=%v, want the project's organization", project["organization"])
	}
	if prompted := promptServices(t, organization); len(prompted) != 1 || prompted[0]["name"] != "agent-control-plane" {
		t.Fatalf("services=%v, want the organization's services in the prompt", organization["services"])
	}
}

func TestThePromptFallsBackToThisProcessWorld(t *testing.T) {
	// No builder at all: a runtime assembled by hand answers with what it has
	// registered, which is what the prompt did before there was a builder.
	previous := _autonomy
	containers := NewContextContainerManager()
	containers.Upsert(ContextContainer{
		ID: "project-2", Name: "Project 2", Description: "Project 2 description",
		DomainType: TaskDomainSoftwareDevelopment, ContextContainerType: ContextContainerTypeProject,
	})
	_autonomy = &Autonomy{ContextContainerManager: containers}
	t.Cleanup(func() { _autonomy = previous })

	decision := DecisionContext{Task: &Task{
		ID: "task-2", ContextRef: map[ContextContainerType]string{ContextContainerTypeProject: "project-2"},
	}}
	entries := contextEntityBlock(t, decision)
	if len(entries) != 1 || entries[0]["name"] != "Project 2" || entries[0]["description"] != "Project 2 description" {
		t.Fatalf("context_entity=%v, want the container this process registered", entries)
	}
}

func TestTheContextBuilderCanBeSwitchedOff(t *testing.T) {
	reads := registriesStub(t, "project-1", "D0005")
	t.Setenv(EnvContextBuilder, "0")

	runtime := &Autonomy{}
	if builder := newContextBuilder(runtime); builder != nil {
		t.Fatal("builder is not nil, want AUTONOMY_CONTEXT_BUILDER=0 to switch it off")
	}
	containers := NewContextContainerManager()
	containers.Upsert(ContextContainer{ID: "project-1", Name: "Project 1", ContextContainerType: ContextContainerTypeProject})
	withRuntime(t, containers)

	reasoner := &captureContextReasoner{}
	agent := &Agent{
		CurrentTask: &Task{ID: "task-1", ContextRef: map[ContextContainerType]string{ContextContainerTypeProject: "project-1"}},
		DecideMaker: &DecisionMaker{reasoner: reasoner},
	}
	if _, err := agent.decide(context.Background(), 1, nil, "do it", nil); err != nil {
		t.Fatal(err)
	}
	if reasoner.seen.ContextSections != nil {
		t.Fatalf("sections=%v, want none with the builder off", reasoner.seen.ContextSections)
	}
	if entries := contextEntityBlock(t, reasoner.seen); len(entries) != 1 || entries[0]["name"] != "Project 1" {
		t.Fatalf("context_entity=%v, want this process's own answer", entries)
	}
	if reads.calls() != 0 {
		t.Fatalf("registry reads=%d, want none with the builder off", reads.calls())
	}
}

func TestARegistryThatIsDownStillRendersTheRef(t *testing.T) {
	deadRegistries(t)
	withRuntime(t, NewContextContainerManager())

	reasoner := &captureContextReasoner{}
	agent := &Agent{
		CurrentTask: &Task{ID: "task-3", ContextRef: map[ContextContainerType]string{ContextContainerTypeProject: "project-9"}},
		DecideMaker: &DecisionMaker{reasoner: reasoner},
	}
	if _, err := agent.decide(context.Background(), 1, nil, "do it", nil); err != nil {
		t.Fatalf("decide: %v, want a registry that is down not to fail the cycle", err)
	}
	entries := contextEntityBlock(t, reasoner.seen)
	if len(entries) != 1 || entries[0]["id"] != "project-9" || entries[0]["type"] != context_builder.RefTypeProject {
		t.Fatalf("context_entity=%v, want the ref the task stated", entries)
	}
}

func TestContextBuilderSettings(t *testing.T) {
	t.Setenv(EnvContextTimeout, "")
	if got := contextBuilderTimeout(); got != context_builder.DefaultTimeout {
		t.Fatalf("timeout=%s, want the default %s", got, context_builder.DefaultTimeout)
	}
	t.Setenv(EnvContextTimeout, "250ms")
	if got := contextBuilderTimeout(); got != 250*time.Millisecond {
		t.Fatalf("timeout=%s, want the configured one", got)
	}
	for _, value := range []string{"0", "off", "false", "no", "OFF"} {
		t.Setenv(EnvContextBuilder, value)
		if contextBuilderEnabled() {
			t.Fatalf("AUTONOMY_CONTEXT_BUILDER=%s, want the builder off", value)
		}
	}
	for _, value := range []string{"", "1", "on", "true"} {
		t.Setenv(EnvContextBuilder, value)
		if !contextBuilderEnabled() {
			t.Fatalf("AUTONOMY_CONTEXT_BUILDER=%s, want the builder on", value)
		}
	}
}
