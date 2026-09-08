package autonomy

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildReasoningPromptUsesAgentPolicy(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROJECT_ROOT", root)
	BootstrapAutonomy()

	task := &Task{
		ID: "t1", Goal: "g", Description: "d", Target: "1",
		Domain:   TaskDomainServer,
		Contract: Contract{ExpectedState: "changed"}, Status: "pending",
	}
	prompt, err := buildReasoningPrompt(DecisionContext{Task: task}, ReasoningInput{Text: "extra"})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := loadAgentPolicy()
	if err != nil {
		t.Fatal(err)
	}
	filledPolicy := applyPolicyPlaceholders(raw, policyPlaceholders())
	if !strings.HasPrefix(prompt, filledPolicy) {
		t.Fatalf("prompt must start with full AGENT_V1.md (placeholders only)")
	}
	if strings.Contains(prompt, "{{CONSTRUCTS}}") {
		t.Fatal("{{CONSTRUCTS}} was not replaced")
	}
	if strings.Contains(prompt, "Respond with a single JSON object only") {
		t.Fatal("must not append extra output instructions beyond AGENT_V1.md")
	}

	for _, want := range []string{
		"## Constructs",
		"asset.change",
		"mutate the task target asset",
		`"type": "plan | done | blocked | need_input"`,
		"## Current Goal",
		"task_id: t1",
		"domain: server",
		"expected_state: changed",
		"## Current World",
		"## Additional Input",
		"extra",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}
}

func TestLoadAgentPolicyRequiresProjectRoot(t *testing.T) {
	t.Setenv("PROJECT_ROOT", "")
	_, err := loadAgentPolicy()
	if err == nil || !strings.Contains(err.Error(), "PROJECT_ROOT") {
		t.Fatalf("expected PROJECT_ROOT error, got %v", err)
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

func TestCapabilityFactoryRegisterAppearsInGetAll(t *testing.T) {
	t.Parallel()
	f := NewCapabilityFactory()
	f.Register(AssetChangeCapability{})
	all := f.GetAll()
	if len(all) != 1 || all[0].Name() != "asset.change" {
		t.Fatalf("GetAll=%v", all)
	}
	if got := f.FormatConstructs(); !strings.Contains(got, "asset.change:") {
		t.Fatalf("constructs=%q", got)
	}
}
