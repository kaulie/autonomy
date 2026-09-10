package autonomy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const defaultAgentPolicyRel = "src/agent_policy/AGENT_V2.md"

// policyPlaceholders are substituted into AGENT_V2.md; the rest of the file is passed through unchanged.
func policyPlaceholders(ctx DecisionContext, input ReasoningInput) map[string]string {
	constructs := "[]"
	if _autonomy != nil && _autonomy.CapabilityFactory != nil {
		constructs = _autonomy.CapabilityFactory.FormatConstructs()
	}
	var goalType GoalType = GoalType_FEATURE
	if ctx.Task != nil && strings.TrimSpace(string(ctx.Task.GoalType)) != "" {
		goalType = ctx.Task.GoalType
	}
	return map[string]string{
		"{{TASK}}":                  fencedJSON(formatTaskJSON(ctx.Task)),
		"{{CONTEXT_ENTITY}}":        fencedJSON(formatContextEntitiesJSON(ctx)),
		"{{GOAL_TYPE}}":             string(goalType),
		"{{WORLD}}":                 fencedJSON(formatWorldJSON(ctx)),
		"{{RUNTIME_CONTEXT}}":       fencedJSON(formatRuntimeContextJSON(ctx, input)),
		"{{COMPLETION_PRINCIPLES}}": CompletionPrinciplesFor(goalType),
		"{{CONSTRUCTS}}":            fencedJSON([]byte(constructs)),
	}
}

func applyPolicyPlaceholders(policy string, values map[string]string) string {
	for k, v := range values {
		policy = strings.ReplaceAll(policy, k, v)
	}
	return policy
}

// projectRoot returns PROJECT_ROOT; empty or unset is an error for callers that need it.
func projectRoot() (string, error) {
	root := strings.TrimSpace(os.Getenv("PROJECT_ROOT"))
	if root == "" {
		return "", fmt.Errorf("PROJECT_ROOT is required")
	}
	return root, nil
}

// loadAgentPolicy reads $PROJECT_ROOT/src/agent_policy/AGENT_V2.md at runtime.
func loadAgentPolicy() (string, error) {
	root, err := projectRoot()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, defaultAgentPolicyRel)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read agent policy %s: %w", path, err)
	}
	return string(b), nil
}

