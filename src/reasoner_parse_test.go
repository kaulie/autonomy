package autonomy

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability"
)

func TestBuildReasoningPromptUsesAgentPolicy(t *testing.T) {
	root := preparePolicyRoot(t)
	t.Setenv("PROJECT_ROOT", root)

	path := filepath.Join(root, "data", "autonomy.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prevStore, prevAuto, prevFlag := _store, _autonomy, bootstrapFlag
	_store = store
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{Assets: worldAssetMutator()})
	_autonomy = &Autonomy{CapabilityFactory: f, Store: store}
	bootstrapFlag = true
	t.Cleanup(func() {
		_store = prevStore
		_autonomy = prevAuto
		bootstrapFlag = prevFlag
	})

	task := &Task{
		ID: "t1", Goal: "g", Description: "d", Target: "1",
		Domain:   TaskDomainServer,
		Contract: Contract{ExpectedState: "changed"}, Status: "pending",
	}
	agent := &Agent{
		ID: 10001, Name: "agent-10001", Lifecycle: AgentLifecycleEphemeral,
		Backend: AgentBackendCursor, Workspace: "/tmp/ws/",
	}
	prompt, err := buildReasoningPrompt(DecisionContext{Task: task, Agent: agent, Step: 2}, ReasoningInput{Text: "extra"})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := loadAgentPolicy()
	if err != nil {
		t.Fatal(err)
	}
	filledPolicy := applyPolicyPlaceholders(raw, policyPlaceholders())
	if !strings.HasPrefix(prompt, filledPolicy) {
		t.Fatalf("prompt must start with full AGENT_V2.md (placeholders only)")
	}
	if strings.Contains(prompt, "{{CONSTRUCTS}}") {
		t.Fatal("{{CONSTRUCTS}} was not replaced")
	}
	if strings.Contains(prompt, "Respond with a single JSON object only") {
		t.Fatal("must not append extra output instructions beyond AGENT_V2.md")
	}
	if strings.Contains(prompt, "task_id: t1") || strings.Contains(prompt, "## Current Goal") {
		t.Fatal("legacy bullet Goal/World appendix must not appear")
	}

	for _, want := range []string{
		"## Constructs",
		"asset.change",
		"mutate the task target asset",
		`"type": "plan | done | blocked | need_input"`,
		"## Runtime Context",
		"### Agent",
		"### Goal",
		"### World",
		"### Additional Input",
		`"completion_contract"`,
		`"focus_target_id"`,
		`"assets"`,
		`"expected_state"`,
		`"agent-10001"`,
		`"text": "extra"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}

	goalRaw := extractFencedJSON(prompt, "### Goal")
	var goal map[string]any
	if err := json.Unmarshal([]byte(goalRaw), &goal); err != nil {
		t.Fatalf("goal json: %v\n%s", err, goalRaw)
	}
	taskObj, _ := goal["task"].(map[string]any)
	if taskObj["id"] != "t1" || taskObj["domain"] != "server" {
		t.Fatalf("goal.task=%v", taskObj)
	}
	contract, _ := goal["completion_contract"].(map[string]any)
	if contract["expected_state"] != "changed" {
		t.Fatalf("contract=%v", contract)
	}
}

func extractFencedJSON(prompt, heading string) string {
	i := strings.Index(prompt, heading)
	if i < 0 {
		return ""
	}
	rest := prompt[i:]
	start := strings.Index(rest, "```json")
	if start < 0 {
		return ""
	}
	rest = rest[start+len("```json"):]
	end := strings.Index(rest, "```")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func TestLoadAgentPolicyRequiresProjectRoot(t *testing.T) {
	t.Setenv("PROJECT_ROOT", "")
	_, err := loadAgentPolicy()
	if err == nil || !strings.Contains(err.Error(), "PROJECT_ROOT") {
		t.Fatalf("expected PROJECT_ROOT error, got %v", err)
	}
}

func TestStripCodeFences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain json unchanged",
			in:   `{"type":"plan","reason":"go","plan":[],"need":{}}`,
			want: `{"type":"plan","reason":"go","plan":[],"need":{}}`,
		},
		{
			name: "json fence stripped",
			in:   "```json\n{\"type\":\"plan\"}\n```",
			want: `{"type":"plan"}`,
		},
		{
			name: "generic fence stripped",
			in:   "```\n{\"type\":\"plan\"}\n```",
			want: `{"type":"plan"}`,
		},
		{
			name: "whitespace trimmed",
			in:   "\n  plain text output  \n",
			want: "plain text output",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := stripCodeFences(tc.in); got != tc.want {
				t.Fatalf("stripCodeFences()=%q want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeReasonOutput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "prose with fenced json",
			raw:  "I've observed the workspace.\n\n```json\n{\"type\":\"need_input\",\"reason\":\"missing info\"}\n```",
			want: `{"type":"need_input","reason":"missing info"}`,
		},
		{
			name: "plain json",
			raw:  `{"type":"plan","reason":"go","plan":[],"need":{}}`,
			want: `{"type":"plan","reason":"go","plan":[],"need":{}}`,
		},
		{
			name: "free text without json",
			raw:  "changed files: a.go",
			want: "changed files: a.go",
		},
		{
			name: "fenced json",
			raw:  "```json\n{\"type\":\"plan\"}\n```",
			want: `{"type":"plan"}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeReasonOutput(tc.raw); got != tc.want {
				t.Fatalf("normalizeReasonOutput()=%q want %q", got, tc.want)
			}
		})
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
