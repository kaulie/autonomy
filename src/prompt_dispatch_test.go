package autonomy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readShippedPolicy reads a policy file the way the runtime reads it — from
// PROJECT_ROOT at call time (src/prompt.go) — so these tests pin the file a
// deployment ships, not a copy of its wording.
func readShippedPolicy(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "src", "agent_policy", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// The planner dispatches on capabilities, and only on capabilities: a plan step
// is a capability plus its input, and who serves it — a deterministic provider, or
// an agent the Runtime acquires for it — is the Runtime's business. "An agent" is
// not something a plan can call, so a policy that sends the planner looking for
// one is describing a mechanism this runtime does not have (see
// docs/capability.md: a capability says what can be done, not who does it).
func TestThePlannerPolicyDispatchesOnCapabilities(t *testing.T) {
	policy := readShippedPolicy(t, "AGENT_V2.md")

	for _, want := range []string{
		"## Capability Dispatch",
		"Steps: Capability + Input",
		"the only moves a plan has",
		"{{CONSTRUCTS}}",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the planner policy no longer says %q", want)
		}
	}
	// The dispatch vocabulary it must not fall back into.
	for _, unwanted := range []string{
		"Sub-Tasks", "Agent Assignment", "sub-agent", "Worker Agent",
		"STOP PLANNING and DELEGATE", "before delegation",
	} {
		if strings.Contains(policy, unwanted) {
			t.Errorf("the planner policy dispatches on agents again: %q", unwanted)
		}
	}
}

// The planner dispatches on capabilities, and binds every step input to a source:
// the two rules that replaced "an agent will figure it out". They are pinned here
// because the policy is what the planner reads, so its wording is the contract.
func TestThePlannerPolicyBindsEveryInputToASource(t *testing.T) {
	policy := readShippedPolicy(t, "AGENT_V2.md")

	for _, want := range []string{
		"## Plan Data Lineage",
		"There are exactly **three**",
		"no fourth source",
		"a literal you wrote",
		"A capability's metadata is not an output",
		"step:<name>.output.<key>",
		"world_model:asset.<id>.<kind|state>",
		"No implicit aggregation",
		"Never write a description of a value you do not have",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the planner policy no longer says %q", want)
		}
	}
	// The shape it must not fall back into: a step whose input is a bare object with
	// no source, or a plan that hands work to a step without saying what it reads.
	for _, unwanted := range []string{
		`"capability": "capability.name",\n        "input": {}`,
		"the input required by the capability",
	} {
		if strings.Contains(policy, unwanted) {
			t.Errorf("the planner policy still describes the old step shape: %q", unwanted)
		}
	}
	// And the schema it shows must be the one the runtime reads.
	for _, want := range []string{`"name":`, `"inputs":`, `{"source": "step:<name>.output.<key>"}`} {
		if !strings.Contains(policy, want) {
			t.Errorf("the planner policy's schema no longer shows %q", want)
		}
	}
}

// The worker prompt is the other side of the same line: it belongs to a
// capability the runtime acquired an agent for, and it must still be the agent's
// own identity and sandbox — not a plan that names agents.
func TestTheWorkerPromptIsStillTheWorkersOwn(t *testing.T) {
	policy := readShippedPolicy(t, "CODE_EDIT.md")
	for _, want := range []string{"{{WORKSPACE}}", "{{GOAL}}", "{{AGENT}}", "{{CONSTRUCTS}}"} {
		if !strings.Contains(policy, want) {
			t.Errorf("the worker prompt no longer carries %q", want)
		}
	}
}
