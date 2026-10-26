package autonomy

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file defines the storage-engine SPI. Store (store.go) is the persistence
// contract the rest of Autonomy codes against — seven cohesive ports (TaskStore,
// AgentStore, InboxStore, ConversationStore, ExecutionStore, VerificationStore,
// TurnQueryStore) whose union is Store — and a StoreEngine is a pluggable backend
// that produces one. SQLite is the built-in engine, and other databases (Postgres,
// MySQL, ...) plug in by registering another engine: switching the database never
// touches callers, and no upper-layer file ever names a driver or a dialect
// (enforced by store_ports_test.go).

const (
	// StoreEngineSQLite is the built-in, file-backed engine.
	StoreEngineSQLite = "sqlite"
	// StoreEnginePostgres is the registered name of the postgres engine.
	StoreEnginePostgres = "postgres"
	// DefaultStoreEngine is used when AUTONOMY_STORE_ENGINE is unset.
	DefaultStoreEngine = StoreEngineSQLite

	// EnvStoreEngine selects the storage engine by its registered name
	// (case-insensitive). Unset selects DefaultStoreEngine.
	EnvStoreEngine = "AUTONOMY_STORE_ENGINE"
	// EnvStoreDSN overrides the selected engine's default data source name
	// (for sqlite, the database file path).
	EnvStoreDSN = "AUTONOMY_STORE_DSN"
	// EnvDatabaseURL is the connection string the deployment platform injects
	// (DATABASE_URL). When it is set, it is the database this process uses:
	// it wins over AUTONOMY_STORE_DSN left in backend/.env, the same way
	// SERVICE_PORT wins over a stale listen address. A Postgres URL also
	// selects the postgres engine.
	EnvDatabaseURL = "DATABASE_URL"
	// EnvSSLMode overrides sslmode inside DATABASE_URL. This is the name the
	// deployment platform is given. EnvPGSSLMode is the libpq spelling and is
	// used only when EnvSSLMode is unset. Libpq itself lets the URL win; here
	// the environment wins, so a local Postgres with SSL off can set
	// sslmode=disable without rewriting DATABASE_URL.
	EnvSSLMode   = "sslmode"
	EnvPGSSLMode = "PGSSLMODE"
	// EnvDataDir is the directory the sqlite engine's default database lives in
	// (`<dir>/autonomy.db`). Unset means ~/database/autonomy, the one database
	// every consumer on the machine shares.
	EnvDataDir = "AUTONOMY_DATA_DIR"
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
// and AUTONOMY_STORE_DSN. DATABASE_URL, when the deployment platform set it, is
// the connection string and wins over both. By default (no DATABASE_URL, no
// explicit DSN) it is a SQLite file at $AUTONOMY_DATA_DIR/autonomy.db
// (~/database/autonomy/autonomy.db) — the one database this machine keeps.
func OpenDefaultStore() (Store, error) {
	engine, dsn := applyDatabaseURL(os.Getenv(EnvStoreEngine), os.Getenv(EnvStoreDSN))
	return OpenStore(engine, dsn)
}

// applyDatabaseURL returns the engine and DSN to open. A non-empty DATABASE_URL
// replaces the DSN. A Postgres connection string also selects the postgres
// engine, so a platform URL is not handed to sqlite.
func applyDatabaseURL(engine, dsn string) (string, string) {
	url := strings.TrimSpace(os.Getenv(EnvDatabaseURL))
	if url == "" {
		return engine, dsn
	}
	url = rewriteSSLRootCert(url)
	url = applySSLMode(url)
	if isPostgresDSN(url) {
		engine = StoreEnginePostgres
	}
	return engine, url
}

// applySSLMode replaces or appends sslmode from the environment. sslmode wins
// over PGSSLMODE. An empty or unknown value leaves the URL alone.
func applySSLMode(dsn string) string {
	mode := strings.TrimSpace(os.Getenv(EnvSSLMode))
	if mode == "" {
		mode = strings.TrimSpace(os.Getenv(EnvPGSSLMode))
	}
	mode = strings.ToLower(mode)
	if !validPGSSLMode(mode) {
		return dsn
	}
	return setDSNParam(dsn, "sslmode", mode)
}

func validPGSSLMode(mode string) bool {
	switch mode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}

// setDSNParam sets key in a postgres URL query or a keyword DSN. It edits the
// existing text so a password is not re-encoded.
func setDSNParam(dsn, key, value string) string {
	token := key + "="
	if strings.Contains(dsn, "://") {
		q := strings.Index(dsn, "?")
		if q < 0 {
			return dsn + "?" + token + value
		}
		query := dsn[q+1:]
		if j := strings.Index(query, token); j >= 0 {
			abs := q + 1 + j + len(token)
			endRel := strings.Index(dsn[abs:], "&")
			if endRel < 0 {
				return dsn[:abs] + value
			}
			return dsn[:abs] + value + dsn[abs+endRel:]
		}
		if query == "" {
			return dsn + token + value
		}
		return dsn + "&" + token + value
	}
	if i := strings.Index(dsn, token); i >= 0 {
		start := i + len(token)
		endRel := strings.IndexAny(dsn[start:], " \t")
		if endRel < 0 {
			return dsn[:start] + value
		}
		return dsn[:start] + value + dsn[start+endRel:]
	}
	if strings.TrimSpace(dsn) == "" {
		return dsn
	}
	return strings.TrimRight(dsn, " \t") + " " + token + value
}

// rewriteSSLRootCert points sslrootcert at this machine's CA when the path in
// DATABASE_URL is not here. The deployment platform stores one URL; the CA file
// is the same bytes at ~/database/autonomy/pg-autonomy-ca.pem on each machine.
func rewriteSSLRootCert(dsn string) string {
	const key = "sslrootcert="
	idx := strings.Index(dsn, key)
	if idx < 0 {
		return dsn
	}
	start := idx + len(key)
	endRel := strings.Index(dsn[start:], "&")
	cert := dsn[start:]
	rest := ""
	if endRel >= 0 {
		cert = dsn[start : start+endRel]
		rest = dsn[start+endRel:]
	}
	decoded := strings.ReplaceAll(cert, "%2F", "/")
	decoded = strings.ReplaceAll(decoded, "%2f", "/")
	if cert != "" && fileExists(decoded) {
		return dsn
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return dsn
	}
	fallback := filepath.Join(home, "database", "autonomy", "pg-autonomy-ca.pem")
	if !fileExists(fallback) {
		return dsn
	}
	return dsn[:start] + fallback + rest
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// isPostgresDSN reports whether dsn is a Postgres URL or a libpq keyword
// string. Anything else (a sqlite file path, another scheme) is not.
func isPostgresDSN(dsn string) bool {
	s := strings.TrimSpace(dsn)
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		return true
	}
	if strings.Contains(s, "://") {
		return false
	}
	return strings.Contains(s, "host=") || strings.Contains(s, "dbname=")
}
