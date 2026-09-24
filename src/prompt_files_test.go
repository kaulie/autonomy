package autonomy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The two halves of the reasoning prompt are files, and this is what that means: editing the file
// changes what the model is told. The frame is the initialization prompt (the agent's system
// prompt plus the policy); the delta is what every later cycle carries.
func TestTheReasoningPromptIsTheFileItComesFrom(t *testing.T) {
	root := preparePolicyRoot(t)
	t.Setenv("PROJECT_ROOT", root)
	policyDir := filepath.Join(root, "src", "agent_policy")

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(policyDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Both templates are replaced with markers, so what the rendered prompt contains can only come
	// from the files.
	write("REASONING_FRAME.md", "FRAME-FROM-FILE\n{{SYSTEM_PROMPT}}{{AGENT_POLICY}}")
	write("REASONING_DELTA.md", "DELTA-FROM-FILE cycle={{CYCLE}} marker={{DELTA_MARKER}}\n{{BRIEFING_NOTE}}PAYLOAD={{PAYLOAD}}\n")

	agent := &Agent{ID: 9101, Name: "agent-9101", SystemPrompt: "be brief"}
	ctx := DecisionContext{Agent: agent, Task: &Task{ID: "task-prompts"}, Cycle: 3}
	input := ReasoningInput{}

	frame, err := buildReasoningFrame(ctx, input)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	for _, want := range []string{"FRAME-FROM-FILE", "## System Prompt", "be brief"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("the frame should carry %q\n%s", want, frame)
		}
	}
	if !strings.Contains(frame, "Autonomy Bootstrap Prompt") {
		t.Fatalf("the frame should still carry the policy file (AGENT_V2.md)\n%s", frame)
	}

	delta, err := buildReasoningDelta(ctx, input)
	if err != nil {
		t.Fatalf("delta: %v", err)
	}
	for _, want := range []string{"DELTA-FROM-FILE", "cycle=3", reasoningDeltaMarker, "PAYLOAD=", "\"runtime_context\""} {
		if !strings.Contains(delta, want) {
			t.Fatalf("the delta should carry %q\n%s", want, delta)
		}
	}

	// A runtime with no PROJECT_ROOT still sends a delta: the build carries its own copy of the
	// templates, so a long-lived session cannot lose its later cycles to a missing file.
	t.Setenv("PROJECT_ROOT", "")
	fallback, err := buildReasoningDelta(ctx, input)
	if err != nil {
		t.Fatalf("delta without PROJECT_ROOT: %v", err)
	}
	if !strings.Contains(fallback, "## Decision Cycle 3") {
		t.Fatalf("the embedded template should have been used\n%s", fallback)
	}
}
