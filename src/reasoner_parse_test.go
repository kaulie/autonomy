package autonomy

import (
	"encoding/json"
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
		ID: "t1", Description: "d",
		Domain:   TaskDomainServer,
		GoalType: GoalType_FEATURE,
		Status:   "pending",
	}
	agent := &Agent{
		ID: 10001, Name: "agent-10001", Lifecycle: AgentLifecycleEphemeral,
		Backend: AgentBackendCursor, Workspace: "/tmp/ws/",
	}
	ctx := DecisionContext{Task: task, Agent: agent, Step: 2}
	prompt, err := buildReasoningPrompt(ctx, ReasoningInput{Text: "extra"})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := loadAgentPolicy()
	if err != nil {
		t.Fatal(err)
	}
	filledPolicy := applyPolicyPlaceholders(raw, policyPlaceholders(ctx, ReasoningInput{Text: "extra"}))
	if !strings.HasPrefix(prompt, filledPolicy) {
		t.Fatalf("prompt must start with full AGENT_V2.md (placeholders only)")
	}
	for _, placeholder := range []string{
		"{{TASK}}", "{{GOAL_TYPE}}", "{{WORLD}}", "{{RUNTIME_CONTEXT}}",
		"{{COMPLETION_PRINCIPLES}}", "{{CONSTRUCTS}}", "{{CONTEXT_ENTITY}}",
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
		"## Task",
		"## Context Entity",
		"## Goal",
		"## World",
		"## Runtime Context",
		"## Constructs",
		`"id": "t1"`,
		`"assets"`,
		`"agent-10001"`,
		`"dev_feature"`,
		`"additional_input"`,
		`"text": "extra"`,
		`"name": "asset.change"`,
		`"name": "code_edit"`,
		`"completion_contracts"`,
		`"steps": []`,
		`"type": "plan | done | blocked | need_input"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}

	taskRaw := extractFencedJSON(prompt, "## Task")
	var taskJSON map[string]any
	if err := json.Unmarshal([]byte(taskRaw), &taskJSON); err != nil {
		t.Fatalf("task json: %v\n%s", err, taskRaw)
	}
	if taskJSON["id"] != "t1" || taskJSON["domain"] != "server" {
		t.Fatalf("task=%v", taskJSON)
	}
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
			if got := normalizeReasonOutput(tc.raw); got != tc.want {
				t.Fatalf("normalizeReasonOutput()=%q want %q", got, tc.want)
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
		name   string
		text   string
		noop   bool
		reason string
		err    bool
	}{
		{
			name:   "plan change",
			text:   `{"type":"plan","reason":"mutate target","plan":{"steps":[{"capability":"asset.change","input":{"target":"1"}}]},"need":{}}`,
			noop:   false,
			reason: "mutate target",
		},
		{
			name:   "plan fenced",
			text:   "```json\n{\"type\":\"plan\",\"reason\":\"go\",\"plan\":{\"steps\":[{\"capability\":\"asset.change\",\"input\":{}}]},\"need\":{}}\n```",
			noop:   false,
			reason: "go",
		},
		{
			name:   "plan legacy array",
			text:   `{"type":"plan","reason":"legacy","plan":[{"capability":"asset.change","input":{}}],"need":{}}`,
			noop:   false,
			reason: "legacy",
		},
		{
			name:   "plan unknown capability",
			text:   `{"type":"plan","reason":"unknown","plan":{"steps":[{"capability":"change","input":{}}]},"need":{}}`,
			noop:   true,
			reason: "unknown",
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
