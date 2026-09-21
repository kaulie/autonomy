package autonomy

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/capability"
)

func TestBuildReasoningPromptUsesAgentPolicy(t *testing.T) {
	root := preparePolicyRoot(t)
	t.Setenv("PROJECT_ROOT", root)

	path := filepath.Join(root, "data", "autonomy.db")
	store, err := openStore(path)
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
		ID: "t1", Description: "d",
		Domain:   TaskDomainServer,
		GoalType: GoalType_FEATURE,
		Status:   "pending",
	}
	agent := &Agent{
		ID: 10001, Name: "agent-10001", Role: AgentRolePlanner, Lifecycle: AgentLifecycleEphemeral,
		Backend: AgentBackendCursor, Workspace: "/tmp/ws/",
	}
	ctx := DecisionContext{Task: task, Agent: agent, Cycle: 2}
	prompt, err := buildReasoningPrompt(ctx, ReasoningInput{Text: "extra"})
	if err != nil {
		t.Fatal(err)
	}

	frame, err := buildReasoningFrame(ctx, ReasoningInput{Text: "extra"})
	if err != nil {
		t.Fatal(err)
	}
	delta, err := buildReasoningDelta(ctx, ReasoningInput{Text: "extra"})
	if err != nil {
		t.Fatal(err)
	}
	// The first cycle's message is the frame plus this cycle's delta; later
	// cycles send only the delta (the session already holds the frame).
	if !strings.HasPrefix(prompt, frame) {
		t.Fatalf("prompt must start with the AGENT_V2 frame")
	}
	if !strings.HasSuffix(strings.TrimSpace(prompt), strings.TrimSpace(delta)) {
		t.Fatalf("prompt must end with this cycle's delta")
	}
	if prompt != buildFramePlusDelta(t, ctx) {
		t.Fatalf("prompt must be frame + delta verbatim")
	}

	// Per-cycle values (task / world / runtime context) moved out of the frame:
	// the frame stays byte-identical for the session, so its placeholders become a
	// marker and the values travel in the delta.
	for _, perCycle := range []string{`"id": "t1"`, `"assets"`, `"additional_input"`} {
		if strings.Contains(frame, perCycle) {
			t.Fatalf("frame must not carry per-cycle value %s", perCycle)
		}
		if !strings.Contains(delta, perCycle) {
			t.Fatalf("delta missing per-cycle value %s\n%s", perCycle, delta)
		}
	}
	// The agent's own identity is the opposite: it does not change between cycles,
	// so it belongs to the frame — and the frame is where the agent is told who it
	// is (role, id, name, how it runs), not only where it is in the task.
	for _, perSession := range []string{`"agent-10001"`, `"role": "planner"`} {
		if !strings.Contains(frame, perSession) {
			t.Fatalf("frame missing the agent's own identity %s\n%s", perSession, frame)
		}
		if strings.Contains(delta, perSession) {
			t.Fatalf("delta repeats the agent's identity %s\n%s", perSession, delta)
		}
	}
	// What does change per cycle — which cycle this is — stays in the delta.
	if !strings.Contains(delta, `"cycle": 2`) {
		t.Fatalf("delta missing this cycle's step\n%s", delta)
	}
	if !strings.Contains(frame, reasoningDeltaMarker) {
		t.Fatalf("frame must mark where the per-cycle values come from")
	}
	if strings.Contains(delta, "# Autonomy Bootstrap Prompt") {
		t.Fatalf("delta must not repeat the frame:\n%s", delta)
	}

	for _, placeholder := range []string{
		"{{AGENT}}",
		"{{TASK}}", "{{GOAL_TYPE}}", "{{WORLD}}", "{{RUNTIME_CONTEXT}}",
		"{{COMPLETION_PRINCIPLES}}", "{{CONSTRUCTS}}", "{{CONTEXT_ENTITY}}",
		"{{CONSTRAINTS}}",
	} {
		if strings.Contains(prompt, placeholder) {
			t.Fatalf("%s was not replaced", placeholder)
		}
	}
	for _, legacy := range []string{
		"### Agent", "### Goal", "### World", "### Additional Input",
		"task_id: t1", "## Current Goal",
	} {
		if strings.Contains(prompt, legacy) {
			t.Fatalf("legacy appendix marker %q must not appear", legacy)
		}
	}

	for _, want := range []string{
		"## Agent",
		"## Capability Dispatch",
		"## Task",
		"## Context Entity",
		"## Goal",
		"## World",
		"## Runtime Context",
		"## Constraints",
		"## Constructs",
		"## Decision Cycle 2",
		`"id": "t1"`,
		`"assets"`,
		`"agent-10001"`,
		`"dev_feature"`,
		`"additional_input"`,
		`"text": "extra"`,
		`"constraints"`,
		`"deploy"`,
		`"name": "asset.change"`,
		`"name": "code_edit"`,
		`"completion_contracts"`,
		`"steps": [`,
		`"requirement": "the fact that must hold, in your words"`,
		`"expect": {"exists": true}`,
		`"type": "plan | done | blocked | need_input"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}

	var payload struct {
		Task map[string]any `json:"task"`
	}
	taskRaw := extractFencedJSON(delta, "## Decision Cycle")
	if err := json.Unmarshal([]byte(taskRaw), &payload); err != nil {
		t.Fatalf("delta json: %v\n%s", err, taskRaw)
	}
	if payload.Task["id"] != "t1" || payload.Task["domain"] != "server" {
		t.Fatalf("task=%v", payload.Task)
	}
}

// buildFramePlusDelta is the contract buildReasoningPrompt must satisfy: frame
// then delta, verbatim.
func buildFramePlusDelta(t *testing.T, ctx DecisionContext) string {
	t.Helper()
	frame, err := buildReasoningFrame(ctx, ReasoningInput{Text: "extra"})
	if err != nil {
		t.Fatal(err)
	}
	delta, err := buildReasoningDelta(ctx, ReasoningInput{Text: "extra"})
	if err != nil {
		t.Fatal(err)
	}
	return frame + "\n" + delta
}

func TestFormatContextEntitiesJSON(t *testing.T) {
	prevAuto := _autonomy
	mgr := NewContextContainerManager()
	mgr.ContextContainers["project-1"] = ContextContainer{
		ID:                   "project-1",
		Name:                 "Project One",
		Description:          "main project",
		DomainType:           TaskDomainSoftwareDevelopment,
		ContextContainerType: ContextContainerTypeProject,
	}
	mgr.ContextContainers["team-1"] = ContextContainer{
		ID:                   "team-1",
		Name:                 "Team One",
		Description:          "core team",
		DomainType:           TaskDomainSoftwareDevelopment,
		ContextContainerType: ContextContainerTypeTeam,
	}
	_autonomy = &Autonomy{ContextContainerManager: mgr}
	t.Cleanup(func() { _autonomy = prevAuto })

	task := &Task{
		ID: "t1",
		ContextRef: map[ContextContainerType]string{
			ContextContainerTypeProject: "project-1",
			ContextContainerTypeTeam:    "team-1",
		},
	}
	raw := formatContextEntitiesJSON(DecisionContext{Task: task})
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d want 2: %s", len(got), raw)
	}
	if got[0]["id"] != "project-1" || got[0]["type"] != "project" || got[0]["name"] != "Project One" || got[0]["domain"] != "software_development" {
		t.Fatalf("got[0]=%v", got[0])
	}
	if got[1]["id"] != "team-1" || got[1]["type"] != "team" {
		t.Fatalf("got[1]=%v", got[1])
	}

	if raw := formatContextEntitiesJSON(DecisionContext{}); string(raw) != "[]" {
		t.Fatalf("nil task raw=%s", raw)
	}

	emptyTask := &Task{ContextRef: map[ContextContainerType]string{ContextContainerTypeProject: ""}}
	if raw := formatContextEntitiesJSON(DecisionContext{Task: emptyTask}); string(raw) != "[]" {
		t.Fatalf("empty id raw=%s", raw)
	}
}

func TestFormatTaskJSONIncludesContextRef(t *testing.T) {
	task := &Task{
		ID:          "t1",
		Domain:      TaskDomainSoftwareDevelopment,
		Description: "d",
		Status:      "pending",
		GoalType:    GoalType_FEATURE,
		ContextRef: map[ContextContainerType]string{
			ContextContainerTypeProject: "project-1",
			ContextContainerTypeTeam:    "team-1",
		},
	}
	raw := formatTaskJSON(task)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	ref, ok := got["context_ref"].(map[string]any)
	if !ok {
		t.Fatalf("context_ref missing or wrong type: %s", raw)
	}
	if ref["project"] != "project-1" || ref["team"] != "team-1" {
		t.Fatalf("context_ref=%v", ref)
	}
}

func TestFormatRuntimeContextJSONIncludesContextContainers(t *testing.T) {
	prevAuto := _autonomy
	prevWorld := _world

	ccMgr := NewContextContainerManager()
	ccMgr.ContextContainers["project-1"] = ContextContainer{
		ID:                   "project-1",
		Name:                 "Project One",
		Description:          "main project",
		DomainType:           TaskDomainSoftwareDevelopment,
		ContextContainerType: ContextContainerTypeProject,
		ContextReferences:    []string{"ctx-1"},
		EntityReferences:     []string{"src-1"},
		AssetReferences:      []string{"asset-1"},
	}

	ceMgr := NewContextEntityManager()
	ceMgr.ContextEntities["ctx-1"] = ContextEntity{
		ID:                   "ctx-1",
		Name:                 "Repo Context",
		Description:          "context for repo",
		DomainType:           TaskDomainSoftwareDevelopment,
		ContextContainerType: ContextContainerTypeProject,
	}

	dem := NewDomainEntityManager()
	dem.DomainEntities["src-1"] = SourceCodeEntity{
		Meta:       Entity{ID: "src-1", Name: "source code", Description: "autonomy repo", CreatedAt: time.Now(), UpdatedAt: time.Now()},
		Repository: Repository{URL: "https://github.com/kaulie/autonomy", MainBranch: "main"},
	}

	am := NewAssetManager()
	am.Set("asset-1", Asset{ID: "asset-1", Kind: "repo", State: "healthy"})

	_autonomy = &Autonomy{
		ContextContainerManager: ccMgr,
		ContextEntityManager:    ceMgr,
		DomainEntityManager:     dem,
	}
	_world = &World{assetManager: am}
	t.Cleanup(func() {
		_autonomy = prevAuto
		_world = prevWorld
	})

	task := &Task{
		ID: "t1",
		ContextRef: map[ContextContainerType]string{
			ContextContainerTypeProject: "project-1",
		},
	}
	raw := formatRuntimeContextJSON(DecisionContext{Task: task}, ReasoningInput{})
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v :: %s", err, raw)
	}
	ctx, ok := got["context"].([]any)
	if !ok || len(ctx) != 1 {
		t.Fatalf("context=%v raw=%s", got["context"], raw)
	}
	first := ctx[0].(map[string]any)
	if first["id"] != "project-1" || first["type"] != "project" {
		t.Fatalf("first=%v", first)
	}
	ces := first["context_entities"].([]any)
	if len(ces) != 1 || ces[0].(map[string]any)["id"] != "ctx-1" {
		t.Fatalf("context_entities=%v", first["context_entities"])
	}
	es := first["entities"].([]any)
	if len(es) != 1 || es[0].(map[string]any)["id"] != "src-1" {
		t.Fatalf("entities=%v", first["entities"])
	}
	ent := es[0].(map[string]any)
	repo := ent["repository"].(map[string]any)
	if repo["url"] != "https://github.com/kaulie/autonomy" || repo["main_branch"] != "main" {
		t.Fatalf("repository=%v", repo)
	}
	if ent["created_at"] == nil || ent["updated_at"] == nil {
		t.Fatalf("created_at/updated_at missing: %v", ent)
	}
	as := first["assets"].([]any)
	if len(as) != 1 || as[0].(map[string]any)["id"] != "asset-1" {
		t.Fatalf("assets=%v", first["assets"])
	}
}

func TestBindEntityToContextContainerPersists(t *testing.T) {
	prevAuto := _autonomy
	mgr := NewContextContainerManager()
	_autonomy = &Autonomy{ContextContainerManager: mgr}
	t.Cleanup(func() { _autonomy = prevAuto })

	container := ContextContainer{ID: "project-1", ContextContainerType: ContextContainerTypeProject}
	if err := RegisterContextContainer(container); err != nil {
		t.Fatal(err)
	}
	if err := BindEntityToContextContainer(Entity{ID: "src-1"}, container); err != nil {
		t.Fatal(err)
	}
	got := mgr.ContextContainers["project-1"]
	if len(got.EntityReferences) != 1 || got.EntityReferences[0] != "src-1" {
		t.Fatalf("EntityReferences=%v", got.EntityReferences)
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
			if got := NormalizeReasonOutput(tc.raw); got != tc.want {
				t.Fatalf("NormalizeReasonOutput()=%q want %q", got, tc.want)
			}
		})
	}
}

func TestParseDecisionJSON(t *testing.T) {
	prevAuto := _autonomy
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{Assets: worldAssetMutator()})
	_autonomy = &Autonomy{CapabilityFactory: f}
	t.Cleanup(func() { _autonomy = prevAuto })

	cases := []struct {
		name    string
		text    string
		wantTyp string
		reason  string
		// actions are the capability names in execution order; nothingAction marks
		// a step the runtime cannot run (unknown capability).
		actions []string
		err     bool
	}{
		{
			name:    "plan change",
			text:    `{"type":"plan","reason":"mutate target","plan":{"steps":[{"capability":"asset.change","input":{"target":"1"}}]},"need":{}}`,
			wantTyp: "plan",
			reason:  "mutate target",
			actions: []string{"asset.change"},
		},
		{
			name:    "plan fenced",
			text:    "```json\n{\"type\":\"plan\",\"reason\":\"go\",\"plan\":{\"steps\":[{\"capability\":\"asset.change\",\"input\":{}}]},\"need\":{}}\n```",
			wantTyp: "plan",
			reason:  "go",
			actions: []string{"asset.change"},
		},
		{
			name:    "plan legacy array",
			text:    `{"type":"plan","reason":"legacy","plan":[{"capability":"asset.change","input":{}}],"need":{}}`,
			wantTyp: "plan",
			reason:  "legacy",
			actions: []string{"asset.change"},
		},
		{
			// A step the runtime does not have stays in the plan as a no-op: it must
			// not delete the steps around it.
			name:    "plan unknown capability keeps its place",
			text:    `{"type":"plan","reason":"unknown","plan":{"steps":[{"capability":"change","input":{}}]},"need":{}}`,
			wantTyp: "plan",
			reason:  "unknown",
			actions: []string{"nothing"},
		},
		{
			// The point of the whole change: a multi-step plan yields every action, in
			// order, instead of collapsing to the first one.
			name: "plan keeps every step",
			text: `{"type":"plan","reason":"chain","plan":{"steps":[` +
				`{"capability":"asset.change","input":{"target":"1"}},` +
				`{"capability":"code_edit","input":{"instruction":"x"}},` +
				`{"capability":"noop","input":{}},` +
				`{"capability":"missing","input":{}}]},"need":{}}`,
			wantTyp: "plan",
			reason:  "chain",
			actions: []string{"asset.change", "code_edit", "nothing"},
		},
		{
			name:    "done",
			text:    `{"type":"done","reason":"verified","plan":[],"need":{}}`,
			wantTyp: "done",
			reason:  "verified",
		},
		{
			name:    "blocked",
			text:    `{"type":"blocked","reason":"missing key","plan":[],"need":{"type":"permission","description":"api key"}}`,
			wantTyp: "blocked",
			reason:  "missing key",
		},
		{
			name: "invalid",
			text: "not json",
			err:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := parseDecision(tc.text)
			if tc.err {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if decision.Type != tc.wantTyp || decision.Reason != tc.reason {
				t.Fatalf("type=%q reason=%q, want %q/%q", decision.Type, decision.Reason, tc.wantTyp, tc.reason)
			}
			got := make([]string, 0, len(decision.Actions))
			for _, action := range decision.Actions {
				switch a := action.(type) {
				case CapabilityAction:
					got = append(got, a.Name)
				case NothingAction:
					got = append(got, "nothing")
				default:
					got = append(got, fmt.Sprintf("%T", action))
				}
			}
			if strings.Join(got, ",") != strings.Join(tc.actions, ",") {
				t.Fatalf("actions=%v, want %v", got, tc.actions)
			}
		})
	}
}

