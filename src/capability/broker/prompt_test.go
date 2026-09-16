package broker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability/broker"
)

// frameSession is an acquired agent whose host can describe the runtime it was
// acquired for (broker.WorkerPromptContext).
type frameSession struct{ frame map[string]string }

func (s *frameSession) ID() string                                     { return "agent-frame-1" }
func (s *frameSession) Workspace() string                              { return "/sandbox/agent-frame-1/" }
func (s *frameSession) Prompt(context.Context, string) (string, error) { return "", nil }
func (s *frameSession) Release(context.Context) error                  { return nil }
func (s *frameSession) WorkerPlaceholders() map[string]string          { return s.frame }

// plainSession is an acquired agent whose host cannot describe one: a test
// double, or a host without a World.
type plainSession struct{}

func (s *plainSession) ID() string                                     { return "agent-plain-1" }
func (s *plainSession) Workspace() string                              { return "/sandbox/agent-plain-1/" }
func (s *plainSession) Prompt(context.Context, string) (string, error) { return "", nil }
func (s *plainSession) Release(context.Context) error                  { return nil }

// TestWorkerFrameIsWhatTheHostSaid: the frame values come from the session's host
// (the runtime that delegated), and a host that cannot describe a runtime yields
// none — the capability then renders the frame as missing rather than inventing
// one.
func TestWorkerFrameIsWhatTheHostSaid(t *testing.T) {
	host := &frameSession{frame: map[string]string{"{{WORLD}}": `{"assets":[]}`}}
	if got := broker.WorkerFrame(host)["{{WORLD}}"]; got != `{"assets":[]}` {
		t.Fatalf("frame=%q, want the host's world", got)
	}
	if got := broker.WorkerFrame(&plainSession{}); got != nil {
		t.Fatalf("frame=%v, want none from a host that has no runtime to describe", got)
	}
	var none broker.AgentSession
	if got := broker.WorkerFrame(none); got != nil {
		t.Fatalf("frame=%v, want none without a session", got)
	}
}

// TestRenderWorkerPrompt: what a capability owns always wins, the host's frame
// fills the rest, a frame section nobody filled reads as a gap, and a placeholder
// outside the vocabulary is left alone so a template typo stays visible.
func TestRenderWorkerPrompt(t *testing.T) {
	const tmpl = "workspace={{WORKSPACE}}\ngoal={{GOAL}}\nworld={{WORLD}}\nrc={{RUNTIME_CONTEXT}}\n" +
		"constraints={{CONSTRAINTS}}\nunknown={{NOT_A_PLACEHOLDER}}\n"
	own := map[string]string{
		"{{WORKSPACE}}": "/sandbox/agent-1/",
		"{{GOAL}}":      "add a health endpoint",
	}
	frame := map[string]string{
		"{{WORLD}}":           `{"assets":[{"id":"asset-1"}]}`,
		"{{RUNTIME_CONTEXT}}": "",
		// The host does not get to decide what the capability owns.
		"{{WORKSPACE}}": "/sandbox/someone-else/",
	}

	got := broker.RenderWorkerPrompt(tmpl, own, frame)
	for _, want := range []string{
		"workspace=/sandbox/agent-1/",
		"goal=add a health endpoint",
		`world={"assets":[{"id":"asset-1"}]}`,
		"rc=" + broker.WorkerFrameMissingValue,
		"constraints=" + broker.WorkerFrameMissingValue,
		"unknown={{NOT_A_PLACEHOLDER}}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/sandbox/someone-else/") {
		t.Errorf("the host overrode the capability's own value:\n%s", got)
	}
}

// TestWorkerFrameVocabulary: the placeholders a worker prompt may use are the
// planner's frame vocabulary, spelled the one way a template can use them.
func TestWorkerFrameVocabulary(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range broker.WorkerFramePlaceholders {
		if !strings.HasPrefix(k, "{{") || !strings.HasSuffix(k, "}}") {
			t.Fatalf("placeholder %q is not written as {{NAME}}", k)
		}
		if seen[k] {
			t.Fatalf("placeholder %q is listed twice", k)
		}
		seen[k] = true
	}
	for _, want := range []string{
		"{{TASK}}", "{{CONTEXT_ENTITY}}", "{{GOAL_TYPE}}", "{{WORLD}}",
		"{{RUNTIME_CONTEXT}}", "{{COMPLETION_PRINCIPLES}}", "{{CONSTRAINTS}}", "{{CONSTRUCTS}}",
	} {
		if !seen[want] {
			t.Errorf("the worker frame vocabulary no longer has %s", want)
		}
	}
}
