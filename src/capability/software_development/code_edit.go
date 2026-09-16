package software_development

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/kaulie/autonomy/src/capability/broker"
	"github.com/kaulie/autonomy/src/capability/spec"
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

// Inputs / Outputs declare the capability's call signature for {{CONSTRUCTS}}.
func (CodeEdit) Inputs() []spec.Field {
	return []spec.Field{
		{Name: "instruction", Aliases: []string{"goal"}, Required: true, Description: "the goal or requirement to hand the coding agent; the runtime fills it from the task description when the step leaves it out"},
		{Name: "task_id", Description: "the task the work belongs to (the runtime fills it); the worker's reason turns and current_task_id carry it"},
	}
}

func (CodeEdit) Outputs() []spec.Field {
	return []spec.Field{
		{Name: "status", Description: `"ok" when the delegation ran`},
		{Name: "summary", Description: "the worker's own report: what it changed, how it verified it, and what it landed"},
		{Name: "pr_url", Description: "the pull request the worker opened, when its report names one — the URL to hand to pull_request.review; empty when the report names none"},
		{Name: "workspace", Description: "the worker's sandbox — its own, never the caller's"},
		{Name: "provider", Description: "the backend that ran the worker (cursor / cline)"},
		{Name: "agent_id", Description: "the worker agent's name, as recorded in agents"},
		{Name: "instruction", Description: "the instruction the worker was actually given"},
	}
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
		"pr_url":      pullRequestIn(summary),
		"workspace":   workspace,
		"provider":    Provider,
		"agent_id":    sess.ID(),
		"instruction": instruction,
	}, nil
}

// pullRequestURLPattern finds a pull request URL as a worker reports it. Only the
// URL form counts: it names the pull request outright (its repository included),
// while a bare number or an owner/name#43 in prose would have to be guessed
// against whichever repository the worker happened to be in.
var pullRequestURLPattern = regexp.MustCompile(`https?://[^\s<>()\[\]"'#]+/pulls?/[0-9]+`)

// pullRequestIn reads the pull request out of a worker's report, if it named one:
// the URL the worker was asked to report is a fact the next step can be given
// (pull_request.review takes it as "pr"), and reading it here is what makes it an
// output instead of a sentence someone has to re-read.
//
// A report that names no pull request yields nothing: not every delegation opens
// one, and a guess about which pull request was meant is exactly what this must
// not produce. Every candidate URL is checked with the same parser that capability
// uses, so what comes back is a pull request that can actually be read.
func pullRequestIn(report string) string {
	for _, candidate := range pullRequestURLPattern.FindAllString(report, -1) {
		if ref, err := parsePullReference(candidate); err == nil && ref.repo != "" {
			return candidate
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
