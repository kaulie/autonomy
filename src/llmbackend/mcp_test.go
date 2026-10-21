package llmbackend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleMCP = `{
  "mcpServers": {
    "docs": {"type": "http", "url": "https://example.com/mcp", "headers": {"Authorization": "Bearer t"}},
    "git-via-proxy": {"command": "node", "args": ["server.mjs"], "env": {"GIT_VIA_PROXY_URL": "http://127.0.0.1:7897"}}
  }
}`

func TestMCPServersFile(t *testing.T) {
	t.Setenv(MCPServersFileEnv, "")
	servers, err := MCPServers()
	if err != nil || servers != nil {
		t.Fatalf("unset: %+v %v", servers, err)
	}

	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(sampleMCP), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(MCPServersFileEnv, path)
	servers, err = MCPServers()
	if err != nil || len(servers) != 2 {
		t.Fatalf("%+v %v", servers, err)
	}
	if servers[0].Type != "http" || servers[0].URL != "https://example.com/mcp" || servers[0].Headers["Authorization"] != "Bearer t" {
		t.Fatalf("docs: %+v", servers[0])
	}
	if servers[1].Name != "git-via-proxy" || !servers[1].Stdio() || servers[1].Command != "node" || servers[1].Env["GIT_VIA_PROXY_URL"] == "" {
		t.Fatalf("stdio: %+v", servers[1])
	}

	t.Setenv(MCPServersFileEnv, filepath.Join(t.TempDir(), "missing.json"))
	if _, err := MCPServers(); err == nil {
		t.Fatal("missing file must fail the turn")
	}
}

func TestParseMCPServersRejectsABadEntry(t *testing.T) {
	for name, body := range map[string]string{
		"not json":        `{`,
		"stdio needs cmd": `{"mcpServers":{"a":{}}}`,
		"http needs url":  `{"mcpServers":{"a":{"type":"http"}}}`,
		"mixed fields":    `{"mcpServers":{"a":{"command":"n","url":"https://x"}}}`,
		"bad name":        `{"mcpServers":{"has space":{"command":"n"}}}`,
		"bad type":        `{"mcpServers":{"a":{"type":"ws","url":"https://x"}}}`,
		"bad header":      `{"mcpServers":{"a":{"type":"sse","url":"https://x","headers":{"Not A Header":"v"}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMCPServers([]byte(body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	servers, err := ParseMCPServers([]byte(`{"mcpServers":{"a":{"type":"streamable-http","url":"https://x"}}}`))
	if err != nil || len(servers) != 1 || servers[0].Type != "http" {
		t.Fatalf("%+v %v", servers, err)
	}
	empty, err := ParseMCPServers([]byte(`{"mcpServers":{}}`))
	if err != nil || empty != nil {
		t.Fatalf("empty object: %+v %v", empty, err)
	}
}

func TestHarnessDialects(t *testing.T) {
	servers, err := ParseMCPServers([]byte(sampleMCP))
	if err != nil {
		t.Fatal(err)
	}
	claude, err := ClaudeMCPConfig(servers)
	if err != nil {
		t.Fatal(err)
	}
	text := string(claude)
	if strings.Contains(text, `"type": "stdio"`) || !strings.Contains(text, `"type": "http"`) || !strings.Contains(text, `"command": "node"`) {
		t.Fatal(text)
	}
	cline, err := ClineMCPSettings(servers)
	if err != nil {
		t.Fatal(err)
	}
	ctext := string(cline)
	if !strings.Contains(ctext, `"type": "streamableHttp"`) || strings.Contains(ctext, `"type": "http"`) || !strings.Contains(ctext, `"disabled": false`) {
		t.Fatal(ctext)
	}
}
