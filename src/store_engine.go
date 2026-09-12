package autonomy

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// This file defines the storage-engine SPI. Store (store.go) is the single,
// database-agnostic contract the rest of Autonomy codes against; a StoreEngine
// is a pluggable backend that produces a Store. SQLite is the built-in engine,
// and other databases (Postgres, MySQL, ...) plug in by registering another
// engine: switching the database never touches callers or the Store interface.

const (
	// StoreEngineSQLite is the built-in, file-backed engine.
	StoreEngineSQLite = "sqlite"
	// DefaultStoreEngine is used when AUTONOMY_STORE_ENGINE is unset.
	DefaultStoreEngine = StoreEngineSQLite

	// EnvStoreEngine selects the storage engine by its registered name
	// (case-insensitive). Unset selects DefaultStoreEngine.
	EnvStoreEngine = "AUTONOMY_STORE_ENGINE"
	// EnvStoreDSN overrides the selected engine's default data source name
	// (for sqlite, the database file path).
	EnvStoreDSN = "AUTONOMY_STORE_DSN"
)

// StoreEngine is the SPI a database backend implements to back a Store. One
// engine maps to one database/dialect (sqlite today; postgres/mysql later) and
// owns its own schema and migrations.
type StoreEngine interface {
	// Name is the stable identifier used by AUTONOMY_STORE_ENGINE.
	Name() string
	// DefaultDSN returns the connection string used when AUTONOMY_STORE_DSN is
	// unset. Engines with no meaningful default (networked databases) return an
	// error so the caller must supply a DSN.
	DefaultDSN() (string, error)
	// Open connects to the backend at dsn, applies/upgrades its schema, and
	// returns a Store. The caller owns Store.Close.
	Open(dsn string) (Store, error)
}

var storeEngines = map[string]StoreEngine{}

// RegisterStoreEngine makes an engine available to OpenStore. Engines register
// once (typically from init). A nil engine, a blank name, or a duplicate
// registration returns an error so a misconfigured build fails loudly.
func RegisterStoreEngine(engine StoreEngine) error {
	if engine == nil {
		return fmt.Errorf("register store engine: nil engine")
	}
	name := normalizeStoreEngineName(engine.Name())
	if name == "" {
		return fmt.Errorf("register store engine: blank name")
	}
	if _, dup := storeEngines[name]; dup {
		return fmt.Errorf("register store engine %q: already registered", name)
	}
	storeEngines[name] = engine
	return nil
}

// StoreEngineNames returns the registered engine names, sorted.
func StoreEngineNames() []string {
	names := make([]string, 0, len(storeEngines))
	for name := range storeEngines {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func normalizeStoreEngineName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func lookupStoreEngine(name string) (StoreEngine, error) {
	engine, ok := storeEngines[normalizeStoreEngineName(name)]
	if !ok {
		return nil, fmt.Errorf("store engine %q is not registered (available: %s)",
			name, strings.Join(StoreEngineNames(), ", "))
	}
	return engine, nil
}

// OpenStore opens a Store through the named engine. An empty name selects
// DefaultStoreEngine; an empty dsn falls back to the engine's DefaultDSN.
func OpenStore(engineName, dsn string) (Store, error) {
	if strings.TrimSpace(engineName) == "" {
		engineName = DefaultStoreEngine
	}
	engine, err := lookupStoreEngine(engineName)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(dsn) == "" {
		dsn, err = engine.DefaultDSN()
		if err != nil {
			return nil, fmt.Errorf("store engine %q: %w", engine.Name(), err)
		}
	}
	store, err := engine.Open(dsn)
	if err != nil {
		return nil, fmt.Errorf("open store (engine=%s): %w", engine.Name(), err)
	}
	return store, nil
}

// OpenDefaultStore opens the configured engine, honouring AUTONOMY_STORE_ENGINE
// and AUTONOMY_STORE_DSN. By default it is a SQLite file at
// $PROJECT_ROOT/data/autonomy.db.
func OpenDefaultStore() (Store, error) {
	return OpenStore(os.Getenv(EnvStoreEngine), os.Getenv(EnvStoreDSN))
}
