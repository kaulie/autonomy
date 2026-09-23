package cursor

import (
	"context"
	"fmt"
	"github.com/kaulie/autonomy/src/llmbackend"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

// cursorSession is the llmbackend.Cursor backend's session: one llmbackend.Cursor agent, attached through the
// process-wide bridge client, re-attached by the id the agent row recorded.
type cursorSession struct {
	host    llmbackend.Host
	agent   *cursorsdk.Agent
	resumed bool
}

func newCursorSession(host llmbackend.Host) *cursorSession { return &cursorSession{host: host} }

// attach opens the llmbackend.Cursor agent: the recorded one when the provider still has it, a fresh
// one when it does not (a session that expired while the runtime was down is a reason to
// start talking again, not a reason to fail the turn). Idempotent: an attached session is
// left alone.
func (c *cursorSession) Attach(ctx context.Context, _ llmbackend.Mode) (bool, error) {
	if c.agent != nil {
		return c.resumed, nil
	}
	facts := c.host.Facts()
	cwd := facts.Workspace
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	model := facts.Model
	if model == "" {
		model = DefaultCursorModel()
	}
	client := cursorClient()
	if err := client.Ping(ctx); err != nil {
		return false, fmt.Errorf("cursor bridge ping: %w", llmbackend.BridgeCallErr(ctx, err))
	}

	resumed := strings.TrimSpace(facts.SessionID) != "" && !facts.Ephemeral
	var (
		agent *cursorsdk.Agent
		err   error
	)
	create := func() error {
		agent, err = client.Agents().Create(ctx, cursorsdk.CreateOptions{Model: model, CWD: cwd})
		if err != nil {
			return fmt.Errorf("create cursor agent: %w", llmbackend.BridgeCallErr(ctx, err))
		}
		return nil
	}
	if resumed {
		agent, err = client.Agents().Resume(ctx, facts.SessionID, model)
		if err != nil {
			// The provider no longer has this session. Remember that on the row (so the
			// next process does not try it again), then open a fresh one.
			fmt.Fprintf(os.Stderr, "[autonomy] cursor session %s of agent %s is gone (%v); starting a new one\n",
				facts.SessionID, facts.Name, err)
			c.host.SetSessionID("")
			resumed = false
			if err := create(); err != nil {
				return false, err
			}
		}
	} else if err := create(); err != nil {
		return false, err
	}

	c.agent, c.resumed = agent, resumed
	host := c.host
	host.SetWorkspace(cwd)
	host.SetModel(model)
	host.SetSessionID(agent.ID)
	// A resumed session already holds the reasoning frame from its earlier turns; a fresh
	// one starts empty and needs it on its first decision cycle (Agent.needsLLMFrame).
	host.SetFrameSent(resumed)
	host.SetBackend(llmbackend.Cursor, llmbackend.ProviderCursor)
	host.Persist()
	return resumed, nil
}

// prompt sends one turn and streams the provider's run events as neutral Events.
func (c *cursorSession) Prompt(ctx context.Context, text string, _ llmbackend.Mode, onEvent func(llmbackend.Event)) (string, llmbackend.RunResult, error) {
	if c.agent == nil {
		return "", llmbackend.RunResult{Status: llmbackend.StatusError, ErrorMessage: "cursor session not attached"}, llmbackend.ErrNoSession
	}
	facts := c.host.Facts()
	provider := facts.Provider
	if provider == "" {
		provider = llmbackend.ProviderCursor
	}
	started := time.Now()
	run, err := c.agent.Send(ctx, text)
	if err != nil {
		return "", llmbackend.RunResult{
			Status: llmbackend.StatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now(),
		}, fmt.Errorf("cursor send: %w", err)
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
			meta = cursorRunResultToLLMRun(*result, started)
		}
		return "", meta, fmt.Errorf("cursor wait: %w", err)
	}
	out := strings.TrimSpace(result.Text)
	meta := cursorRunResultToLLMRun(*result, started)
	if out == "" {
		// A run that answered nothing is a failed run, whatever the provider calls it: an
		// exhausted account ends a session as "finished" with a message and no text at
		// all. Recording that as finished would leave the only account of the failure in
		// the run's message.
		meta.Status = llmbackend.StatusError
		return "", meta, llmbackend.EmptyModelResponseErr(result.Status, result.ErrorMessage)
	}
	return out, meta, nil
}

// dispose puts the llmbackend.Cursor agent down: Delete for a one-shot worker (and the row stops
// naming it), Close for a resident agent — durable state is kept on the provider's side,
// which is what a later Resume re-attaches.
func (c *cursorSession) Dispose(ctx context.Context, ephemeral bool) {
	if c.agent == nil {
		return
	}
	id := c.agent.ID
	host := c.host
	if ephemeral {
		if err := c.agent.Delete(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] DeleteAgent %s failed: %v\n", id, err)
		} else {
			fmt.Fprintf(os.Stderr, "[autonomy] DeleteAgent %s ok\n", id)
		}
		host.SetSessionID("")
	} else if err := c.agent.Close(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] CloseAgent %s failed: %v\n", id, err)
	}
	c.agent = nil
	// The shared bridge client is process-wide and must NOT be closed here.
}

func (c *cursorSession) SessionID() string {
	if c.agent == nil {
		return ""
	}
	return c.agent.ID
}

func (c *cursorSession) Mode() string { return "" }

func (c *cursorSession) Resumed() bool { return c.resumed }

// resumedFrom is the session this attach was asked to continue, when it did re-attach one.
func (c *cursorSession) ResumedFrom() string {
	if !c.resumed {
		return ""
	}
	return c.host.Facts().SessionID
}
