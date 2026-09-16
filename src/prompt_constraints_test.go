package autonomy

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	for _, want := range []string{"scope", "workspace_rule", "task", "workspace"} {
		if keys[want] == "" {
			t.Errorf("constraints=%v, want the runtime's own fact %s", keys, want)
		}
	}
	if len(keys) != 4 {
		t.Fatalf("constraints=%v, want only the runtime's own facts without a policy", keys)
	}
	if keys["task"] != "task-9" || keys["workspace"] != "/sandbox/agent-9/" {
		t.Errorf("constraints=%v, want this cycle's task and sandbox", keys)
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
	if len(keys) != 3 {
		t.Fatalf("constraints=%v, want the runtime's own facts and nothing else", keys)
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
}