// TestParseDecisionRealPlannerPayload parses an answer in the shape the planner
// actually returns (reason_turns.raw_output, e.g. id=105): evidence items with
// ids, one plan step whose expected_effect is a string, a deliverable and a
// presentation. Those fields used to be dropped, and only the first plan step
// survived as an action.
func TestParseDecisionRealPlannerPayload(t *testing.T) {
	prevAuto := _autonomy
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{Assets: worldAssetMutator()})
	_autonomy = &Autonomy{CapabilityFactory: f}
	t.Cleanup(func() { _autonomy = prevAuto })

	text := `{
  "goal_type": "dev_feature",
  "completion_contracts": {"steps": ["locate the pipeline event names", "standardise them"]},
  "type": "plan",
  "reason": "delegate the code change to a worker",
  "evidence": [
    {"id": "E1", "source": "goal", "reference": "task-8 description", "fact": "tasks must be committed and pushed"},
    {"id": "E2", "source": "world", "reference": "src-1", "fact": "repository kaulie/agent-control-plane-deployment"}
  ],
  "plan": {
    "steps": [
      {
        "capability": "code_edit",
        "input": {"instruction": "standardise the pipeline event names"},
        "expected_effect": "the worker renames the events and opens a PR",
        "evidence_refs": ["E1", "E2"]
      }
    ]
  },
  "need": {},
  "deliverable": [
    {"type": "asset", "concrete_type": "Repository", "detail": {"main_branch": "main"}}
  ],
  "presentation": [
    {"type": "summary", "content": "delegated the rename to a worker"}
  ]
}`
	decision, err := parseDecision(text)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Type != "plan" || decision.Reason != "delegate the code change to a worker" {
		t.Fatalf("type=%q reason=%q", decision.Type, decision.Reason)
	}
	if len(decision.Actions) != 1 {
		t.Fatalf("actions=%d, want 1: %+v", len(decision.Actions), decision.Actions)
	}
	action, ok := decision.Actions[0].(CapabilityAction)
	if !ok {
		t.Fatalf("action type %T", decision.Actions[0])
	}
	if action.Name != "code_edit" || action.Inputs["instruction"].Literal != "standardise the pipeline event names" {
		t.Fatalf("action=%+v", action)
	}
	if action.ExpectedEffect != "the worker renames the events and opens a PR" {
		t.Fatalf("expected_effect=%q", action.ExpectedEffect)
	}
	if strings.Join(action.EvidenceRefs, ",") != "E1,E2" {
		t.Fatalf("evidence_refs=%v", action.EvidenceRefs)
	}
	if len(decision.Evidence) != 2 || decision.Evidence[0].ID != "E1" || decision.Evidence[0].Source != "goal" {
		t.Fatalf("evidence=%+v", decision.Evidence)
	}
	if decision.Need != (Need{}) {
		t.Fatalf("need=%+v, want empty", decision.Need)
	}
	if len(decision.Deliverables) != 1 || decision.Deliverables[0].Type != "asset" ||
		decision.Deliverables[0].ConcreteType != "Repository" || decision.Deliverables[0].Detail["main_branch"] != "main" {
		t.Fatalf("deliverables=%+v", decision.Deliverables)
	}
	if len(decision.Presentation) != 1 || decision.Presentation[0].Type != "summary" || decision.Presentation[0].Content == "" {
		t.Fatalf("presentation=%+v", decision.Presentation)
	}
}

