package autonomy

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability/broker"
)

// One session, two identities. A task's own agent and a delegated worker take their
// turns through the same code (LLMSession.Say), and what the record shows is what the
// identity on the agent makes it: plan mode with the user as the author for the
// planner, agent mode with the delegating agent as the author for the worker — both
// attributed to the task the work belongs to.
func TestOneSessionServesThePlannerAndTheWorker(t *testing.T) {
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	store := executionTestStore(t)

	cases := []struct {
		name string
		// worker is the door the agent came in through: a task's own agent, or a worker
		// a capability acquired through the broker.
		worker    bool
		round     int // what the caller passes as the round
		wantMode  ReasonMode
		wantRole  LLMMessageRole
		wantCycle int
	}{
		{
			name: "the task's own agent", round: 3,
			wantMode: ReasonModePlan, wantRole: LLMMessageRoleUser, wantCycle: 3,
		},
		{
			name: "a delegated worker", worker: true, round: RoundAuto,
			wantMode: ReasonModeAgent, wantRole: LLMMessageRoleAgent, wantCycle: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := NewRuntime(NewAgentFactory())
			var sess *LLMSession
			if tc.worker {
				acquired, err := rt.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
					Purpose: "code_edit", TaskID: "task-1",
				})
				if err != nil {
					t.Fatal(err)
				}
				var ok bool
				if sess, ok = acquired.(*LLMSession); !ok {
					t.Fatalf("the worker's session is %T, want the runtime's own", acquired)
				}
			} else {
				agent := rt.agents.NewAgent()
				agent.Role = AgentRolePlanner
				sess = NewLLMSession(rt, agent, SessionOpts{TaskID: "task-1"})
				agent.Session = sess
			}
			defer func() { _ = sess.Close(context.Background()) }()

			res, err := sess.Say(context.Background(), "hello", tc.round)
			if err != nil {
				t.Fatalf("say: %v", err)
			}
			if strings.TrimSpace(res.Text) == "" {
				t.Fatal("the turn answered nothing")
			}
			if res.Origin.ReasonTurnID == 0 {
				t.Fatal("the turn was not recorded")
			}

			var mode, taskID string
			var cycle int
			if err := store.db.QueryRow(`SELECT mode, cycle, task_id FROM reason_turns WHERE id = ?`, res.Origin.ReasonTurnID).
				Scan(&mode, &cycle, &taskID); err != nil {
				t.Fatal(err)
			}
			if mode != string(tc.wantMode) || cycle != tc.wantCycle || taskID != "task-1" {
				t.Fatalf("turn recorded mode=%q cycle=%d task=%q, want %q/%d/task-1",
					mode, cycle, taskID, tc.wantMode, tc.wantCycle)
			}
			messages, err := store.ListLLMMessages(res.Origin.ReasonTurnID)
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) == 0 || messages[0].Role != tc.wantRole {
				t.Fatalf("input row=%+v, want role %q", messages, tc.wantRole)
			}
			if got := sess.Turns(); len(got) != 1 || got[0] != res.Origin.ReasonTurnID {
				t.Fatalf("turns=%v, want the one turn this session took", got)
			}
		})
	}
}

// A turn the provider cut off is retried on the same session whoever took it: the
// retry is the session's, not the worker's, so the planner keeps its cycle instead of
// losing the run to one over-long turn. Both attempts are the same round — a retry is
// not a new decision cycle.
func TestAPlannerTurnIsRetriedWhenTheTurnWasCutOff(t *testing.T) {
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("PROJECT_ROOT", filepath.Join(".."))
	t.Setenv("AUTONOMY_LLM_TURN_RETRIES", "") // one retry, the default
	store := executionTestStore(t)

	rt := NewRuntime(NewAgentFactory())
	agent := rt.agents.NewAgent()
	agent.Role = AgentRolePlanner
	sess := NewLLMSession(rt, agent, SessionOpts{TaskID: "task-1"})
	agent.Session = sess
	defer func() { _ = sess.Close(context.Background()) }()

	if _, err := sess.Say(context.Background(), "truncate my turn", 2); err != nil {
		t.Fatalf("a truncated turn should be retried, not reported: %v", err)
	}

	rows, err := store.db.Query(`SELECT cycle, status, input FROM reason_turns WHERE task_id = 'task-1' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cycles []int
	var inputs []string
	var statuses []string
	for rows.Next() {
		var cycle int
		var status, input string
		if err := rows.Scan(&cycle, &status, &input); err != nil {
			t.Fatal(err)
		}
		cycles = append(cycles, cycle)
		statuses = append(statuses, status)
		inputs = append(inputs, input)
	}
	if len(cycles) != 2 {
		t.Fatalf("recorded %d turns, want the truncated one and its retry: %v", len(cycles), statuses)
	}
	if cycles[0] != 2 || cycles[1] != 2 {
		t.Fatalf("cycles=%v, want both attempts recorded as the decision cycle they are", cycles)
	}
	if statuses[0] != string(LLMStatusError) || inputs[0] != "truncate my turn" {
		t.Errorf("first turn = %q/%q, want the failure the provider reported on the original prompt", statuses[0], inputs[0])
	}
	if !strings.Contains(inputs[1], "Your turn was cut off") {
		t.Errorf("retry input=%q, want the continuation reminder", inputs[1])
	}
}
