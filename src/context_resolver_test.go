package autonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
// the hook read — with a context builder wired the way BootstrapAutonomy wires one.
func withRuntime(t *testing.T, containers *ContextContainerManager) *Autonomy {
	t.Helper()
	previous := _autonomy
	runtime := &Autonomy{ContextContainerManager: containers}
	runtime.ContextBuilder = newContextBuilder(runtime)
	_autonomy = runtime
	t.Cleanup(func() { _autonomy = previous })
	return runtime
}

// registriesStub points the builder's two registries at stubs and counts their reads.
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
	return reads
}

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
	if _, err := agent.decide(context.Background(), 1, nil, "do it"); err != nil {
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
	if _, err := agent.decide(context.Background(), 1, nil, "do it"); err != nil {
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
	if _, err := agent.decide(context.Background(), 1, nil, "do it"); err != nil {
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
