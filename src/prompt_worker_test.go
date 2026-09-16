package autonomy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability"
)

// A delegated worker's prompt is rendered from the same placeholder vocabulary
// as the planner's policy (see src/prompt.go and src/agent_policy/CODE_EDIT.md),
// but from the worker's own side of the delegation: the World and the runtime are
// the ones delegating, while the agent identity and sandbox are the worker's.
// These tests pin that split.

// TestIdentityIsWhatTheAgentIsNotItsSituation: the ## Agent section is what the
// agent is (role, purpose, id, name, how it runs); the task it works on, the cycle
// it is in and who delegated are the Runtime Context, not the identity.
func TestIdentityIsWhatTheAgentIsNotItsSituation(t *testing.T) {
	worker := &Agent{
		ID: 10126, Name: "agent-10126", Role: AgentRoleWorker, Purpose: "code_edit",
		Model: "composer-2", LLMProvider: LLMProviderCline, Backend: AgentBackendCline,
		Workspace: "/sandbox/10126/",
	}
	got := string(formatAgentIdentityJSON(worker))
	for _, want := range []string{
		`"role": "worker"`, `"purpose": "code_edit"`, `"id": 10126`, `"agent-10126"`,
		`"backend": "cline"`, `"llm_provider": "cline"`, `"model": "composer-2"`,
		`"lifecycle": "ephemeral"`, `"/sandbox/10126/"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("identity missing %s:\n%s", want, got)
		}
	}
	// Nothing of the situation leaked in, and an agent nobody described is given
	// no role: the prompt states what the runtime knows.
	for _, unwanted := range []string{`"task"`, `"cycle":`, `"delegated_by"`, `"previous_actions"`} {
		if strings.Contains(got, unwanted) {
			t.Errorf("identity carries %s:\n%s", unwanted, got)
		}
	}
	if role := string(formatAgentIdentityJSON(&Agent{ID: 1, Name: "agent-1"})); strings.Contains(role, `"role"`) {
		t.Errorf("identity invents a role: %s", role)
	}
	// A plan the runtime never created an agent for has no identity at all.
	if got := string(formatAgentIdentityJSON(nil)); got != "null" {
		t.Errorf("identity of no agent=%s, want null", got)
	}
}

// unfenceObject reads a fenced JSON object (```json … ```) as its keys, so a test
// can say what a rendered section does and does not contain.
func unfenceObject(t *testing.T, fenced string) map[string]json.RawMessage {
	t.Helper()
	body := strings.TrimSpace(fenced)
	body = strings.TrimPrefix(body, "```json")
	body = strings.TrimSuffix(strings.TrimSpace(body), "```")
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &out); err != nil {
		t.Fatalf("section is not the fenced JSON it should be: %v\n%s", err, fenced)
	}
	return out
}

// workerPlaceholderAction asks the runtime for the values it would render into a
// delegated worker's prompt, while its cycle is running.
type workerPlaceholderAction struct {
	rt     *Runtime
	worker *Agent
	values map[string]string
}

func (a *workerPlaceholderAction) Execute(DecisionContext) (ActionResult, error) {
	a.values = a.rt.WorkerPlaceholders(a.worker)
	return ActionResult{Capability: "code_edit"}, nil
}

