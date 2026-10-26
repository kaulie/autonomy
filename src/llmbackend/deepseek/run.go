package deepseek

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/llmbackend"
	"github.com/kaulie/autonomy/src/llmrun"
)

// systemPrompt is the harness's own preamble: what the tools are and where they act. The
// agent's role prompt is the runtime's and arrives as the first turn (SystemInject first_turn).
func systemPrompt(cwd string, mode llmbackend.Mode) string {
	var b strings.Builder
	b.WriteString("You are a software agent running inside the autonomy runtime through the DeepSeek harness.\n")
	fmt.Fprintf(&b, "Your workspace is %s. Every tool call acts inside it; relative paths are relative to it.\n", cwd)
	if mode == llmbackend.ModePlan {
		b.WriteString("This is a read-only planning turn: you can read, list and search files, not change them or run commands.\n")
	} else {
		b.WriteString("Use the tools to inspect and change files and to run commands (build, test, git). Verify your work before you answer.\n")
	}
	b.WriteString("When you are done, reply with your final answer as plain text and no tool call.")
	return b.String()
}

// Prompt runs one turn: the user message, then the tool loop — request, run every tool call
// the model made, feed the results back — until the model answers without a tool call or
// the loop reaches its bound. A failed turn leaves the stored conversation as it was.
func (s *session) Prompt(ctx context.Context, prompt string, mode llmbackend.Mode, sink func(llmbackend.Event)) (out string, result llmbackend.RunResult, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result.StartedAt = time.Now()
	result.Status = llmbackend.StatusError
	emit := func(native map[string]any) {
		result.EventCount++
		if ev, ok := llmbackend.MapNativeLLMEvent(llmbackend.ProviderDeepseekHarness, native, result.StartedAt); ok && sink != nil {
			sink(ev)
		}
	}
	var conv *transcript
	base := -1
	defer func() {
		result.EndedAt = time.Now()
		result.DurationMS = result.EndedAt.Sub(result.StartedAt).Milliseconds()
		result.RawOutput = out
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			result.Status = llmbackend.StatusError
			if ctx.Err() != nil {
				result.Status = llmbackend.StatusCancelled
			}
			result.ErrorMessage = err.Error()
			emit(map[string]any{"type": nativeError, "message": err.Error()})
			if conv != nil && base >= 0 && base <= len(conv.Messages) {
				conv.Messages = conv.Messages[:base]
			}
		}
	}()
	if _, err = s.attach(ctx, mode); err != nil {
		return
	}
	conv = s.conv
	facts := s.host.Facts()
	s.from, s.resumed = conv.ID, len(conv.Messages) > 0
	result.ProviderRunID, result.LLMAgentID = conv.ID, conv.ID
	model := s.model()
	conv.Model, conv.Workspace = model, s.cwd
	base = len(conv.Messages)
	if base == 0 {
		conv.Messages = append(conv.Messages, message{Role: "system", Content: systemPrompt(s.cwd, mode)})
	}
	conv.Messages = append(conv.Messages, message{Role: "user", Content: prompt})

	api := newClient(facts.Creds.BaseURL, facts.Creds.APIKey)
	tools := toolbox{root: s.cwd, mode: mode}
	touch := func() { llmrun.Touch(ctx) }
	limit := maxIterations()
	for iteration := 0; ; iteration++ {
		if iteration >= limit {
			err = fmt.Errorf("deepseek: the tool loop reached its limit of %d requests without a final answer (AUTONOMY_DEEPSEEK_MAX_ITERATIONS)", limit)
			return
		}
		req := chatRequest{Model: model, Messages: requestMessages(conv.Messages), Tools: toolSpecs(mode)}
		var done completion
		done, err = api.stream(ctx, req, emit, touch)
		if err != nil {
			return
		}
		result.Usage = addUsage(result.Usage, usageOf(done.Usage))
		if done.Message.ReasoningContent != "" {
			emit(map[string]any{"type": nativeReasoningEnd, "text": done.Message.ReasoningContent})
		}
		conv.Messages = append(conv.Messages, done.Message)
		if len(done.Message.ToolCalls) == 0 {
			out = strings.TrimSpace(done.Message.Content)
			break
		}
		for _, call := range done.Message.ToolCalls {
			emit(map[string]any{"type": nativeToolCall, "id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			started := time.Now()
			content, failed := tools.run(ctx, call.Function.Name, call.Function.Arguments)
			touch()
			emit(map[string]any{"type": nativeToolResult, "id": call.ID, "name": call.Function.Name,
				"content": content, "is_error": failed, "duration_ms": time.Since(started).Milliseconds()})
			conv.Messages = append(conv.Messages, message{Role: "tool", ToolCallID: call.ID, Content: content})
			if err = ctx.Err(); err != nil {
				return
			}
		}
	}
	if out == "" {
		err = fmt.Errorf("deepseek returned an empty response")
		return
	}
	if !facts.Ephemeral {
		if err = saveTranscript(*conv); err != nil {
			err = fmt.Errorf("deepseek: store session %s: %w", conv.ID, err)
			return
		}
	}
	result.Status = llmbackend.StatusFinished
	return
}

// requestMessages is the history as a request sends it. DeepSeek's thinking mode wants an
// assistant's reasoning_content back while the tool loop of that same question runs, and
// not for questions already answered — so it is kept only after the last user message.
func requestMessages(history []message) []message {
	last := -1
	for i, m := range history {
		if m.Role == "user" {
			last = i
		}
	}
	out := make([]message, len(history))
	for i, m := range history {
		if i < last {
			m.ReasoningContent = ""
		}
		out[i] = m
	}
	return out
}
