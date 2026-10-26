package deepseek

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/llmbackend"
)

func TestStreamingTextReply(t *testing.T) {
	f := newFake(t, []string{
		delta("reasoning_content", "think "), delta("reasoning_content", "hard"),
		delta("content", "Hello"), delta("content", ", world"), finish("stop"), usageChunk(10, 5),
	})
	host := f.host(t.TempDir())
	var events []llmbackend.Event
	out, res, err := newSession(host).Prompt(context.Background(), "say hello", llmbackend.ModeAgent, func(ev llmbackend.Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "Hello, world" || res.Status != llmbackend.StatusFinished {
		t.Fatalf("out=%q status=%s", out, res.Status)
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 5 || res.Usage.TotalTokens != 15 || res.Usage.ReasoningTokens != 2 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if got, want := kinds(events), "thought_delta,thought_delta,assistant_delta,assistant_delta,status,usage,thought_end"; got != want {
		t.Fatalf("kinds = %s, want %s", got, want)
	}
	if f.auth[0] != "Bearer good-key" {
		t.Fatalf("auth header = %q", f.auth[0])
	}
	req := f.request(0)
	if req.Model != DefaultModel || !req.Stream || len(req.Tools) == 0 {
		t.Fatalf("request = model %q stream %v tools %d", req.Model, req.Stream, len(req.Tools))
	}
	if req.Messages[0].Role != "system" || req.Messages[1].Content != "say hello" {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if host.facts.Backend != llmbackend.Deepseek || host.facts.Provider != llmbackend.ProviderDeepseekHarness {
		t.Fatalf("host backend = %s/%s", host.facts.Backend, host.facts.Provider)
	}
}

func TestToolCallRoundTrip(t *testing.T) {
	f := newFake(t,
		[]string{
			delta("content", "Writing."),
			toolDelta(0, "call_w", "write_file", `{"path":"notes/a.txt",`),
			toolDelta(0, "", "", `"content":"hi there"}`),
			toolDelta(1, "call_s", "run_shell", `{"command":"cat notes/a.txt"}`),
			finish("tool_calls"), usageChunk(20, 8),
		},
		[]string{delta("content", "Done: wrote notes/a.txt"), finish("stop"), usageChunk(40, 4)},
	)
	dir := t.TempDir()
	var events []llmbackend.Event
	out, res, err := newSession(f.host(dir)).Prompt(context.Background(), "write a note", llmbackend.ModeAgent, func(ev llmbackend.Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "Done: wrote notes/a.txt" {
		t.Fatalf("out = %q", out)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "notes", "a.txt")); err != nil || string(raw) != "hi there" {
		t.Fatalf("file = %q, %v", raw, err)
	}
	if res.Usage.InputTokens != 60 || res.Usage.OutputTokens != 12 {
		t.Fatalf("usage not summed over the loop: %+v", res.Usage)
	}
	second := f.request(1)
	n := len(second.Messages)
	assistant, write, shell := second.Messages[n-3], second.Messages[n-2], second.Messages[n-1]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 2 || assistant.ToolCalls[0].Function.Arguments != `{"path":"notes/a.txt","content":"hi there"}` {
		t.Fatalf("assistant message = %+v", assistant)
	}
	if write.Role != "tool" || write.ToolCallID != "call_w" || !strings.Contains(write.Content, "wrote notes/a.txt") {
		t.Fatalf("write result = %+v", write)
	}
	if shell.ToolCallID != "call_s" || !strings.Contains(shell.Content, "hi there") || !strings.Contains(shell.Content, "[exit code 0]") {
		t.Fatalf("shell result = %+v", shell)
	}
	var started, completed int
	for _, ev := range events {
		switch ev.Kind {
		case llmbackend.KindToolCallStarted:
			started++
			if ev.Payload[llmbackend.KeyCallID] == "call_w" {
				args, _ := ev.Payload[llmbackend.KeyArgs].(map[string]any)
				if args["path"] != "notes/a.txt" {
					t.Fatalf("started args = %#v", ev.Payload[llmbackend.KeyArgs])
				}
			}
		case llmbackend.KindToolCallCompleted:
			completed++
			if ev.Payload[llmbackend.KeyStatus] != "completed" {
				t.Fatalf("completed status = %v", ev.Payload[llmbackend.KeyStatus])
			}
		}
	}
	if started != 2 || completed != 2 {
		t.Fatalf("tool events: %d started, %d completed (%s)", started, completed, kinds(events))
	}
}

func TestSessionResume(t *testing.T) {
	f := newFake(t,
		[]string{delta("reasoning_content", "first thoughts"), delta("content", "plan A"), finish("stop")},
		[]string{delta("content", "plan B"), finish("stop")},
	)
	dir := t.TempDir()
	host := f.host(dir)
	first := newSession(host)
	if resumed, err := first.Attach(context.Background(), llmbackend.ModePlan); err != nil || resumed {
		t.Fatalf("first attach resumed=%v err=%v", resumed, err)
	}
	if _, _, err := first.Prompt(context.Background(), "cycle 1", llmbackend.ModePlan, nil); err != nil {
		t.Fatal(err)
	}
	id := host.facts.SessionID
	if id == "" || id != first.SessionID() {
		t.Fatalf("recorded session id %q, session %q", id, first.SessionID())
	}
	first.Dispose(context.Background(), false)

	// A new process: a fresh session object on the agent row's recorded id.
	second := newSession(&probeHost{facts: host.facts})
	resumed, err := second.Attach(context.Background(), llmbackend.ModePlan)
	if err != nil || !resumed || second.ResumedFrom() != id || second.SessionID() != id {
		t.Fatalf("resume: resumed=%v from=%q id=%q err=%v", resumed, second.ResumedFrom(), second.SessionID(), err)
	}
	out, _, err := second.Prompt(context.Background(), "cycle 2", llmbackend.ModePlan, nil)
	if err != nil || out != "plan B" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	msgs := f.request(1).Messages
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role+":"+m.Content)
		if m.ReasoningContent != "" {
			t.Fatalf("an answered turn's reasoning was sent back: %+v", m)
		}
	}
	if got := strings.Join(roles[1:], "|"); got != "user:cycle 1|assistant:plan A|user:cycle 2" {
		t.Fatalf("resumed history = %s", got)
	}
	// Plan mode is read-only: no tool that writes or runs.
	for _, tool := range f.request(1).Tools {
		if tool.Function.Name == toolWriteFile || tool.Function.Name == toolRunShell || tool.Function.Name == toolEditFile {
			t.Fatalf("plan mode offered %s", tool.Function.Name)
		}
	}
}

func TestResumeOfUnknownSessionStartsFresh(t *testing.T) {
	f := newFake(t)
	host := f.host(t.TempDir())
	host.facts.SessionID = "ds-gone"
	s := newSession(host)
	resumed, err := s.Attach(context.Background(), llmbackend.ModePlan)
	if err != nil || resumed || s.SessionID() == "ds-gone" || host.facts.SessionID != s.SessionID() {
		t.Fatalf("resumed=%v id=%q recorded=%q err=%v", resumed, s.SessionID(), host.facts.SessionID, err)
	}
}
