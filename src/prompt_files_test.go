package autonomy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// The two halves of the reasoning prompt are files, and this is what that means: editing the file
// changes what the model is told. The frame is the initialization prompt (the agent's system
// prompt plus the policy); the delta is what every later cycle carries.
func TestTheReasoningPromptIsTheFileItComesFrom(t *testing.T) {
	root := preparePolicyRoot(t)
	t.Setenv("PROJECT_ROOT", root)
	policyDir := filepath.Join(root, "src", "agent_policy")

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(policyDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Both templates are replaced with markers, so what the rendered prompt contains can only come
	// from the files.
	write("REASONING_FRAME.md", "FRAME-FROM-FILE\n{{SYSTEM_PROMPT}}{{AGENT_POLICY}}")
	write("REASONING_DELTA.md", "DELTA-FROM-FILE cycle={{CYCLE}} marker={{DELTA_MARKER}}\n{{BRIEFING_NOTE}}PAYLOAD={{PAYLOAD}}\n")

	agent := &Agent{ID: 9101, Name: "agent-9101", SystemPrompt: "be brief"}
	ctx := DecisionContext{Agent: agent, Task: &Task{ID: "task-prompts"}, Cycle: 3}
	input := ReasoningInput{}

	frame, err := buildReasoningFrame(ctx, input)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	for _, want := range []string{"FRAME-FROM-FILE", "## System Prompt", "be brief"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("the frame should carry %q\n%s", want, frame)
		}
	}
	if !strings.Contains(frame, "Autonomy Bootstrap Prompt") {
		t.Fatalf("the frame should still carry the policy file (AGENT_V2.md)\n%s", frame)
	}

	delta, err := buildReasoningDelta(ctx, input)
	if err != nil {
		t.Fatalf("delta: %v", err)
	}
	for _, want := range []string{"DELTA-FROM-FILE", "cycle=3", reasoningDeltaMarker, "PAYLOAD=", "\"runtime_context\""} {
		if !strings.Contains(delta, want) {
			t.Fatalf("the delta should carry %q\n%s", want, delta)
		}
	}

	// A runtime with no PROJECT_ROOT still sends a delta: the build carries its own copy of the
	// templates, so a long-lived session cannot lose its later cycles to a missing file.
	t.Setenv("PROJECT_ROOT", "")
	fallback, err := buildReasoningDelta(ctx, input)
	if err != nil {
		t.Fatalf("delta without PROJECT_ROOT: %v", err)
	}
	if !strings.Contains(fallback, "## Decision Cycle 3") {
		t.Fatalf("the embedded template should have been used\n%s", fallback)
	}
}

// 别处的提示词也是文件 —— 包括以前写在 Go 里的那些：chat 模式的守卫、重启简报自己的话、
// 探活那句。改文件 = 改它们说的话；文件不在则由构建里带的那份顶上。
// 初始化 system prompt 不是 harness 文件：它是会话的第一条 prompt（frame）。
func TestTheOtherPromptsAreTheFilesTheyComeFrom(t *testing.T) {
	root := preparePolicyRoot(t)
	t.Setenv("PROJECT_ROOT", root)
	policyDir := filepath.Join(root, "src", "agent_policy")

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(policyDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("PROBE.md", "PROBE-FROM-FILE")
	write("CHAT_MODE.md", "GUARD-FROM-FILE")
	write("BRIEFING_NOTE.md", "BRIEFING-FROM-FILE")
	write("INTERRUPTED_NOTE.md", " INTERRUPTED-FROM-FILE")

	got, err := llmbackend.PromptFile(llmbackend.ProbePromptRel)
	if err != nil || got != "PROBE-FROM-FILE" {
		t.Fatalf("probe = %q (%v), want PROBE-FROM-FILE", got, err)
	}

	// chat 模式的守卫：那段话在文件里，用户的话跟在后面。
	guard, err := chatPlannerInput("  hello  ")
	if err != nil {
		t.Fatalf("chat planner input: %v", err)
	}
	if !strings.HasPrefix(guard, "GUARD-FROM-FILE") || !strings.HasSuffix(guard, "User: hello") {
		t.Fatalf("chat planner input should be the file's guard plus the user's words:\n%s", guard)
	}

	// 简报的两个 note：briefing 是块自己的话，interrupted 是拼在它后面的那一句（开头那个空格是原文）。
	if got, err := loadPromptFile(briefingNoteRel); err != nil || strings.TrimSpace(got) != "BRIEFING-FROM-FILE" {
		t.Fatalf("briefing note = %q (%v)", got, err)
	}
	if got, err := loadPromptFile(interruptedNoteRel); err != nil || strings.TrimRight(got, "\n") != " INTERRUPTED-FROM-FILE" {
		t.Fatalf("interrupted note = %q (%v)", got, err)
	}

	// 文件不在：构建里带的那份顶上，读的人照旧有话可说。
	t.Setenv("PROJECT_ROOT", "")
	got, err = llmbackend.PromptFile(llmbackend.ProbePromptRel)
	if err != nil || strings.TrimSpace(got) == "" {
		t.Fatalf("the embedded probe should answer without PROJECT_ROOT: %q (%v)", got, err)
	}
	guard, err = chatPlannerInput("hi")
	if err != nil || !strings.Contains(guard, "CHAT MODE") {
		t.Fatalf("the embedded chat-mode guard should answer without PROJECT_ROOT: %q (%v)", guard, err)
	}
}
