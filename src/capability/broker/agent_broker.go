// Package broker defines the generic agent-acquisition hooks shared by
// capabilities and the autonomy runtime. It is capability-agnostic and must
// not import any concrete capability package.
package broker

import (
	"context"
	"strings"
)

// AcquireAgentOpts tells Runtime how to register and back an agent for a capability.
type AcquireAgentOpts struct {
	// Purpose is a short label describing why the agent is acquired.
	Purpose string
	// Workspace overrides the agent's own AGENT_WORKSPACE. Leave it empty to let
	// the runtime give the agent its own sandbox: a delegated sub-task must run in
	// the worker's workspace, never in the delegating agent's (see code_edit).
	Workspace string
	// Model overrides AUTONOMY_LLM_MODEL when non-empty.
	Model string
	// TaskID, when non-empty, is the task the acquired agent works on: it is
	// recorded as the agent's current_task_id and on every reason_turns row the
	// agent produces. A delegated worker carries the delegating agent's task id.
	TaskID string
	// Backend is "cursor" (default) or "local".
	Backend string
}

// AgentSession is control of one autonomy-registered agent (backend may be Cursor).
type AgentSession interface {
	ID() string
	// Workspace is where this agent runs (the agent's own AGENT_WORKSPACE unless
	// the acquirer asked for another one), so a capability can tell the agent and
	// its caller which workspace the work happens in.
	Workspace() string
	Prompt(ctx context.Context, prompt string) (string, error)
	Release(ctx context.Context) error
}

// AgentBroker is how capabilities obtain agent control via Runtime.
type AgentBroker interface {
	AcquireAgent(ctx context.Context, opts AcquireAgentOpts) (AgentSession, error)
}

// WorkerPromptContext is implemented by an AgentSession whose host can describe
// the runtime context of the delegation it was acquired for: the World, Runtime
// Context, Constraints, Constructs and Completion Principles of the cycle that
// delegated the sub-task, keyed by the placeholder names a worker prompt may use
// (see WorkerFramePlaceholders — the vocabulary of src/agent_policy/AGENT_V2.md,
// see src/agent_policy/CODE_EDIT.md).
//
// It is optional on purpose: a session whose host has no runtime to describe (a
// test double, a host without a World) simply does not implement it, and the
// prompt renders those sections without them.
type WorkerPromptContext interface {
	WorkerPlaceholders() map[string]string
}

// WorkerFramePlaceholders is the frame vocabulary a delegated worker prompt may
// use — the same placeholder names the planner's policy is rendered from
// (src/agent_policy/AGENT_V2.md, see promptPlaceholders): the agent's own
// identity, the Task the work belongs to, the Context Entity that anchors it, its
// Goal Type, the World, the Runtime Context of the delegating cycle, that goal
// type's Completion Principles, the Constraints the runtime holds the agent to,
// and the Constructs the runtime can already do the work with.
//
// A capability that needs an agent renders this frame into that agent's prompt
// (RenderWorkerPrompt), so the agent works against the runtime that delegated to
// it instead of guessing at the world it is working in.
var WorkerFramePlaceholders = []string{
	"{{AGENT}}",
	"{{TASK}}",
	"{{CONTEXT_ENTITY}}",
	"{{GOAL_TYPE}}",
	"{{WORLD}}",
	"{{RUNTIME_CONTEXT}}",
	"{{COMPLETION_PRINCIPLES}}",
	"{{CONSTRAINTS}}",
	"{{CONSTRUCTS}}",
}

// WorkerFrameMissingValue is what a frame placeholder the host has no value for
// renders as: the prompt reads as a gap the runtime left, instead of reaching the
// agent as a raw {{NAME}}.
const WorkerFrameMissingValue = "(not provided by this runtime)"

// WorkerFrame asks an acquired session's host for the frame values of the
// delegation it was acquired for (WorkerPromptContext). It is nil for a session
// whose host cannot describe a runtime — a test double, a host without a World —
// and the prompt then renders the frame as missing.
func WorkerFrame(sess AgentSession) map[string]string {
	if sess == nil {
		return nil
	}
	provider, ok := sess.(WorkerPromptContext)
	if !ok {
		return nil
	}
	return provider.WorkerPlaceholders()
}

// RenderWorkerPrompt fills a worker prompt template for one agent. `own` are the
// values the capability owns — the agent's own workspace, the goal it was
// delegated, the URLs it is asked to look at — and they always win; `frame` are
// the ones the host rendered for this delegation (WorkerFrame).
//
// A frame placeholder nobody filled renders as WorkerFrameMissingValue, so the
// prompt reads as a gap rather than leaking a placeholder to the agent. A
// placeholder outside the vocabulary is left as-is, so a template typo stays
// visible in the trace instead of silently dropping text.
func RenderWorkerPrompt(tmpl string, own, frame map[string]string) string {
	owes := func(k string) bool { return strings.TrimSpace(own[k]) != "" }
	out := tmpl
	for k, v := range own {
		if !owes(k) {
			continue
		}
		out = strings.ReplaceAll(out, k, v)
	}
	for k, v := range frame {
		if owes(k) || strings.TrimSpace(v) == "" {
			continue
		}
		out = strings.ReplaceAll(out, k, v)
	}
	for _, k := range WorkerFramePlaceholders {
		if owes(k) || strings.TrimSpace(frame[k]) != "" {
			continue
		}
		out = strings.ReplaceAll(out, k, WorkerFrameMissingValue)
	}
	return out
}
