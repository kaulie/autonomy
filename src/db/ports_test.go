package db

import . "github.com/kaulie/autonomy/src"

// Both engines implement each port of the persistence contract (src/store.go), so
// an engine can also be built port by port (or be a decorator over one port) and
// still compose with Store. The two implementations are independent — different
// SQL, different schema, different drivers — and they answer to the same contract,
// which is what makes the database a configuration choice. These assertions live
// with the engines; the rest of the boundary check (no upper-layer file names a
// driver or a concrete engine) stays in the root package.
var (
	_ TaskStore         = (*SQLiteStore)(nil)
	_ AgentStore        = (*SQLiteStore)(nil)
	_ ConversationStore = (*SQLiteStore)(nil)
	_ ExecutionStore    = (*SQLiteStore)(nil)
	_ VerificationStore = (*SQLiteStore)(nil)
	_ InboxStore        = (*SQLiteStore)(nil)
	_ TurnQueryStore    = (*SQLiteStore)(nil)
	_ Store             = (*SQLiteStore)(nil)
	_ TaskStore         = (*PostgresStore)(nil)
	_ AgentStore        = (*PostgresStore)(nil)
	_ ConversationStore = (*PostgresStore)(nil)
	_ ExecutionStore    = (*PostgresStore)(nil)
	_ VerificationStore = (*PostgresStore)(nil)
	_ InboxStore        = (*PostgresStore)(nil)
	_ TurnQueryStore    = (*PostgresStore)(nil)
	_ Store             = (*PostgresStore)(nil)
)
