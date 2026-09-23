package autonomy

import (
	"path/filepath"
)

// DefaultAgentWorkspaceRoot is the sandbox root for per-agent working directories.
// AGENT_WORKSPACE = {root}/{agent_name}/
const DefaultAgentWorkspaceRoot = "/Users/gaolei/agent-workspace-sandbox"

// AgentWorkspacePath returns AGENT_WORKSPACE for an agent name/id, under the runtime's default
// root.
func AgentWorkspacePath(agentName string) string {
	return AgentWorkspacePathIn(DefaultAgentWorkspaceRoot, agentName)
}

// AgentWorkspacePathIn is AgentWorkspacePath under a specific root: an account's own root, when
// the agent runs on one (src/agent_account.go). The trailing separator is what makes it a
// directory to work *inside*.
func AgentWorkspacePathIn(root, agentName string) string {
	return filepath.Join(root, agentName) + string(filepath.Separator)
}
