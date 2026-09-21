package autonomy

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The persistence contract is a union of seven ports (src/store.go), and these tests
// pin the split from both ends: every engine satisfies every port, the runtime drives
// its own record-keeping through the ports alone, and no file outside an engine may
// reach for a database driver or a concrete engine.

// A fake that implements every port still composes with Store, so an engine can be
// built port by port (or be a decorator over one port). The concrete engines' own
// "implements every port" assertions live with the engines (src/db).
var (
	_ Store = fakeStore{}
)

// recordingStore is fakeStore (a no-op Store) plus a memory of what the
// upper-layer writers asked of it, so a test can drive the runtime's own
// record-keeping against a store that is not SQLite and see which port calls
// came out. Nothing in that path needs SQL, a dialect, or a driver.
type recordingStore struct {
	fakeStore
	tasks        []*Task
	agents       []*Agent
	deleted      []int64
	turns        []ReasonTurn
	plans        []ExecutionPlan
	planSteps    []ExecutionStepPlan
	steps        []ExecutionStep
	interactions []ExecutionStepInteraction
	verdicts     []Verification
	contract     []ContractCriterion
	inputMessage int64
	inputFound   bool
	nextID       int64
}

// id mints row ids as a database would, so plan_step_id / parent ids can be
// linked the way the real engine links them.
func (s *recordingStore) id() int64 { s.nextID++; return s.nextID }

func (s *recordingStore) UpsertTask(task *Task) error { s.tasks = append(s.tasks, task); return nil }

func (s *recordingStore) UpsertAgent(agent *Agent) error {
	s.agents = append(s.agents, agent)
	return nil
}

func (s *recordingStore) SoftDeleteAgent(id int64) error {
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *recordingStore) InsertReasonTurn(turn ReasonTurn) error {
	s.turns = append(s.turns, turn)
	return nil
}

func (s *recordingStore) TaskInputMessageID(string) (int64, bool, error) {
	return s.inputMessage, s.inputFound, nil
}

func (s *recordingStore) CreateExecutionPlan(plan ExecutionPlan) (int64, error) {
	plan.ID = s.id()
	s.plans = append(s.plans, plan)
	return plan.ID, nil
}

func (s *recordingStore) AppendExecutionStepPlans(steps []ExecutionStepPlan) error {
	for _, step := range steps {
		step.ID = s.id()
		s.planSteps = append(s.planSteps, step)
	}
	return nil
}

func (s *recordingStore) ListExecutionStepPlan(planID int64) ([]ExecutionStepPlan, error) {
	var out []ExecutionStepPlan
	for _, step := range s.planSteps {
		if step.PlanID == planID {
			out = append(out, step)
		}
	}
	return out, nil
}

func (s *recordingStore) AppendExecutionStep(step ExecutionStep) (int64, error) {
	step.ID = s.id()
	s.steps = append(s.steps, step)
	return step.ID, nil
}

func (s *recordingStore) AppendExecutionStepInteraction(in ExecutionStepInteraction) (int64, error) {
	in.ID = s.id()
	s.interactions = append(s.interactions, in)
	return in.ID, nil
}

func (s *recordingStore) AppendVerification(v Verification) (int64, error) {
	v.ID = s.id()
	s.verdicts = append(s.verdicts, v)
	return v.ID, nil
}

func (s *recordingStore) AppendCompletionContract(c ContractCriterion) error {
	s.contract = append(s.contract, c)
	return nil
}

func (s *recordingStore) ListCompletionContract(taskID string) ([]ContractCriterion, error) {
	var out []ContractCriterion
	for _, row := range s.contract {
		if row.TaskID == taskID {
			out = append(out, row)
		}
	}
	return out, nil
}

