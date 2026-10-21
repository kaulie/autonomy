package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/llmbackend"
)

func fakeCLI(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTONOMY_CLAUDE_BIN", path)
	return dir
}

const success = `{"type":"result","subtype":"success","session_id":"session-1","result":"OK","usage":{"input_tokens":2,"output_tokens":3,"cache_read_input_tokens":4,"cache_creation_input_tokens":5},"total_cost_usd":0.01}`

func TestSessionThroughRegistry(t *testing.T) {
	dir := fakeCLI(t, "printf '%s\\n' \"$@\" > args\ncat > prompt\nprintf '%s\\n' \"$ANTHROPIC_API_KEY|$ANTHROPIC_BASE_URL|$ANTHROPIC_AUTH_TOKEN\" > creds\npwd > cwd\nprintf '%s\\n' '"+success+"'\n")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "ambient-must-not-leak")
	h := &probeHost{facts: llmbackend.Facts{Backend: llmbackend.Claude, Workspace: dir, Creds: llmbackend.Creds{APIKey: "account-key", Model: "sonnet", BaseURL: "https://example.invalid"}}}
	s := llmbackend.New(h)
	var evs []llmbackend.Event
	out, res, err := s.Prompt(context.Background(), "hello\nworld", llmbackend.ModePlan, func(ev llmbackend.Event) { evs = append(evs, ev) })
	if err != nil || out != "OK" || res.Status != llmbackend.StatusFinished {
		t.Fatalf("%q %+v %v", out, res, err)
	}
	if h.facts.SessionID != "session-1" || res.Usage.TotalTokens != 14 || res.Usage.CostCents != 1 || len(evs) != 1 {
		t.Fatalf("facts=%+v result=%+v events=%v", h.facts, res, evs)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	physicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if read("prompt") != "hello\nworld" || strings.TrimSpace(read("cwd")) != physicalDir || read("creds") != "account-key|https://example.invalid|\n" {
		t.Fatal("prompt, cwd or credentials were not propagated")
	}
	if !strings.Contains(read("args"), "--permission-mode\nplan\n") || !strings.Contains(read("args"), "--model\nsonnet\n") {
		t.Fatal(read("args"))
	}
	// A new runtime session uses the provider ID persisted on the host.
	s = llmbackend.New(h)
	if _, _, err := s.Prompt(context.Background(), "again", llmbackend.ModePlan, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read("args"), "--resume\nsession-1\n") || !s.Resumed() {
		t.Fatal(read("args"))
	}
	if _, _, err := s.Prompt(context.Background(), "work", llmbackend.ModeAgent, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read("args"), "--resume") || !strings.Contains(read("args"), "acceptEdits") {
		t.Fatal(read("args"))
	}
}

func TestMCPServersArePassedToTheCLI(t *testing.T) {
	dir := fakeCLI(t, "printf '%s\\n' \"$@\" > args\ncat >/dev/null\nprintf '%s\\n' '"+success+"'\n")
	file := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(file, []byte(`{"mcpServers":{"git-via-proxy":{"command":"node","args":["server.mjs"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(llmbackend.MCPServersFileEnv, file)
	h := &probeHost{facts: llmbackend.Facts{Workspace: dir}}
	if _, _, err := newSession(h).Prompt(context.Background(), "hello", llmbackend.ModeAgent, nil); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(args)
	if !strings.Contains(text, "--strict-mcp-config\n") || !strings.Contains(text, "--mcp-config\n") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "mcp__git-via-proxy__*") || !strings.Contains(text, "Read,Edit,Write,Glob,Grep,Bash,mcp__git-via-proxy__*") {
		t.Fatal(text)
	}
	// The config path is an argument of its own, and the file is the Claude dialect.
	var configPath string
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line == "--mcp-config" && i+1 < len(lines) {
			configPath = lines[i+1]
		}
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"type": "stdio"`) || !strings.Contains(string(body), `"command": "node"`) {
		t.Fatal(string(body))
	}
}

func TestPlanModeAllowsOnlyMCPTools(t *testing.T) {
	if got := claudeAllowedTools(llmbackend.ModePlan, nil); got != "" {
		t.Fatalf("planner with no servers omits the flag, got %q", got)
	}
	got := claudeAllowedTools(llmbackend.ModePlan, []llmbackend.MCPServer{{Name: "docs", Type: "http", URL: "https://example.com/mcp"}})
	if got != "mcp__docs__*" {
		t.Fatalf("planner with servers allows only those tools, got %q", got)
	}
}

func TestTaskModelOutranksTheAccountModel(t *testing.T) {
	dir := fakeCLI(t, "printf '%s\\n' \"$@\" > args\ncat >/dev/null\nprintf '%s\\n' '"+success+"'\n")
	h := &probeHost{facts: llmbackend.Facts{
		Workspace: dir,
		Model:     "opus",
		Creds:     llmbackend.Creds{Model: "sonnet"},
	}}
	if _, _, err := newSession(h).Prompt(context.Background(), "hello", llmbackend.ModePlan, nil); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--model\nopus\n") {
		t.Fatalf("args=%s want --model opus from Facts.Model, not the account's sonnet", args)
	}
}

