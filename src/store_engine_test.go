package autonomy

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeStore is a no-op Store used to prove the engine SPI is truly pluggable:
// a non-SQLite backend can be registered and opened without touching callers.
type fakeStore struct{}

func (fakeStore) UpsertTask(*Task) error                          { return nil }
func (fakeStore) UpsertAgent(*Agent) error                        { return nil }
func (fakeStore) SoftDeleteAgent(int64) error                     { return nil }
func (fakeStore) InsertReasonTurn(ReasonTurn) error               { return nil }
func (fakeStore) BeginReasonTurn(ReasonTurn) (int64, error)       { return 0, nil }
func (fakeStore) AppendLLMEvents(int64, string, []LLMEvent) error { return nil }
func (fakeStore) FinishReasonTurn(int64, LLMRunResult) error      { return nil }
func (fakeStore) ListLLMEvents(int64) ([]LLMEvent, error)         { return nil, nil }
func (fakeStore) Close() error                                    { return nil }

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

func TestSQLiteEngineDefaultDSNUsesProjectRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJECT_ROOT", root)
	dsn, err := sqliteEngine{}.DefaultDSN()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "data", "autonomy.db")
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

func TestOpenDefaultStoreDefaultsToProjectRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJECT_ROOT", root)
	t.Setenv(EnvStoreEngine, "")
	t.Setenv(EnvStoreDSN, "")

	store, err := OpenDefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "data", "autonomy.db")); err != nil {
		t.Fatalf("expected default sqlite file: %v", err)
	}
}
