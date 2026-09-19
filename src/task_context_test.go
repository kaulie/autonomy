package autonomy

import (
	"path/filepath"
	"strings"
	"testing"
)

// A task's world — the goal type it was accepted as, and the context containers it
// names — lives on its row, not only in the request that said it. These tests are
// the two halves of that: the row keeps it (store), and an instruction that names
// no world continues the task with the one the row has (accept → the run's prompt).

func TestTaskRowKeepsItsGoalTypeAndContextRef(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	written := &Task{
		ID:          "task-world",
		Description: "do it",
		Domain:      TaskDomainSoftwareDevelopment,
		GoalType:    GoalType_Resolve_ISSUE,
		Status:      TaskStatusPending,
		ContextRef: map[ContextContainerType]string{
			ContextContainerTypeProject: "project-2",
			ContextContainerTypeTeam:    "team-1",
		},
	}
	if err := store.UpsertTask(written); err != nil {
		t.Fatal(err)
	}

	// The row itself carries them, not just the value that was written.
	var column string
	if err := store.db.QueryRow(`SELECT context_ref FROM tasks WHERE id = ?`, written.ID).Scan(&column); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(column, "project-2") || !strings.Contains(column, "team-1") {
		t.Fatalf("tasks.context_ref=%q, want the containers the task was accepted with", column)
	}

	read, err := store.GetTask(written.ID)
	if err != nil || read == nil {
		t.Fatalf("task=%v err=%v", read, err)
	}
	if read.GoalType != GoalType_Resolve_ISSUE {
		t.Fatalf("goal_type=%q, want %q", read.GoalType, GoalType_Resolve_ISSUE)
	}
	if read.ContextRef[ContextContainerTypeProject] != "project-2" || read.ContextRef[ContextContainerTypeTeam] != "team-1" {
		t.Fatalf("context_ref=%v, want both containers back", read.ContextRef)
	}

	// A later write that says nothing about the world does not take it away: the
	// runtime upserts Task values on its way through a run (a status, an error, the
	// agent it was paired with) and those need not carry it.
	read.Status = TaskStatusRunning
	read.GoalType = ""
	read.ContextRef = nil
	if err := store.UpsertTask(read); err != nil {
		t.Fatal(err)
	}
	again, err := store.GetTask(written.ID)
	if err != nil || again == nil {
		t.Fatalf("task=%v err=%v", again, err)
	}
	if again.Status != TaskStatusRunning {
		t.Fatalf("status=%q, want the write's own status", again.Status)
	}
	if again.GoalType != GoalType_Resolve_ISSUE || again.ContextRef[ContextContainerTypeProject] != "project-2" {
		t.Fatalf("goal_type=%q context_ref=%v, want what the task was accepted as", again.GoalType, again.ContextRef)
	}

	// A write that does give a world replaces it — the refs are the task's, not a
	// bag that only ever grows.
	again.ContextRef = map[ContextContainerType]string{ContextContainerTypeTeam: "team-9"}
	if err := store.UpsertTask(again); err != nil {
		t.Fatal(err)
	}
	final, err := store.GetTask(written.ID)
	if err != nil || final == nil {
		t.Fatalf("task=%v err=%v", final, err)
	}
	if len(final.ContextRef) != 1 || final.ContextRef[ContextContainerTypeTeam] != "team-9" {
		t.Fatalf("context_ref=%v, want the write's own refs and nothing else", final.ContextRef)
	}
}

func TestASecondInstructionInheritsTheTasksWorld(t *testing.T) {
	store, rt := plannerRun(t, "answer done")

	// The world the id names has to exist for the prompt to expand it: registering a
	// container is the runtime's own wiring (docs/context.md), not the request's.
	_autonomy.ContextContainerManager = NewContextContainerManager()
	if err := RegisterContextContainer(ContextContainer{
		ID:                   "project-2",
		Name:                 "Project 2",
		Description:          "the demo project",
		DomainType:           TaskDomainSoftwareDevelopment,
		ContextContainerType: ContextContainerTypeProject,
	}); err != nil {
		t.Fatal(err)
	}

	first, err := rt.Run(AcceptTaskRequest{
		ID:          "task-world",
		Description: "answer done",
		GoalType:    GoalType_Resolve_ISSUE,
		ContextRef:  map[string]string{"project": "project-2"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The same task again, this time naming neither a goal nor a world.
	if _, err := rt.Run(AcceptTaskRequest{ID: "task-world"}); err != nil {
		t.Fatalf("Run with only the task's id: %v", err)
	}

	row, err := store.GetTask("task-world")
	if err != nil || row == nil {
		t.Fatalf("task=%v err=%v", row, err)
	}
	if row.GoalType != GoalType_Resolve_ISSUE {
		t.Fatalf("goal_type=%q, want the goal the task was accepted as", row.GoalType)
	}
	if row.ContextRef[ContextContainerTypeProject] != "project-2" {
		t.Fatalf("context_ref=%v, want the row's own world", row.ContextRef)
	}

	// It is what the run's agent reads, too: a decision context is the task the agent
	// is on (Agent.decide), so the delta's context_entity block carries the container
	// the id names — not just the id.
	agent := rt.AgentFactory.ForTask("task-world")
	if agent == nil || agent.CurrentTask == nil {
		t.Fatal("no agent on the task after two runs")
	}
	if agent.ID != first.AgentID {
		t.Fatalf("agent=%d, want the same agent the first instruction was accepted for (%d)", agent.ID, first.AgentID)
	}
	if agent.CurrentTask.ContextRef[ContextContainerTypeProject] != "project-2" {
		t.Fatalf("the run's own task=%+v, want the refs the row carries", agent.CurrentTask)
	}
	block := string(formatContextEntitiesJSON(DecisionContext{Task: agent.CurrentTask}))
	if !strings.Contains(block, `"project-2"`) || !strings.Contains(block, "Project 2") {
		t.Fatalf("context_entity=%s, want the container the id names", block)
	}
}
