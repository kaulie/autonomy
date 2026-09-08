package autonomy

import (
	"context"
	"fmt"
	"strings"

	"github.com/kaulie/autonomy/src/capability"
	sd "github.com/kaulie/autonomy/src/capability/software_development"
)

// Runtime reliably executes actions and exposes agent acquisition to capabilities.
type Runtime struct {
	caps   map[string]capability.Capability
	world  *World
	agents *AgentFactory
}

func NewRuntime(agents *AgentFactory, caps ...capability.Capability) *Runtime {
	m := make(map[string]capability.Capability, len(caps))
	for _, c := range caps {
		m[c.Name()] = c
	}
	return &Runtime{caps: m, agents: agents}
}

// SetCapabilities replaces the capability lookup table (used after RegisterDefaults).
func (r *Runtime) SetCapabilities(caps ...capability.Capability) {
	m := make(map[string]capability.Capability, len(caps))
	for _, c := range caps {
		m[c.Name()] = c
	}
	r.caps = m
}

func (r *Runtime) Execute(decision Decision) (Result, error) {
	action := decision.Action
	err := action.Execute(decision.Ctx)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Message: "Action executed",
	}, nil
}

// AcquireAgent registers an agent via AgentFactory and attaches the requested backend.
// Capabilities call this instead of creating Cursor clients themselves.
func (r *Runtime) AcquireAgent(ctx context.Context, opts sd.AcquireAgentOpts) (sd.AgentSession, error) {
	if r == nil || r.agents == nil {
		return nil, fmt.Errorf("runtime agent factory not ready")
	}
	purpose := strings.TrimSpace(opts.Purpose)
	if purpose == "" {
		purpose = "cap"
	}
	agent := r.agents.NewAgent(newAgentID(purpose))
	agent.Lifecycle = AgentLifecycleEphemeral
	if ws := strings.TrimSpace(opts.Workspace); ws != "" {
		agent.Workspace = ws
	}
	backend := strings.ToLower(strings.TrimSpace(opts.Backend))
	if backend == "" {
		backend = string(AgentBackendCursor)
	}
	switch AgentBackend(backend) {
	case AgentBackendCursor:
		if err := agent.AttachCursor(ctx, opts.Model); err != nil {
			r.releaseRegistered(agent)
			return nil, err
		}
	case AgentBackendLocal:
		// Local-only session: no Cursor attach; Prompt is unsupported.
	default:
		r.releaseRegistered(agent)
		return nil, fmt.Errorf("unknown agent backend %q", opts.Backend)
	}
	agent.Start()
	return &runtimeAgentSession{rt: r, agent: agent}, nil
}

func (r *Runtime) releaseRegistered(agent *Agent) {
	if agent == nil {
		return
	}
	agent.disposeCursorSession(context.Background())
	softDeleteAgent(agent.ID)
	r.agents.Delete(agent.ID)
}

type runtimeAgentSession struct {
	rt    *Runtime
	agent *Agent
}

func (s *runtimeAgentSession) ID() string {
	if s == nil || s.agent == nil {
		return ""
	}
	return s.agent.ID
}

func (s *runtimeAgentSession) Prompt(ctx context.Context, prompt string) (string, error) {
	if s == nil || s.agent == nil {
		return "", fmt.Errorf("nil agent session")
	}
	if s.agent.Backend != AgentBackendCursor {
		return "", fmt.Errorf("prompt unsupported for backend %q", s.agent.Backend)
	}
	timeout := llmTimeout()
	goCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.agent.PromptCursor(goCtx, prompt)
}

func (s *runtimeAgentSession) Release(ctx context.Context) error {
	if s == nil || s.agent == nil || s.rt == nil {
		return nil
	}
	s.agent.Stop()
	if ctx == nil {
		ctx = context.Background()
	}
	s.agent.disposeCursorSession(ctx)
	if s.agent.IsEphemeral() {
		softDeleteAgent(s.agent.ID)
		s.rt.agents.Delete(s.agent.ID)
	} else {
		persistAgent(s.agent)
	}
	s.agent = nil
	return nil
}

var _ sd.AgentBroker = (*Runtime)(nil)
