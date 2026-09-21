package autonomy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const defaultAgentPolicyRel = "src/agent_policy/AGENT_V2.md"

// promptPlaceholders is the placeholder vocabulary a policy or a delegated
// worker prompt is rendered from, as of one decision cycle. AGENT_V2.md (the
// planner, see buildReasoningFrame) and CODE_EDIT.md (a delegated worker, see
// Runtime.WorkerPlaceholders) use the same names — one vocabulary, one source of
// truth; the rest of a file is passed through unchanged.
func promptPlaceholders(ctx DecisionContext, input ReasoningInput) map[string]string {
	constructs := "[]"
	if _autonomy != nil && _autonomy.CapabilityFactory != nil {
		constructs = _autonomy.CapabilityFactory.FormatConstructs()
	}
	goalType := goalTypeOf(ctx.Task)
	return map[string]string{
		"{{AGENT}}":                 fencedJSON(formatAgentIdentityJSON(ctx.Agent)),
		"{{TASK}}":                  fencedJSON(formatTaskJSON(ctx.Task)),
		"{{CONTEXT_ENTITY}}":        fencedJSON(formatContextEntitiesJSON(ctx)),
		"{{GOAL_TYPE}}":             string(goalType),
		"{{WORLD}}":                 fencedJSON(formatWorldJSON(ctx)),
		"{{RUNTIME_CONTEXT}}":       fencedJSON(formatRuntimeContextJSON(ctx, input)),
		"{{COMPLETION_PRINCIPLES}}": CompletionPrinciplesFor(goalType),
		"{{CONSTRAINTS}}":           fencedJSON(constraintsJSON(ctx)),
		"{{CONSTRUCTS}}":            fencedJSON([]byte(constructs)),
	}
}

// WorkerPlaceholders renders the placeholder vocabulary of a delegated worker's
// prompt (src/agent_policy/CODE_EDIT.md, DEPLOYMENT_MONITOR.md) as of the cycle
// that is delegating: the same Agent / World / Runtime Context / Constructs /
// Completion Principles the planner's policy gets, plus the Constraints the
// Runtime holds the agent to.
//
// The agent's own sections are rendered as the *worker's* — its identity, its
// workspace, its sandbox constraints — because a delegated worker is a different
// agent from the one delegating, and the delegating agent's sandbox must never be
// presented as the worker's own. The delegating agent is named as `delegated_by`
// instead.
//
// It is what runtimeAgentSession implements for broker.WorkerPromptContext.
// Outside a decision cycle only the parts that do not depend on one are rendered.
func (r *Runtime) WorkerPlaceholders(worker *Agent) map[string]string {
	if r == nil || r.cycle == nil {
		return workerPromptPlaceholders(DecisionContext{}, worker)
	}
	return workerPromptPlaceholders(*r.cycle, worker)
}

// workerPromptPlaceholders renders promptPlaceholders for the worker agent a
// delegated prompt is addressed to: the frame is the delegating runtime's (World,
// Constructs, Completion Principles), while the two sections that speak about this
// agent itself — its identity ({{AGENT}}), its situation ({{RUNTIME_CONTEXT}}) and
// the constraints it works under ({{CONSTRAINTS}}) — speak for the worker, never
// for the agent that delegated.
func workerPromptPlaceholders(ctx DecisionContext, worker *Agent) map[string]string {
	values := promptPlaceholders(ctx, ReasoningInput{})
	values["{{AGENT}}"] = fencedJSON(formatAgentIdentityJSON(worker))
	values["{{RUNTIME_CONTEXT}}"] = fencedJSON(workerRuntimeContextJSON(ctx))
	workerCtx := ctx
	workerCtx.Agent = worker
	values["{{CONSTRAINTS}}"] = fencedJSON(constraintsJSON(workerCtx))
	return values
}

// goalTypeOf is the task's goal type, defaulting to feature when the task or its
// classification is missing.
func goalTypeOf(task *Task) GoalType {
	if task != nil && strings.TrimSpace(string(task.GoalType)) != "" {
		return task.GoalType
	}
	return GoalType_FEATURE
}

