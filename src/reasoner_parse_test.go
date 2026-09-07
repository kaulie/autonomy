package autonomy

import (
	"strings"
	"testing"
)

func TestBuildReasoningPromptUsesAgentPolicy(t *testing.T) {
	t.Parallel()
	task := &Task{
		ID: "t1", Goal: "g", Description: "d", Target: "1",
		Contract: Contract{ExpectedState: "changed"}, Status: "pending",
	}
	prompt := buildReasoningPrompt(DecisionContext{Task: task}, ReasoningInput{Text: "extra"})
	for _, want := range []string{
		"You are an autonomous agent running inside an Autonomy Runtime.",
		"## Constructs",
		"asset.change",
		"## Current Goal",
		"task_id: t1",
		"expected_state: changed",
		"## Current World",
		"## Additional Input",
		"extra",
		`"type": "plan | done | blocked | need_input"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "{{CONSTRUCTS}}") {
		t.Fatal("{{CONSTRUCTS}} was not replaced")
	}
}

func TestParseDecisionJSON(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		text   string
		noop   bool
		reason string
		err    bool
	}{
		{
			name:   "plan change",
			text:   `{"type":"plan","reason":"mutate target","plan":[{"capability":"asset.change","input":{"target":"1"}}],"need":{}}`,
			noop:   false,
			reason: "mutate target",
		},
		{
			name:   "plan fenced",
			text:   "```json\n{\"type\":\"plan\",\"reason\":\"go\",\"plan\":[{\"capability\":\"change\",\"input\":{}}],\"need\":{}}\n```",
			noop:   false,
			reason: "go",
		},
		{
			name:   "done",
			text:   `{"type":"done","reason":"verified","plan":[],"need":{}}`,
			noop:   true,
			reason: "verified",
		},
		{
			name:   "blocked",
			text:   `{"type":"blocked","reason":"missing key","plan":[],"need":{"missing":"api_key"}}`,
			noop:   true,
			reason: "missing key",
		},
		{
			name: "invalid",
			text: "not json",
			err:  true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reason, action, err := parseDecision(tc.text)
			if tc.err {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if reason != tc.reason {
				t.Fatalf("reason=%q want %q", reason, tc.reason)
			}
			_, isNoop := action.(NothingAction)
			if isNoop != tc.noop {
				t.Fatalf("noop=%v want %v", isNoop, tc.noop)
			}
		})
	}
}
