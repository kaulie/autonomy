package cline

import (
	"strings"

	"github.com/kaulie/autonomy/src/bridgesdk"
	"github.com/kaulie/autonomy/src/llmbackend"
)

// clineMCPEnv is the environment a Cline bridge process needs when the runtime
// has a server list. The SDK reads CLINE_MCP_SETTINGS_PATH when it starts, so
// the variable has to be on the child before node is exec'd. An empty list
// sets nothing and leaves the user's own Cline settings in place.
func clineMCPEnv() ([]string, error) {
	servers, err := llmbackend.MCPServers()
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, nil
	}
	body, err := llmbackend.ClineMCPSettings(servers)
	if err != nil {
		return nil, err
	}
	path, err := llmbackend.WriteMCPFile("cline_mcp_settings.json", body)
	if err != nil {
		return nil, err
	}
	return []string{"CLINE_MCP_SETTINGS_PATH=" + path}, nil
}

func bridgeMCP(servers []llmbackend.MCPServer) []bridgesdk.MCPServer {
	if len(servers) == 0 {
		return nil
	}
	out := make([]bridgesdk.MCPServer, len(servers))
	for i, server := range servers {
		out[i] = bridgesdk.MCPServer{
			Name: server.Name, Type: server.Type, Command: server.Command, Args: server.Args,
			Env: server.Env, Cwd: server.Cwd, URL: server.URL, Headers: server.Headers,
		}
	}
	return out
}

func mcpEnvLine(env []string) string {
	for _, entry := range env {
		if strings.HasPrefix(entry, "CLINE_MCP_SETTINGS_PATH=") {
			return entry
		}
	}
	return ""
}