// buildReasoningPrompt sends AGENT_V2.md through to the model, filling every
// {{...}} placeholder (Task / GoalType / World / Runtime Context / Completion
// Principles / Constructs). No extra appendix is appended; the policy already
// contains the ## Runtime Context section.
func buildReasoningPrompt(ctx DecisionContext, input ReasoningInput) (string, error) {
	raw, err := loadAgentPolicy()
	if err != nil {
		return "", err
	}
	policy := applyPolicyPlaceholders(raw, policyPlaceholders(ctx, input))

	var b strings.Builder
	b.WriteString(policy)
	if !strings.HasSuffix(policy, "\n") {
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func fencedJSON(raw []byte) string {
	return "```json\n" + string(raw) + "\n```"
}

func mustJSON(v any) []byte {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte("null")
	}
	return raw
}

func formatAgentContextJSON(ctx DecisionContext) []byte {
	if ctx.Agent == nil {
		return []byte("null")
	}
	lifecycle := string(ctx.Agent.Lifecycle)
	if lifecycle == "" {
		lifecycle = string(AgentLifecycleEphemeral)
	}
	backend := string(ctx.Agent.Backend)
	if backend == "" {
		backend = string(AgentBackendLocal)
	}
	return mustJSON(map[string]any{
		"id":        ctx.Agent.ID,
		"name":      ctx.Agent.Name,
		"lifecycle": lifecycle,
		"backend":   backend,
		"workspace": ctx.Agent.Workspace,
		"step":      ctx.Step,
	})
}

func formatTaskJSON(task *Task) []byte {
	if task == nil {
		return []byte("null")
	}
	return mustJSON(map[string]any{
		"id":          task.ID,
		"domain":      string(task.Domain),
		"description": task.Description,
		"status":      task.Status,
		"goal_type":   string(task.GoalType),
	})
}

func formatRuntimeContextJSON(ctx DecisionContext, input ReasoningInput) []byte {
	m := map[string]any{}
	if ctx.Agent != nil {
		m["agent"] = json.RawMessage(formatAgentContextJSON(ctx))
	}
	if text := strings.TrimSpace(input.Text); text != "" {
		m["additional_input"] = map[string]string{"text": text}
	}
	return mustJSON(m)
}

func formatWorldJSON(ctx DecisionContext) []byte {
	type assetJSON struct {
		ID    string `json:"id"`
		Kind  string `json:"kind"`
		State string `json:"state"`
	}
	assets := []assetJSON{}
	if _world != nil && _world.assetManager != nil {
		for _, a := range _world.assetManager.List() {
			assets = append(assets, assetJSON{ID: a.ID, Kind: a.Kind, State: a.State})
		}
	}
	return mustJSON(map[string]any{
		"assets": assets,
	})
}

func formatContextEntitiesJSON(ctx DecisionContext) []byte {
	type contextEntityJSON struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Type        string `json:"type"`
		Domain      string `json:"domain"`
	}
	entries := []contextEntityJSON{}
	if ctx.Task != nil && ctx.Task.ContextRef != nil {
		for ctype, id := range ctx.Task.ContextRef {
			entry := contextEntityJSON{ID: id, Type: string(ctype)}
			if _autonomy != nil && _autonomy.ContextEntityManager != nil {
				if e, ok := _autonomy.ContextEntityManager.ContextEntities[id]; ok {
					entry.Name = e.Name
					entry.Description = e.Description
					entry.Domain = string(e.DomainType)
				}
			}
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return mustJSON(entries)
}

type agentDecisionJSON struct {
	Type   string          `json:"type"`
	Reason string          `json:"reason"`
	Plan   json.RawMessage `json:"plan"`
	Need   json.RawMessage `json:"need"`
}

type planStepJSON struct {
	Capability string          `json:"capability"`
	Input      json.RawMessage `json:"input"`
}

// parsePlanSteps accepts both legacy "plan": [...] and the AGENT_V2
// "plan": {"steps": [...]} shapes.
func parsePlanSteps(raw json.RawMessage) []planStepJSON {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var steps []planStepJSON
	if err := json.Unmarshal(raw, &steps); err == nil {
		return steps
	}
	var obj struct {
		Steps []planStepJSON `json:"steps"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Steps
	}
	return nil
}

// parseDecision maps AGENT_V2 JSON into a Decision reason + Action.
// Registered capabilities become CapabilityAction; unknown plan steps become NothingAction.
func parseDecision(text string) (string, Action, error) {
	raw := extractJSONObject(text)
	if raw == "" {
		return "", nil, fmt.Errorf("no JSON object in model response")
	}
	var d agentDecisionJSON
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return "", nil, fmt.Errorf("parse agent decision JSON: %w", err)
	}
	typ := strings.ToLower(strings.TrimSpace(d.Type))
	reason := strings.TrimSpace(d.Reason)
	if reason == "" {
		reason = typ
	}
	switch typ {
	case "plan":
		for _, step := range parsePlanSteps(d.Plan) {
			capName := strings.ToLower(strings.TrimSpace(step.Capability))
			switch capName {
			case "noop", "nothing", "none", "":
				continue
			}
			if f := activeCapabilityFactory(); f != nil && f.Has(capName) {
				return reason, CapabilityAction{Name: capName, Input: planInputMap(step.Input)}, nil
			}
			return reason, NothingAction{}, nil
		}
		return reason, NothingAction{}, nil
	case "done", "blocked", "need_input":
		return reason, NothingAction{}, nil
	default:
		return "", nil, fmt.Errorf("unknown decision type %q", d.Type)
	}
}

func planInputMap(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return out
	}
	for k, v := range m {
		out[k] = fmt.Sprint(v)
	}
	return out
}

func extractJSONObject(text string) string {
	s := stripCodeFences(text)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}

// stripCodeFences removes a markdown code fence (e.g. ```json ... ```) around
// text so persisted outputs stay as plain text.
func stripCodeFences(text string) string {
	s := strings.TrimSpace(text)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "```"))
	if len(s) >= 4 && strings.EqualFold(s[:4], "json") {
		s = strings.TrimSpace(s[4:])
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// normalizeReasonOutput returns the structured, machine-readable form of a
// model's raw output. It prefers the embedded JSON object (the decision
// envelope produced by the reasoner) and falls back to the fence-stripped text
// when no JSON object is present (e.g. free-form code_edit results).
func normalizeReasonOutput(raw string) string {
	if j := extractJSONObject(raw); j != "" {
		return j
	}
	return stripCodeFences(raw)
}
