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
	captureAt := strings.Index(text, "_platform_database_url=")
	sourceAt := strings.Index(text, `. "${ENV_FILE}"`)
	applyAt := strings.Index(text, "apply_database_url")
	unifyAt := strings.Index(text, "unify_store_reads_onto_writer")
	launchAt := strings.Index(text, `nohup "${BIN}"`)
	if captureAt < 0 || sourceAt < 0 || applyAt < 0 || unifyAt < 0 || launchAt < 0 {
		t.Fatalf("start.sh is missing database-url capture, env source, apply, unify, or launch (capture=%d source=%d apply=%d unify=%d launch=%d)", captureAt, sourceAt, applyAt, unifyAt, launchAt)
	}
	if !(captureAt < sourceAt && sourceAt < applyAt && applyAt < unifyAt && unifyAt < launchAt) {
		t.Fatal("start.sh must keep the platform DATABASE_URL across .env, apply it, then drop the read replica, all before launching autonomyd")
	}
	for _, need := range []string{"store_env.sh", "缺少 ${DIR}/store_env.sh"} {
		if !strings.Contains(text, need) {
			t.Errorf("scripts/start.sh is missing %q", need)
		}
	}
}

func TestApplyDatabaseURLOverridesEnvFileDSN(t *testing.T) {
	const platform = "postgres://autonomy:secret@10.0.0.8:5432/autonomy?sslmode=disable"
	const stale = "postgres://autonomy:secret@127.0.0.1:5432/autonomy"
	out := runStoreEnv(t, `
		set -a
		. "$1"
		export DATABASE_URL='`+platform+`'
		export AUTONOMY_STORE_DSN='`+stale+`'
		export AUTONOMY_STORE_ENGINE=sqlite
		export AUTONOMY_STORE_READ_DSN='postgres://autonomy:secret@127.0.0.1:5433/autonomy'
		apply_database_url
		unify_store_reads_onto_writer
		printf 'engine=%s\n' "${AUTONOMY_STORE_ENGINE}"
		printf 'write=%s\n' "${AUTONOMY_STORE_DSN}"
		printf 'read=%s\n' "${AUTONOMY_STORE_READ_DSN-unset}"
	`)
	if strings.Contains(out, "secret") && strings.Contains(strings.Split(out, "\n")[0], "secret") {
		t.Fatalf("log leaked the password:\n%s", out)
	}
	if !strings.Contains(out, "10.0.0.8:5432") {
		t.Fatalf("log did not name the platform database:\n%s", out)
	}
	if !strings.Contains(out, "engine=postgres") {
		t.Fatalf("engine was not postgres:\n%s", out)
	}
	if !strings.Contains(out, "write="+platform) {
		t.Fatalf("writer DSN was not DATABASE_URL:\n%s", out)
	}
	if !strings.Contains(out, "read=unset") {
		t.Fatalf("read DSN still set:\n%s", out)
	}
}

func TestApplyDatabaseURLBlankLeavesExistingDSN(t *testing.T) {
	out := runStoreEnv(t, `
		. "$1"
		export DATABASE_URL='   '
		export AUTONOMY_STORE_DSN='/tmp/autonomy.db'
		export AUTONOMY_STORE_ENGINE=sqlite
		apply_database_url
		printf 'engine=%s\n' "${AUTONOMY_STORE_ENGINE}"
		printf 'write=%s\n' "${AUTONOMY_STORE_DSN}"
	`)
	if strings.Contains(out, "数据库连接来自 DATABASE_URL") {
		t.Fatalf("blank DATABASE_URL should not apply:\n%s", out)
	}
	if !strings.Contains(out, "engine=sqlite") || !strings.Contains(out, "write=/tmp/autonomy.db") {
		t.Fatalf("existing config changed:\n%s", out)
	}
}

func TestApplyDatabaseURLKeywordDSNSelectsPostgres(t *testing.T) {
	out := runStoreEnv(t, `
		. "$1"
		export DATABASE_URL='host=10.0.0.8 port=5432 user=autonomy password=secret dbname=autonomy'
		export AUTONOMY_STORE_ENGINE=sqlite
		apply_database_url
		printf 'engine=%s\n' "${AUTONOMY_STORE_ENGINE}"
	`)
	logLine := out
	if i := strings.Index(out, "\n"); i >= 0 {
		logLine = out[:i]
	}
	if strings.Contains(logLine, "secret") {
		t.Fatalf("log leaked the password:\n%s", logLine)
	}
	if !strings.Contains(logLine, "10.0.0.8:5432") {
		t.Fatalf("log did not name the host:\n%s", logLine)
	}
	if !strings.Contains(out, "engine=postgres") {
		t.Fatalf("keyword DSN did not select postgres:\n%s", out)
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
