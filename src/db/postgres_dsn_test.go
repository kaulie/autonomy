package db

import (
	"strings"
	"testing"
)

// TestPostgresDefaultDSNPrefersDatabaseURL: the deployment platform injects
// DATABASE_URL. That string is the connection, ahead of AUTONOMY_POSTGRES_DSN.
func TestPostgresDefaultDSNPrefersDatabaseURL(t *testing.T) {
	t.Setenv(EnvPostgresDatabaseURL, "postgres://platform/autonomy")
	t.Setenv(EnvPostgresDSN, "postgres://explicit/autonomy")

	dsn, err := postgresEngine{}.DefaultDSN()
	if err != nil {
		t.Fatal(err)
	}
	if dsn != "postgres://platform/autonomy" {
		t.Fatalf("dsn=%q, want DATABASE_URL", dsn)
	}

	t.Setenv(EnvPostgresDatabaseURL, "  ")
	dsn, err = postgresEngine{}.DefaultDSN()
	if err != nil {
		t.Fatal(err)
	}
	if dsn != "postgres://explicit/autonomy" {
		t.Fatalf("dsn=%q, want AUTONOMY_POSTGRES_DSN", dsn)
	}

	t.Setenv(EnvPostgresDSN, "")
	_, err = postgresEngine{}.DefaultDSN()
	if err == nil || !strings.Contains(err.Error(), EnvPostgresDatabaseURL) {
		t.Fatalf("err=%v, want it to name %s", err, EnvPostgresDatabaseURL)
	}
}
