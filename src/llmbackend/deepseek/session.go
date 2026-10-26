// Package deepseek is the native DeepSeek harness: Go talks to DeepSeek's OpenAI-compatible
// Chat Completions API directly (no Node bridge, no CLI) and owns what those harnesses get
// from their SDK — the conversation, the tools, and the loop that runs them.
package deepseek

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// DefaultModel is what a session runs on when neither the task nor the account names one.
const DefaultModel = "deepseek-v4-pro"

// defaultMaxIterations bounds one turn's tool loop: requests to the model, not tool calls.
const defaultMaxIterations = 50

// maxIterations is AUTONOMY_DEEPSEEK_MAX_ITERATIONS, or the default.
func maxIterations() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("AUTONOMY_DEEPSEEK_MAX_ITERATIONS"))); err == nil && n > 0 {
		return n
	}
	return defaultMaxIterations
}

// session is one agent's DeepSeek conversation(s). Like the claude harness it keeps one
// conversation per (mode, workspace): a planner's read-only cycles and a worker's turns
// never share a history.
type session struct {
	mu      sync.Mutex
	host    llmbackend.Host
	convs   map[string]*transcript
	key     string
	cwd     string
	mode    llmbackend.Mode
	conv    *transcript
	from    string
	resumed bool
}

func newSession(host llmbackend.Host) *session {
	return &session{host: host, convs: make(map[string]*transcript)}
}

// model is the id this session runs on: the task's choice outranks the account's
// (the same order as the claude harness), and the harness default comes last.
func (s *session) model() string {
	facts := s.host.Facts()
	return llmbackend.FirstNonEmptyString(facts.Model, facts.Creds.Model, DefaultModel)
}

func (s *session) Attach(ctx context.Context, mode llmbackend.Mode) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attach(ctx, mode)
}

func (s *session) attach(ctx context.Context, mode llmbackend.Mode) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	facts := s.host.Facts()
	// Credentials come from the agent's pool account and nowhere else.
	if strings.TrimSpace(facts.Creds.APIKey) == "" {
		return false, fmt.Errorf("deepseek: the agent's account has no API key (add one at /accounts)")
	}
	cwd := facts.Workspace
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return false, err
		}
	}
	key := string(mode) + "\x00" + cwd
	if key == s.key && s.conv != nil {
		return s.resumed, nil
	}
	conv, seen := s.convs[key]
	from, resumed := "", false
	if !seen {
		// The recorded session is the planner's (the agent row keeps only that one, as for
		// every harness), so only a first plan attach of a kept agent continues it.
		if s.key == "" && mode == llmbackend.ModePlan && !facts.Ephemeral && facts.SessionID != "" {
			from = facts.SessionID
			stored, ok, err := loadTranscript(from)
			if err != nil {
				return false, err
			}
			if ok {
				conv, resumed = &stored, true
			}
		}
		if conv == nil {
			// A session the store no longer has is a reason to start again, not to fail.
			conv, from = &transcript{ID: newSessionID(), Workspace: cwd}, ""
		}
		s.convs[key] = conv
	} else {
		from, resumed = conv.ID, len(conv.Messages) > 0
	}
	s.key, s.cwd, s.mode, s.conv, s.from, s.resumed = key, cwd, mode, conv, from, resumed
	s.host.SetBackend(llmbackend.Deepseek, llmbackend.ProviderDeepseekHarness)
	s.host.SetWorkspace(cwd)
	s.host.SetModel(s.model())
	if !resumed {
		s.host.SetFrameSent(false)
	}
	if mode == llmbackend.ModePlan && !facts.Ephemeral && facts.SessionID != conv.ID {
		s.host.SetSessionID(conv.ID)
		s.host.Persist()
	}
	return resumed, nil
}

// Dispose drops the conversations from memory; a one-shot worker's stored ones go too.
func (s *session) Dispose(_ context.Context, ephemeral bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ephemeral {
		for _, conv := range s.convs {
			deleteTranscript(conv.ID)
		}
	}
	s.convs = make(map[string]*transcript)
	s.key, s.cwd, s.mode, s.conv, s.from, s.resumed = "", "", "", nil, "", false
}

func (s *session) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conv == nil {
		return ""
	}
	return s.conv.ID
}

func (s *session) Mode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.mode)
}

func (s *session) Resumed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resumed
}

func (s *session) ResumedFrom() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.from
}
