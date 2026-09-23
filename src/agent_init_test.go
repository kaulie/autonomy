package autonomy

import (
	"fmt"
	"strings"
	"testing"
)

// TestInitializeAgentIsIndependentOfTask: initialization names no task and needs
// none — the agent exists, with its system prompt, before anything is accepted for
// it (CurrentTask stays nil), and it is the agent a later task is paired with.
func TestInitializeAgentIsIndependentOfTask(t *testing.T) {
	factory := NewAgentFactory()
	agent, err := NewAgentInitializer(factory).Initialize(AgentInitOptions{
		Role:                AgentRolePlanner,
		SystemPrompt:        "be terse",
		RequirePlanApproval: true,
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if agent == nil {
		t.Fatal("Initialize returned no agent")
	}
	if agent.CurrentTask != nil {
		t.Fatalf("initialization bound a task: CurrentTask=%+v", agent.CurrentTask)
	}
	if agent.Role != AgentRolePlanner {
		t.Fatalf("Role=%q want planner", agent.Role)
	}
	if agent.SystemPrompt != "be terse" {
		t.Fatalf("SystemPrompt=%q want the prompt it was initialized with", agent.SystemPrompt)
	}
	if !agent.RequirePlanApproval {
		t.Fatal("RequirePlanApproval was not recorded")
	}
	if got := factory.forInitialization(); got != agent {
		t.Fatalf("the initialized agent is not the one a task would be paired with: got %+v", got)
	}
}

// TestInitializeAgentPairsWithTheTaskAcceptedAfterIt: the flow is initialize first,
// accept the task second — so the agent the caller built is the one the task gets,
// not a fresh one.
func TestInitializeAgentPairsWithTheTaskAcceptedAfterIt(t *testing.T) {
	store := executionTestStore(t)
	factory := NewAgentFactory()
	agent, err := NewAgentInitializer(factory).Initialize(AgentInitOptions{SystemPrompt: "be terse"})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	auto := &Autonomy{AgentFactory: factory, Runtime: NewRuntime(factory), Store: store}

	task := &Task{ID: "task-after-init", Description: "do it", Status: TaskStatusPending}
	paired, err := auto.resumeAgentForTask(task)
	if err != nil {
		t.Fatalf("resumeAgentForTask: %v", err)
	}
	if paired != agent {
		t.Fatalf("the task was paired with a different agent: got %+v want %+v", paired, agent)
	}
	if task.AgentID != agent.ID {
		t.Fatalf("task.AgentID=%d want the initialized agent's id %d", task.AgentID, agent.ID)
	}
	if agent.CurrentTask != task {
		t.Fatal("the initialized agent was not bound to the task")
	}
	if got := factory.forInitialization(); got != nil {
		t.Fatalf("the paired agent is still offered as unpaired: %+v", got)
	}
}

// TestAgentSystemPromptTravelsInTheReasoningFrame: the system prompt given at
// initialization reaches the agent ahead of the task, in the frame — the stable
// half of the prompt that travels once per session.
func TestAgentSystemPromptTravelsInTheReasoningFrame(t *testing.T) {
	t.Setenv("PROJECT_ROOT", "..")
	factory := NewAgentFactory()
	agent, err := NewAgentInitializer(factory).Initialize(AgentInitOptions{SystemPrompt: "be terse"})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	frame, err := buildReasoningFrame(DecisionContext{Agent: agent}, ReasoningInput{})
	if err != nil {
		t.Fatalf("buildReasoningFrame: %v", err)
	}
	if !strings.Contains(frame, "## System Prompt") || !strings.Contains(frame, "be terse") {
		t.Fatalf("the system prompt is not in the frame:\n%s", frame)
	}

	// An agent initialized without one carries no such section.
	plain, err := NewAgentInitializer(NewAgentFactory()).Initialize(AgentInitOptions{})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	bare, err := buildReasoningFrame(DecisionContext{Agent: plain}, ReasoningInput{})
	if err != nil {
		t.Fatalf("buildReasoningFrame: %v", err)
	}
	if strings.Contains(bare, "## System Prompt") {
		t.Fatalf("a frame with no system prompt carries one:\n%s", bare)
	}
}

// TestNeedsPlanApproval: only a plan waits for the user's confirmation, and only
// while the agent wants it and the run has not already been approved.
func TestNeedsPlanApproval(t *testing.T) {
	plan := Decision{Type: decisionPlanType}
	done := Decision{Type: decisionDone}

	if needsPlanApproval(nil, plan) {
		t.Fatal("a nil agent never needs approval")
	}
	plain := &Agent{}
	if needsPlanApproval(plain, plan) {
		t.Fatal("an agent that did not ask for approval must not pause")
	}
	approving := &Agent{RequirePlanApproval: true}
	if !needsPlanApproval(approving, plan) {
		t.Fatal("a plan under RequirePlanApproval must pause")
	}
	if needsPlanApproval(approving, done) {
		t.Fatal("a concluding decision is an answer, not a plan to approve")
	}
	approving.planApproved = true
	if needsPlanApproval(approving, plan) {
		t.Fatal("an already-approved run must not pause again")
	}
}

// scriptedReasoner answers with a fixed list of decisions, one per cycle, so a test
// can drive the loop without a provider.
type scriptedReasoner struct {
	decisions []Decision
	calls     int
}

func (r *scriptedReasoner) Reason(ctx DecisionContext, _ ReasoningInput) (ReasoningResult, error) {
	if r.calls >= len(r.decisions) {
		return ReasoningResult{}, fmt.Errorf("scripted reasoner: decision %d is beyond the script", r.calls)
	}
	d := r.decisions[r.calls]
	r.calls++
	d.Ctx = ctx
	return ReasoningResult{Decision: d}, nil
}
