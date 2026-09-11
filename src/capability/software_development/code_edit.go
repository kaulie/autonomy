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
	return `autonomously implement a coding task in the agent workspace. input: {"instruction":"<goal or requirement>"} (or "goal"), "workspace" (or "cwd") required`
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

	prompt := fmt.Sprintf(`You are an autonomous software engineer working in this workspace:

%s

Goal:

%s

Own the task end-to-end: understand the requirement, explore the codebase, choose the implementation approach, make the changes, and verify your work. Treat the Goal as the objective rather than a step-by-step specification. When you are done, give a concise summary of what you changed and why.`, workspace, instruction)

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
