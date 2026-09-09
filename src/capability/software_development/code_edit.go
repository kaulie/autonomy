package software_development

import (
	"context"
	"fmt"
	"strings"

	"github.com/kaulie/autonomy/src/capability/broker"
)

const (
	Name     = "code_edit"
	Domain   = "software_development"
	Provider = "cursor"
)

// CodeEdit edits code in the agent workspace via a Runtime-acquired Cursor-backed agent.
type CodeEdit struct {
	Agents broker.AgentBroker
}

func (CodeEdit) Name() string { return Name }

func (CodeEdit) Domain() string { return Domain }

func (CodeEdit) Provider() string { return Provider }

func (CodeEdit) Description() string {
	return `edit code in the agent workspace to implement a feature. input: {"instruction":"<what to build>"} (or "goal"); optional "workspace"/"cwd" overrides Agent.Workspace`
}

func (c CodeEdit) Run(in map[string]string) (map[string]string, error) {
	instruction := firstNonEmpty(in["instruction"], in["goal"])
	if instruction == "" {
		return nil, fmt.Errorf("code_edit: missing instruction/goal")
	}
	workspace := firstNonEmpty(in["workspace"], in["cwd"])
	if workspace == "" {
		return nil, fmt.Errorf("code_edit: missing workspace (set Agent.Workspace or pass workspace/cwd)")
	}
	taskID := firstNonEmpty(in["task_id"])
	if c.Agents == nil {
		return nil, fmt.Errorf("code_edit: agent broker not configured (use Runtime.AcquireAgent)")
	}

	ctx := context.Background()
	sess, err := c.Agents.AcquireAgent(ctx, broker.AcquireAgentOpts{
		Purpose:   Name,
		Workspace: workspace,
		Backend:   Provider, // cursor
		TaskID:    taskID,
	})
	if err != nil {
		return nil, fmt.Errorf("code_edit: acquire agent: %w", err)
	}
	defer func() { _ = sess.Release(ctx) }()

	prompt := fmt.Sprintf(`You are editing a software project at workspace:
%s

Implement the following feature by modifying the codebase as needed. Make only necessary changes. Prefer small, focused edits.

Feature / instruction:
%s

When done, briefly summarize which files you changed and why.`, workspace, instruction)

	summary, err := sess.Prompt(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("code_edit: %w", err)
	}
	return map[string]string{
		"status":      "ok",
		"summary":     summary,
		"workspace":   workspace,
		"provider":    Provider,
		"agent_id":    sess.ID(),
		"instruction": instruction,
	}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
