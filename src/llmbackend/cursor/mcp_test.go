package cursor

import (
	"os"
	"path/filepath"
	"testing"

	sdkv1 "github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1"
	"github.com/kaulie/autonomy/src/llmbackend"
)

func TestMCPConfigsMatchTheFile(t *testing.T) {
	t.Setenv(llmbackend.MCPServersFileEnv, "")
	got, err := mcpConfigs()
	if err != nil || got != nil {
		t.Fatalf("unset: %+v %v", got, err)
	}

	path := filepath.Join(t.TempDir(), "mcp.json")
	body := []byte(`{"mcpServers":{
		"docs":{"type":"sse","url":"https://example.com/mcp","headers":{"Authorization":"Bearer t"}},
		"local":{"command":"node","args":["server.mjs"],"env":{"GIT_VIA_PROXY_URL":"http://127.0.0.1:7897"},"cwd":"/tmp/srv"}
	}}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(llmbackend.MCPServersFileEnv, path)
	got, err = mcpConfigs()
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	http := got["docs"].GetHttp()
	if http.GetType() != sdkv1.HttpMcpTransportType_HTTP_MCP_TRANSPORT_TYPE_SSE || http.GetUrl() != "https://example.com/mcp" {
		t.Fatalf("http: %+v", http)
	}
	stdio := got["local"].GetStdio()
	if stdio.GetCommand() != "node" || stdio.GetCwd() != "/tmp/srv" || stdio.GetEnv()["GIT_VIA_PROXY_URL"] == "" {
		t.Fatalf("stdio: %+v", stdio)
	}
}