// constraintsJSON is what the Runtime holds an agent to, as data a ## Constraints
// section renders: its own facts about this cycle — the task the work belongs to,
// the sandbox files may be changed in — plus the rules of the runtime's policy
// (src/policy.go, src/agent_policy/CONSTRAINTS.json). No rule of any particular
// domain is written here: deployment, budget or privacy are the policy file's
// words about the world, not this layer's.
func constraintsJSON(ctx DecisionContext) []byte {
	m := map[string]any{}
	for key, value := range policyConstraints() {
		m[key] = value
	}
	// The runtime's own facts come last: its policy cannot redefine the task, nor
	// the sandbox an agent works in.
	m["scope"] = "the files this task touches"
	m["workspace_rule"] = "the only place files may be changed"
	if ctx.Task != nil && strings.TrimSpace(ctx.Task.ID) != "" {
		m["task"] = ctx.Task.ID
	}
	if ctx.Agent != nil && strings.TrimSpace(ctx.Agent.Workspace) != "" {
		m["workspace"] = ctx.Agent.Workspace
	}
	return mustJSON(m)
}

func applyPolicyPlaceholders(policy string, values map[string]string) string {
	for k, v := range values {
		policy = strings.ReplaceAll(policy, k, v)
	}
	return policy
}

// projectRoot returns PROJECT_ROOT; empty or unset is an error for callers that need it.
func projectRoot() (string, error) {
	root := strings.TrimSpace(os.Getenv("PROJECT_ROOT"))
	if root == "" {
		return "", fmt.Errorf("PROJECT_ROOT is required")
	}
	return root, nil
}

// loadAgentPolicy reads $PROJECT_ROOT/src/agent_policy/AGENT_V2.md at runtime.
func loadAgentPolicy() (string, error) {
	root, err := projectRoot()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, defaultAgentPolicyRel)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read agent policy %s: %w", path, err)
	}
	return string(b), nil
}

// A reasoning session is multi-turn: the Cline session / Cursor agent keeps the
// conversation, so AGENT_V2.md is sent once and every later decision cycle sends
// only what changed. The prompt therefore has two halves — a frame and a delta.
//
// reasoningDeltaPlaceholders are the placeholders whose values change per cycle
// (task status, world, context entities, previous actions keep growing): they are
// the delta. reasoningDeltaMarker replaces them inside the frame, so the frame
// stays byte-identical for the session's lifetime while each decision message
// still carries the current values.
const reasoningDeltaMarker = "→ supplied with each decision message (see the Delta block)"

var reasoningDeltaPlaceholders = []string{"{{TASK}}", "{{CONTEXT_ENTITY}}", "{{WORLD}}", "{{RUNTIME_CONTEXT}}", "{{CONSTRAINTS}}"}

// buildReasoningFrame is the stable half of the reasoning prompt: AGENT_V2.md
// with the per-session placeholders (Agent, Goal Type, Completion Principles,
// Constructs) filled in, and the per-cycle placeholders replaced by a marker.
// It is sent once per session — on the first decision cycle after the session
// was created; later cycles send only buildReasoningDelta.
func buildReasoningFrame(ctx DecisionContext, input ReasoningInput) (string, error) {
	raw, err := loadAgentPolicy()
	if err != nil {
		return "", err
	}
	values := promptPlaceholders(ctx, input)
	for _, key := range reasoningDeltaPlaceholders {
		values[key] = reasoningDeltaMarker
	}
	policy := applyPolicyPlaceholders(raw, values)
	if !strings.HasSuffix(policy, "\n") {
		policy += "\n"
	}
	return policy, nil
}

