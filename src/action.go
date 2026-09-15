package autonomy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaulie/autonomy/src/capability"
)

// Action is one concrete execution of a capability against a target. Execute runs
// it and returns the action's record — the effective input and the raw output —
// which Runtime.Execute collects into the cycle's Result so the next decision can
// see what actually happened (previous_actions). An error means the action
// failed; the record still carries the input and whatever output it produced.
type Action interface {
	Execute(ctx DecisionContext) (ActionResult, error)
}

// NothingAction is a plan step that executes nothing: a noop/none step, or one
// naming a capability this runtime does not have (Reason says which, so a plan
// that lost a step is visible instead of silent).
type NothingAction struct {
	Reason string
}

func (a NothingAction) Execute(ctx DecisionContext) (ActionResult, error) {
	record := ActionResult{Capability: "nothing"}
	if reason := strings.TrimSpace(a.Reason); reason != "" {
		fmt.Printf("NothingAction: %s\n", reason)
		// Why nothing ran is the only thing this action can report.
		record.Output = map[string]string{"reason": reason}
		return record, nil
	}
	fmt.Println("NothingAction: Execute")
	return record, nil
}

// CapabilityAction looks up a registered capability and runs it.
type CapabilityAction struct {
	Name  string
	Input map[string]string
	// ExpectedEffect and EvidenceRefs come from the plan step (AGENT_V2): what the
	// step is meant to change, and which evidence items justified it.
	ExpectedEffect string
	EvidenceRefs   []string
}

func (a CapabilityAction) Execute(ctx DecisionContext) (ActionResult, error) {
	record := ActionResult{
		Capability:     strings.ToLower(strings.TrimSpace(a.Name)),
		ExpectedEffect: a.ExpectedEffect,
		EvidenceRefs:   a.EvidenceRefs,
	}
	f := activeCapabilityFactory()
	if f == nil {
		return record, fmt.Errorf("capability factory not ready")
	}
	name := strings.ToLower(strings.TrimSpace(a.Name))
	cap := f.Get(name)
	if cap == nil {
		return record, fmt.Errorf("unknown capability %q", a.Name)
	}
	in := copyStringMap(a.Input)
	// The runtime deliberately does not default a workspace into the input: a
	// capability that acts in place gets the workspace the plan gives it, and one
	// that delegates to another agent (code_edit) must let that agent work in its
	// own workspace instead of the caller's.
	if ctx.Task != nil {
		if in["task_id"] == "" && ctx.Task.ID != "" {
			in["task_id"] = ctx.Task.ID
		}
		if in["instruction"] == "" && in["goal"] == "" && ctx.Task.Description != "" {
			in["instruction"] = ctx.Task.Description
		}
	}
	// The record keeps the input the capability was actually called with, not the
	// plan's raw step input: the task defaults the runtime added are part of what
	// ran.
	record.Input = in
	out, err := cap.Run(in)
	// A failed capability may still have produced something; keep it verbatim.
	record.Output = out
	if err != nil {
		return record, err
	}
	fmt.Printf("CapabilityAction %s: %v\n", name, out)
	return record, nil
}

func activeCapabilityFactory() *capability.Factory {
	if _autonomy != nil {
		return _autonomy.CapabilityFactory
	}
	return nil
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ensureAgentWorkspace creates AGENT_WORKSPACE for the agent name and returns the path with trailing separator.
func ensureAgentWorkspace(agentName string) (string, error) {
	ws := AgentWorkspacePath(agentName)
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return "", fmt.Errorf("mkdir AGENT_WORKSPACE %s: %w", ws, err)
	}
	abs, err := filepath.Abs(ws)
	if err != nil {
		return ws, nil
	}
	if len(abs) > 0 && !os.IsPathSeparator(abs[len(abs)-1]) {
		abs += string(os.PathSeparator)
	}
	return abs, nil
}
