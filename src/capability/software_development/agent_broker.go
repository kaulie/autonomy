package software_development

import "context"

// AcquireAgentOpts tells Runtime how to register and back an agent for a capability.
type AcquireAgentOpts struct {
	// Purpose is a short label included in the registered agent id (e.g. "code_edit").
	Purpose string
	// Workspace is the AGENT_WORKSPACE / Cursor CWD; required for cursor backends.
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
	Prompt(ctx context.Context, prompt string) (string, error)
	Release(ctx context.Context) error
}

// AgentBroker is how capabilities obtain agent control via Runtime.
type AgentBroker interface {
	AcquireAgent(ctx context.Context, opts AcquireAgentOpts) (AgentSession, error)
}