// buildReasoningDelta is the per-cycle half: the current Task / Context Entity /
// World / Runtime Context values (Runtime Context carries the step and
// previous_actions) as one JSON block. It is what every decision cycle sends,
// frame or no frame. The agent's own identity is not here: it does not change
// between cycles, so it is part of the frame.
func buildReasoningDelta(ctx DecisionContext, input ReasoningInput) (string, error) {
	payload := struct {
		Task          json.RawMessage `json:"task"`
		ContextEntity json.RawMessage `json:"context_entity"`
		World         json.RawMessage `json:"world"`
		RuntimeCtx    json.RawMessage `json:"runtime_context"`
		Constraints   json.RawMessage `json:"constraints"`
	}{
		Task:          formatTaskJSON(ctx.Task),
		ContextEntity: formatContextEntitiesJSON(ctx),
		World:         formatWorldJSON(ctx),
		RuntimeCtx:    formatRuntimeContextJSON(ctx, input),
		Constraints:   constraintsJSON(ctx),
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal reasoning delta: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Decision Cycle %d — current values\n", ctx.Cycle)
	fmt.Fprintf(&b, "Current values for the placeholders marked \"%s\" above.\n\n", reasoningDeltaMarker)
	b.WriteString("```json\n")
	b.Write(raw)
	b.WriteString("\n```\n\n")
	b.WriteString("Reply with the AGENT_V2 Output Schema JSON for this cycle.\n")
	return b.String(), nil
}

// buildReasoningPrompt is the message for a session's FIRST decision cycle: the
// frame plus this cycle's delta. Later cycles send only the delta, because the
// session already holds the frame.
func buildReasoningPrompt(ctx DecisionContext, input ReasoningInput) (string, error) {
	frame, err := buildReasoningFrame(ctx, input)
	if err != nil {
		return "", err
	}
	delta, err := buildReasoningDelta(ctx, input)
	if err != nil {
		return "", err
	}
	return frame + "\n" + delta, nil
}

func fencedJSON(raw []byte) string {
	return "```json\n" + string(raw) + "\n```"
}

func mustJSON(v any) []byte {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte("null")
	}
	return raw
}

// agentIdentityMap is one agent's identity as its own prompt states it, in its own
// ## Agent section: what it is (role, and for a worker what it was acquired for),
// which agent it is (id, name), and how it runs (backend, model, lifecycle,
// workspace).
//
// It is the agent's identity, not its situation: the task it works on, the cycle
// it is in, who delegated to it and what that task already did are the Runtime
// Context (see runtimeContextMap) — the identity is what stays true for the whole
// session, which is why it travels in the frame rather than in the per-cycle delta.
func agentIdentityMap(agent *Agent) map[string]any {
	if agent == nil {
		return nil
	}
	lifecycle := string(agent.Lifecycle)
	if lifecycle == "" {
		lifecycle = string(AgentLifecycleEphemeral)
	}
	backend := string(agent.Backend)
	if backend == "" {
		backend = string(AgentBackendLocal)
	}
	m := map[string]any{
		"id":        agent.ID,
		"name":      agent.Name,
		"lifecycle": lifecycle,
		"backend":   backend,
		"workspace": agent.Workspace,
	}
	// An agent whose role was never set is not given one here: the prompt says
	// what the runtime knows, and inventing "worker" for an agent nothing
	// acquired would be a fact nobody stated.
	if role := strings.TrimSpace(string(agent.Role)); role != "" {
		m["role"] = role
	}
	if purpose := strings.TrimSpace(agent.Purpose); purpose != "" {
		m["purpose"] = purpose
	}
	if provider := strings.TrimSpace(string(agent.LLMProvider)); provider != "" {
		m["llm_provider"] = provider
	}
	if model := strings.TrimSpace(agent.Model); model != "" {
		m["model"] = model
	}
	return m
}

func formatAgentIdentityJSON(agent *Agent) []byte {
	return mustJSON(agentIdentityMap(agent))
}

func formatTaskJSON(task *Task) []byte {
	if task == nil {
		return []byte("null")
	}
	return mustJSON(map[string]any{
		"id":          task.ID,
		"domain":      string(task.Domain),
		"description": task.Description,
		"status":      task.Status,
		"goal_type":   string(task.GoalType),
		"context_ref": formatContextRefMap(task.ContextRef),
	})
}

func formatContextRefMap(ref map[ContextContainerType]string) map[string]string {
	out := map[string]string{}
	for ctype, id := range ref {
		if id == "" {
			continue
		}
		out[string(ctype)] = id
	}
	return out
}

