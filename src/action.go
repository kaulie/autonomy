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
	// Name is the capability to call.
	Name string
	// StepName is what the plan calls this step, so another step's input can bind
	// to it ("step:<name>.output.<key>"). It is empty for a plan that names none.
	StepName string
	// Inputs are the values the step calls the capability with: literals, or
	// bindings to an earlier step's output / a World Model value (src/plan_input.go).
	Inputs map[string]StepInput
	// ExpectedEffect and EvidenceRefs come from the plan step (AGENT_V2): what the
	// step is meant to change, and which evidence items justified it.
	ExpectedEffect string
	EvidenceRefs   []string
}

func (a CapabilityAction) Execute(ctx DecisionContext) (ActionResult, error) {
	record := ActionResult{
		Capability:     strings.ToLower(strings.TrimSpace(a.Name)),
		StepName:       strings.TrimSpace(a.StepName),
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
	// The step's inputs are what the plan bound: literals as written, bindings
	// resolved against the steps that already ran in this plan and the World Model.
	// A binding that cannot be read fails the step here, before the capability is
	// called — the report names the input and the source (src/plan_lineage.go).
	in, err := resolveStepInputs(a.Inputs, ctx.StepOutputs)
	if err != nil {
		record.Input = in
		record.Error = err.Error()
		return record, err
	}
	// The runtime adds no input of its own except the task id, and only for a
	// capability that declares it (code_edit attributes its worker's turns with
	// it). Everything else a step takes has to be in the plan: an input no step
	// bound is not looked up anywhere, and one the runtime would have to invent is
	// not invented (see docs/execution-step.md, §Runtime Responsibility).
	fillTaskID(name, ctx.Task, in)
	// The record keeps the input the capability was actually called with, not the
	// plan's raw step input: a resolved binding is part of what ran, and the plan row
	// keeps the binding (docs/execution-step.md).
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

// fillTaskID adds the one input the runtime supplies on its own: the id of the task
// the call is being made for, and only to a capability that *declares* `task_id`
// (code_edit / deployment.monitor hand it to the worker they acquire, so the worker's
// agent row and every reason_turns row it produces belong to that task).
//
// Every call this runtime makes on a task's behalf goes through here — a plan step and
// a verification reader alike. A verification reader is the same act as a step
// (docs/verification.md: the runtime fills the slot and asks the authoritative source),
// so an agent-backed reader acquired by verification lands under the same task as the
// step that produced the evidence it is judging; calling a capability around this is
// how a run ends up with turns that belong to nobody.
//
// A nil task (or one with no id) adds nothing, and an input the caller already set is
// never overwritten: what the plan bound wins.
func fillTaskID(name string, task *Task, in map[string]string) {
	if task == nil || in == nil || strings.TrimSpace(task.ID) == "" || in["task_id"] != "" {
		return
	}
	if fields, declared := declaredInputsOf(name); declared && fieldAccepts(fields, "task_id") {
		in["task_id"] = task.ID
	}
}

func activeCapabilityFactory() *capability.Factory {
	if _autonomy != nil {
		return _autonomy.CapabilityFactory
	}
	return nil
}

// ensureAgentWorkspace creates AGENT_WORKSPACE for the agent name and returns the path with trailing separator.
func ensureAgentWorkspace(agentName string) (string, error) {
	return ensureAgentWorkspaceIn(DefaultAgentWorkspaceRoot, agentName)
}

// ensureAgentWorkspaceIn is ensureAgentWorkspace under a specific root (an account's own root,
// when the agent runs on one).
func ensureAgentWorkspaceIn(root, agentName string) (string, error) {
	ws := AgentWorkspacePathIn(root, agentName)
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
