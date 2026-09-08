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
// All Cursor-backed agents must go through this path so NewClient appears only via newCursorClient.
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

	client := a.cursorClient
	if client == nil {
		client = newCursorClient(cwd)
		a.cursorClient = client
	}
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
	if a == nil || a.cursorAgent == nil {
		return "", fmt.Errorf("cursor session not attached")
	}
	run, err := a.cursorAgent.Send(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("cursor send: %w", err)
	}
	result, err := run.Wait(ctx)
	if err != nil {
		return "", fmt.Errorf("cursor wait: %w", err)
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		return "", fmt.Errorf("empty model response (status=%s msg=%s)", result.Status, result.ErrorMessage)
	}
	return text, nil
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
	if a.cursorClient != nil {
		if err := a.cursorClient.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] cursor client Close failed: %v\n", err)
		}
		a.cursorClient = nil
	}
}
