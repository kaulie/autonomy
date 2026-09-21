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

// openStore opens the built-in sqlite engine at path for a test.
func openStore(path string) (rawStore, error) {
	s, err := OpenStore(StoreEngineSQLite, path)
	if err != nil {
		return nil, err
	}
	rs, ok := s.(rawStore)
	if !ok {
		return nil, fmt.Errorf("sqlite engine does not expose RawDB")
	}
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
