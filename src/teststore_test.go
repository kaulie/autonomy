package autonomy

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The built-in sqlite engine now lives in src/db. Tests open it through the engine
// registry (OpenStore), so they keep depending on the Store port; the rows a port
// does not surface are reached through the engine's RawDB seam.

// rawStore is a Store that also exposes its underlying connection, so a test can
// assert on rows the ports do not surface (the schema, the id-sequence floors).
type rawStore interface {
	Store
	RawDB() *sql.DB
}

// openStore opens the built-in sqlite engine at path for a test, with a pool an agent can
// actually run on (seedTestAccounts): a runtime refuses to attach a session when the pool has
// nothing for the harness it needs (src/agent_account.go), so a test that drives runs needs
// accounts as much as it needs a store.
func openStore(t *testing.T, path string) (rawStore, error) {
	t.Helper()
	s, err := OpenStore(StoreEngineSQLite, path)
	if err != nil {
		return nil, err
	}
	rs, ok := s.(rawStore)
	if !ok {
		return nil, fmt.Errorf("sqlite engine does not expose RawDB")
	}
	seedTestAccounts(t, rs)
	return rs, nil
}

// preparePolicyRoot is a PROJECT_ROOT holding the runtime's agent policy and the
// runtime's policy file (src/agent_policy/), so a prompt rendered against it is
// the one a deployment renders. Both are files now: the policy wording is not in
// the code that renders the prompt.
func preparePolicyRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "src", "agent_policy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AGENT_V2.md", "CONSTRAINTS.json"} {
		b, err := os.ReadFile(filepath.Join("agent_policy", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// seedTestAccounts puts one enabled account per harness in a test store's pool.
//
// A runtime resolves an agent's account before it attaches any session (src/agent_account.go),
// and refuses when the pool has nothing for that harness — so a fixture that hands a test a
// store a runtime can run on must hand it a pool too. The accounts are key-less: they run on
// whatever the provider's own CLI saved, which is what a test's fake bridge stands in for.
// Seeding is idempotent, so a helper may call it on a store that already has accounts.
func seedTestAccounts(t *testing.T, store Store) {
	t.Helper()
	if err := seedPool(store); err != nil {
		t.Fatalf("seed the pool: %v", err)
	}
}

// seedPool is seedTestAccounts without a *testing.T: the fixture that ensures a pool can decide
// for itself what to do when a store turns out to be unusable (a closed one left behind by an
// earlier test, for instance).
func seedPool(store Store) error {
	if store == nil {
		return fmt.Errorf("no store")
	}
	existing, err := store.ListAccounts(AccountFilter{})
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}
	byHarness := map[string]bool{}
	for _, account := range existing {
		byHarness[account.Harness] = true
	}
	for _, harness := range AccountHarnesses() {
		if byHarness[harness] {
			continue
		}
		if _, err := store.CreateAccount(Account{
			Harness: harness, Label: "test " + harness, Enabled: true, IsDefault: true,
		}); err != nil {
			return fmt.Errorf("seed %s account: %w", harness, err)
		}
	}
	return nil
}
