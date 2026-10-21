package cursor

import (
	"fmt"

	sdkv1 "github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1"
	"github.com/kaulie/autonomy/src/llmbackend"
)

// mcpConfigs is the process MCP list as Cursor's Create/Resume map. An empty
// list is nil, which leaves the request's mcp_servers field unset.
func mcpConfigs() (map[string]*sdkv1.McpServerConfig, error) {
	servers, err := llmbackend.MCPServers()
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, nil
	}
	out := make(map[string]*sdkv1.McpServerConfig, len(servers))
	for _, server := range servers {
		cfg, err := oneMCP(server)
		if err != nil {
			return nil, err
		}
		out[server.Name] = cfg
	}
	return out, nil
}

func oneMCP(server llmbackend.MCPServer) (*sdkv1.McpServerConfig, error) {
	if server.Stdio() {
		return &sdkv1.McpServerConfig{Config: &sdkv1.McpServerConfig_Stdio{Stdio: &sdkv1.StdioMcpServerConfig{
			Command: server.Command,
			Args:    server.Args,
			Env:     server.Env,
			Cwd:     server.Cwd,
		}}}, nil
	}
	kind := sdkv1.HttpMcpTransportType_HTTP_MCP_TRANSPORT_TYPE_HTTP
	if server.Type == "sse" {
		kind = sdkv1.HttpMcpTransportType_HTTP_MCP_TRANSPORT_TYPE_SSE
	} else if server.Type != "http" {
		return nil, fmt.Errorf("mcp server %q: cursor has no transport %q", server.Name, server.Type)
	}
	return &sdkv1.McpServerConfig{Config: &sdkv1.McpServerConfig_Http{Http: &sdkv1.HttpMcpServerConfig{
		Type:    kind,
		Url:     server.URL,
		Headers: server.Headers,
	}}}, nil
}
