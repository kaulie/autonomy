package start_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func storeEnvScript(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "scripts", "store_env.sh")
}

func runStoreEnv(t *testing.T, body string) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", body, "bash", storeEnvScript(t))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("store_env.sh: %v\n%s", err, out)
	}
	return string(out)
}

// TestUnifyStoreReadsOntoWriter is the deploy contract: backend/.env is kept
// across releases and still names the local replica, but the process start.sh
// launches reads and writes the writer (49.234.45.173), and the log line does
// not carry the password.
func TestUnifyStoreReadsOntoWriter(t *testing.T) {
	const writer = "postgres://autonomy:secret@49.234.45.173:5432/autonomy?sslmode=verify-ca&sslrootcert=/tmp/ca.pem"
	const reader = "postgres://autonomy:secret@127.0.0.1:5433/autonomy?sslmode=disable"
	out := runStoreEnv(t, `
		set -a
		. "$1"
		export AUTONOMY_STORE_DSN='`+writer+`'
		export AUTONOMY_STORE_READ_DSN='`+reader+`'
		export AUTONOMY_POSTGRES_READ_DSN='`+reader+`'
		unify_store_reads_onto_writer
		printf 'read=%s\n' "${AUTONOMY_STORE_READ_DSN-unset}"
		printf 'pgread=%s\n' "${AUTONOMY_POSTGRES_READ_DSN-unset}"
		printf 'write=%s\n' "${AUTONOMY_STORE_DSN}"
	`)
	logLine := out
	if i := strings.Index(out, "\n"); i >= 0 {
		logLine = out[:i]
	}
	if strings.Contains(logLine, "secret") {
		t.Fatalf("log leaked the password:\n%s", logLine)
	}
	if !strings.Contains(logLine, "49.234.45.173:5432") {
		t.Fatalf("log did not name the writer:\n%s", logLine)
	}
	if !strings.Contains(out, "read=unset") || !strings.Contains(out, "pgread=unset") {
		t.Fatalf("read DSNs still set:\n%s", out)
	}
	if !strings.Contains(out, "write="+writer) {
		t.Fatalf("writer DSN changed:\n%s", out)
	}
}

func TestUnifyStoreReadsLeavesASingleDatabaseAlone(t *testing.T) {
	out := runStoreEnv(t, `
		set -a
		. "$1"
		export AUTONOMY_STORE_DSN='postgres://autonomy:secret@49.234.45.173:5432/autonomy'
		unify_store_reads_onto_writer
		printf 'read=%s\n' "${AUTONOMY_STORE_READ_DSN-unset}"
		printf 'write=%s\n' "${AUTONOMY_STORE_DSN}"
	`)
	if strings.Contains(out, "忽略读侧副本") {
		t.Fatalf("a store with no read DSN should stay quiet:\n%s", out)
	}
	if !strings.Contains(out, "read=unset") {
		t.Fatalf("read DSN appeared:\n%s", out)
	}
}

func TestStoreWriterTargetReadsKeywordDSN(t *testing.T) {
	out := runStoreEnv(t, `
		. "$1"
		AUTONOMY_STORE_DSN='host=49.234.45.173 port=5432 user=autonomy password=secret dbname=autonomy'
		store_writer_target
	`)
	out = strings.TrimSpace(out)
	if out != "49.234.45.173:5432" {
		t.Fatalf("target=%q", out)
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("keyword DSN leaked the password: %q", out)
	}
}

func TestStartScriptUnifiesStoreBeforeLaunch(t *testing.T) {
	startPath := filepath.Join(filepath.Dir(storeEnvScript(t)), "start.sh")
	body, err := os.ReadFile(startPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	sourceAt := strings.Index(text, `. "${ENV_FILE}"`)
	unifyAt := strings.Index(text, "unify_store_reads_onto_writer")
	launchAt := strings.Index(text, `nohup "${BIN}"`)
	if sourceAt < 0 || unifyAt < 0 || launchAt < 0 {
		t.Fatalf("start.sh is missing env source, unify, or launch (source=%d unify=%d launch=%d)", sourceAt, unifyAt, launchAt)
	}
	if !(sourceAt < unifyAt && unifyAt < launchAt) {
		t.Fatal("start.sh must drop the read replica after sourcing .env and before launching autonomyd")
	}
	for _, need := range []string{"store_env.sh", "缺少 ${DIR}/store_env.sh"} {
		if !strings.Contains(text, need) {
			t.Errorf("scripts/start.sh is missing %q", need)
		}
	}
}

func TestReleasePackageCarriesStoreEnv(t *testing.T) {
	root := filepath.Join(filepath.Dir(storeEnvScript(t)), "..")
	build, err := os.ReadFile(filepath.Join(root, "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(build), "store_env.sh") {
		t.Fatal("build.sh does not copy store_env.sh into the release package")
	}
}
