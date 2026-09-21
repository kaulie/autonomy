package db

import . "github.com/kaulie/autonomy/src"

import (
	"database/sql"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The split (docs/store.md「读写分离」): a store opened with a follower serves the
// reads that only observe from it, and a store with one side serves everything from
// the writer it was opened on. These tests pin both the routing (which pool a read
// goes to) and the checks that make a follower trustworthy enough to read from.

// TestReadPoolRoutesFollowsTheFollower: the routing itself, without a server — the two
// pools are only compared by identity, never queried.
func TestReadPoolRoutesFollowsTheFollower(t *testing.T) {
	writer, follower := new(sql.DB), new(sql.DB)

	split := &PostgresStore{db: writer, reader: follower}
	if got := split.readPool(); got != follower {
		t.Fatalf("a store with a follower reads from %p, want the follower %p", got, follower)
	}
	// The store as opened is the follower side; asking for the follower changes nothing.
	if got := split.Reading(ReadFollower); got != Store(split) {
		t.Fatalf("Reading(ReadFollower) returned %#v, want the same store", got)
	}
	// Asking for the writer hands out a store with no follower left in it.
	writerOnly, ok := split.Reading(ReadWriter).(*PostgresStore)
	if !ok {
		t.Fatalf("Reading(ReadWriter) returned %T, want the engine's store", split.Reading(ReadWriter))
	}
	if writerOnly.readPool() != writer {
		t.Fatalf("the writer side reads from %p, want the writer %p", writerOnly.readPool(), writer)
	}
	if writerOnly.db != writer || writerOnly.reader != nil {
		t.Fatalf("the writer side must share the writer pool and have no follower: db=%p reader=%v",
			writerOnly.db, writerOnly.reader)
	}

	// A store with one side answers the same question without a second copy.
	plain := &PostgresStore{db: writer}
	if got := plain.readPool(); got != writer {
		t.Fatalf("a store without a follower reads from %p, want the writer %p", got, writer)
	}
	if got := plain.Reading(ReadWriter); got != Store(plain) {
		t.Fatalf("Reading(ReadWriter) on a store with one side returned %#v, want the same store", got)
	}
}

// TestPostgresReadFollowerDSNIsConfiguredLikeTheWriter: the read side takes the same two
// names as the writer, with the same precedence — the engine-neutral variable wins over
// the engine's own.
func TestPostgresReadFollowerDSNIsConfiguredLikeTheWriter(t *testing.T) {
	t.Setenv(EnvStoreReadDSN, "")
	t.Setenv(EnvPostgresReadDSN, "")
	if got := postgresReadFollowerDSN(); got != "" {
		t.Fatalf("with neither variable set the read follower is %q, want none", got)
	}
	t.Setenv(EnvPostgresReadDSN, "postgres://follower:5433/autonomy")
	if got := postgresReadFollowerDSN(); got != "postgres://follower:5433/autonomy" {
		t.Fatalf("read follower is %q, want the engine-specific DSN", got)
	}
	t.Setenv(EnvStoreReadDSN, "postgres://store-level:5433/autonomy")
	if got := postgresReadFollowerDSN(); got != "postgres://store-level:5433/autonomy" {
		t.Fatalf("read follower is %q, want %s to win", got, EnvStoreReadDSN)
	}
}

// now is time.Now, named so the tests below read as "a task written just now".
func now() time.Time { return time.Now() }

// TestReadsAreServedByTheFollower is the split proved against a real server: the
// follower stands in as a second database holding the same schema and no rows, so a
// read that comes back empty can only have been served by the follower — and the same
// read on the writer side finds what was written.
func TestReadsAreServedByTheFollower(t *testing.T) {
	writer := newPostgresStore(t)
	// A second database with the same schema: what the follower would be if it were a
	// replica that had replayed nothing yet. It is never written through the split.
	follower := newPostgresStore(t)

	split := &PostgresStore{db: writer.db, reader: follower.db}
	// The split store owns no pools of its own: both sides are closed by the stores that
	// opened them (their t.Cleanup), not here.
	task := &Task{ID: "task-split", Description: "written to the writer", Status: TaskStatusPending, CreatedAt: now()}
	if err := split.UpsertTask(task); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}

	if got, err := split.GetTask(task.ID); err != nil || got != nil {
		t.Fatalf("a read found %v (err=%v) in the follower, which holds no rows: the read did not go to the follower", got, err)
	}
	stored, err := split.Reading(ReadWriter).GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask on the writer side: %v", err)
	}
	if stored == nil || stored.Description != task.Description {
		t.Fatalf("the writer side read %v, want the task that was just written", stored)
	}
}

