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
	return `delegate a software-development task to a coding agent, which runs in its own agent workspace. input: {"instruction":"<goal or requirement>"} (or "goal"). A "workspace"/"cwd" input is ignored: the worker never runs in the delegating agent's workspace`
}

func (c CodeEdit) Run(in map[string]string) (map[string]string, error) {
	instruction := firstNonEmpty(in["instruction"], in["goal"])
	if instruction == "" {
		return nil, fmt.Errorf("code_edit: missing instruction/goal")
	}
	taskID := firstNonEmpty(in["task_id"])
	if c.Agents == nil {
		return nil, fmt.Errorf("code_edit: agent broker not configured (use Runtime.AcquireAgent)")
	}

	ctx := context.Background()
	// The prompt comes from $PROJECT_ROOT before anything is acquired, so a
	// missing template fails the delegation instead of creating an agent that
	// cannot be prompted.
	tmpl, err := readPromptTemplate()
	if err != nil {
		return nil, fmt.Errorf("code_edit: %w", err)
	}
	// No Workspace: the broker gives the worker its own AGENT_WORKSPACE. Passing
	// the caller's (the delegating agent's) workspace here is what used to run the
	// worker inside the caller's sandbox.
	sess, err := c.Agents.AcquireAgent(ctx, broker.AcquireAgentOpts{
		Purpose: Name,
		Backend: Provider, // the host's default LLM backend (cursor / cline / …)
		TaskID:  taskID,
	})
	if err != nil {
		return nil, fmt.Errorf("code_edit: acquire agent: %w", err)
	}
	defer func() { _ = sess.Release(ctx) }()

	// The workspace is the worker's own; rendering it into the prompt is what the
	// worker treats as binding (see src/agent_policy/CODE_EDIT.md).
	workspace := sess.Workspace()
	// The prompt may also carry the frame vocabulary of the planner policy (World
	// / Runtime Context / Completion / Completion Principles / Constraints /
	// Constructs): the host that acquired this session renders those values for
	// this worker (see broker.WorkerFrame), so the worker sees the runtime it is
	// working for rather than guessing at it.
	prompt := renderPrompt(tmpl, workspace, instruction, broker.WorkerFrame(sess))

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
