package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
	"github.com/kaulie/autonomy/src/llmrun"
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
		model = llmbackend.DefaultCursorModel()
	}

	client := llmbackend.CursorClient()
	if err := client.Ping(ctx); err != nil {
		return fmt.Errorf("cursor bridge ping: %w", bridgeCallErr(ctx, err))
	}

	var (
		cAgent *cursorsdk.Agent
		err    error
	)
	resumed := a.LLMAgentID != "" && !a.IsEphemeral()
	if resumed {
		cAgent, err = client.Agents().Resume(ctx, a.LLMAgentID, model)
		if err != nil {
			return fmt.Errorf("resume cursor agent: %w", bridgeCallErr(ctx, err))
		}
	} else {
		cAgent, err = client.Agents().Create(ctx, cursorsdk.CreateOptions{
			Model: model,
			CWD:   cwd,
		})
		if err != nil {
			return fmt.Errorf("create cursor agent: %w", bridgeCallErr(ctx, err))
		}
	}
	a.cursorAgent = cAgent
	a.LLMAgentID = cAgent.ID
	// A resumed session already holds the reasoning frame from its earlier turns;
	// a freshly created one starts empty and needs it on its first decision cycle
	// (see Agent.needsLLMFrame).
	a.llmFrameSent = resumed
	a.Backend = llmbackend.Cursor
	a.LLMProvider = llmbackend.ProviderCursor
	a.Model = model
	persistAgent(a)
	return nil
}

// bridgeCallErr is a failed session-setup call on the bridge, read against the
// context this run gave it. When the call ended because that context ended, the
// reason it ended is the answer — a run cut for being idle says "run idle for
// 3m0s: no provider activity", where the transport can only report the bare
// cancellation it saw ("context canceled"), which reads like the caller's own
// doing and hides three minutes of silence behind it. It is the same reading the
// stream paths already make (src/cursorsdk/run.go → llmrun.CtxErr).
//
// A call that failed while the run was still alive keeps its own error: that is
// the bridge's word about the bridge, and nothing here improves on it.
func bridgeCallErr(ctx context.Context, err error) error {
	if err == nil || ctx == nil || ctx.Err() == nil {
		return err
	}
	return llmrun.CtxErr(ctx)
}

// PromptCursor sends a prompt on the attached Cursor session and waits for the result.
func (a *Agent) PromptCursor(ctx context.Context, prompt string) (string, error) {
	text, _, err := a.PromptCursorStream(ctx, prompt, nil)
	return text, err
}

// PromptCursorStream sends a prompt and streams the provider's run events to
// onEvent as neutral LLMEvents while the run is live, returning the final text
// and run-level metadata. onEvent may be nil; the stream is still drained.
func (a *Agent) PromptCursorStream(ctx context.Context, prompt string, onEvent func(llmbackend.Event)) (string, llmbackend.RunResult, error) {
	if a == nil || a.cursorAgent == nil {
		return "", llmbackend.RunResult{}, fmt.Errorf("cursor session not attached")
	}
	started := time.Now()
	run, err := a.cursorAgent.Send(ctx, prompt)
	if err != nil {
		return "", llmbackend.RunResult{
			Status: llmbackend.StatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now(),
		}, fmt.Errorf("cursor send: %w", err)
	}
	provider := a.LLMProvider
	if provider == "" {
		provider = llmbackend.ProviderCursor
	}
	sink := func(native cursorsdk.RunEvent) {
		if onEvent == nil {
			return
		}
		if ev, ok := llmbackend.MapNativeLLMEvent(provider, native, started); ok {
			onEvent(ev)
		}
	}
	result, err := run.WaitStream(ctx, sink)
	if err != nil {
		meta := llmbackend.RunResult{Status: llmbackend.StatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now()}
		if result != nil {
			meta = llmbackend.CursorRunResultToLLMRun(*result, started)
		}
		return "", meta, fmt.Errorf("cursor wait: %w", err)
	}
	text := strings.TrimSpace(result.Text)
	meta := llmbackend.CursorRunResultToLLMRun(*result, started)
	if text == "" {
		// A run that answered nothing is a failed run, whatever the provider calls
		// it: an exhausted account ends a session as "finished" with a message and
		// no text at all. Recording that as finished would leave the only account
		// of the failure in the run's message.
		meta.Status = llmbackend.StatusError
		return "", meta, emptyModelResponseErr(result.Status, result.ErrorMessage)
	}
	return text, meta, nil
}

// ensureCursorSession attaches Cursor for LLMReasoner. It is the turn's door onto
// resumeCursorSession (reuses AttachCursor): the session the agent was recorded
// with when the provider still has it, a fresh one when it is gone — an agent whose
// session expired while the runtime was idle is a reason to start talking again,
// not a reason to fail the turn.
func (a *Agent) ensureCursorSession(ctx context.Context, model, cwd string) (*cursorsdk.Agent, error) {
	if cwd != "" {
		a.Workspace = cwd
	}
	if model != "" {
		a.Model = model
	}
	if _, err := a.resumeCursorSession(ctx); err != nil {
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
