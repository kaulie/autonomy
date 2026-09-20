package autonomy

import (
	"os"
	"path/filepath"
	"strings"
)

// sqliteEngine backs a Store with a local SQLite file (modernc.org/sqlite, pure
// Go). It is the built-in default engine; the SQL, schema, and migrations it
// needs stay entirely inside sqlite_store.go so no dialect leaks to callers.
type sqliteEngine struct{}

func (sqliteEngine) Name() string { return StoreEngineSQLite }

// DefaultDSN places the database at $AUTONOMY_DATA_DIR/autonomy.db, which
// defaults to ~/database/autonomy/autonomy.db.
//
// One path for the machine, not one per checkout: the deployed runtime, a dev
// run, the benchmark tool and a SQL editor should all be looking at the same
// database, so "which database am I reading" has one answer (see docs/store.md).
func (sqliteEngine) DefaultDSN() (string, error) {
	dir := strings.TrimSpace(os.Getenv(EnvDataDir))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, "database", "autonomy")
	}
	return filepath.Join(dir, "autonomy.db"), nil
}

func (sqliteEngine) Open(dsn string) (Store, error) {
	store, err := OpenSQLiteStore(dsn)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func init() {
	if err := RegisterStoreEngine(sqliteEngine{}); err != nil {
		panic(err)
	}
}