// TestTheSplitAgainstARealReplica is the end-to-end check of the topology
// docs/local-replica.md describes: a writer on a server, a streaming replica of it on
// this machine, and a store that writes to the first while reading the second.
//
// It needs all three ends configured, because none can stand in for another:
//
//	AUTONOMY_POSTGRES_TEST_DSN          the writer's server, where this test may create
//	                                    a database (dropped again at the end)
//	AUTONOMY_POSTGRES_SPLIT_WRITE_DSN   the writer, as the role the runtime uses
//	AUTONOMY_POSTGRES_SPLIT_READ_DSN    the replica, as that same role
//
// The scratch database is created on the writer **owned by the runtime's role** (the
// tables a store makes belong to whoever made them, and on the replica they are read as
// the same role), so it reaches the replica by replication — which is itself part of
// what is being tested. Over a slow link that is not instant: creating a database copies
// template1 into the WAL stream (several MB), so the waits below are generous on purpose.
func TestTheSplitAgainstARealReplica(t *testing.T) {
	writeDSN := strings.TrimSpace(os.Getenv("AUTONOMY_POSTGRES_SPLIT_WRITE_DSN"))
	readDSN := strings.TrimSpace(os.Getenv("AUTONOMY_POSTGRES_SPLIT_READ_DSN"))
	if writeDSN == "" || readDSN == "" {
		t.Skip("no replica pair configured (AUTONOMY_POSTGRES_SPLIT_WRITE_DSN + AUTONOMY_POSTGRES_SPLIT_READ_DSN)")
	}
	admin, _ := pgAdminConn(t)
	name := pgTestDatabaseOwnedBy(t, admin, pgDSNUser(t, writeDSN))
	if !pgWaitForDatabase(t, pgWithDatabase(readDSN, name)) {
		t.Fatalf("the database %s never appeared on the replica %s: is it still streaming?", name, readDSN)
	}

	store, err := OpenPostgresStoreWithFollower(pgWithDatabase(writeDSN, name), pgWithDatabase(readDSN, name))
	if err != nil {
		t.Fatalf("OpenPostgresStoreWithFollower: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.reader == nil {
		t.Fatalf("the replica at %s was not accepted as a follower", readDSN)
	}

	task := &Task{ID: "task-replica", Description: "written to the writer", Status: TaskStatusPending, CreatedAt: now()}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	// Read-your-writes is immediate on the writer side...
	if stored, err := store.Reading(ReadWriter).GetTask(task.ID); err != nil || stored == nil {
		t.Fatalf("the writer side does not see the task it just wrote: err=%v task=%v", err, stored)
	}
	// ...and the follower catches up on its own, which is the whole reason a read may
	// be served there (and why writes are not).
	deadline := time.Now().Add(2 * time.Minute)
	for {
		stored, err := store.GetTask(task.ID)
		if err == nil && stored != nil && stored.Description == task.Description {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the replica never delivered the written task: err=%v task=%v", err, stored)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// pgDSNUser is the role a DSN connects as.
func pgDSNUser(t *testing.T, dsn string) string {
	t.Helper()
	if u, err := url.Parse(dsn); err == nil && u.User != nil && u.User.Username() != "" {
		return u.User.Username()
	}
	if m := regexpUser.FindStringSubmatch(dsn); m != nil {
		return m[1]
	}
	t.Fatalf("cannot read the role from the DSN %q", dsn)
	return ""
}

var regexpUser = regexp.MustCompile(`(?:^|\s)user=(\S+)`)

// pgWaitForDatabase waits for a database to be reachable, which on a replica means
// "the CREATE DATABASE has been replayed". It waits as long as the test above does, for
// the same reason: the WAL carrying a new database has to arrive first.
func pgWaitForDatabase(t *testing.T, dsn string) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		pool, err := openPostgresPool(dsn)
		if err == nil {
			_ = pool.Close()
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// must not fail the open (a replica is an optimization, the database is the point), and
// it must not be read from either. A second, writable database is exactly the mistake the
// open-time checks are there to catch — it would answer with another world's rows.
func TestOpenWithAFollowerThatIsNotAReplicaKeepsTheWriter(t *testing.T) {
	admin, base := pgAdminConn(t)
	writerDB := pgTestDatabase(t, admin)
	followerDB := pgTestDatabase(t, admin)

	store, err := OpenPostgresStoreWithFollower(
		pgWithDatabase(base, writerDB), pgWithDatabase(base, followerDB))
	if err != nil {
		t.Fatalf("a writer with an unusable follower must still open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.reader != nil {
		t.Fatalf("a writable database was accepted as the follower")
	}
	if _, err := openPostgresFollower(store.db, pgWithDatabase(base, followerDB)); err == nil {
		t.Fatalf("openPostgresFollower accepted a writable database")
	} else if !strings.Contains(err.Error(), "not in recovery") {
		t.Fatalf("openPostgresFollower said %q, want it to name the recovery check", err)
	}

	task := &Task{ID: "task-fallback", Description: "reads stay on the writer", Status: TaskStatusPending, CreatedAt: now()}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	if stored, err := store.GetTask(task.ID); err != nil || stored == nil {
		t.Fatalf("GetTask after the follower was refused: %v (task=%v)", err, stored)
	}
}
