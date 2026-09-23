package autonomy

import (
	"path/filepath"
	"testing"
)

// The split's upper-layer half (docs/store.md「读写分离」): writerReads answers with the
// writer side of a store that has a follower, and hands back a store with one side
// untouched. Those are the two answers every call site depends on, and they have to
// hold for a store that knows nothing about followers at all (the default, sqlite).

// splitTestStore is a store with two sides: the embedded Store answers as the writer
// would, and Reading says which side a read asked for.
type splitTestStore struct {
	Store
	follower Store
}

func (s splitTestStore) Reading(source ReadSource) Store {
	if source == ReadWriter {
		return s.Store
	}
	return s
}

var _ StoreReadSplit = splitTestStore{}

// TestWriterReadsAsksForTheWriterSide: a caller that needs read-your-writes gets the
// writer side, through the port it holds — not the store, not the engine.
func TestWriterReadsAsksForTheWriterSide(t *testing.T) {
	writer, follower := &fakeStore{}, &fakeStore{}
	split := Store(splitTestStore{Store: writer, follower: follower})

	if got := writerReads(split); got != Store(writer) {
		t.Fatalf("writerReads returned %#v, want the writer side", got)
	}
	// The same answer when the caller holds the port rather than the store, which is
	// how every call site in the runtime holds it.
	var tasks TaskStore = splitTestStore{Store: writer, follower: follower}
	if got := writerReads(tasks); got != TaskStore(writer) {
		t.Fatalf("writerReads on a port returned %#v, want the writer side", got)
	}
	// And a store with one side (no follower to split) is handed back untouched.
	var plain Store = writer
	if got := writerReads(plain); got != plain {
		t.Fatalf("writerReads on a store with one side returned %#v, want the same store", got)
	}
}

// TestWriterReadsLeavesAStoreWithOneSideAlone: the built-in engine has no follower, so
// asking for the writer must change nothing — and the port has to keep working.
func TestWriterReadsLeavesAStoreWithOneSideAlone(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if got := writerReads(store); got != store {
		t.Fatalf("writerReads returned %#v, want the same store", got)
	}
	if task, err := writerReads(store).GetTask("task-nowhere"); err != nil || task != nil {
		t.Fatalf("a read through writerReads: task=%v err=%v, want a missing row and no error", task, err)
	}
}
