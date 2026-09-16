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
