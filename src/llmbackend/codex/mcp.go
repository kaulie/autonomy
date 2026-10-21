package codex

import (
	"github.com/kaulie/autonomy/src/bridgesdk"
	"github.com/kaulie/autonomy/src/llmbackend"
)

func codexBridgeMCP(servers []llmbackend.MCPServer) []bridgesdk.MCPServer {
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
