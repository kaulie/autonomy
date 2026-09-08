package autonomy

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

// CursorCodeEditor runs one-shot Cursor agents for code_edit.
type CursorCodeEditor struct {
	Model string
}

func (e CursorCodeEditor) RunEdit(ctx context.Context, workspace, instruction string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", fmt.Errorf("empty workspace")
	}
	if strings.TrimSpace(instruction) == "" {
		return "", fmt.Errorf("empty instruction")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return "", fmt.Errorf("mkdir workspace: %w", err)
	}

	model := strings.TrimSpace(e.Model)
	if model == "" {
		model = os.Getenv("AUTONOMY_LLM_MODEL")
	}
	if model == "" {
		model = "composer-2"
	}

	timeout := llmTimeout()
	goCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := cursorsdk.NewClient(
		cursorsdk.WithAPIKey(os.Getenv("CURSOR_API_KEY")),
		cursorsdk.WithWorkspace(workspace),
		cursorsdk.WithBridgeBin(os.Getenv("CURSOR_SDK_BRIDGE_BIN")),
	)
	defer func() { _ = client.Close() }()

	if err := client.Ping(goCtx); err != nil {
		return "", fmt.Errorf("cursor ping: %w", err)
	}

	agent, err := client.Agents().Create(goCtx, cursorsdk.CreateOptions{
		Model: model,
		CWD:   workspace,
	})
	if err != nil {
		return "", fmt.Errorf("create agent: %w", err)
	}
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		if err := agent.Delete(dctx); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] code_edit DeleteAgent %s failed: %v\n", agent.ID, err)
		}
	}()

	prompt := fmt.Sprintf(`You are editing a software project at workspace:
%s

Implement the following feature by modifying the codebase as needed. Make only necessary changes. Prefer small, focused edits.

Feature / instruction:
%s

When done, briefly summarize which files you changed and why.`, workspace, instruction)

	run, err := agent.Send(goCtx, prompt)
	if err != nil {
		return "", fmt.Errorf("send: %w", err)
	}
	result, err := run.Wait(goCtx)
	if err != nil {
		return "", fmt.Errorf("wait: %w", err)
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		return "", fmt.Errorf("empty model response (status=%s msg=%s)", result.Status, result.ErrorMessage)
	}
	return text, nil
}