// TestParseDecisionReadsStepNamesAndBindings: a plan step says what it is called and
// where each input comes from (AGENT_V2 §Plan Data Lineage), and a binding the
// runtime cannot read fails the plan instead of arriving as text.
func TestParseDecisionReadsStepNamesAndBindings(t *testing.T) {
	// The plan's capabilities have to be registered, or the steps become
	// NothingActions and there is nothing to assert about their inputs.
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{Assets: worldAssetMutator()})
	prev := _autonomy
	_autonomy = &Autonomy{CapabilityFactory: f}
	t.Cleanup(func() { _autonomy = prev })

	text := "```json\n" + `{
  "goal_type": "dev_feature",
  "type": "plan",
  "reason": "land what the edit produced",
  "plan": {
    "steps": [
      {
        "name": "edit",
        "capability": "code_edit",
        "inputs": {"instruction": "rename the events", "task_id": {"source": "world_model:asset.repo.kind"}},
        "evidence_refs": ["E1"]
      },
      {
        "name": "land",
        "capability": "pull_request.review",
        "inputs": {"pr": {"source": "step:edit.output.pr_url"}, "method": "squash"}
      }
    ]
  }
}` + "\n```"
	decision, err := parseDecision(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(decision.Actions) != 2 {
		t.Fatalf("actions=%d, want 2", len(decision.Actions))
	}
	edit, _ := decision.Actions[0].(CapabilityAction)
	land, _ := decision.Actions[1].(CapabilityAction)
	if edit.StepName != "edit" || land.StepName != "land" {
		t.Fatalf("step names=%q/%q, want edit/land", edit.StepName, land.StepName)
	}
	if edit.Inputs["instruction"].Literal != "rename the events" || edit.Inputs["instruction"].IsBinding() {
		t.Fatalf("instruction=%+v, want a literal", edit.Inputs["instruction"])
	}
	if got := edit.Inputs["task_id"].Source; got != "world_model:asset.repo.kind" {
		t.Fatalf("task_id source=%q, want the binding", got)
	}
	if got := land.Inputs["pr"].Source; got != "step:edit.output.pr_url" {
		t.Fatalf("pr source=%q, want the binding", got)
	}
	if land.Inputs["method"].Literal != "squash" {
		t.Fatalf("method=%+v, want the literal", land.Inputs["method"])
	}

	// An input that is neither a value nor a binding is a plan the runtime refuses to
	// read — and a scalar is a value (a model writing `300` means 300).
	broken := "```json\n" + `{"type":"plan","plan":{"steps":[{"capability":"code_edit","inputs":{"instruction":{"spec":"rename"}}}]}}` + "\n```"
	if _, err := parseDecision(broken); err == nil || !strings.Contains(err.Error(), "names neither a source nor a value") {
		t.Fatalf("err=%v, want the malformed input refused", err)
	}

	scalar := "```json\n" + `{"type":"plan","plan":{"steps":[{"name":"monitor","capability":"deployment.monitor","inputs":{"deployment":"p-1","timeout":300}}]}}` + "\n```"
	parsed, err := parseDecision(scalar)
	if err != nil {
		t.Fatalf("a numeric input was refused: %v", err)
	}
	monitor, _ := parsed.Actions[0].(CapabilityAction)
	if monitor.Inputs["timeout"].Literal != "300" {
		t.Fatalf("timeout=%+v, want the value as text", monitor.Inputs["timeout"])
	}
}