func TestWorkerPlaceholdersAreTheWorkersOwnContext(t *testing.T) {
	prevAuto, prevWorld := _autonomy, _world
	t.Cleanup(func() {
		_autonomy = prevAuto
		_world = prevWorld
	})

	// The World is the one the delegation happens in: formatWorldJSON reads the
	// process World, so the worker's {{WORLD}} is the delegating runtime's.
	asset := Asset{ID: "asset-1", Kind: "repo", State: "healthy"}
	_world = &World{assetManager: &AssetManager{
		assets:     []Asset{asset},
		assetsByID: map[string]Asset{asset.ID: asset},
	}}
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{})
	_autonomy = &Autonomy{CapabilityFactory: f}

	planner := &Agent{ID: 10095, Name: "agent-10095", Role: AgentRolePlanner, Lifecycle: AgentLifecycleEphemeral, Backend: AgentBackendCursor, Workspace: "/sandbox/10095/"}
	worker := &Agent{
		ID: 10099, Name: "agent-code_edit-1", Role: AgentRoleWorker, Purpose: "code_edit", Model: "composer-2",
		Lifecycle: AgentLifecycleEphemeral, Backend: AgentBackendCline, Workspace: "/sandbox/10099/",
	}
	task := &Task{ID: "task-9", Description: "add a /healthz endpoint", GoalType: GoalType_FEATURE}
	ctx := DecisionContext{
		Task:    task,
		Agent:   planner,
		Cycle:   2,
		History: []Result{{Message: "executed 1 action(s): code_edit"}},
	}

	rt := NewRuntime(NewAgentFactory())
	action := &workerPlaceholderAction{rt: rt, worker: worker}
	if _, err := rt.Execute(Decision{Type: "plan", Ctx: ctx, Actions: []Action{action}}); err != nil {
		t.Fatal(err)
	}
	values := action.values

	// The World, the capabilities and the completion principles are the runtime
	// the delegation happens in — the same values the planner's policy carries.
	if !strings.Contains(values["{{WORLD}}"], `"asset-1"`) {
		t.Fatalf("world=%s, want the runtime's world", values["{{WORLD}}"])
	}
	if !strings.Contains(values["{{CONSTRUCTS}}"], `"code_edit"`) {
		t.Fatalf("constructs=%s, want the runtime's capabilities", values["{{CONSTRUCTS}}"])
	}
	if got, want := values["{{COMPLETION_PRINCIPLES}}"], CompletionPrinciplesFor(GoalType_FEATURE); got != want {
		t.Fatalf("completion principles=%q, want the goal type's %q", got, want)
	}

	// The identity is the worker's own: what it is (a worker, acquired for
	// code_edit), which agent it is, and how it runs.
	id := values["{{AGENT}}"]
	for _, want := range []string{
		`"role": "worker"`, `"purpose": "code_edit"`,
		`"agent-code_edit-1"`, `"cline"`, `"composer-2"`, `"/sandbox/10099/"`,
	} {
		if !strings.Contains(id, want) {
			t.Fatalf("identity missing %s:\n%s", want, id)
		}
	}

	// The context is the situation, over the delegating task: the task, who
	// delegated, and what the task already did.
	rc := values["{{RUNTIME_CONTEXT}}"]
	for _, want := range []string{
		`"task-9"`, `"delegated_by"`, `"agent-10095"`, `"previous_actions"`,
	} {
		if !strings.Contains(rc, want) {
			t.Fatalf("runtime context missing %s:\n%s", want, rc)
		}
	}
	// The delegating agent's identity is not the worker's: it is named as
	// delegated_by, and there is no agent of its own in the context.
	if _, ok := unfenceObject(t, rc)["agent"]; ok {
		t.Fatalf("runtime context still presents an agent as the worker's own:\n%s", rc)
	}
	// A worker has no decision cycle of its own, so it is told no step.
	if _, ok := unfenceObject(t, rc)["cycle"]; ok {
		t.Fatalf("runtime context gives the worker a cycle it does not have:\n%s", rc)
	}

	// The delegating agent's sandbox is not the worker's, so it must not appear as
	// the worker's own — neither as its identity nor as its scope.
	for name, value := range map[string]string{
		"identity":        id,
		"runtime context": rc,
		"constraints":     values["{{CONSTRAINTS}}"],
	} {
		if strings.Contains(value, "/sandbox/10095/") {
			t.Fatalf("%s presents the delegating agent's workspace as the worker's:\n%s", name, value)
		}
	}
	if c := values["{{CONSTRAINTS}}"]; !strings.Contains(c, `"/sandbox/10099/"`) || !strings.Contains(c, `"task-9"`) {
		t.Fatalf("constraints=%s, want the worker's own sandbox and the task", c)
	}
}

// TestWorkerPlaceholdersWithoutACycle: a capability run outside a decision cycle
// still gets a renderable prompt — only the parts that need a cycle (the task,
// who delegated, what the task did) are missing.
func TestWorkerPlaceholdersWithoutACycle(t *testing.T) {
	prevAuto, prevWorld := _autonomy, _world
	t.Cleanup(func() {
		_autonomy = prevAuto
		_world = prevWorld
	})
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{})
	_autonomy = &Autonomy{CapabilityFactory: f}
	_world = nil

	worker := &Agent{ID: 10099, Name: "agent-code_edit-1", Role: AgentRoleWorker, Workspace: "/sandbox/10099/"}
	values := NewRuntime(NewAgentFactory()).WorkerPlaceholders(worker)

	if values["{{WORLD}}"] == "" || values["{{CONSTRUCTS}}"] == "" {
		t.Fatalf("values=%v, want the world and constructs rendered anyway", values)
	}
	// The agent's own identity does not need a cycle to be rendered.
	if id := values["{{AGENT}}"]; !strings.Contains(id, `"agent-code_edit-1"`) || !strings.Contains(id, `"role": "worker"`) {
		t.Fatalf("identity=%s, want the worker's own identity", id)
	}
	rc := values["{{RUNTIME_CONTEXT}}"]
	if strings.Contains(rc, "delegated_by") || strings.Contains(rc, `"task"`) {
		t.Fatalf("runtime context invents a delegation:\n%s", rc)
	}
	if !strings.Contains(values["{{CONSTRAINTS}}"], `"/sandbox/10099/"`) {
		t.Fatalf("constraints=%s, want the worker's own sandbox", values["{{CONSTRAINTS}}"])
	}

	var nilRuntime *Runtime
	if values := nilRuntime.WorkerPlaceholders(worker); values["{{WORLD}}"] == "" {
		t.Fatalf("a nil runtime must still render what does not need it: %v", values)
	}
}
