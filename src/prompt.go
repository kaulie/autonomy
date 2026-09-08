package autonomy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultAgentPolicyRel = "src/agent_policy/AGENT_V1.md"

// policyPlaceholders are substituted into AGENT_V1.md; the rest of the file is passed through unchanged.
func policyPlaceholders() map[string]string {
	constructs := "(none)"
	if _autonomy != nil && _autonomy.CapabilityFactory != nil {
		constructs = _autonomy.CapabilityFactory.FormatConstructs()
	}
	return map[string]string{
		"{{CONSTRUCTS}}": constructs,
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

// loadAgentPolicy reads $PROJECT_ROOT/src/agent_policy/AGENT_V1.md at runtime.
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

// buildReasoningPrompt sends AGENT_V1.md through to the model (placeholders only),
// then appends runtime Goal / World / extra input as an appendix.
func buildReasoningPrompt(ctx DecisionContext, input ReasoningInput) (string, error) {
	raw, err := loadAgentPolicy()
	if err != nil {
		return "", err
	}
	policy := applyPolicyPlaceholders(raw, policyPlaceholders())

	var b strings.Builder
	b.WriteString(policy)
	if !strings.HasSuffix(policy, "\n") {
		b.WriteByte('\n')
	}

	b.WriteString("\n## Current Goal\n\n")
	if ctx.Task != nil {
		fmt.Fprintf(&b, "- task_id: %s\n", ctx.Task.ID)
		fmt.Fprintf(&b, "- goal: %s\n", ctx.Task.Goal)
		fmt.Fprintf(&b, "- domain: %s\n", ctx.Task.Domain)
		fmt.Fprintf(&b, "- description: %s\n", ctx.Task.Description)
		fmt.Fprintf(&b, "- target: %s\n", ctx.Task.Target)
		fmt.Fprintf(&b, "- expected_state: %s\n", ctx.Task.Contract.ExpectedState)
		fmt.Fprintf(&b, "- status: %s\n", ctx.Task.Status)
	} else {
		b.WriteString("(no task)\n")
	}

	b.WriteString("\n## Current World\n\n")
	b.WriteString(formatWorldSnapshot(ctx))

	if strings.TrimSpace(input.Text) != "" {
		b.WriteString("\n## Additional Input\n\n")
		b.WriteString(strings.TrimSpace(input.Text))
		b.WriteString("\n")
	}

	return b.String(), nil
}

func formatWorldSnapshot(ctx DecisionContext) string {
	var assets []Asset
	if _world != nil && _world.assetManager != nil {
		assets = _world.assetManager.List()
	}
	if len(assets) == 0 {
		return "(empty)\n"
	}
	var b strings.Builder
	for _, a := range assets {
		fmt.Fprintf(&b, "- id=%s kind=%s state=%s\n", a.ID, a.Kind, a.State)
	}
	if ctx.Task != nil && ctx.Task.Target != "" {
		fmt.Fprintf(&b, "focus_target: %s\n", ctx.Task.Target)
	}
	return b.String()
}

type agentDecisionJSON struct {
	Type   string          `json:"type"`
	Reason string          `json:"reason"`
	Plan   []planStepJSON  `json:"plan"`
	Need   json.RawMessage `json:"need"`
}

type planStepJSON struct {
	Capability string          `json:"capability"`
	Input      json.RawMessage `json:"input"`
}

// parseDecision maps AGENT_V1 JSON into a Decision reason + Action.
// Executable mapping for v1: plan with asset.change → SimpleAction; otherwise NothingAction.
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
		for _, step := range d.Plan {
			capName := strings.ToLower(strings.TrimSpace(step.Capability))
			switch capName {
			case "asset.change", "change":
				return reason, SimpleAction{}, nil
			case "noop", "nothing", "none", "":
				continue
			default:
				return reason, NothingAction{}, nil
			}
		}
		return reason, NothingAction{}, nil
	case "done", "blocked", "need_input":
		return reason, NothingAction{}, nil
	default:
		return "", nil, fmt.Errorf("unknown decision type %q", d.Type)
	}
}

func extractJSONObject(text string) string {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSpace(s)
		if len(s) >= 4 && strings.EqualFold(s[:4], "json") {
			s = strings.TrimSpace(s[4:])
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}
