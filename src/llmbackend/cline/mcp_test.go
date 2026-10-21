package cline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/llmbackend"
)

func TestClineClientEnvPointsAtSettingsWeWrote(t *testing.T) {
	t.Setenv(llmbackend.MCPServersFileEnv, "")
	client, err := newClineClient(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if line := mcpEnvLine(client.Manager.Env); line != "" {
		t.Fatalf("unset file must not override Cline's own settings, got %s", line)
	}

	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"docs":{"type":"http","url":"https://example.com/mcp"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(llmbackend.MCPServersFileEnv, path)
	client, err = newClineClient(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	line := mcpEnvLine(client.Manager.Env)
	settings := strings.TrimPrefix(line, "CLINE_MCP_SETTINGS_PATH=")
	if settings == line || settings == "" {
		t.Fatal(client.Manager.Env)
	}
	body, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"type": "streamableHttp"`) || !strings.Contains(string(body), "https://example.com/mcp") {
		t.Fatal(string(body))
	}

	t.Setenv(llmbackend.MCPServersFileEnv, filepath.Join(t.TempDir(), "missing.json"))
	if _, err := newClineClient(t.TempDir()); err == nil {
		t.Fatal("a missing mcp file must refuse the client")
	}
}