// TestOnlyTheStorageEngineMayImportADriver is the boundary as a test. Every
// production file outside an engine stays free of the database, so plugging in
// another engine cannot require touching the upper layer — and a later commit
// cannot quietly reach for sql.DB from, say, the runtime.
func TestOnlyTheStorageEngineMayImportADriver(t *testing.T) {
	drivers := []string{
		"database/sql",
		"modernc.org/sqlite",
		"github.com/mattn/go-sqlite3",
		"github.com/lib/pq",
		"github.com/go-sql-driver/mysql",
		"github.com/jackc/pgx",
	}
	// The concrete engines' own names: the upper layer takes the port, never the
	// type behind it.
	concrete := []string{"SQLiteStore", "openStore", "sqliteEngine", "PostgresStore", "OpenPostgresStore", "postgresEngine", "sql.Open("}

	var engineFiles, driverImports, checked int
	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "vendor", "node_modules", "gen", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ImportsOnly)
		if err != nil {
			return err
		}
		engine := isEngineFile(filepath.Base(path))
		if engine {
			engineFiles++
		}
		for _, imp := range file.Imports {
			imported := strings.Trim(imp.Path.Value, `"`)
			if imported != "" && !importsADriver(imported, drivers) {
				continue
			}
			driverImports++
			if !engine {
				t.Errorf("%s imports %q: only a storage engine may import a database driver", path, imported)
			}
		}
		if engine {
			return nil
		}
		checked++
		for _, name := range concrete {
			if strings.Contains(string(src), name) {
				t.Errorf("%s names the concrete engine (%s): take the Store port instead", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 || engineFiles == 0 || driverImports == 0 {
		t.Fatalf("the boundary check did not look at anything real: files=%d engines=%d driver imports=%d",
			checked, engineFiles, driverImports)
	}
}

// importsADriver reports whether an import path is a database driver (or a driver's
// subpackage, or a sqlite binding, whatever it is called).
func importsADriver(path string, drivers []string) bool {
	for _, driver := range drivers {
		if path == driver || strings.HasPrefix(path, driver+"/") || strings.Contains(path, "sqlite") {
			return true
		}
	}
	return false
}

// isEngineFile says whether a production .go file belongs to a storage engine.
// Engines are named <engine>_*.go, so the sqlite engine owns sqlite_*.go, the postgres
// engine owns postgres_*.go, and those are the only places a driver may be imported.
func isEngineFile(name string) bool {
	for _, engine := range []string{"sqlite", "postgres"} {
		if strings.HasPrefix(name, engine+"_") {
			return true
		}
	}
	return false
}

// runtime uses through a store that is not SQLite: the task, the agent, the run,
// the plan and its steps, the verdict, and the Completion Contract all land as
// port calls, with the ordering and the ids each port's contract promises.
func TestTheUpperLayerWritesThroughItsPorts(t *testing.T) {
	store := &recordingStore{inputMessage: 42, inputFound: true}
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	persistTask(&Task{ID: "task-1", Description: "ship it"})
	persistAgent(&Agent{ID: 4, Name: "agent-4"})
	softDeleteAgent(4)
	recordReasonTurn(&Agent{ID: 4, LLMProvider: LLMProvider("cline"), Model: "m"}, "task-1", 2, ReasonModePlan, "ask", "answer")

	if len(store.tasks) != 1 || store.tasks[0].Description != "ship it" {
		t.Fatalf("task write did not reach the store: %+v", store.tasks)
	}
	if len(store.agents) != 1 || store.agents[0].Name != "agent-4" {
		t.Fatalf("agent write did not reach the store: %+v", store.agents)
	}
	if len(store.deleted) != 1 || store.deleted[0] != 4 {
		t.Fatalf("soft delete did not reach the store: %v", store.deleted)
	}
	if len(store.turns) != 1 {
		t.Fatalf("reason turn did not reach the store: %+v", store.turns)
	}
	turn := store.turns[0]
	if turn.TaskID != "task-1" || turn.AgentID != 4 || turn.Cycle != 2 || turn.Mode != ReasonModePlan ||
		turn.Input != "ask" || turn.RawOutput != "answer" || turn.Model != "m" {
		t.Fatalf("reason turn lost its fields on the way to the store: %+v", turn)
	}
	if turn.CreatedAt.IsZero() {
		t.Fatal("reason turn reached the store without a created_at: the writer stamps it")
	}

	if id, found := taskInputMessageID("task-1"); !found || id != 42 {
		t.Fatalf("taskInputMessageID = (%d, %v), want (42, true)", id, found)
	}

	planID, saved, err := saveExecutionPlan(
		ExecutionPlan{TaskID: "task-1", Cycle: 1},
		[]ExecutionStepPlan{{Idx: 1, Name: "build"}, {Idx: 2, Name: "deploy"}},
	)
	if err != nil {
		t.Fatalf("saveExecutionPlan: %v", err)
	}
	if len(store.plans) != 1 || planID != store.plans[0].ID || len(saved) != 2 {
		t.Fatalf("plan not stored as asked: planID=%d saved=%d", planID, len(saved))
	}
	for i, step := range saved {
		if step.ID == 0 || step.PlanID != planID {
			t.Fatalf("step %d came back without an id or its plan: %+v", i, step)
		}
	}

	stepID := saveExecutionStep(ExecutionStep{PlanID: planID, PlanStepID: saved[0].ID, TaskID: "task-1", Name: "build", Status: "ok"})
	if stepID == 0 || len(store.steps) != 1 || store.steps[0].PlanStepID != saved[0].ID {
		t.Fatalf("execution step not stored as asked: id=%d steps=%+v", stepID, store.steps)
	}
	saveExecutionStepInteraction(ExecutionStepInteraction{StepID: stepID, Seq: 1, Kind: "llm"})
	if len(store.interactions) != 1 || store.interactions[0].StepID != stepID {
		t.Fatalf("step interaction not stored as asked: %+v", store.interactions)
	}
	saveVerification(Verification{TaskID: "task-1", Criterion: "service is up", Result: "pass"})
	if len(store.verdicts) != 1 || store.verdicts[0].Result != "pass" {
		t.Fatalf("verdict not stored as asked: %+v", store.verdicts)
	}

	// The Completion Contract round trip: the pin writes through the verification
	// port, and the read parses back the criterion's own words.
	pinCompletionContract(
		Decision{Ctx: DecisionContext{Task: &Task{ID: "task-1"}}, Contract: []Criterion{{Name: "service is up", Raw: `"the service answers"`}}},
		planID,
	)
	pinned := pinnedCompletionContract("task-1")
	if len(pinned) != 1 || pinned[0].Requirement != "the service answers" {
		t.Fatalf("pinned contract did not round-trip: %+v", pinned)
	}
}
