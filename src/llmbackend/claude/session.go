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
	s.host.SetModel(s.host.Facts().Creds.Model)
	if !s.resumed {
		s.host.SetFrameSent(false)
	}
	return s.resumed, nil
}

func (s *session) args() []string {
	permission := "acceptEdits"
	if s.mode == llmbackend.ModePlan {
		permission = "plan"
	}
	args := []string{"--print", "--verbose", "--output-format", "stream-json", "--permission-mode", permission}
	if s.mode != llmbackend.ModePlan {
		// Explicitly permitted worker tools; never bypass all permission checks.
		args = append(args, "--allowedTools", "Read,Edit,Write,Glob,Grep,Bash")
	}
	if s.host.Facts().Ephemeral {
		args = append(args, "--no-session-persistence")
	} else if s.id != "" {
		args = append(args, "--resume", s.id)
	}
	if model := s.host.Facts().Creds.Model; model != "" {
		args = append(args, "--model", model)
	}
	return args
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