func formatRuntimeContextJSON(ctx DecisionContext, input ReasoningInput) []byte {
	return mustJSON(runtimeContextMap(ctx, input))
}

// runtimeContextMap is the Runtime Context of one decision cycle: the runtime's
// context containers, what the task already did, and which cycle this is — the
// situation of the agent, not the agent itself, which has its own section
// (agentIdentityMap).
func runtimeContextMap(ctx DecisionContext, input ReasoningInput) map[string]any {
	m := map[string]any{}
	if ctx.Cycle > 0 {
		m["cycle"] = ctx.Cycle
	}
	if text := strings.TrimSpace(input.Text); text != "" {
		m["additional_input"] = map[string]string{"text": text}
	}
	m["context"] = json.RawMessage(formatRuntimeContextContainersJSON(ctx))
	// completion_contract is the contract this task is judged by, as the planner wrote
	// it: the first answer of the run declares it and the runtime pins it there, so a
	// re-plan is made against the same facts the `done` will be verified against.
	if ctx.Task != nil {
		if contract := completionContractJSON(ctx.Task.ID); len(contract) > 0 {
			m["completion_contract"] = contract
		}
	}
	// previous_actions is what this task already did: the planner's own plan from
	// an earlier cycle and how it went, so re-planning is not blind (AGENT_V2
	// names previous_action as an evidence source).
	if len(ctx.History) > 0 {
		m["previous_actions"] = formatPreviousActionsJSON(ctx.History)
	}
	return m
}

// workerRuntimeContextJSON is the Runtime Context a delegated worker is given: the
// context of the task it was delegated (the task, who delegated it and at which
// cycle, what that task already did) over the World the delegating runtime holds.
// The worker's own identity — its agent, workspace and backend, never the
// delegating agent's — is its own section ({{AGENT}}), not this one.
func workerRuntimeContextJSON(ctx DecisionContext) []byte {
	// A worker's Runtime Context describes the situation it was handed, not a cycle
	// of its own: the cycle that would render here belongs to the agent that
	// delegated (it appears as delegated_by.cycle). The worker's own cycles — it may
	// have several, and need not be a worker forever — are its own, counted from 1
	// on its own run headers (see beginDelegatedTrace).
	workerCtx := ctx
	workerCtx.Cycle = 0
	m := runtimeContextMap(workerCtx, ReasoningInput{})
	if ctx.Task != nil {
		m["task"] = json.RawMessage(formatTaskJSON(ctx.Task))
	}
	if ctx.Agent != nil {
		m["delegated_by"] = map[string]any{"agent": ctx.Agent.Name, "cycle": ctx.Cycle}
	}
	return mustJSON(m)
}

// formatPreviousActionsJSON lists the task's earlier cycles, oldest first. Each
// cycle keeps its actions verbatim — capability, input, output, error, and the
// step's expected_effect/evidence_refs — because the planner, not this layer,
// decides which of them matters.
func formatPreviousActionsJSON(history []Result) []map[string]any {
	out := make([]map[string]any, 0, len(history))
	for i, result := range history {
		entry := map[string]any{
			"cycle":   i + 1,
			"message": result.Message,
			"status":  "ok",
		}
		if result.Err != nil {
			entry["status"] = "failed"
			entry["error"] = result.Err.Error()
		}
		if len(result.Actions) > 0 {
			actions := make([]map[string]any, 0, len(result.Actions))
			for _, a := range result.Actions {
				item := map[string]any{"capability": a.Capability}
				if a.StepName != "" {
					item["step"] = a.StepName
				}
				if len(a.Input) > 0 {
					item["input"] = a.Input
				}
				if len(a.Output) > 0 {
					item["output"] = a.Output
				}
				if a.Error != "" {
					item["error"] = a.Error
				}
				if a.ExpectedEffect != "" {
					item["expected_effect"] = a.ExpectedEffect
				}
				if len(a.EvidenceRefs) > 0 {
					item["evidence_refs"] = a.EvidenceRefs
				}
				actions = append(actions, item)
			}
			entry["actions"] = actions
		}
		out = append(out, entry)
	}
	return out
}

