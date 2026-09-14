// Package broker defines the generic agent-acquisition hooks shared by
// capabilities and the autonomy runtime. It is capability-agnostic and must
// not import any concrete capability package.
package broker

import "context"

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
	// TaskID, when non-empty, is recorded on reason_turns for prompts made by
	// the acquired agent.
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
