package deepseek

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/llmbackend"
)

func TestToolsStayInWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n// TODO one\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	box := toolbox{root: dir, mode: llmbackend.ModeAgent}
	ctx := context.Background()

	if out, failed := box.run(ctx, toolReadFile, `{"path":"../outside.txt"}`); !failed || !strings.Contains(out, "outside the workspace") {
		t.Fatalf("escape read: %q", out)
	}
	if out, failed := box.run(ctx, toolWriteFile, `{"path":"/etc/x","content":"no"}`); !failed || !strings.Contains(out, "outside the workspace") {
		t.Fatalf("absolute write: %q", out)
	}
	if out, failed := box.run(ctx, toolReadFile, `{"path":"main.go","offset":2,"limit":1}`); failed || out != "// TODO one\n" {
		t.Fatalf("read lines: %q", out)
	}
	if out, failed := box.run(ctx, toolEditFile, `{"path":"main.go","old_string":"TODO one","new_string":"DONE"}`); failed {
		t.Fatalf("edit: %q", out)
	}
	if out, failed := box.run(ctx, toolEditFile, `{"path":"main.go","old_string":"absent","new_string":"x"}`); !failed {
		t.Fatalf("edit of a missing string succeeded: %q", out)
	}
	if out, _ := box.run(ctx, toolSearchFiles, `{"pattern":"DONE","glob":"*.go"}`); out != "main.go:2: // DONE" {
		t.Fatalf("search: %q", out)
	}
	if out, _ := box.run(ctx, toolListFiles, `{"pattern":"*.go"}`); out != "main.go" {
		t.Fatalf("list: %q", out)
	}
	if out, failed := box.run(ctx, toolRunShell, `{"command":"pwd; exit 3"}`); !failed || !strings.Contains(out, dir) || !strings.Contains(out, "[exit code 3]") {
		t.Fatalf("shell: %q", out)
	}
	if out, failed := box.run(ctx, toolReadFile, `not json`); !failed || !strings.Contains(out, "invalid tool arguments") {
		t.Fatalf("bad args: %q", out)
	}

	plan := toolbox{root: dir, mode: llmbackend.ModePlan}
	if out, failed := plan.run(ctx, toolRunShell, `{"command":"touch x"}`); !failed || !strings.Contains(out, "not available") {
		t.Fatalf("plan mode ran a shell: %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); err == nil {
		t.Fatal("plan mode changed the workspace")
	}
}

func TestToolLoopIsBounded(t *testing.T) {
	t.Setenv("AUTONOMY_DEEPSEEK_MAX_ITERATIONS", "2")
	loop := []string{toolDelta(0, "c", "list_files", `{}`), finish("tool_calls")}
	f := newFake(t, loop, loop, loop)
	s := newSession(f.host(t.TempDir()))
	var sawError bool
	_, res, err := s.Prompt(context.Background(), "loop forever", llmbackend.ModeAgent, func(ev llmbackend.Event) {
		sawError = sawError || ev.Kind == llmbackend.KindError
	})
	if err == nil || !strings.Contains(err.Error(), "limit of 2") || res.Status != llmbackend.StatusError || !sawError {
		t.Fatalf("err=%v status=%s sawError=%v", err, res.Status, sawError)
	}
	if len(f.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(f.requests))
	}
	// A failed turn leaves the conversation as it was before it.
	if n := len(s.conv.Messages); n != 0 {
		t.Fatalf("history after a failed turn has %d messages", n)
	}
}

func TestAPIErrorIsReported(t *testing.T) {
	f := newFake(t) // no scripted reply: the fake answers HTTP 500
	_, res, err := newSession(f.host(t.TempDir())).Prompt(context.Background(), "hi", llmbackend.ModeAgent, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(res.ErrorMessage, "no scripted reply") {
		t.Fatalf("err=%v result=%+v", err, res)
	}
}