func formatWorldJSON(ctx DecisionContext) []byte {
	type assetJSON struct {
		ID    string `json:"id"`
		Kind  string `json:"kind"`
		State string `json:"state"`
	}
	assets := []assetJSON{}
	if _world != nil && _world.assetManager != nil {
		for _, a := range _world.assetManager.List() {
			assets = append(assets, assetJSON{ID: a.ID, Kind: a.Kind, State: a.State})
		}
	}
	return mustJSON(map[string]any{
		"assets": assets,
	})
}

// formatContextEntitiesJSON is the decision context's `context_entity` block (and the
// {{CONTEXT_ENTITY}} placeholder): what the task's context_ref names, one entry per
// container, as the context builder resolved it before this cycle's prompt
// (src/context_builder) — the project and the organization it belongs to, not just the id
// the task stated.
//
// A ref is resolved into more than itself when the container names a world of its own: a
// ref naming a task carries that task *and* the world the task is written in (its project,
// with that project's organization and services). Those are the sections the builder
// resolved — the refs the task named, and what they expanded to — so the prompt shows the
// world, not the way it was reached.
func formatContextEntitiesJSON(ctx DecisionContext) []byte {
	entries := []map[string]any{}
	if ctx.Task == nil || len(ctx.Task.ContextRef) == 0 {
		return mustJSON(entries)
	}
	if len(ctx.ContextSections) > 0 {
		for _, section := range ctx.ContextSections {
			if section != nil {
				entries = append(entries, section)
			}
		}
		sort.Slice(entries, func(i, j int) bool { return contextSectionID(entries[i]) < contextSectionID(entries[j]) })
		return mustJSON(entries)
	}
	// No builder (a runtime assembled by hand, or AUTONOMY_CONTEXT_BUILDER=0): the refs the
	// task named, answered from this process's own world.
	for ctype, id := range ctx.Task.ContextRef {
		if strings.TrimSpace(id) == "" {
			continue
		}
		entries = append(entries, contextSectionFields(nil, runtimeContextContainers(), string(ctype), id))
	}
	sort.Slice(entries, func(i, j int) bool { return contextSectionID(entries[i]) < contextSectionID(entries[j]) })
	return mustJSON(entries)
}

// runtimeContextContainers is this process's registered world, or nil when there is
// none (a runtime assembled by hand).
func runtimeContextContainers() *ContextContainerManager {
	if _autonomy == nil {
		return nil
	}
	return _autonomy.ContextContainerManager
}

func contextSectionID(section map[string]any) string {
	return contextSectionString(section["id"])
}

