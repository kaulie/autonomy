package autonomy

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeStore is a no-op Store used to prove the engine SPI is truly pluggable:
// a non-SQLite backend can be registered and opened without touching callers.
type fakeStore struct{}

func (fakeStore) UpsertTask(*Task) error            { return nil }
func (fakeStore) UpsertAgent(*Agent) error          { return nil }
func (fakeStore) SoftDeleteAgent(int64) error       { return nil }
func (fakeStore) InsertReasonTurn(ReasonTurn) error { return nil }
func (fakeStore) BeginReasonTurn(ReasonTurn) (ReasonTurnHandle, error) {
	return ReasonTurnHandle{}, nil
}
func (fakeStore) AppendLLMEvents(int64, string, []LLMEvent) error { return nil }
func (fakeStore) AppendLLMMessages(int64, []LLMMessage) error     { return nil }
func (fakeStore) FinishReasonTurn(ReasonTurnHandle, LLMRunResult) error {
	return nil
}
func (fakeStore) ListLLMEvents(int64) ([]LLMEvent, error)     { return nil, nil }
func (fakeStore) ListLLMMessages(int64) ([]LLMMessage, error) { return nil, nil }
func (fakeStore) AssistantMessageID(int64) (int64, bool, error) {
	return 0, false, nil
}
func (fakeStore) CreateExecutionPlan(ExecutionPlan) (int64, error) {
	return 0, nil
}
func (fakeStore) AppendExecutionStepPlans([]ExecutionStepPlan) error { return nil }
func (fakeStore) AppendExecutionStep(ExecutionStep) (int64, error)   { return 0, nil }
func (fakeStore) AppendExecutionStepInteraction(ExecutionStepInteraction) (int64, error) {
	return 0, nil
}
func (fakeStore) TaskInputMessageID(string) (int64, bool, error) { return 0, false, nil }
func (fakeStore) ListExecutionPlans(string) ([]ExecutionPlan, error) {
	return nil, nil
}
func (fakeStore) ListExecutionStepPlan(int64) ([]ExecutionStepPlan, error) {
	return nil, nil
}
func (fakeStore) ListExecutionSteps(int64) ([]ExecutionStep, error) { return nil, nil }
func (fakeStore) ListExecutionStepInteractions(int64) ([]ExecutionStepInteraction, error) {
	return nil, nil
}
func (fakeStore) ExecutionPlanOutcome(int64) (ExecutionPlanOutcome, bool, error) {
	return ExecutionPlanOutcome{}, false, nil
}
func (fakeStore) AppendCompletionContract(ContractCriterion) error { return nil }
func (fakeStore) ListCompletionContract(string) ([]ContractCriterion, error) {
	return nil, nil
}
func (fakeStore) AppendVerification(Verification) (int64, error) { return 0, nil }
func (fakeStore) ListVerifications(string) ([]Verification, error) {
	return nil, nil
}
func (fakeStore) EnqueueMessage(AgentMessage) (int64, error) { return 0, nil }
func (fakeStore) ClaimNextMessage(int64) (AgentMessage, bool, error) {
	return AgentMessage{}, false, nil
}
func (fakeStore) FinishAgentMessage(int64, AgentMessageStatus, string) error { return nil }
func (fakeStore) RequeueRunningMessages(int64) error                         { return nil }
func (fakeStore) ListAgentMessages(int64, int) ([]AgentMessage, error)       { return nil, nil }
func (fakeStore) CountQueuedMessages(int64) (int, error)                     { return 0, nil }
func (fakeStore) CountMessagesAhead(int64, int64) (int, error)               { return 0, nil }
func (fakeStore) GetTask(string) (*Task, error)                              { return nil, nil }
func (fakeStore) ListTasks() ([]*Task, error)                                { return nil, nil }
func (fakeStore) GetAgent(int64) (*Agent, error)                             { return nil, nil }
func (fakeStore) ActiveReasonTurn(string, int64) (*ReasonTurn, error) {
	return nil, nil
}
func (fakeStore) ListLLMMessagesAfter(string, int64, int64, int) ([]LLMMessage, error) {
	return nil, nil
}

// The data API's read port is a no-op here too: this fake exists to prove the engine
// SPI needs no SQLite, and a store that keeps nothing answers every one of those
// reads with nothing.
func (fakeStore) QueryTurns(TurnQuery) ([]TurnRecord, int, error) { return nil, 0, nil }
func (fakeStore) GetTurn(int64) (*TurnRecord, error)              { return nil, nil }
func (fakeStore) TurnFacets() (TurnFacets, error)                 { return TurnFacets{}, nil }
func (fakeStore) ListTaskOptions() ([]TaskOption, error)          { return nil, nil }
func (fakeStore) ListTurnsByTask(string, int) ([]TurnRecord, int, error) {
	return nil, 0, nil
}
func (fakeStore) CountTurns() (int, error) { return 0, nil }
func (fakeStore) Close() error             { return nil }