func TestFailuresAndCancellation(t *testing.T) {
	for name, body := range map[string]string{
		"missing result": "exit 0", "invalid JSON": "echo broken", "exit failure": "exit 2",
		"provider error": `echo '{"type":"result","subtype":"error_max_turns","is_error":true,"result":"partial"}'`,
		"empty response": `echo '{"type":"result","subtype":"success","result":""}'`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := fakeCLI(t, body)
			h := &probeHost{facts: llmbackend.Facts{Workspace: dir}}
			_, res, err := newSession(h).Prompt(context.Background(), "test", llmbackend.ModePlan, nil)
			if err == nil || res.Status != llmbackend.StatusError {
				t.Fatalf("%+v %v", res, err)
			}
		})
	}
	dir := fakeCLI(t, "exec sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, res, err := newSession(&probeHost{facts: llmbackend.Facts{Workspace: dir}}).Prompt(ctx, "test", llmbackend.ModePlan, nil)
	if err != context.DeadlineExceeded || res.Status != llmbackend.StatusCancelled {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestContentBlocksAndProbe(t *testing.T) {
	native := map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "hello"},
		map[string]any{"type": "thinking", "thinking": "reason"},
		map[string]any{"type": "tool_use", "id": "call-1", "name": "Bash", "input": map[string]any{"command": "pwd"}},
		map[string]any{"type": "tool_result", "tool_use_id": "call-1", "content": "output", "is_error": true},
	}}}
	evs := events(native)
	if len(evs) != 5 || evs[1].TextDelta != "hello" || evs[2].Channel != llmbackend.ChannelThought || evs[3].Name != "Bash" || evs[4].Payload[llmbackend.KeyStatus] != "failed" {
		t.Fatalf("%+v", evs)
	}
	if evs[3].Payload[llmbackend.KeyCallID] != evs[4].Payload[llmbackend.KeyCallID] {
		t.Fatal("lost tool correlation")
	}
	fakeCLI(t, "if [ \"$1\" = --version ]; then echo fake; exit 0; fi\ncat >/dev/null\nprintf '%s\\n' '"+success+"'\n")
	for _, live := range []bool{false, true} {
		r, err := llmbackend.ProbeHarness(context.Background(), llmbackend.Claude, llmbackend.Creds{}, live)
		if err != nil || !r.Load || r.Live != live {
			t.Fatalf("%+v %v", r, err)
		}
	}
	h := &probeHost{facts: llmbackend.Facts{Workspace: t.TempDir(), Ephemeral: true, SessionID: "old"}}
	s := newSession(h)
	if _, err := s.Attach(context.Background(), llmbackend.ModePlan); err != nil {
		t.Fatal(err)
	}
	argv, err := s.args()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(argv, " "), "--no-session-persistence") || s.Resumed() {
		t.Fatal(argv)
	}
	s.record("transient")
	if h.facts.SessionID != "old" {
		t.Fatal("ephemeral session persisted")
	}
}