func formatRuntimeContextContainersJSON(ctx DecisionContext) []byte {
	type contextEntityJSON struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Type        string `json:"type"`
		Domain      string `json:"domain"`
	}
	type assetJSON struct {
		ID    string `json:"id"`
		Kind  string `json:"kind"`
		State string `json:"state"`
	}
	type containerJSON struct {
		ID              string              `json:"id"`
		Type            string              `json:"type"`
		ContextEntities []contextEntityJSON `json:"context_entities"`
		Entities        []map[string]any    `json:"entities"`
		Assets          []assetJSON         `json:"assets"`
	}

	entries := []containerJSON{}
	if ctx.Task != nil {
		for ctype, id := range ctx.Task.ContextRef {
			if id == "" {
				continue
			}
			entry := containerJSON{
				ID:              id,
				Type:            string(ctype),
				ContextEntities: []contextEntityJSON{},
				Entities:        []map[string]any{},
				Assets:          []assetJSON{},
			}
			if _autonomy != nil && _autonomy.ContextContainerManager != nil {
				if cc, ok := _autonomy.ContextContainerManager.ContextContainers[id]; ok {
					if _autonomy.ContextEntityManager != nil {
						for _, ctxID := range cc.ContextReferences {
							if e, found := _autonomy.ContextEntityManager.ContextEntities[ctxID]; found {
								entry.ContextEntities = append(entry.ContextEntities, contextEntityJSON{
									ID:          e.ID,
									Name:        e.Name,
									Description: e.Description,
									Type:        string(e.ContextContainerType),
									Domain:      string(e.DomainType),
								})
							}
						}
					}
					if _autonomy.DomainEntityManager != nil {
						for _, entityID := range cc.EntityReferences {
							if de, found := _autonomy.DomainEntityManager.DomainEntities[entityID]; found {
								if raw, err := json.Marshal(de); err == nil {
									var obj map[string]any
									if err := json.Unmarshal(raw, &obj); err == nil {
										entry.Entities = append(entry.Entities, obj)
									}
								}
							}
						}
					}
					if _world != nil && _world.assetManager != nil {
						for _, assetID := range cc.AssetReferences {
							if a, err := _world.assetManager.Get(assetID); err == nil {
								entry.Assets = append(entry.Assets, assetJSON{
									ID:    a.ID,
									Kind:  a.Kind,
									State: a.State,
								})
							}
						}
					}
				}
			}
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return mustJSON(entries)
}

// agentDecisionJSON is the AGENT_V2 output schema field for field (see
// src/agent_policy/AGENT_V2.md §Output Schema; a real answer is what
// reason_turns.raw_output holds):
//
//	{"goal_type": …, "completion_contracts": {"steps": […]},
//	 "type": "plan|done|blocked|need_input", "reason": …,
//	 "evidence": [{"id","source","reference","fact"}],
//	 "plan": {"steps": [{"capability","input","expected_effect","evidence_refs"}]},
//	 "need": {"type","description"},
//	 "deliverable": [{"type","concrete_type","detail"}],
//	 "presentation": [{"type","content"}]}
//
// goal_type is the planner echoing its classification back; completion_contracts is the
// submission of the contract itself — the facts that must hold for the task to be done,
// each with its evidence slot (src/completion_contract.go). The runtime pins the first
// contract of a run and verifies every `done` against it; see docs/verification.md.
type agentDecisionJSON struct {
	GoalType           string          `json:"goal_type"`
	CompletionContract json.RawMessage `json:"completion_contracts"`
	Type               string          `json:"type"`
	Reason             string          `json:"reason"`
	Evidence           []Evidence      `json:"evidence"`
	Plan               json.RawMessage `json:"plan"`
	Need               Need            `json:"need"`
	Deliverables       []Deliverable   `json:"deliverable"`
	Presentation       []Presentation  `json:"presentation"`
}

// planStepJSON is one entry of plan.steps (AGENT_V2 §Plan Actions).
//
// A step may name itself (`name`), which is what another step's input binds to:
// "step:<name>.output.<key>". Its inputs arrive either as "inputs" (the AGENT_V2
// shape: value or {"source": …}) or as "input" (the older shape, still read).
type planStepJSON struct {
	Name           string          `json:"name"`
	Capability     string          `json:"capability"`
	Input          json.RawMessage `json:"input"`
	Inputs         json.RawMessage `json:"inputs"`
	ExpectedEffect json.RawMessage `json:"expected_effect"`
	EvidenceRefs   []string        `json:"evidence_refs"`
}

// parsePlanSteps accepts both legacy "plan": [...] and the AGENT_V2
// "plan": {"steps": [...]} shapes.
func parsePlanSteps(raw json.RawMessage) []planStepJSON {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var steps []planStepJSON
	if err := json.Unmarshal(raw, &steps); err == nil {
		return steps
	}
	var obj struct {
		Steps []planStepJSON `json:"steps"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Steps
	}
	return nil
}

// parseDecision maps the model's AGENT_V2 answer onto a Decision — the WHOLE
// plan, not just its first step: every step becomes an action in order, so the
// runtime can execute the plan instead of dropping everything after the first
// capability. Registered capabilities become CapabilityAction; a step naming an
// unknown capability becomes NothingAction so one bad step cannot delete the
// rest of the plan (the runtime skips it and runs on).
func parseDecision(text string) (Decision, error) {
	raw := extractJSONObject(text)
	if raw == "" {
		return Decision{}, fmt.Errorf("no JSON object in model response")
	}
	var d agentDecisionJSON
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return Decision{}, fmt.Errorf("parse agent decision JSON: %w", err)
	}
	typ := strings.ToLower(strings.TrimSpace(d.Type))
	contract, err := parseCompletionContract(d.CompletionContract)
	if err != nil {
		return Decision{}, err
	}
	decision := Decision{
		Type:         typ,
		Reason:       firstNonEmptyString(strings.TrimSpace(d.Reason), typ),
		Evidence:     d.Evidence,
		Contract:     contract,
		Need:         d.Need,
		Deliverables: d.Deliverables,
		Presentation: d.Presentation,
	}
	switch typ {
	case "plan":
		actions, err := planActions(parsePlanSteps(d.Plan))
		if err != nil {
			return Decision{}, err
		}
		decision.Actions = actions
	case "done", "blocked", "need_input":
		// Nothing to execute: the decision's own outcome is the plan.
	default:
		return Decision{}, fmt.Errorf("unknown decision type %q", d.Type)
	}
	return decision, nil
}

// planActions turns every plan step into an action, preserving order. Inputs that
// cannot be read (a binding that is neither of the two forms) are an error: a plan
// the runtime cannot read is a plan it must not run (src/plan_input.go).
func planActions(steps []planStepJSON) ([]Action, error) {
	actions := make([]Action, 0, len(steps))
	for _, step := range steps {
		capName := strings.ToLower(strings.TrimSpace(step.Capability))
		switch capName {
		case "noop", "nothing", "none", "":
			continue
		}
		inputs, err := planInputs(step)
		if err != nil {
			return nil, fmt.Errorf("plan step %s: %w", planStepLabel(step), err)
		}
		if f := activeCapabilityFactory(); f != nil && f.Has(capName) {
			actions = append(actions, CapabilityAction{
				Name:           capName,
				StepName:       strings.TrimSpace(step.Name),
				Inputs:         inputs,
				ExpectedEffect: stepText(step.ExpectedEffect),
				EvidenceRefs:   step.EvidenceRefs,
			})
			continue
		}
		actions = append(actions, NothingAction{Reason: fmt.Sprintf("unknown capability %q", capName)})
	}
	return actions, nil
}

// planInputs reads a step's inputs: a literal value per key, or a binding saying
// where the value comes from.
func planInputs(step planStepJSON) (map[string]StepInput, error) {
	raw := step.Inputs
	if len(raw) == 0 || string(raw) == "null" {
		raw = step.Input
	}
	out := map[string]StepInput{}
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("input: %w", err)
	}
	for key, entry := range entries {
		var in StepInput
		if err := json.Unmarshal(entry, &in); err != nil {
			return nil, fmt.Errorf("input %q: %w", key, err)
		}
		out[key] = in
	}
	return out, nil
}

// planStepLabel names a step in an error about the plan itself.
func planStepLabel(step planStepJSON) string {
	if name := strings.TrimSpace(step.Name); name != "" {
		return fmt.Sprintf("%q", name)
	}
	if cap := strings.TrimSpace(step.Capability); cap != "" {
		return cap
	}
	return "(unnamed)"
}

// stepText renders a plan-step field the models fill with either a string or an
// object (expected_effect appears as both in practice) as one line of text.
func stepText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func extractJSONObject(text string) string {
	s := stripCodeFences(text)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}

// stripCodeFences removes a markdown code fence (e.g. ```json ... ```) around
// text so persisted outputs stay as plain text.
func stripCodeFences(text string) string {
	s := strings.TrimSpace(text)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "```"))
	if len(s) >= 4 && strings.EqualFold(s[:4], "json") {
		s = strings.TrimSpace(s[4:])
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// NormalizeReasonOutput returns the structured, machine-readable form of a
// model's raw output. It prefers the embedded JSON object (the decision
// envelope produced by the reasoner) and falls back to the fence-stripped text
// when no JSON object is present (e.g. free-form code_edit results).
func NormalizeReasonOutput(raw string) string {
	if j := extractJSONObject(raw); j != "" {
		return j
	}
	return stripCodeFences(raw)
}
