package llmbackend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// MCPServersFileEnv names the JSON file whose mcpServers object is given to every
// harness. Unset means the process passes no MCP servers, and each harness keeps
// the behavior it had without this list.
const MCPServersFileEnv = "AUTONOMY_MCP_SERVERS_FILE"

// mcpName matches a server, env, or header key that every harness can carry.
// Codex flattens these into dotted --config keys, and Claude builds tool names
// from the server name, so the key has to be a bare token in all four dialects.
var mcpName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// MCPServer is one server in the process-wide list. An empty Type is stdio.
// Type "http" and "sse" are remote. "streamableHttp" and "streamable-http" are
// accepted as aliases of http so a file copied from Cline or Claude still loads.
type MCPServer struct {
	Name    string            `json:"name"`
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Stdio reports whether this server is a local process.
func (s MCPServer) Stdio() bool { return s.Type == "" || s.Type == "stdio" }

// MCPServers reads AUTONOMY_MCP_SERVERS_FILE. An unset variable is an empty list.
// A set variable that does not point at a readable, valid file is an error: the
// turn fails instead of starting an agent that looks like it has no MCP.
func MCPServers() ([]MCPServer, error) {
	path := strings.TrimSpace(os.Getenv(MCPServersFileEnv))
	if path == "" {
		return nil, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", MCPServersFileEnv, err)
	}
	return ParseMCPServers(body)
}

// ParseMCPServers decodes the mcpServers object. An empty object is an empty list.
func ParseMCPServers(body []byte) ([]MCPServer, error) {
	var doc struct {
		MCPServers map[string]mcpServerFile `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", MCPServersFileEnv, err)
	}
	if len(doc.MCPServers) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(doc.MCPServers))
	for name := range doc.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]MCPServer, 0, len(names))
	for _, name := range names {
		server, err := doc.MCPServers[name].server(name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", MCPServersFileEnv, err)
		}
		out = append(out, server)
	}
	return out, nil
}

type mcpServerFile struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

func (f mcpServerFile) server(name string) (MCPServer, error) {
	if !mcpName.MatchString(name) {
		return MCPServer{}, fmt.Errorf("mcp server %q: name must match %s", name, mcpName.String())
	}
	kind, err := normalizeMCPType(f.Type)
	if err != nil {
		return MCPServer{}, fmt.Errorf("mcp server %q: %w", name, err)
	}
	server := MCPServer{
		Name: name, Type: kind, Command: strings.TrimSpace(f.Command), Args: f.Args,
		Env: f.Env, Cwd: strings.TrimSpace(f.Cwd), URL: strings.TrimSpace(f.URL), Headers: f.Headers,
	}
	if err := checkMCPKeys(name, "env", server.Env); err != nil {
		return MCPServer{}, err
	}
	if err := checkMCPKeys(name, "header", server.Headers); err != nil {
		return MCPServer{}, err
	}
	if server.Stdio() {
		if server.Command == "" {
			return MCPServer{}, fmt.Errorf("mcp server %q: stdio server needs a command", name)
		}
		if server.URL != "" || len(server.Headers) > 0 {
			return MCPServer{}, fmt.Errorf("mcp server %q: stdio server cannot set url or headers", name)
		}
		server.Type = ""
		return server, nil
	}
	if server.URL == "" {
		return MCPServer{}, fmt.Errorf("mcp server %q: %s server needs a url", name, kind)
	}
	if server.Command != "" || len(server.Args) > 0 || server.Cwd != "" || len(server.Env) > 0 {
		return MCPServer{}, fmt.Errorf("mcp server %q: %s server cannot set command, args, cwd, or env", name, kind)
	}
	return server, nil
}

func normalizeMCPType(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "stdio":
		return "", nil
	case "http", "streamablehttp", "streamable-http":
		return "http", nil
	case "sse":
		return "sse", nil
	default:
		return "", fmt.Errorf("type %q is not stdio, http, or sse", raw)
	}
}

func checkMCPKeys(server, kind string, values map[string]string) error {
	for key := range values {
		if !mcpName.MatchString(key) {
			return fmt.Errorf("mcp server %q: %s %q must match %s", server, kind, key, mcpName.String())
		}
	}
	return nil
}

// ClaudeMCPConfig is the JSON Claude Code accepts on --mcp-config. Stdio entries
// omit type. Remote entries say http or sse. A url without a type is dropped by
// Claude, so this file always sets one.
func ClaudeMCPConfig(servers []MCPServer) ([]byte, error) {
	return mcpObject(servers, func(s MCPServer) any {
		if s.Stdio() {
			entry := map[string]any{"command": s.Command}
			if len(s.Args) > 0 {
				entry["args"] = s.Args
			}
			if len(s.Env) > 0 {
				entry["env"] = s.Env
			}
			if s.Cwd != "" {
				entry["cwd"] = s.Cwd
			}
			return entry
		}
		entry := map[string]any{"type": s.Type, "url": s.URL}
		if len(s.Headers) > 0 {
			entry["headers"] = s.Headers
		}
		return entry
	})
}

// ClineMCPSettings is cline_mcp_settings.json. HTTP is written as streamableHttp,
// which is the type the Cline SDK starts. The file replaces the user's own
// settings only when CLINE_MCP_SETTINGS_PATH points at it.
func ClineMCPSettings(servers []MCPServer) ([]byte, error) {
	return mcpObject(servers, func(s MCPServer) any {
		entry := map[string]any{"disabled": false}
		if s.Stdio() {
			entry["command"] = s.Command
			if len(s.Args) > 0 {
				entry["args"] = s.Args
			}
			if len(s.Env) > 0 {
				entry["env"] = s.Env
			}
			if s.Cwd != "" {
				entry["cwd"] = s.Cwd
			}
			return entry
		}
		kind := "streamableHttp"
		if s.Type == "sse" {
			kind = "sse"
		}
		entry["type"] = kind
		entry["url"] = s.URL
		if len(s.Headers) > 0 {
			entry["headers"] = s.Headers
		}
		return entry
	})
}

func mcpObject(servers []MCPServer, entry func(MCPServer) any) ([]byte, error) {
	doc := map[string]any{"mcpServers": map[string]any{}}
	serversByName := doc["mcpServers"].(map[string]any)
	for _, server := range servers {
		serversByName[server.Name] = entry(server)
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// WriteMCPFile writes body to name inside the process MCP directory and returns
// the path. Callers point a harness at that path instead of editing a user's
// own MCP config.
func WriteMCPFile(name string, body []byte) (string, error) {
	dir, err := mcpDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func mcpDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "autonomy", "mcp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
