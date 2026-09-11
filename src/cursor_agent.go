package autonomy

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

// AttachCursor binds a Cursor SDK session to this autonomy agent (registered in AgentFactory).
// All Cursor-backed agents must go through this path so the shared bridge client
// (one per process) is the only bridge used.
func (a *Agent) AttachCursor(ctx context.Context, model string) error {
	if a == nil {
		return fmt.Errorf("nil agent")
	}
	if a.cursorAgent != nil {
		return nil
	}
	cwd := a.Workspace
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if model == "" {
		model = defaultCursorModel()
	}

	client := sharedCursorClient()
	if err := client.Ping(ctx); err != nil {
		return fmt.Errorf("cursor bridge ping: %w", err)
	}

	var (
		cAgent *cursorsdk.Agent
		err    error
	)
	if a.LLMAgentID != "" && !a.IsEphemeral() {
		cAgent, err = client.Agents().Resume(ctx, a.LLMAgentID, model)
		if err != nil {
			return fmt.Errorf("resume cursor agent: %w", err)
		}
	} else {
		cAgent, err = client.Agents().Create(ctx, cursorsdk.CreateOptions{
			Model: model,
			CWD:   cwd,
		})
		if err != nil {
			return fmt.Errorf("create cursor agent: %w", err)
		}
	}
	a.cursorAgent = cAgent
	a.LLMAgentID = cAgent.ID
	a.Backend = AgentBackendCursor
	a.LLMProvider = LLMProviderCursor
	a.Model = model
	persistAgent(a)
	return nil
}

// PromptCursor sends a prompt on the attached Cursor session and waits for the result.
func (a *Agent) PromptCursor(ctx context.Context, prompt string) (string, error) {
	text, _, err := a.PromptCursorStream(ctx, prompt, nil)
	return text, err
}

// PromptCursorStream sends a prompt and streams the provider's run events to
// onEvent as neutral LLMEvents while the run is live, returning the final text
// and run-level metadata. onEvent may be nil; the stream is still drained.
func (a *Agent) PromptCursorStream(ctx context.Context, prompt string, onEvent func(LLMEvent)) (string, LLMRunResult, error) {
	if a == nil || a.cursorAgent == nil {
		return "", LLMRunResult{}, fmt.Errorf("cursor session not attached")
	}
	started := time.Now()
	run, err := a.cursorAgent.Send(ctx, prompt)
	if err != nil {
		return "", LLMRunResult{
			Status: LLMStatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now(),
		}, fmt.Errorf("cursor send: %w", err)
	}
	provider := a.LLMProvider
	if provider == "" {
		provider = LLMProviderCursor
	}
	sink := func(native cursorsdk.RunEvent) {
		if onEvent == nil {
			return
		}
		if ev, ok := MapNativeLLMEvent(provider, native, started); ok {
			onEvent(ev)
		}
	}
	result, err := run.WaitStream(ctx, sink)
	if err != nil {
		meta := LLMRunResult{Status: LLMStatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now()}
		if result != nil {
			meta = cursorRunResultToLLMRun(*result, started)
		}
		return "", meta, fmt.Errorf("cursor wait: %w", err)
	}
	text := strings.TrimSpace(result.Text)
	meta := cursorRunResultToLLMRun(*result, started)
	if text == "" {
		return "", meta, fmt.Errorf("empty model response (status=%s msg=%s)", result.Status, result.ErrorMessage)
	}
	return text, meta, nil
}

// ensureCursorSession attaches Cursor for LLMReasoner (reuses AttachCursor).
func (a *Agent) ensureCursorSession(ctx context.Context, model, cwd string) (*cursorsdk.Agent, error) {
	if cwd != "" {
		a.Workspace = cwd
	}
	if err := a.AttachCursor(ctx, model); err != nil {
		return nil, err
	}
	return a.cursorAgent, nil
}

// disposeCursorSession ends the Cursor SDK session for this task.
func (a *Agent) disposeCursorSession(ctx context.Context) {
	if a.cursorAgent != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		agentID := a.cursorAgent.ID
		if a.IsEphemeral() {
			if err := a.cursorAgent.Delete(cctx); err != nil {
				fmt.Fprintf(os.Stderr, "[autonomy] DeleteAgent %s failed: %v\n", agentID, err)
			} else {
				fmt.Fprintf(os.Stderr, "[autonomy] DeleteAgent %s ok\n", agentID)
			}
			a.LLMAgentID = ""
		} else {
			if err := a.cursorAgent.Close(cctx); err != nil {
				fmt.Fprintf(os.Stderr, "[autonomy] CloseAgent %s failed: %v\n", agentID, err)
			}
		}
		a.cursorAgent = nil
	}
	// The shared bridge client is process-wide and must NOT be closed here.
}
