// Package claude runs Claude Code's non-interactive CLI through the neutral harness interface.
package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/kaulie/autonomy/src/llmbackend"
)

type session struct {
	host               llmbackend.Host
	ids                map[string]string
	key, cwd, id, from string
	mode               llmbackend.Mode
	resumed            bool
}

func newSession(host llmbackend.Host) *session {
	return &session{host: host, ids: make(map[string]string)}
}

func binary() string {
	return llmbackend.FirstNonEmptyString(os.Getenv("AUTONOMY_CLAUDE_BIN"), "claude")
}

func (s *session) Attach(ctx context.Context, mode llmbackend.Mode) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, err := exec.LookPath(binary()); err != nil {
		return false, fmt.Errorf("claude CLI: %w", err)
	}
	cwd := s.host.Facts().Workspace
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return false, err
		}
	}
	key := string(mode) + "\x00" + cwd
	if key == s.key {
		return s.resumed, nil
	}
	id, seen := s.ids[key]
	if !seen && s.key == "" && mode == llmbackend.ModePlan && !s.host.Facts().Ephemeral {
		id = s.host.Facts().SessionID
	}
	s.key, s.cwd, s.mode, s.id, s.from = key, cwd, mode, id, id
	s.resumed = id != ""
	s.ids[key] = id
	s.host.SetBackend(llmbackend.Claude, llmbackend.ProviderClaude)
	s.host.SetWorkspace(cwd)
	s.host.SetModel(s.model())
	if !s.resumed {
		s.host.SetFrameSent(false)
	}
	return s.resumed, nil
}

func (s *session) args() ([]string, error) {
	permission := "acceptEdits"
	if s.mode == llmbackend.ModePlan {
		permission = "plan"
	}
	args := []string{"--print", "--verbose", "--output-format", "stream-json", "--permission-mode", permission}
	servers, err := llmbackend.MCPServers()
	if err != nil {
		return nil, fmt.Errorf("mcp servers: %w", err)
	}
	// --print denies a tool that is not already allowed. MCP tools are named
	// mcp__<server>__<tool>, so each server needs that glob or the model sees
	// the server and then cannot call it. Plan mode has no other allow list;
	// a worker keeps its editor tools and gains the same globs.
	allowed := claudeAllowedTools(s.mode, servers)
	if allowed != "" {
		args = append(args, "--allowedTools", allowed)
	}
	if len(servers) > 0 {
		body, err := llmbackend.ClaudeMCPConfig(servers)
		if err != nil {
			return nil, err
		}
		path, err := llmbackend.WriteMCPFile("claude.mcp.json", body)
		if err != nil {
			return nil, err
		}
		// strict: this file is the list, not the user's ~/.claude.json as well.
		args = append(args, "--mcp-config", path, "--strict-mcp-config")
	}
	if s.host.Facts().Ephemeral {
		args = append(args, "--no-session-persistence")
	} else if s.id != "" {
		args = append(args, "--resume", s.id)
	}
	if model := s.model(); model != "" {
		args = append(args, "--model", model)
	}
	return args, nil
}

// claudeAllowedTools is the --allowedTools value for this mode. Empty means the
// flag is omitted (a planner with no MCP servers).
func claudeAllowedTools(mode llmbackend.Mode, servers []llmbackend.MCPServer) string {
	var names []string
	if mode != llmbackend.ModePlan {
		names = append(names, "Read", "Edit", "Write", "Glob", "Grep", "Bash")
	}
	for _, server := range servers {
		names = append(names, "mcp__"+server.Name+"__*")
	}
	return strings.Join(names, ",")
}

// model is the id this session opens with: a task-level choice on Facts.Model
// outranks the account's stored Creds.Model (POST /api/tasks `model`).
func (s *session) model() string {
	facts := s.host.Facts()
	return llmbackend.FirstNonEmptyString(facts.Model, facts.Creds.Model)
}

// Credentials only come from the account. Keyless accounts use Claude's saved login.
func environment(creds llmbackend.Creds) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE_CODE_") || key == "CLAUDECODE" {
			continue
		}
		env = append(env, entry)
	}
	if creds.APIKey != "" {
		env = append(env, "ANTHROPIC_API_KEY="+creds.APIKey)
	}
	if creds.BaseURL != "" {
		env = append(env, "ANTHROPIC_BASE_URL="+creds.BaseURL)
	}
	return env
}

func (s *session) record(id string) {
	if id == "" {
		return
	}
	s.id, s.ids[s.key] = id, id
	if s.mode == llmbackend.ModePlan && !s.host.Facts().Ephemeral && s.host.Facts().SessionID != id {
		s.host.SetSessionID(id)
		s.host.Persist()
	}
}

func (s *session) Dispose(context.Context, bool) {
	s.ids = make(map[string]string)
	s.key, s.id, s.from, s.mode, s.resumed = "", "", "", "", false
}
func (s *session) SessionID() string   { return s.id }
func (s *session) Mode() string        { return string(s.mode) }
func (s *session) Resumed() bool       { return s.resumed }
func (s *session) ResumedFrom() string { return s.from }
