package autonomy

import (
	"path/filepath"
)

// DefaultAgentWorkspaceRoot is the sandbox root for per-agent working directories.
// AGENT_WORKSPACE = {root}/{agent_name}/
const DefaultAgentWorkspaceRoot = "/Users/gaolei/agent-workspace-sandbox"

// AgentWorkspacePath returns AGENT_WORKSPACE for an agent name/id.
func AgentWorkspacePath(agentName string) string {
	return filepath.Join(DefaultAgentWorkspaceRoot, agentName) + string(filepath.Separator)
}
