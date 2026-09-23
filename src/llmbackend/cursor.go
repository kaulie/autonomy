package llmbackend

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

// cursorSession is the Cursor backend's session: one Cursor agent, attached through the
// process-wide bridge client, re-attached by the id the agent row recorded.
type cursorSession struct {
	session *Session
	agent   *cursorsdk.Agent
	resumed bool
}

func newCursorSession(s *Session) *cursorSession { return &cursorSession{session: s} }

// attach opens the Cursor agent: the recorded one when the provider still has it, a fresh
// one when it does not (a session that expired while the runtime was down is a reason to
// start talking again, not a reason to fail the turn). Idempotent: an attached session is
// left alone.
func (c *cursorSession) attach(ctx context.Context, _ Mode) (bool, error) {
	if c.agent != nil {
		return c.resumed, nil
	}
	facts := c.session.host.Facts()
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
		return false, fmt.Errorf("cursor bridge ping: %w", bridgeCallErr(ctx, err))
	}

	resumed := strings.TrimSpace(facts.SessionID) != "" && !facts.Ephemeral
	var (
		agent *cursorsdk.Agent
		err   error
	)
	create := func() error {
		agent, err = client.Agents().Create(ctx, cursorsdk.CreateOptions{Model: model, CWD: cwd})
		if err != nil {
			return fmt.Errorf("create cursor agent: %w", bridgeCallErr(ctx, err))
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
			c.session.host.SetSessionID("")
			resumed = false
			if err := create(); err != nil {
				return false, err
			}
		}
	} else if err := create(); err != nil {
		return false, err
	}

	c.agent, c.resumed = agent, resumed
	host := c.session.host
	host.SetWorkspace(cwd)
	host.SetModel(model)
	host.SetSessionID(agent.ID)
	// A resumed session already holds the reasoning frame from its earlier turns; a fresh
	// one starts empty and needs it on its first decision cycle (Agent.needsLLMFrame).
	host.SetFrameSent(resumed)
	host.SetBackend(Cursor, ProviderCursor)
	host.Persist()
	return resumed, nil
}

// prompt sends one turn and streams the provider's run events as neutral Events.
func (c *cursorSession) prompt(ctx context.Context, text string, _ Mode, onEvent func(Event)) (string, RunResult, error) {
	if c.agent == nil {
		return "", RunResult{Status: StatusError, ErrorMessage: "cursor session not attached"}, errNoSession
	}
	facts := c.session.host.Facts()
	provider := facts.Provider
	if provider == "" {
		provider = ProviderCursor
	}
	started := time.Now()
	run, err := c.agent.Send(ctx, text)
	if err != nil {
		return "", RunResult{
			Status: StatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now(),
		}, fmt.Errorf("cursor send: %w", err)
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
		meta := RunResult{Status: StatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now()}
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
		meta.Status = StatusError
		return "", meta, emptyModelResponseErr(result.Status, result.ErrorMessage)
	}
	return out, meta, nil
}

// dispose puts the Cursor agent down: Delete for a one-shot worker (and the row stops
// naming it), Close for a resident agent — durable state is kept on the provider's side,
// which is what a later Resume re-attaches.
func (c *cursorSession) dispose(ctx context.Context, ephemeral bool) {
	if c.agent == nil {
		return
	}
	id := c.agent.ID
	host := c.session.host
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

func (c *cursorSession) sessionID() string {
	if c.agent == nil {
		return ""
	}
	return c.agent.ID
}

func (c *cursorSession) mode() string { return "" }

// resumedFrom is the session this attach was asked to continue, when it did re-attach one.
func (c *cursorSession) resumedFrom() string {
	if !c.resumed {
		return ""
	}
	return c.session.host.Facts().SessionID
}