// fakeEngine records the DSN it was opened with so tests can assert dispatch to
// the registered engine (and its DefaultDSN fallback).
type fakeEngine struct {
	name       string
	defaultDSN string
	openedDSN  string
}

func (e *fakeEngine) Name() string                { return e.name }
func (e *fakeEngine) DefaultDSN() (string, error) { return e.defaultDSN, nil }
func (e *fakeEngine) Open(dsn string) (Store, error) {
	e.openedDSN = dsn
	return fakeStore{}, nil
}

func TestSQLiteEngineIsRegisteredByDefault(t *testing.T) {
	for _, name := range StoreEngineNames() {
		if name == StoreEngineSQLite {
			return
		}
	}
	t.Fatalf("sqlite engine not registered; got %v", StoreEngineNames())
}

func TestOpenStoreDispatchesToRegisteredEngine(t *testing.T) {
	engine := &fakeEngine{name: "test-fake-engine", defaultDSN: "fake://default"}
	if err := RegisterStoreEngine(engine); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore("test-fake-engine", "fake://explicit")
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("expected a store")
	}
	if engine.openedDSN != "fake://explicit" {
		t.Fatalf("openedDSN=%q, want the explicit DSN", engine.openedDSN)
	}
}

func TestOpenStoreFallsBackToEngineDefaultDSN(t *testing.T) {
	engine := &fakeEngine{name: "test-default-dsn", defaultDSN: "fake://default"}
	if err := RegisterStoreEngine(engine); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore("test-default-dsn", ""); err != nil {
		t.Fatal(err)
	}
	if engine.openedDSN != "fake://default" {
		t.Fatalf("openedDSN=%q, want DefaultDSN", engine.openedDSN)
	}
}

func TestOpenStoreUnknownEngineFails(t *testing.T) {
	if _, err := OpenStore("does-not-exist", "dsn"); err == nil {
		t.Fatal("expected error for unknown engine")
	}
}

func TestRegisterStoreEngineRejectsBlankNilAndDuplicate(t *testing.T) {
	if err := RegisterStoreEngine(nil); err == nil {
		t.Fatal("expected error for nil engine")
	}
	if err := RegisterStoreEngine(&fakeEngine{name: "  "}); err == nil {
		t.Fatal("expected error for blank engine name")
	}
	engine := &fakeEngine{name: "test-dup-engine"}
	if err := RegisterStoreEngine(engine); err != nil {
		t.Fatal(err)
	}
	if err := RegisterStoreEngine(engine); err == nil {
		t.Fatal("expected error for duplicate engine")
	}
}

func TestSQLiteEngineDefaultDSNUsesDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDataDir, dir)
	dsn, err := sqliteEngine{}.DefaultDSN()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "autonomy.db"); dsn != want {
		t.Fatalf("default DSN=%q, want %q", dsn, want)
	}
}

// Unset, the default is the one database this machine keeps:
// ~/database/autonomy/autonomy.db. Every consumer — the deployed runtime, a dev
// run, the benchmark tool — reads that file, not one per checkout.
func TestSQLiteEngineDefaultDSNFallsBackToHomeDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvDataDir, "")
	t.Setenv("HOME", home)
	dsn, err := sqliteEngine{}.DefaultDSN()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "database", "autonomy", "autonomy.db")
	if dsn != want {
		t.Fatalf("default DSN=%q, want %q", dsn, want)
	}
}

func TestOpenDefaultStoreHonoursEnv(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "custom.db")
	t.Setenv(EnvStoreEngine, StoreEngineSQLite)
	t.Setenv(EnvStoreDSN, path)

	store, err := OpenDefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTask(&Task{ID: "t-env"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected sqlite file at DSN: %v", err)
	}
}

func TestOpenDefaultStoreDefaultsToTheMachinesDatabase(t *testing.T) {
	dir := t.TempDir()
	// PROJECT_ROOT is set (as a deployment sets it) and must NOT be what picks
	// the database: the machine has one.
	t.Setenv("PROJECT_ROOT", dir)
	t.Setenv(EnvDataDir, dir)
	t.Setenv(EnvStoreEngine, "")
	t.Setenv(EnvStoreDSN, "")

	store, err := OpenDefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "autonomy.db")); err != nil {
		t.Fatalf("expected default sqlite file: %v", err)
	}
}
