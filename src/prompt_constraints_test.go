package autonomy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Runtime constraints are two things, and they come from two places on purpose:
// the facts this runtime knows about the cycle (its task, its sandbox) and the
// rules of its policy file. The layer that renders prompts knows neither
// deployment nor any other domain: it renders what it is given.

// constraintKeys is the rendered {{CONSTRAINTS}} object.
func constraintKeys(t *testing.T, ctx DecisionContext) map[string]string {
	t.Helper()
	var keys map[string]string
	if err := json.Unmarshal(constraintsJSON(ctx), &keys); err != nil {
		t.Fatalf("constraints are not the documented JSON: %v", err)
	}
	return keys
}

func writePolicy(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, "src", "agent_policy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CONSTRAINTS.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestConstraintsAreTheRuntimesFactsPlusItsPolicy: a runtime with no policy file
// adds no rules — and says nothing about deployment, because nothing in this
// layer is deployment's business.
func TestConstraintsAreTheRuntimesFactsPlusItsPolicy(t *testing.T) {
	t.Setenv("PROJECT_ROOT", t.TempDir())
	ctx := DecisionContext{Task: &Task{ID: "task-9"}, Agent: &Agent{Workspace: "/sandbox/agent-9/"}}

	keys := constraintKeys(t, ctx)
	for _, want := range []string{"scope", "workspace_rule", "task", "workspace", "role", "this_agent_edits_files", "file_changes"} {
		if keys[want] == "" {
			t.Errorf("constraints=%v, want the runtime's own fact %s", keys, want)
		}
	}
	if len(keys) != 7 {
		t.Fatalf("constraints=%v, want only the runtime's own planner facts without a policy", keys)
	}
	if keys["task"] != "task-9" || keys["workspace"] != "/sandbox/agent-9/" {
		t.Errorf("constraints=%v, want this cycle's task and sandbox", keys)
	}
	if keys["role"] != "planner" || keys["this_agent_edits_files"] != "false" {
		t.Errorf("constraints=%v, want a planner that does not edit files", keys)
	}
	if !strings.Contains(keys["file_changes"], "runtime") || !strings.Contains(keys["file_changes"], "does not choose") {
		t.Errorf("file_changes=%q, want the runtime to assign the worker workspace", keys["file_changes"])
	}
	if strings.Contains(keys["workspace_rule"], "the only place files may be changed") {
		t.Errorf("workspace_rule=%q still describes a single sandbox as exclusive", keys["workspace_rule"])
	}
}

// TestAPolicyRuleRendersNextToTheFacts: the deploy rule is a sentence about
// deploying, so it is written in the runtime's policy — and it travels into the
// same object the ## Constraints section renders, for the planner and for every
// delegated worker alike.
func TestAPolicyRuleRendersNextToTheFacts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJECT_ROOT", root)
	writePolicy(t, root, `{
  "deploy": "the Runtime's move, not the agent's",
  "budget": "a rule the core has never heard of",
  "task": "not this task",
  "blank": "   "
}`)
	keys := constraintKeys(t, DecisionContext{Task: &Task{ID: "task-9"}})
	if got, want := keys["deploy"], "the Runtime's move, not the agent's"; got != want {
		t.Errorf("deploy=%q want %q", got, want)
	}
	if keys["budget"] == "" {
		t.Errorf("constraints=%v, want the policy's rules whatever they are about", keys)
	}
	if keys["blank"] != "" {
		t.Errorf("constraints=%v, want an empty rule dropped", keys)
	}
	// The runtime's facts win: a policy does not get to redefine the task.
	if keys["task"] != "task-9" {
		t.Errorf("task=%q, want the runtime's own task id", keys["task"])
	}
}

// TestAnUnreadablePolicyDoesNotBreakThePrompt: a policy file that cannot be read
// adds nothing and is reported — a prompt still has to render, and nobody may
// silently obey (or silently drop) a rule they could not read.
func TestAnUnreadablePolicyDoesNotBreakThePrompt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJECT_ROOT", root)
	writePolicy(t, root, "{ this is not json")
	keys := constraintKeys(t, DecisionContext{Task: &Task{ID: "task-9"}})
	if len(keys) != 6 {
		t.Fatalf("constraints=%v, want the runtime's own planner facts and nothing else", keys)
	}
	if keys["task"] != "task-9" {
		t.Errorf("constraints=%v, want the runtime's facts", keys)
	}
}

// TestTheShippedPolicyKeepsDeployingTheRuntimesMove: the rule is a file now, so
// the test reads the file a deployment ships rather than the code that renders it.
func TestTheShippedPolicyKeepsDeployingTheRuntimesMove(t *testing.T) {
	t.Setenv("PROJECT_ROOT", filepath.Join(".."))
	keys := constraintKeys(t, DecisionContext{})
	if got, want := keys["deploy"], "the Runtime's move, not the agent's"; got != want {
		t.Fatalf("deploy=%q want %q (src/agent_policy/CONSTRAINTS.json)", got, want)
	}
	if _, ok := keys["worker_workspace"]; ok {
		t.Fatalf("constraints=%v, worker authorization is not a shared policy sentence", keys)
	}
}

// TestPlannerAndWorkerConstraintsAreAuthoredSeparately: the runtime writes each
// role's constraints. The planner is told it does not edit files; the worker is
// told its own sandbox. The planner's sentences must not appear in the worker
// prompt — worker authorization is not a planner control surface.
func TestPlannerAndWorkerConstraintsAreAuthoredSeparately(t *testing.T) {
	t.Setenv("PROJECT_ROOT", t.TempDir())
	planner := constraintKeys(t, DecisionContext{
		Task:  &Task{ID: "task-9"},
		Agent: &Agent{Role: AgentRolePlanner, Workspace: "/sandbox/planner/"},
	})
	worker := constraintKeys(t, DecisionContext{
		Task:  &Task{ID: "task-9"},
		Agent: &Agent{Role: AgentRoleWorker, Workspace: "/sandbox/worker/"},
	})
	if planner["role"] != "planner" || planner["this_agent_edits_files"] != "false" {
		t.Fatalf("planner=%v", planner)
	}
	if worker["role"] != "worker" || worker["this_agent_edits_files"] != "true" {
		t.Fatalf("worker=%v", worker)
	}
	if worker["workspace"] != "/sandbox/worker/" || strings.Contains(fmtJoin(worker), "/sandbox/planner/") {
		t.Fatalf("worker constraints leaked the planner sandbox: %v", worker)
	}
	if _, ok := worker["file_changes"]; ok {
		t.Fatalf("worker=%v, file_changes is a planner fact", worker)
	}
	for _, unwanted := range []string{"planner does not edit", "does not choose", "ask the owner", "authorize"} {
		if strings.Contains(fmtJoin(worker), unwanted) {
			t.Fatalf("worker constraints still speak in the planner's voice (%q): %v", unwanted, worker)
		}
	}
	if !strings.Contains(worker["workspace_rule"], "this worker may change files") {
		t.Fatalf("worker workspace_rule=%q, want this worker's own sandbox", worker["workspace_rule"])
	}
}

func fmtJoin(keys map[string]string) string {
	var b strings.Builder
	for _, v := range keys {
		b.WriteString(v)
		b.WriteByte('\n')
	}
	return b.String()
}
