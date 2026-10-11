package replica_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func scriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "scripts", "local-replica.sh")
}

func readScript(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(scriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestLocalReplicaScriptSetsLocale keeps the macOS launchd locale contract in
// the script: an empty LC_ALL makes postmaster multithreaded during startup,
// the replica never listens on 5433, and autonomy falls back with
// "connection refused".
func TestLocalReplicaScriptSetsLocale(t *testing.T) {
	text := readScript(t)
	for _, need := range []string{
		`REPLICA_LC_ALL=`,
		`export LC_ALL=`,
		`export LANG=`,
		`<key>EnvironmentVariables</key>`,
		`<key>LC_ALL</key>`,
		`<key>LANG</key>`,
		`launchctl bootout`,
	} {
		if !strings.Contains(text, need) {
			t.Errorf("scripts/local-replica.sh is missing %q (macOS replica start dies without a valid locale)", need)
		}
	}
}

// TestLocalReplicaStartDoesNotNeedPrimaryHost is the already-initialized
// path: primary_conninfo lives in the data directory, so start/restart must
// not demand PRIMARY_HOST (replica.env is optional after init).
func TestLocalReplicaStartDoesNotNeedPrimaryHost(t *testing.T) {
	text := readScript(t)
	if !strings.Contains(text, `start)`) {
		t.Fatal("scripts/local-replica.sh has no start command")
	}
	// The old `start) require_primary; start_replica` line is what left a
	// built replica unstartable after a reboot when replica.env was absent.
	if strings.Contains(text, "start) require_primary") {
		t.Error("scripts/local-replica.sh start still requires PRIMARY_HOST; an initialized replica cannot come up without replica.env")
	}
	if strings.Contains(text, "restart) stop_replica; require_primary") {
		t.Error("scripts/local-replica.sh restart still requires PRIMARY_HOST")
	}
}
