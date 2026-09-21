package db

import . "github.com/kaulie/autonomy/src"

import (
	"fmt"
	"os"
	"strings"
)

// postgresEngine backs a Store with a PostgreSQL database (pgx over database/sql).
//
// It is a second, independent engine — not a mode of the sqlite one: the schema,
// the SQL, the placeholders and the returned-row handling are its own
// (postgres_store.go and friends), and it registers under its own name so
// AUTONOMY_STORE_ENGINE=postgres selects it (see docs/store.md).
//
// The one thing it deliberately does *not* have is a migration path from sqlite:
// the two engines keep two databases. Old data stays where it is (the sqlite file),
// a postgres deployment starts from an empty schema, and nothing copies rows
// between them — see "两个引擎，两份数据" in docs/store.md.
type postgresEngine struct{}

// StoreEnginePostgres is the registered name of the postgres engine.
const StoreEnginePostgres = "postgres"

const (
	// EnvPostgresDSN is where the postgres engine looks for its connection string
	// when AUTONOMY_STORE_DSN is unset. DATABASE_URL is accepted as well because
	// that is the name a container/deployment platform hands a service.
	EnvPostgresDSN = "AUTONOMY_POSTGRES_DSN"
	// EnvPostgresDatabaseURL is the conventional fallback DSN variable.
	EnvPostgresDatabaseURL = "DATABASE_URL"
	// EnvPostgresReadDSN is the connection string of the read follower: a streaming
	// replica of the same database whose rows serve the reads that only observe
	// (docs/store.md「读写分离」). AUTONOMY_STORE_READ_DSN is accepted as well — it is
	// the engine-neutral name, the read half of AUTONOMY_STORE_DSN.
	//
	// Unset means one database for both: every read goes to the writer, exactly as
	// before this was configurable.
	EnvPostgresReadDSN = "AUTONOMY_POSTGRES_READ_DSN"
	// EnvStoreReadDSN is the engine-neutral read follower variable.
	EnvStoreReadDSN = "AUTONOMY_STORE_READ_DSN"
)

func (postgresEngine) Name() string { return StoreEnginePostgres }

// DefaultDSN returns the DSN from the environment. A networked database has no
// meaningful default, so an unset environment is an error rather than a guess:
// connecting to "some local postgres" is exactly the mistake that ends up writing
// a production run into a laptop database.
func (postgresEngine) DefaultDSN() (string, error) {
	for _, env := range []string{EnvPostgresDSN, EnvPostgresDatabaseURL} {
		if dsn := strings.TrimSpace(os.Getenv(env)); dsn != "" {
			return dsn, nil
		}
	}
	return "", fmt.Errorf("no default dsn for %s: set %s (or %s / %s)",
		StoreEnginePostgres, EnvStoreDSN, EnvPostgresDSN, EnvPostgresDatabaseURL)
}

// Open connects to the database at dsn and makes the schema it needs. A read
// follower (AUTONOMY_STORE_READ_DSN / AUTONOMY_POSTGRES_READ_DSN) is attached when
// one is configured, so reads that only observe are served locally without any
// caller asking (docs/store.md「读写分离」).
func (postgresEngine) Open(dsn string) (Store, error) {
	return OpenPostgresStoreWithFollower(dsn, postgresReadFollowerDSN())
}

// postgresReadFollowerDSN is the configured read follower, if any. The store-level
// variable wins over the engine's own, exactly like AUTONOMY_STORE_DSN over
// AUTONOMY_POSTGRES_DSN: the generic name is the explicit override.
func postgresReadFollowerDSN() string {
	for _, env := range []string{EnvStoreReadDSN, EnvPostgresReadDSN} {
		if dsn := strings.TrimSpace(os.Getenv(env)); dsn != "" {
			return dsn
		}
	}
	return ""
}

func init() {
	if err := RegisterStoreEngine(postgresEngine{}); err != nil {
		panic(err)
	}
}
