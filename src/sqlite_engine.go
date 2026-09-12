package autonomy

import "path/filepath"

// sqliteEngine backs a Store with a local SQLite file (modernc.org/sqlite, pure
// Go). It is the built-in default engine; the SQL, schema, and migrations it
// needs stay entirely inside sqlite_store.go so no dialect leaks to callers.
type sqliteEngine struct{}

func (sqliteEngine) Name() string { return StoreEngineSQLite }

// DefaultDSN places the database at $PROJECT_ROOT/data/autonomy.db.
func (sqliteEngine) DefaultDSN() (string, error) {
	root, err := projectRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "data", "autonomy.db"), nil
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
